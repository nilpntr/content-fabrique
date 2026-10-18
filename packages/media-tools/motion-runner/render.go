package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Options configure one run.
type Options struct {
	Chrome string // the chrome-headless-shell binary
	Bundle string // the template bundle directory (read-only mount): scene.html and its scripts
	Fonts  string // the directory of the font files the job names
	Job    string // the job file
	Out    string // where the frames and the manifest go (created; must not exist or be empty)
	Tmp    string // scratch space for Chrome's profile (a tmpfs in the sandbox)
	// FrameTimeout bounds one frame (seek and capture); LoadTimeout the page load and mount.
	FrameTimeout time.Duration
	LoadTimeout  time.Duration
}

// Manifest is what the runner writes next to the frames. The worker reads it as untrusted output.
type Manifest struct {
	Version  int             `json:"version"`
	Chrome   string          `json:"chrome"`
	Arch     string          `json:"arch"`
	Segments []SegmentResult `json:"segments"`
	Timing   Timing          `json:"timing"`
}

// SegmentResult lists the frames of one segment: Dir/f%06d.png for i in [0, Frames), the first being StartFrame.
type SegmentResult struct {
	StartFrame int64     `json:"start_frame"`
	Frames     int64     `json:"frames"`
	Dir        string    `json:"dir"`
	Clip       PixelRect `json:"clip"`
	SHA256     []string  `json:"sha256"`
}

// Timing is how long each phase took, in milliseconds.
type Timing struct {
	LaunchMs int64 `json:"launch_ms"`
	LoadMs   int64 `json:"load_ms"`
	FramesMs int64 `json:"frames_ms"`
	TotalMs  int64 `json:"total_ms"`
}

// problems collects everything that went wrong inside the page. Any problem fails the job: an error in a template, a
// refused request, a failed font, a crash. A frame drawn after one of those is not trusted.
type problems struct {
	mu   sync.Mutex
	list []string
}

func (p *problems) add(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.list) < 20 {
		p.list = append(p.list, fmt.Sprintf(format, args...))
	}
}

func (p *problems) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.list) == 0 {
		return nil
	}
	return fmt.Errorf("the page failed: %s", strings.Join(p.list, "; "))
}

// run renders the job. It returns an error for anything that should fail the render: a bad job, a browser that does not
// start, a page error, a request outside the bundle, a blank frame or a frame that paints outside the overlay boxes.
func run(ctx context.Context, o Options) (err error) {
	start := time.Now()
	data, err := readCapped(o.Job, 1<<20)
	if err != nil {
		return err
	}
	job, err := parseJob(data)
	if err != nil {
		return err
	}
	fonts, err := loadFonts(o.Fonts, job.Fonts)
	if err != nil {
		return err
	}
	bundle, err := filepath.Abs(o.Bundle)
	if err != nil {
		return err
	}
	scene := filepath.Join(bundle, "scene.html")
	if st, err := os.Stat(scene); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("bundle: %s has no scene.html", bundle)
	}
	if err := os.MkdirAll(o.Out, 0o755); err != nil { //nolint:gosec // read by the worker, which may be another uid
		return fmt.Errorf("output: %w", err)
	}

	b, err := launchChrome(ctx, o.Chrome, o.Tmp, job.CSSWidth, job.CSSHeight)
	if err != nil {
		return err
	}
	defer b.close()
	defer func() {
		if err != nil {
			if tail := b.stderr.String(); tail != "" && (errors.Is(err, errChromeGone) || strings.Contains(err.Error(), "closed the pipe")) {
				err = fmt.Errorf("%w\nchrome's log:\n%s", err, tail)
			}
		}
	}()
	p := &problems{}
	s := &session{c: b.client, p: p, bundle: bundle, loaded: make(chan struct{}, 1)}

	var version struct {
		Product string `json:"product"`
	}
	if err := b.client.call(ctx, "", "Browser.getVersion", nil, &version); err != nil {
		return fmt.Errorf("chrome did not answer: %w", err)
	}
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := b.client.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.client.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached); err != nil {
		return err
	}
	s.id = attached.SessionID
	go s.watch(ctx)
	launched := time.Now()

	if err := s.prepare(ctx, job); err != nil {
		return err
	}
	if err := s.load(ctx, o.LoadTimeout, scene, job, fonts); err != nil {
		return err
	}
	loaded := time.Now()

	m := Manifest{Version: 1, Chrome: version.Product, Arch: runtime.GOARCH}
	checks := inkChecks(job)
	for k, seg := range job.Segments {
		dir := fmt.Sprintf("s%d", k)
		if err := os.Mkdir(filepath.Join(o.Out, dir), 0o755); err != nil { //nolint:gosec // read by the worker
			return fmt.Errorf("output: %w", err)
		}
		res := SegmentResult{StartFrame: seg.StartFrame, Frames: seg.EndFrame - seg.StartFrame, Dir: dir, Clip: seg.Clip}
		for n := seg.StartFrame; n < seg.EndFrame; n++ {
			png, err := s.frame(ctx, o.FrameTimeout, job.FrameMs(n), seg.Clip, job.Scale())
			if err != nil {
				return fmt.Errorf("frame %d (%.3f s): %w", n, job.FrameMs(n)/1000, err)
			}
			if cs, ok := checks[n]; ok || n == seg.StartFrame || n == seg.EndFrame-1 {
				if err := checkFrame(png, job, cs, n, seg.Clip); err != nil {
					return fmt.Errorf("frame %d (%.3f s): %w", n, job.FrameMs(n)/1000, err)
				}
			}
			name := filepath.Join(o.Out, dir, fmt.Sprintf("f%06d.png", n-seg.StartFrame))
			if err := writeNew(name, png); err != nil {
				return err
			}
			sum := sha256.Sum256(png)
			res.SHA256 = append(res.SHA256, hex.EncodeToString(sum[:]))
		}
		m.Segments = append(m.Segments, res)
	}
	if err := p.err(); err != nil {
		return err
	}
	done := time.Now()
	m.Timing = Timing{
		LaunchMs: launched.Sub(start).Milliseconds(),
		LoadMs:   loaded.Sub(launched).Milliseconds(),
		FramesMs: done.Sub(loaded).Milliseconds(),
		TotalMs:  done.Sub(start).Milliseconds(),
	}
	out, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return writeNew(filepath.Join(o.Out, "manifest.json"), out)
}

// writeNew writes an output file that must not exist yet, and never through a symlink in its place: the output directory is fresh
// for every run, so anything already there is not ours.
func writeNew(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644) //nolint:gosec // read by the worker, which may be another uid
	if err != nil {
		return fmt.Errorf("output: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("output: %w", err)
	}
	return f.Close()
}

// session is the page the frames are drawn in.
type session struct {
	c      *client
	id     string
	p      *problems
	bundle string
	obj    string // the remote object id of window.__cfMotion
	loaded chan struct{}
}

func (s *session) call(ctx context.Context, method string, params, out any) error {
	return s.c.call(ctx, s.id, method, params, out)
}

// watch handles the page's events until the browser goes away: it answers every paused request (allowed only inside the
// bundle) and records every error.
func (s *session) watch(ctx context.Context) {
	for ev := range s.c.events {
		if ev.SessionID != s.id {
			continue
		}
		switch ev.Method {
		case "Page.loadEventFired":
			select {
			case s.loaded <- struct{}{}:
			default:
			}
		case "Fetch.requestPaused":
			var rp struct {
				RequestID string `json:"requestId"`
				Request   struct {
					URL string `json:"url"`
				} `json:"request"`
			}
			if err := json.Unmarshal(ev.Params, &rp); err != nil {
				s.p.add("an undecodable paused request")
				continue
			}
			if allowedURL(rp.Request.URL, s.bundle) {
				go func() { _ = s.call(ctx, "Fetch.continueRequest", map[string]any{"requestId": rp.RequestID}, nil) }()
			} else {
				s.p.add("the page asked for %s, which is outside the template bundle: refused", truncate(rp.Request.URL, 200))
				go func() {
					_ = s.call(ctx, "Fetch.failRequest", map[string]any{"requestId": rp.RequestID, "errorReason": "BlockedByClient"}, nil)
				}()
			}
		case "Runtime.exceptionThrown":
			var ex struct {
				ExceptionDetails struct {
					Text      string `json:"text"`
					Exception struct {
						Description string `json:"description"`
					} `json:"exception"`
				} `json:"exceptionDetails"`
			}
			_ = json.Unmarshal(ev.Params, &ex)
			d := ex.ExceptionDetails.Exception.Description
			if d == "" {
				d = ex.ExceptionDetails.Text
			}
			s.p.add("uncaught exception: %s", truncate(d, 300))
		case "Runtime.consoleAPICalled":
			var c struct {
				Type string `json:"type"`
				Args []struct {
					Value       any    `json:"value"`
					Description string `json:"description"`
				} `json:"args"`
			}
			_ = json.Unmarshal(ev.Params, &c)
			if c.Type == "error" || c.Type == "assert" {
				var parts []string
				for _, a := range c.Args {
					if a.Description != "" {
						parts = append(parts, a.Description)
					} else {
						parts = append(parts, fmt.Sprint(a.Value))
					}
				}
				s.p.add("console.%s: %s", c.Type, truncate(strings.Join(parts, " "), 300))
			}
		case "Log.entryAdded":
			var l struct {
				Entry struct {
					Level  string `json:"level"`
					Source string `json:"source"`
					Text   string `json:"text"`
					URL    string `json:"url"`
				} `json:"entry"`
			}
			_ = json.Unmarshal(ev.Params, &l)
			if l.Entry.Level == "error" {
				s.p.add("%s error: %s %s", l.Entry.Source, truncate(l.Entry.Text, 300), truncate(l.Entry.URL, 120))
			}
		case "Network.loadingFailed":
			var f struct {
				ErrorText     string `json:"errorText"`
				BlockedReason string `json:"blockedReason"`
				Type          string `json:"type"`
			}
			_ = json.Unmarshal(ev.Params, &f)
			s.p.add("a %s load failed: %s %s", f.Type, f.ErrorText, f.BlockedReason)
		case "Inspector.targetCrashed":
			s.p.add("the page crashed (out of memory or a renderer bug)")
		case "Inspector.detached":
			s.p.add("the page was detached from the protocol")
		}
	}
}

// allowedURL reports whether a request may go through: only file URLs of regular paths inside the bundle. Everything else,
// http(s), ws, data and blob included, is refused (the templates need none of it; fonts arrive as bytes).
func allowedURL(raw, bundle string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	p := path.Clean(u.Path)
	if p != u.Path || strings.Contains(p, "\x00") {
		return false
	}
	root := filepath.ToSlash(bundle)
	return strings.HasPrefix(p, root+"/")
}

// prepare enables the domains the runner needs and sets up the viewport: the layout size, the device scale factor of the
// profile and a transparent background.
func (s *session) prepare(ctx context.Context, job *Job) error {
	for _, m := range []string{"Page.enable", "Runtime.enable", "Log.enable", "Network.enable", "Inspector.enable"} {
		if err := s.call(ctx, m, nil, nil); err != nil {
			return err
		}
	}
	if err := s.call(ctx, "Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}}}, nil); err != nil {
		return err
	}
	if err := s.call(ctx, "Network.setCacheDisabled", map[string]any{"cacheDisabled": true}, nil); err != nil {
		return err
	}
	if err := s.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": job.CSSWidth, "height": job.CSSHeight, "deviceScaleFactor": job.Scale(), "mobile": false,
		"screenWidth": job.CSSWidth, "screenHeight": job.CSSHeight,
	}, nil); err != nil {
		return err
	}
	if err := s.call(ctx, "Emulation.setDefaultBackgroundColorOverride", map[string]any{"color": map[string]any{"r": 0, "g": 0, "b": 0, "a": 0}}, nil); err != nil {
		return err
	}
	// Every animation the page does not drive itself stands still: the frames are seeked, never played.
	if err := s.call(ctx, "Animation.enable", nil, nil); err != nil {
		return err
	}
	return s.call(ctx, "Animation.setPlaybackRate", map[string]any{"playbackRate": 0}, nil)
}

// pageFont is a font as the page receives it.
type pageFont struct {
	Family string `json:"family"`
	Data   string `json:"data"` // base64 of the font file
}

// load opens the scene, mounts the items and waits for the fonts.
func (s *session) load(ctx context.Context, timeout time.Duration, scene string, job *Job, fonts []pageFont) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(scene)}
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := s.call(ctx, "Page.navigate", map[string]any{"url": u.String()}, &nav); err != nil {
		return err
	}
	if nav.ErrorText != "" {
		return fmt.Errorf("the scene did not load: %s", nav.ErrorText)
	}
	select {
	case <-s.loaded:
	case <-ctx.Done():
		return fmt.Errorf("the scene did not load in %v", timeout)
	}
	if err := s.p.err(); err != nil {
		return err
	}
	var ev struct {
		Result struct {
			ObjectID string `json:"objectId"`
			Type     string `json:"type"`
		} `json:"result"`
	}
	if err := s.call(ctx, "Runtime.evaluate", map[string]any{"expression": "window.__cfMotion"}, &ev); err != nil {
		return err
	}
	if ev.Result.Type != "object" || ev.Result.ObjectID == "" {
		return errors.New("the scene has no motion runtime (window.__cfMotion)")
	}
	s.obj = ev.Result.ObjectID
	page := map[string]any{
		"css_width": job.CSSWidth, "css_height": job.CSSHeight, "fps": job.FPS, "seed": job.Seed, "items": job.Items,
	}
	var res struct {
		Problems []string `json:"problems"`
	}
	if err := s.callRuntime(ctx, "function(job, fonts) { return this.mount(job, fonts); }", []any{page, fonts}, &res); err != nil {
		return fmt.Errorf("mount: %w", err)
	}
	if len(res.Problems) > 0 {
		return fmt.Errorf("mount: %s", strings.Join(res.Problems, "; "))
	}
	return s.p.err()
}

// frame seeks the page to ms and captures the clip (frame pixels) as a PNG.
func (s *session) frame(ctx context.Context, timeout time.Duration, ms float64, clip PixelRect, scale float64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var res struct {
		Problems []string `json:"problems"`
	}
	if err := s.callRuntime(ctx, "function(ms) { return this.seek(ms); }", []any{ms}, &res); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}
	if len(res.Problems) > 0 {
		return nil, fmt.Errorf("seek: %s", strings.Join(res.Problems, "; "))
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := s.call(ctx, "Page.captureScreenshot", map[string]any{
		"format": "png", "fromSurface": true, "captureBeyondViewport": false, "optimizeForSpeed": true,
		"clip": map[string]any{
			"x": float64(clip.X) / scale, "y": float64(clip.Y) / scale,
			"width": float64(clip.W) / scale, "height": float64(clip.H) / scale, "scale": 1,
		},
	}, &shot); err != nil {
		return nil, err
	}
	if err := s.p.err(); err != nil {
		return nil, err
	}
	png, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return nil, fmt.Errorf("capture: %w", err)
	}
	return png, nil
}

// callRuntime calls a function on window.__cfMotion with JSON arguments (passed as values, never spliced into code), waits
// for its promise and decodes its JSON result.
func (s *session) callRuntime(ctx context.Context, fn string, args []any, out any) error {
	cargs := make([]map[string]any, len(args))
	for i, a := range args {
		cargs[i] = map[string]any{"value": a}
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := s.call(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId": s.obj, "functionDeclaration": fn, "arguments": cargs, "awaitPromise": true, "returnByValue": true,
	}, &r); err != nil {
		return err
	}
	if r.ExceptionDetails != nil {
		d := r.ExceptionDetails.Exception.Description
		if d == "" {
			d = r.ExceptionDetails.Text
		}
		return fmt.Errorf("the page threw: %s", truncate(d, 400))
	}
	if out != nil && len(r.Result.Value) > 0 {
		return json.Unmarshal(r.Result.Value, out)
	}
	return nil
}

// loadFonts reads the job's font files from dir: plain names inside it, regular files of at most 4 MB.
func loadFonts(dir string, fonts []Font) ([]pageFont, error) {
	out := make([]pageFont, 0, len(fonts))
	for _, f := range fonts {
		data, err := readCapped(filepath.Join(dir, f.File), 4<<20)
		if err != nil {
			return nil, fmt.Errorf("font %s: %w", f.Family, err)
		}
		out = append(out, pageFont{Family: f.Family, Data: base64.StdEncoding.EncodeToString(data)})
	}
	return out, nil
}

// readCapped reads a regular file of at most limit bytes.
func readCapped(name string, limit int64) ([]byte, error) {
	st, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%s is %d bytes, over %d", name, st.Size(), limit)
	}
	return os.ReadFile(name) //nolint:gosec // a path given by the worker, checked above
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
