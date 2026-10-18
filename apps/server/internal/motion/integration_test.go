package motion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// These tests need Docker and a built media-tools image (Chrome and cf-motion are in it), so they only run when asked; CI's
// media-tools job does:
//
//	go test ./internal/motion -run Integration -docker-image media-tools:dev [-docker-runtime runsc] [-update]
var (
	dockerImage   = flag.String("docker-image", "", "media-tools image for the motion integration tests")
	dockerRuntime = flag.String("docker-runtime", "", "container runtime, for example runsc")
)

// goldenPath holds the golden hashes of every template's frames, per platform (architecture and Chrome version): Skia's software
// raster is deterministic on one platform, not across them.
const goldenPath = "testdata/golden-hashes.json"

type goldens struct {
	// Bundle is the bundle the hashes were made with; a template change makes them stale (rerun with -update and review the frames).
	Bundle    string                            `json:"bundle"`
	Platforms map[string]map[string]goldenEntry `json:"platforms"`
}

type goldenEntry struct {
	Frames int64  `json:"frames"`
	SHA256 string `json:"sha256"` // over the frames' own SHA-256s, in order
}

func dockerRunner(t *testing.T) (media.Runner, string) {
	t.Helper()
	if *dockerImage == "" {
		t.Skip("set -docker-image to run the motion integration tests")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := sandbox.NewDocker(sandbox.Policy{AllowedRoots: []string{root}})
	d.Runtime = *dockerRuntime
	return &media.SandboxRunner{Provider: d, Image: *dockerImage, Limits: DefaultLimits(), Labels: map[string]string{"kind": "motion-test"}}, root
}

// renderJob renders a job in a fresh work directory under root and returns the work directory and the manifest.
func renderJob(t *testing.T, runner media.Runner, root, name string, job Job) (string, Report, Manifest) {
	t.Helper()
	work := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(work, "bundle"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := Render(context.Background(), runner, job, work, filepath.Join(work, "bundle"), Options{InImage: true})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	m, err := ReadManifest(filepath.Join(work, OutDir), job)
	if err != nil {
		t.Fatal(err)
	}
	return work, rep, m
}

func combined(m Manifest) (int64, string) {
	h := sha256.New()
	var n int64
	for _, s := range m.Segments {
		for _, f := range s.SHA256 {
			_, _ = h.Write([]byte(f))
			n++
		}
	}
	return n, hex.EncodeToString(h.Sum(nil))
}

// Every template renders, twice to the same bytes, with ink only inside its box (every frame is decoded and checked), and to the
// golden hashes of the platform when there are some.
func TestIntegrationTemplatesInDocker(t *testing.T) {
	runner, root := dockerRunner(t)
	var gold goldens
	if data, err := os.ReadFile(goldenPath); err == nil {
		if err := json.Unmarshal(data, &gold); err != nil {
			t.Fatal(err)
		}
	}
	if gold.Platforms == nil {
		gold.Platforms = map[string]map[string]goldenEntry{}
	}
	got := map[string]goldenEntry{}
	var chrome string
	for _, size := range [][2]int{{1080, 1920}} { // the only size a job has: the output's own (BuildJob)
		for _, it := range sampleItems() {
			testTemplate(t, runner, root, it, size, got, &chrome)
		}
	}
	checkGoldens(t, gold, got, chrome)
}

func testTemplate(t *testing.T, runner media.Runner, root string, it timeline.GraphicsItem, size [2]int, got map[string]goldenEntry, chrome *string) {
	t.Helper()
	{
		it.AtMs, it.Params.AccentsMs = 0, shift(it.Params.AccentsMs, -it.AtMs)
		r := resolveAll(t, []timeline.GraphicsItem{it})
		job, err := BuildJob(out916, 30, timeline.Frame(it.DurationMs, 30), r)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("%s-%d", it.ID, size[0])
		work, rep, m := renderJob(t, runner, root, name+"-a", job)
		_, _, m2 := renderJob(t, runner, root, name+"-b", job)
		n, sum := combined(m)
		if _, sum2 := combined(m2); sum2 != sum {
			var diff []int
			for i, h := range m.Segments[0].SHA256 {
				if m2.Segments[0].SHA256[i] != h {
					diff = append(diff, i)
				}
			}
			t.Errorf("%s (%s) at %dx%d: two renders of the same job differ in frames %v", it.ID, it.Template, size[0], size[1], diff)
		}
		*chrome = rep.Arch + "/" + rep.Chrome
		got[fmt.Sprintf("%s@%d", it.Template, size[0])] = goldenEntry{Frames: n, SHA256: sum}
		checkInkInBox(t, work, job)
		if *update {
			writePreview(t, work, job, r[0])
		}
	}
}

func checkGoldens(t *testing.T, gold goldens, got map[string]goldenEntry, platform string) {
	t.Helper()
	if *update {
		if gold.Bundle != BundleHash() {
			gold.Platforms = map[string]map[string]goldenEntry{}
		}
		gold.Bundle = BundleHash()
		gold.Platforms[platform] = got
		data, _ := json.MarshalIndent(gold, "", "  ")
		if err := os.WriteFile(goldenPath, append(data, '\n'), 0o644); err != nil { //nolint:gosec // test data
			t.Fatal(err)
		}
		return
	}
	want, ok := gold.Platforms[platform]
	switch {
	case gold.Bundle != BundleHash():
		t.Fatalf("the golden hashes were made with another template bundle: render the templates, look at the frames, then rerun with -update")
	case !ok && os.Getenv("CF_MOTION_REQUIRE_GOLDEN") != "":
		data, _ := json.Marshal(got)
		t.Fatalf("no golden hashes for %s (CF_MOTION_REQUIRE_GOLDEN is set); these are this platform's, to add under platforms[%q] after a review: %s", platform, platform, data)
	case !ok:
		t.Logf("no golden hashes for %s; rerun with -update to record them", platform)
		return
	}
	for name, g := range got {
		if want[name] != g {
			t.Errorf("%s: frames %d sha256 %s, golden %d %s (rerun with -update if the change is meant)", name, g.Frames, g.SHA256, want[name].Frames, want[name].SHA256)
		}
	}
}

func shift(xs []int64, d int64) []int64 {
	out := slices.Clone(xs)
	for i := range out {
		out[i] += d
	}
	return out
}

// writePreview writes the template's still preview (previews/<template>.png, what list_motion_templates shows the editor): the frame
// where it is fully on screen, its box and a margin, over a dark grey, at half size.
func writePreview(t *testing.T, work string, job Job, r Resolved) {
	t.Helper()
	at := r.InkTimesMs()[0]
	s := job.Segments[0]
	n := timeline.Frame(at, job.FPS) - s.StartFrame
	data, err := os.ReadFile(filepath.Join(work, OutDir, "s0", fmt.Sprintf("f%06d.png", n))) //nolint:gosec // test output
	if err != nil {
		t.Fatal(err)
	}
	frame, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	pv := Preview(frame)
	var buf bytes.Buffer
	if err := png.Encode(&buf, pv); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("previews", r.Item.Template+".png"), buf.Bytes(), 0o644); err != nil { //nolint:gosec // test data
		t.Fatal(err)
	}
}

// checkInkInBox decodes every frame and requires the ink (alpha over 24) to be inside the box of an item on screen, grown by the spill
// margin.
func checkInkInBox(t *testing.T, work string, job Job) {
	t.Helper()
	scale := float64(job.Width) / float64(job.CSSWidth)
	margin := int(SpillMargin*float64(job.CSSHeight)*scale) + 1
	for k, s := range job.Segments {
		for i := int64(0); i < s.Frames(); i++ {
			data, err := os.ReadFile(filepath.Join(work, OutDir, fmt.Sprintf("s%d", k), fmt.Sprintf("f%06d.png", i))) //nolint:gosec // test output
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			ms := float64(s.StartFrame+i) * 1000 / float64(job.FPS)
			var boxes []image.Rectangle
			for _, it := range job.Items {
				if ms >= float64(it.StartMs) && ms < float64(it.EndMs) {
					boxes = append(boxes, image.Rect(int(it.Box.X*scale)-margin, int(it.Box.Y*scale)-margin,
						int((it.Box.X+it.Box.W)*scale)+margin, int((it.Box.Y+it.Box.H)*scale)+margin))
				}
			}
			b := img.Bounds()
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					if _, _, _, a := img.At(x, y).RGBA(); a>>8 <= 24 {
						continue
					}
					pt := image.Pt(s.Clip.X+x-b.Min.X, s.Clip.Y+y-b.Min.Y)
					if !slices.ContainsFunc(boxes, pt.In) {
						t.Fatalf("frame %d of %s: ink at %v is outside every box %v", s.StartFrame+i, job.Items[0].Template, pt, boxes)
					}
				}
			}
		}
	}
}

// hostileRefusals is the reason each hostile scene must fail with: which guard caught it. The runner's request allowlist (every
// request a clean file: path inside the bundle) is the real guard for file: URLs: on a page opened from a file: URL the CSP's 'self'
// matches every file: URL, so the csp_file_* scenes (with the scene's own CSP) get past the CSP and the allowlist refuses them. A
// data: URL never reaches the allowlist (it is not a request); the CSP alone blocks it. fetch() of a file: URL is refused by Chrome
// itself, and a WebSocket dies on the dead proxy.
var hostileRefusals = map[string]string{
	"file_fetch":          `URL scheme "file" is not supported`,
	"file_iframe":         "file:///etc/passwd, which is outside the template bundle: refused",
	"file_script":         "file:///etc/hostname, which is outside the template bundle: refused",
	"file_xhr":            "file:///etc/passwd, which is outside the template bundle: refused",
	"net_fetch":           "http://example.com/, which is outside the template bundle: refused",
	"net_image":           "https://93.184.216.34/x.png, which is outside the template bundle: refused",
	"net_websocket":       "ERR_PROXY_CONNECTION_FAILED",
	"csp_file_image":      "file:///etc/passwd, which is outside the template bundle: refused",
	"csp_file_stylesheet": "file:///etc/hostname, which is outside the template bundle: refused",
	"csp_file_font":       "file:///etc/passwd, which is outside the template bundle: refused",
	"csp_data_image":      `violates the following Content Security Policy directive: "img-src 'self'"`,
}

// The sandbox and the runner contain a page that misbehaves: a request to the network, a read of a file outside the bundle, a
// WebSocket to the DevTools port. The scenes without a CSP test the browser's own refusal and the runner's request allowlist; the
// csp_* scenes carry the scene's CSP and test which of the two guards holds (hostileRefusals). Each run must fail with that reason
// in the error, and draw nothing from outside the bundle.
func TestIntegrationHostilePagesInDocker(t *testing.T) {
	runner, root := dockerRunner(t)
	cases, err := os.ReadDir("testdata/hostile")
	if err != nil {
		t.Fatal(err)
	}
	it := sampleItems()[0]
	it.Params.AccentsMs = nil
	job, err := BuildJob(out916, 30, 30, resolveAll(t, []timeline.GraphicsItem{it}))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name(), func(t *testing.T) {
			work := filepath.Join(root, c.Name())
			if err := os.MkdirAll(filepath.Join(work, "bundle"), 0o755); err != nil {
				t.Fatal(err)
			}
			_, err := render(context.Background(), runner, job, work, filepath.Join(work, "bundle"), Options{InImage: true},
				os.DirFS(filepath.Join("testdata", "hostile", c.Name())))
			if err == nil {
				t.Fatal("the hostile page rendered without an error")
			}
			if !errors.Is(err, ErrRun) {
				t.Fatalf("not a motion run failure: %v", err)
			}
			msg := err.Error()
			t.Logf("refused: %s", firstLine(msg))
			if strings.Contains(msg, "root:x:0:0") {
				t.Fatal("the contents of /etc/passwd reached the runner's output")
			}
			if !strings.Contains(msg, "the page failed") && !strings.Contains(msg, "mount:") {
				t.Errorf("the failure does not name the page: %s", msg)
			}
			want, ok := hostileRefusals[c.Name()]
			if !ok {
				t.Fatalf("no expected refusal for %s: add it to hostileRefusals", c.Name())
			}
			if !strings.Contains(msg, want) {
				t.Errorf("refused, but not for the expected reason %q: %s", want, msg)
			}
		})
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
