package ingest

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The pipeline tests generate real media with the ffmpeg on the machine and run the pipeline on it through the local
// sandbox provider. They skip when ffmpeg is missing. With -docker-image they run the tools in the media-tools image
// instead (the clips are still generated on the host):
//
//	go test ./internal/ingest -docker-image media-tools:dev
var dockerImage = flag.String("docker-image", "", "run the pipeline's tools in this media-tools image instead of the local ffmpeg")

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// ---- fakes ----------------------------------------------------------------------------------------------------------

// fakeObjects is in-memory object storage that records every call.
type fakeObjects struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    []string
	deleted []string
	putErr  error
}

func newFakeObjects() *fakeObjects { return &fakeObjects{objects: map[string][]byte{}} }

func (f *fakeObjects) Get(_ context.Context, key string) (storage.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return storage.Object{}, storage.ErrNotFound
	}
	return storage.Object{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b))}, nil
}

func (f *fakeObjects) Put(_ context.Context, key string, body io.ReadSeeker, size int64, _ string) error {
	if f.putErr != nil {
		return f.putErr
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return fmt.Errorf("put %s: %d bytes for size %d", key, len(b), size)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = b
	f.puts = append(f.puts, key)
	return nil
}

func (f *fakeObjects) DeletePrefix(_ context.Context, prefix string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			delete(f.objects, k)
			f.deleted = append(f.deleted, k)
			n++
		}
	}
	return n, nil
}

// fakeStore is an in-memory Store with the state machine of the real one.
type fakeStore struct {
	mu          sync.Mutex
	assets      map[string]*store.Asset // by workspace/asset
	derivatives map[string][]store.NewDerivative
	results     map[string]store.IngestResult
	progress    []string
	// onFinish runs before FinishAssetIngest looks at the asset (a test deletes it there).
	onFinish func()
}

func newFakeStore() *fakeStore {
	return &fakeStore{assets: map[string]*store.Asset{}, derivatives: map[string][]store.NewDerivative{}, results: map[string]store.IngestResult{}}
}

func fkey(ws ids.WorkspaceID, id ids.AssetID) string { return ws.String() + "/" + id.String() }

func (f *fakeStore) add(a store.Asset) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assets[fkey(a.WorkspaceID, a.ID)] = &a
}

func (f *fakeStore) get(ws ids.WorkspaceID, id ids.AssetID) store.Asset {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.assets[fkey(ws, id)]
}

func (f *fakeStore) ClaimAssetForIngest(_ context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.assets[fkey(ws, id)]
	if !ok || a.DeletedAt != nil || (a.Status != store.AssetUploaded && a.Status != store.AssetProcessing && a.Status != store.AssetReady) {
		return store.Asset{}, store.ErrNotFound
	}
	a.Status = store.AssetProcessing
	return *a, nil
}

func (f *fakeStore) SetIngestProgress(_ context.Context, ws ids.WorkspaceID, id ids.AssetID, progress int, stage string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.assets[fkey(ws, id)]
	if !ok || a.DeletedAt != nil || a.Status != store.AssetProcessing {
		return store.ErrNotFound
	}
	f.progress = append(f.progress, fmt.Sprintf("%d:%s", progress, stage))
	return nil
}

func (f *fakeStore) FinishAssetIngest(_ context.Context, ws ids.WorkspaceID, id ids.AssetID, r store.IngestResult) (store.Asset, error) {
	if f.onFinish != nil {
		f.onFinish()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.assets[fkey(ws, id)]
	if !ok || a.DeletedAt != nil || a.Status != store.AssetProcessing {
		return store.Asset{}, store.ErrNotFound
	}
	a.Status = store.AssetReady
	f.results[fkey(ws, id)] = r
	f.derivatives[fkey(ws, id)] = r.Derivatives
	return *a, nil
}

func (f *fakeStore) FailAssetIngest(_ context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string) (store.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.assets[fkey(ws, id)]
	if !ok || a.DeletedAt != nil || (a.Status != store.AssetUploaded && a.Status != store.AssetProcessing) {
		return store.Asset{}, store.ErrNotFound
	}
	a.Status, a.FailureReason = store.AssetFailed, &reason
	return *a, nil
}

func (f *fakeStore) GetAssetForObjectDeletion(_ context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.assets[fkey(ws, id)]
	if !ok || (a.DeletedAt == nil && a.Status != store.AssetFailed) {
		return store.Asset{}, store.ErrNotFound
	}
	return *a, nil
}

// ---- harness --------------------------------------------------------------------------------------------------------

type harness struct {
	t       *testing.T
	root    string // the sandbox's allowed root; also holds the generated clips
	clips   string
	store   *fakeStore
	objects *fakeObjects
	p       *Pipeline
	ws      ids.WorkspaceID
}

func newWorkspaceID() ids.WorkspaceID { return ids.WorkspaceID{UUID: uuid.Must(uuid.NewV7())} }
func newAssetID() ids.AssetID         { return ids.AssetID{UUID: uuid.Must(uuid.NewV7())} }

func newHarness(t *testing.T) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var provider sandbox.Provider
	image := "unused"
	if *dockerImage != "" {
		provider, image = sandbox.NewDocker(sandbox.Policy{AllowedRoots: []string{root}}), *dockerImage
	} else {
		for _, tool := range []string{"ffmpeg", "ffprobe"} {
			if _, err := exec.LookPath(tool); err != nil {
				t.Skipf("skipping: %s is not installed (or use -docker-image)", tool)
			}
		}
		provider = sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root}})
	}
	clips := filepath.Join(root, "clips")
	if err := os.Mkdir(clips, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, root: root, clips: clips, store: newFakeStore(), objects: newFakeObjects(), ws: newWorkspaceID()}
	runner := &media.SandboxRunner{Provider: provider, Image: image, Limits: sandbox.Limits{CPU: 2, MemoryMB: 2048, Timeout: 5 * time.Minute, PIDs: 512}}
	h.p, err = New(Config{Store: h.store, Objects: h.objects, Runner: runner, TempDir: filepath.Join(root, "jobs"), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// upload puts a file in fake storage as the original of a new asset of the harness's workspace.
func (h *harness) upload(kind, path string) store.Asset {
	h.t.Helper()
	return h.uploadTo(h.ws, kind, path)
}

func (h *harness) uploadTo(ws ids.WorkspaceID, kind, path string) store.Asset {
	h.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatal(err)
	}
	id := newAssetID()
	key := storage.OriginalKey(ws, id)
	h.objects.objects[key] = data
	a := store.Asset{
		ID: id, WorkspaceID: ws, Kind: kind, Status: store.AssetUploaded, Filename: filepath.Base(path), SizeBytes: int64(len(data)), R2Key: key,
	}
	h.store.add(a)
	return a
}

// derivative returns the stored object of a kind after a run, written to a file the tests can probe.
func (h *harness) derivative(a store.Asset, kind string) (path string, row store.NewDerivative) {
	h.t.Helper()
	for _, d := range h.store.derivatives[fkey(a.WorkspaceID, a.ID)] {
		if d.Kind == kind {
			p := filepath.Join(h.clips, kind+"-"+a.ID.String())
			if err := os.WriteFile(p, h.objects.objects[d.Key], 0o644); err != nil {
				h.t.Fatal(err)
			}
			return p, d
		}
	}
	h.t.Fatalf("no %s derivative; have %v", kind, h.kinds(a))
	return "", store.NewDerivative{}
}

func (h *harness) kinds(a store.Asset) []string {
	var k []string
	for _, d := range h.store.derivatives[fkey(a.WorkspaceID, a.ID)] {
		k = append(k, d.Kind)
	}
	sort.Strings(k)
	return k
}

func (h *harness) probe(path string) media.Metadata {
	h.t.Helper()
	m, err := media.Probe(context.Background(), h.p.cfg.Runner, path)
	if err != nil {
		h.t.Fatal(err)
	}
	return m
}

func (h *harness) probeImage(path string) media.Metadata {
	h.t.Helper()
	m, err := media.ProbeImage(context.Background(), h.p.cfg.Runner, path)
	if err != nil {
		h.t.Fatal(err)
	}
	return m
}

func (h *harness) run(a store.Asset) Result {
	h.t.Helper()
	res, err := h.p.Run(context.Background(), a.WorkspaceID, a.ID)
	if err != nil {
		h.t.Fatalf("run: %v", err)
	}
	return res
}

// ---- clip generation --------------------------------------------------------------------------------------------------

type clip struct {
	name    string
	w, h    int    // stored size
	seconds int    // length
	fps     int    // default 30
	hdr     string // "", "pq" or "hlg"
	rotate  int    // display matrix rotation as ffprobe prints it (-90 is a phone held upright), 0 for none
	audio   bool   // an AAC track
	apac    bool   // plus a second audio stream with the 'apac' tag
}

// ffmpeg runs the host's ffmpeg and skips the test when it cannot do what a clip needs.
func ffmpeg(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", append([]string{"-nostdin", "-hide_banner", "-v", "error", "-y"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("skipping: this ffmpeg cannot make the test clip: %v: %s", err, out)
	}
}

func (h *harness) makeClip(c clip) string {
	h.t.Helper()
	if c.fps == 0 {
		c.fps = 30
	}
	d := strconv.Itoa(c.seconds)
	base := filepath.Join(h.clips, c.name+"-base.mp4")
	args := []string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%d:duration=%s", c.w, c.h, c.fps, d)}
	if c.audio {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration="+d)
	}
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast")
	switch c.hdr {
	case "":
		args = append(args, "-pix_fmt", "yuv420p")
	case "pq":
		args = append(args, "-vf", "format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc", "-profile:v", "high10", "-pix_fmt", "yuv420p10le")
	case "hlg":
		args = append(args, "-vf", "format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc", "-profile:v", "high10", "-pix_fmt", "yuv420p10le")
	}
	if c.audio {
		args = append(args, "-c:a", "aac")
	}
	ffmpeg(h.t, append(args, base)...)

	out := filepath.Join(h.clips, c.name+".mov")
	remux := []string{}
	if c.rotate != 0 {
		remux = append(remux, "-display_rotation:v:0", strconv.Itoa(c.rotate))
	}
	remux = append(remux, "-i", base, "-map", "0:v:0")
	if c.audio {
		remux = append(remux, "-map", "0:a:0")
		if c.apac {
			remux = append(remux, "-map", "0:a:0")
		}
	}
	remux = append(remux, "-c", "copy", "-strict", "unofficial")
	if c.apac {
		remux = append(remux, "-tag:a:1", "apac") // the second audio stream claims to be Apple's spatial-audio codec
	}
	ffmpeg(h.t, append(remux, out)...)
	return out
}

// keyframes returns the presentation times of the video's keyframes.
func keyframes(t *testing.T, path string) []float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var ks []float64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ts, flags, ok := strings.Cut(line, ",")
		if ok && strings.HasPrefix(flags, "K") {
			f, err := strconv.ParseFloat(ts, 64)
			if err != nil {
				t.Fatal(err)
			}
			ks = append(ks, f)
		}
	}
	return ks
}

// moovBeforeMdat reports whether an MP4's index comes before its media data (faststart).
func moovBeforeMdat(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var off int64
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			t.Fatalf("no mdat/moov in %s", path)
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		switch string(hdr[4:]) {
		case "moov":
			return true
		case "mdat":
			return false
		}
		if size < 8 {
			t.Fatalf("odd atom size %d at %d", size, off)
		}
		off += size
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			t.Fatal(err)
		}
	}
}

// ---- tests ------------------------------------------------------------------------------------------------------------

// checkProxy asserts what every proxy must be: upright, SDR BT.709, 8-bit 4:2:0 H.264, a keyframe every second, the index
// up front, and nothing left to rotate.
func (h *harness) checkProxy(a store.Asset, wantW, wantH int, wantAudio bool) media.Metadata {
	h.t.Helper()
	path, row := h.derivative(a, store.DerivativeProxy720)
	m := h.probe(path)
	if m.Codecs.Video != "h264" || m.PixFmt != "yuv420p" || m.Width != wantW || m.Height != wantH || m.Rotation != 0 || m.HDR {
		h.t.Fatalf("proxy: %+v", m)
	}
	if m.CodedWidth != wantW || m.CodedHeight != wantH {
		h.t.Fatalf("proxy is stored %dx%d, want %dx%d (no rotation flag may be needed)", m.CodedWidth, m.CodedHeight, wantW, wantH)
	}
	if m.ColorTransfer != "bt709" || m.ColorPrimaries != "bt709" || m.ColorSpace != "bt709" {
		h.t.Fatalf("proxy colour is %s/%s/%s, want bt709", m.ColorTransfer, m.ColorPrimaries, m.ColorSpace)
	}
	if wantAudio {
		if len(m.AudioStreams) != 1 || m.AudioStreams[0].Codec != "aac" || !m.AudioStreams[0].Decodable || m.SelectedAudio == nil {
			h.t.Fatalf("proxy audio: %+v", m.AudioStreams)
		}
	} else if len(m.AudioStreams) != 0 {
		h.t.Fatalf("proxy has audio streams %+v, want none", m.AudioStreams)
	}
	if !moovBeforeMdat(h.t, path) {
		h.t.Fatal("proxy is not faststart")
	}
	ks := keyframes(h.t, path)
	if len(ks) < 2 {
		h.t.Fatalf("proxy keyframes: %v", ks)
	}
	for i := 1; i < len(ks); i++ {
		if gap := ks[i] - ks[i-1]; gap < 0.9 || gap > 1.1 {
			h.t.Fatalf("keyframes %v: gap %v is not about a second", ks, gap)
		}
	}
	if row.ContentType != "video/mp4" || row.SizeBytes <= 0 {
		h.t.Fatalf("row %+v", row)
	}
	return m
}

func TestIngestPortraitPhoneClipWithAPAC(t *testing.T) {
	h := newHarness(t)
	// A phone held upright: stored 1920x1080 with a -90 degree display matrix, an AAC track and an 'apac' one.
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "iphone", w: 1920, h: 1080, seconds: 12, rotate: -90, audio: true, apac: true}))
	src := h.probe(filepath.Join(h.clips, "iphone.mov"))
	if src.Width != 1080 || src.Height != 1920 || src.Rotation != -90 || len(src.AudioStreams) != 2 || src.AudioStreams[1].Decodable {
		t.Fatalf("test clip: %+v", src)
	}

	res := h.run(a)
	if res.Status != store.AssetReady {
		t.Fatalf("%+v", res)
	}
	got := h.checkProxy(a, 720, 1280, true)
	if got.DurationMs < 11500 || got.DurationMs > 12500 {
		t.Fatalf("proxy duration %d ms", got.DurationMs)
	}

	want := []string{"audio_16k", "hover_preview", "poster", "proxy_720", "sprite", "sprite_vtt", "waveform"}
	if k := h.kinds(a); strings.Join(k, ",") != strings.Join(want, ",") {
		t.Fatalf("derivatives %v, want %v", k, want)
	}
	for _, d := range h.store.derivatives[fkey(a.WorkspaceID, a.ID)] {
		if !strings.HasPrefix(d.Key, storage.AssetPrefix(a.WorkspaceID, a.ID)) || d.Key == a.R2Key {
			t.Errorf("key %q is not a derivative key of the asset", d.Key)
		}
	}

	r := h.store.results[fkey(a.WorkspaceID, a.ID)]
	if r.Width != 1080 || r.Height != 1920 || r.Rotation != 90 || r.VideoCodec != "h264" || r.AudioCodec != "aac" || !r.HasAudio || r.HDR || r.FPS < 29 || r.DurationMs < 11500 {
		t.Fatalf("asset result %+v", r)
	}
	var probe media.Metadata
	if err := json.Unmarshal(r.Probe, &probe); err != nil || probe.SelectedAudio == nil || *probe.SelectedAudio != 1 {
		t.Fatalf("stored probe: %v %s", err, r.Probe)
	}

	// Poster: a JPEG of the proxy's size.
	pp, _ := h.derivative(a, store.DerivativePoster)
	if pm := h.probeImage(pp); pm.Width != 720 || pm.Height != 1280 {
		t.Fatalf("poster %+v", pm)
	}

	// Sprite sheet and its VTT: 13 tiles (12.04 s, one per second) of 160x284 in a 10-wide grid, and a cue per tile pointing into it.
	sp, spRow := h.derivative(a, store.DerivativeSprite)
	if sm := h.probeImage(sp); sm.CodedWidth != 1600 || sm.Height != 568 {
		t.Fatalf("sprite %+v (%s)", sm, spRow.Meta)
	}
	vp, _ := h.derivative(a, store.DerivativeSpriteVTT)
	vtt, _ := os.ReadFile(vp)
	if !strings.HasPrefix(string(vtt), "WEBVTT\n") || strings.Count(string(vtt), "-->") != 13 || !strings.Contains(string(vtt), "sprite.jpg#xywh=160,0,160,284") ||
		!strings.Contains(string(vtt), "sprite.jpg#xywh=160,284,160,284") {
		t.Fatalf("vtt:\n%s", vtt)
	}

	// Waveform: peaks of the 440 Hz sine, nowhere near silent.
	wp, _ := h.derivative(a, store.DerivativeWaveform)
	var wf Waveform
	b, _ := os.ReadFile(wp)
	if err := json.Unmarshal(b, &wf); err != nil || wf.Version != 1 || len(wf.Peaks) != 1000 || wf.DurationMs < 11500 {
		t.Fatalf("waveform: %v %d peaks %+v", err, len(wf.Peaks), wf.DurationMs)
	}
	var loud int
	for _, p := range wf.Peaks {
		if p < 0 || p > 1 {
			t.Fatalf("peak %v", p)
		}
		if p > 0.1 {
			loud++
		}
	}
	if loud < 900 {
		t.Fatalf("only %d of 1000 peaks are above 0.1", loud)
	}

	// audio_16k.wav: mono 16 kHz PCM.
	ap, _ := h.derivative(a, store.DerivativeAudio16k)
	am := h.probe(ap)
	if len(am.AudioStreams) != 1 || am.AudioStreams[0].Channels != 1 || am.AudioStreams[0].SampleRate != 16000 || am.AudioStreams[0].Codec != "pcm_s16le" {
		t.Fatalf("audio_16k %+v", am.AudioStreams)
	}

	// Hover preview: muted, 360x640, about 8 s, faststart and small.
	hp, hrow := h.derivative(a, store.DerivativeHoverPreview)
	hm := h.probe(hp)
	if hm.Width != 360 || hm.Height != 640 || len(hm.AudioStreams) != 0 || hm.Codecs.Video != "h264" || hm.DurationMs < 7500 || hm.DurationMs > 8500 {
		t.Fatalf("hover %+v", hm)
	}
	if hrow.SizeBytes >= 1<<20 || !moovBeforeMdat(t, hp) {
		t.Fatalf("hover is %d bytes (faststart %v)", hrow.SizeBytes, moovBeforeMdat(t, hp))
	}

	// Progress went forward through every stage, and the job directory is gone.
	if len(h.store.progress) < 6 || h.store.progress[0] != "2:downloading" {
		t.Fatalf("progress %v", h.store.progress)
	}
	last := 0
	for _, p := range h.store.progress {
		n, _ := strconv.Atoi(strings.SplitN(p, ":", 2)[0])
		if n < last {
			t.Fatalf("progress went backwards: %v", h.store.progress)
		}
		last = n
	}
	if entries, _ := os.ReadDir(filepath.Join(h.root, "jobs")); len(entries) != 0 {
		t.Fatalf("job directory left behind: %v", entries)
	}
}

func TestIngestHDRPortraitBecomesSDR(t *testing.T) {
	for _, hdr := range []string{"hlg", "pq"} {
		t.Run(hdr, func(t *testing.T) {
			h := newHarness(t)
			a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: hdr, w: 1280, h: 720, seconds: 4, hdr: hdr, rotate: -90, audio: true}))
			src := h.probe(filepath.Join(h.clips, hdr+".mov"))
			if !src.HDR || src.HDRFormat != hdr || src.Width != 720 || src.Height != 1280 || src.Rotation != -90 {
				t.Fatalf("test clip %+v", src)
			}
			if res := h.run(a); res.Status != store.AssetReady {
				t.Fatalf("%+v", res)
			}
			h.checkProxy(a, 720, 1280, true)
			if r := h.store.results[fkey(a.WorkspaceID, a.ID)]; !r.HDR || r.Width != 720 || r.Height != 1280 {
				t.Fatalf("result %+v", r)
			}
			// The picture is not washed out or crushed: mean luma of the proxy is in a normal range.
			path, _ := h.derivative(a, store.DerivativeProxy720)
			out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-i", path, "-vf", "signalstats,metadata=print:key=lavfi.signalstats.YAVG:file=-", "-f", "null", "-").CombinedOutput()
			if err != nil {
				t.Fatal(err, string(out))
			}
			var sum float64
			var n int
			for _, line := range strings.Split(string(out), "\n") {
				if _, v, ok := strings.Cut(line, "lavfi.signalstats.YAVG="); ok {
					f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
					sum += f
					n++
				}
			}
			if n == 0 || sum/float64(n) < 40 || sum/float64(n) > 220 {
				t.Fatalf("mean luma %v over %d frames", sum/float64(max(n, 1)), n)
			}
		})
	}
}

func TestIngestShortClipWithoutAudio(t *testing.T) {
	h := newHarness(t)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "short", w: 640, h: 360, seconds: 3, audio: false}))
	if res := h.run(a); res.Status != store.AssetReady {
		t.Fatalf("%+v", res)
	}
	h.checkProxy(a, 640, 360, false) // smaller than 720 on the short side: never scaled up
	if k := strings.Join(h.kinds(a), ","); k != "hover_preview,poster,proxy_720,sprite,sprite_vtt" {
		t.Fatalf("derivatives %s", k)
	}
	hp, _ := h.derivative(a, store.DerivativeHoverPreview)
	hm := h.probe(hp)
	if hm.Width != 640 || hm.Height != 360 || hm.DurationMs < 2800 || hm.DurationMs > 3300 { // not scaled up either
		t.Fatalf("a clip under 8 s is used whole: %+v", hm)
	}
	r := h.store.results[fkey(a.WorkspaceID, a.ID)]
	if r.HasAudio || r.AudioCodec != "" || r.Rotation != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestIngestOnlyUndecodableAudioMeansNoAudio(t *testing.T) {
	h := newHarness(t)
	// One audio stream and it is the 'apac' one.
	base := h.makeClip(clip{name: "apaconly-src", w: 320, h: 240, seconds: 3, audio: true})
	out := filepath.Join(h.clips, "apaconly.mov")
	ffmpeg(t, "-i", base, "-map", "0:v:0", "-map", "0:a:0", "-c", "copy", "-strict", "unofficial", "-tag:a:0", "apac", out)
	a := h.upload(store.AssetKindVideo, out)
	if res := h.run(a); res.Status != store.AssetReady {
		t.Fatalf("%+v", res)
	}
	h.checkProxy(a, 320, 240, false)
	if r := h.store.results[fkey(a.WorkspaceID, a.ID)]; r.HasAudio || r.AudioCodec != "" {
		t.Fatalf("%+v", r)
	}
	if k := strings.Join(h.kinds(a), ","); strings.Contains(k, "audio_16k") || strings.Contains(k, "waveform") {
		t.Fatalf("derivatives %s", k)
	}
}

func TestIngestFailures(t *testing.T) {
	h := newHarness(t)
	write := func(name string, data []byte) string {
		p := filepath.Join(h.clips, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	junk := bytes.Repeat([]byte("this is not a video. "), 5000)
	mp3 := filepath.Join(h.clips, "tone.mp3")
	ffmpeg(t, "-f", "lavfi", "-i", "sine=duration=2", mp3)
	good := h.makeClip(clip{name: "good", w: 320, h: 240, seconds: 2, audio: true})
	goodBytes, _ := os.ReadFile(good)
	playlist := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n/etc/passwd\n#EXT-X-ENDLIST\n"

	for _, c := range []struct {
		name, kind, path, reason string
	}{
		{"random bytes", store.AssetKindVideo, write("junk.mp4", junk), ""},
		{"a playlist that points at another file", store.AssetKindVideo, write("list.m3u8", []byte(playlist)), ""},
		{"audio uploaded as video", store.AssetKindVideo, mp3, store.FailureNoVideo},
		{"random bytes as audio", store.AssetKindAudio, write("junk2.mp3", junk), ""},
		{"truncated video", store.AssetKindVideo, write("cut.mov", goodBytes[:len(goodBytes)/3]), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := h.upload(c.kind, c.path)
			before := len(h.objects.puts)
			res := h.run(a)
			if res.Status != store.AssetFailed || res.Reason == "" {
				t.Fatalf("%+v", res)
			}
			if c.reason != "" && res.Reason != c.reason {
				t.Errorf("reason %q, want %q", res.Reason, c.reason)
			}
			if c.reason == "" && res.Reason != store.FailureCorruptMedia && res.Reason != store.FailureUnsupportedFormat && res.Reason != store.FailureUndecodableVideo {
				t.Errorf("reason %q is not about the file", res.Reason)
			}
			got := h.store.get(a.WorkspaceID, a.ID)
			if got.Status != store.AssetFailed || got.FailureReason == nil || *got.FailureReason != res.Reason {
				t.Fatalf("asset %+v", got)
			}
			if len(h.objects.puts) != before || len(h.store.derivatives[fkey(a.WorkspaceID, a.ID)]) != 0 {
				t.Fatal("a failed ingest wrote derivatives")
			}
			// The reason is a short code, never a path or tool output.
			if strings.ContainsAny(res.Reason, " /\\:") {
				t.Fatalf("reason %q is not a plain code", res.Reason)
			}
		})
	}
}

func TestIngestRunsAgainAndReplacesDerivatives(t *testing.T) {
	h := newHarness(t)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "again", w: 640, h: 360, seconds: 3, audio: true}))
	h.run(a)
	first := append([]string(nil), h.objects.puts...)
	rows1 := append([]store.NewDerivative(nil), h.store.derivatives[fkey(a.WorkspaceID, a.ID)]...)
	if len(rows1) != 7 {
		t.Fatalf("%d derivatives", len(rows1))
	}

	// The asset is ready; a second run (a retry after a crash at the very end, or an explicit re-run) does the whole job again
	// and lands on the same keys.
	if res := h.run(a); res.Status != store.AssetReady {
		t.Fatalf("%+v", res)
	}
	rows2 := h.store.derivatives[fkey(a.WorkspaceID, a.ID)]
	if len(rows2) != 7 {
		t.Fatalf("%d derivatives", len(rows2))
	}
	keys := func(rs []store.NewDerivative) string {
		var k []string
		for _, r := range rs {
			k = append(k, r.Key)
		}
		sort.Strings(k)
		return strings.Join(k, "\n")
	}
	if keys(rows1) != keys(rows2) {
		t.Fatalf("keys changed between runs:\n%s\n--\n%s", keys(rows1), keys(rows2))
	}
	if len(h.objects.puts) != 2*len(first) {
		t.Fatalf("puts %d, want %d", len(h.objects.puts), 2*len(first))
	}
	// Exactly the original plus seven derivatives are in the bucket.
	if n := len(h.objects.objects); n != 8 {
		t.Fatalf("%d objects", n)
	}

	// A deleted or failed asset is not ingested at all.
	h.store.assets[fkey(a.WorkspaceID, a.ID)].Status = store.AssetFailed
	if _, err := h.p.Run(context.Background(), a.WorkspaceID, a.ID); !errors.Is(err, ErrAssetGone) {
		t.Fatalf("a failed asset: %v", err)
	}
}

func TestIngestSkipsDeletedAndOtherWorkspacesAssets(t *testing.T) {
	h := newHarness(t)
	clipPath := h.makeClip(clip{name: "tenant", w: 320, h: 240, seconds: 2, audio: true})
	other := newWorkspaceID()
	b := h.uploadTo(other, store.AssetKindVideo, clipPath) // workspace B's asset
	puts := len(h.objects.puts)

	// A job for workspace A naming workspace B's asset finds nothing, touches nothing and writes nothing.
	if _, err := h.p.Run(context.Background(), h.ws, b.ID); !errors.Is(err, ErrAssetGone) {
		t.Fatalf("got %v", err)
	}
	if len(h.objects.puts) != puts || h.store.get(other, b.ID).Status != store.AssetUploaded || len(h.store.progress) != 0 {
		t.Fatal("the job for the wrong workspace did something")
	}

	// An asset deleted while it waited is skipped.
	a := h.upload(store.AssetKindVideo, clipPath)
	now := time.Now()
	h.store.assets[fkey(a.WorkspaceID, a.ID)].DeletedAt = &now
	if _, err := h.p.Run(context.Background(), a.WorkspaceID, a.ID); !errors.Is(err, ErrAssetGone) {
		t.Fatalf("got %v", err)
	}
	if len(h.objects.puts) != puts {
		t.Fatal("a deleted asset was ingested")
	}
}

func TestIngestAssetDeletedWhileProcessingLeavesNoObjects(t *testing.T) {
	h := newHarness(t)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "racing", w: 320, h: 240, seconds: 2, audio: true}))
	h.store.onFinish = func() {
		now := time.Now()
		h.store.assets[fkey(a.WorkspaceID, a.ID)].DeletedAt = &now
	}
	if _, err := h.p.Run(context.Background(), a.WorkspaceID, a.ID); !errors.Is(err, ErrAssetGone) {
		t.Fatalf("got %v", err)
	}
	// The derivatives went up before the commit found the asset gone; they are removed again, with the original.
	if len(h.objects.puts) == 0 || len(h.objects.objects) != 0 {
		t.Fatalf("puts %d, objects left %d", len(h.objects.puts), len(h.objects.objects))
	}
}

func TestIngestStorageFailureIsRetried(t *testing.T) {
	h := newHarness(t)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "flaky", w: 320, h: 240, seconds: 2, audio: true}))
	h.objects.putErr = errors.New("storage is down")
	_, err := h.p.Run(context.Background(), a.WorkspaceID, a.ID)
	if err == nil || errors.Is(err, ErrAssetGone) {
		t.Fatalf("got %v", err)
	}
	if got := h.store.get(a.WorkspaceID, a.ID); got.Status != store.AssetProcessing {
		t.Fatalf("a storage failure must leave the asset processing for the retry, got %s", got.Status)
	}
	h.objects.putErr = nil
	if res := h.run(a); res.Status != store.AssetReady {
		t.Fatalf("%+v", res)
	}
}

func TestIngestAudioAndImageAssets(t *testing.T) {
	h := newHarness(t)
	mp3 := filepath.Join(h.clips, "voice.mp3")
	ffmpeg(t, "-f", "lavfi", "-i", "sine=duration=3", mp3)
	a := h.upload(store.AssetKindAudio, mp3)
	if res := h.run(a); res.Status != store.AssetReady {
		t.Fatalf("%+v", res)
	}
	if k := strings.Join(h.kinds(a), ","); k != "audio_16k,waveform" {
		t.Fatalf("audio derivatives %s", k)
	}
	if r := h.store.results[fkey(a.WorkspaceID, a.ID)]; !r.HasAudio || r.DurationMs < 2900 || r.Width != 0 {
		t.Fatalf("%+v", r)
	}

	png := filepath.Join(h.clips, "still.png")
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc2=size=1600x900", "-frames:v", "1", png)
	b := h.upload(store.AssetKindImage, png)
	if res := h.run(b); res.Status != store.AssetReady {
		t.Fatalf("%+v", res)
	}
	if k := strings.Join(h.kinds(b), ","); k != "poster" {
		t.Fatalf("image derivatives %s", k)
	}
	pp, _ := h.derivative(b, store.DerivativePoster)
	if pm := h.probeImage(pp); pm.Width != 1280 || pm.Height != 720 {
		t.Fatalf("poster %+v", pm)
	}
	if r := h.store.results[fkey(b.WorkspaceID, b.ID)]; r.Width != 1600 || r.Height != 900 || r.HasAudio {
		t.Fatalf("%+v", r)
	}
}

func TestIngestRefusesAKeyThatIsNotTheAssets(t *testing.T) {
	h := newHarness(t)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "swapped", w: 320, h: 240, seconds: 2}))
	// A row whose r2_key points at another workspace's object is not followed.
	h.store.assets[fkey(a.WorkspaceID, a.ID)].R2Key = storage.OriginalKey(newWorkspaceID(), newAssetID())
	if _, err := h.p.Run(context.Background(), a.WorkspaceID, a.ID); !errors.Is(err, ErrAssetGone) {
		t.Fatalf("got %v", err)
	}
	if got := h.store.get(a.WorkspaceID, a.ID); got.Status != store.AssetFailed {
		t.Fatalf("status %s", got.Status)
	}
	if len(h.objects.puts) != 0 {
		t.Fatal("wrote objects")
	}
}

func TestScopedObjectsStayInTheAssetsPrefix(t *testing.T) {
	ws, other := newWorkspaceID(), newWorkspaceID()
	asset := newAssetID()
	objs := newFakeObjects()
	s, err := newScopedObjects(objs, ws, asset, storage.OriginalKey(ws, asset))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	body := bytes.NewReader([]byte("x"))
	key, err := s.put(ctx, "poster.jpg", body, 1, "image/jpeg")
	if err != nil || key != storage.AssetPrefix(ws, asset)+"poster.jpg" {
		t.Fatalf("%q %v", key, err)
	}
	for _, name := range []string{"", "original", "../x", "a/b", "/abs", "..", ".hidden", "a b", "ws/" + other.String() + "/assets/" + asset.String() + "/poster.jpg", strings.Repeat("a", 65)} {
		if _, err := s.put(ctx, name, body, 1, "x"); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
	if len(objs.puts) != 1 {
		t.Fatalf("puts %v", objs.puts)
	}
	// The key on the row must be the one the ids give.
	for _, bad := range []string{"", storage.OriginalKey(other, asset), storage.OriginalKey(ws, newAssetID()), storage.AssetPrefix(ws, asset) + "poster.jpg"} {
		if _, err := newScopedObjects(objs, ws, asset, bad); err == nil {
			t.Errorf("original key %q was accepted", bad)
		}
	}
}

func TestSandboxOutputIsNotTrusted(t *testing.T) {
	r := &run{dir: t.TempDir()}
	if err := os.Mkdir(filepath.Join(r.dir, "derived"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(out, "poster.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.adopt(out, "poster.jpg", 1<<20); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("a symlink was adopted: %v", err)
	}
	out = t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "poster.jpg"), make([]byte, 2000), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.adopt(out, "poster.jpg", 1000); err == nil {
		t.Fatal("an oversized file was adopted")
	}
	out = t.TempDir()
	if _, _, err := r.adopt(out, "poster.jpg", 1000); !errors.Is(err, errNoOutput) {
		t.Fatalf("a missing file: %v", err)
	}
}
