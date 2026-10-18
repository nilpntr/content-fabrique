package motion

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

func rect(l, t, r, b int) style.Rect { return style.Rect{Left: l, Top: t, Right: r, Bottom: b} }

var update = flag.Bool("update", false, "rewrite the runner's contract job (packages/media-tools/motion-runner/testdata/job.json) and the golden hashes")

var out916 = timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"}

func ptr64(v float64) *float64 { return &v }
func ptrI(v int) *int          { return &v }

// sampleItems is one item of every template, as the valid fixture v48 has them.
func sampleItems() []timeline.GraphicsItem {
	return []timeline.GraphicsItem{
		{ID: "g1", Template: timeline.TemplateTitlePop, AtMs: 0, DurationMs: 2400, Anchor: "upper_third",
			Params: timeline.GraphicsParams{Text: "BACK DAY 101", Style: "title_series", Fill: "#F2E9A0", Energy: "hype", AccentsMs: []int64{484, 968, 1452, 1936}}},
		{ID: "g2", Template: timeline.TemplateChapterCard, AtMs: 2400, DurationMs: 2200, Anchor: "upper_third",
			Params: timeline.GraphicsParams{Text: "SHRUGS VOOR UPPER TRAPS", Number: ptrI(1), Style: "title_series", Accent: "#FF5A1F", AccentsMs: []int64{2420, 2904}}},
		{ID: "g3", Template: timeline.TemplateChapterCard, AtMs: 4600, DurationMs: 2200, Anchor: "upper_third",
			Params: timeline.GraphicsParams{Text: "SEATED ROW", Number: ptrI(2), Layout: "two_line", Align: "left"}},
		{ID: "g4", Template: timeline.TemplateHighlightWord, AtMs: 6800, DurationMs: 2000, Anchor: "upper_third",
			Params: timeline.GraphicsParams{Text: "SQUEEZE AT THE TOP", Word: ptrI(1), Energy: "calm", Backing: "plate"}},
		{ID: "g5", Template: timeline.TemplateCounter, AtMs: 8800, DurationMs: 1800, Anchor: "center",
			Params: timeline.GraphicsParams{Text: "REPS", From: ptr64(0), To: ptr64(12)}},
		{ID: "g6", Template: timeline.TemplateLowerLabel, AtMs: 10600, DurationMs: 1600, Anchor: "lower_third",
			Params: timeline.GraphicsParams{Text: "LAT PULLDOWN"}},
		{ID: "g7", Template: timeline.TemplateBlurIn, AtMs: 12200, DurationMs: 1800, Position: &timeline.Point{X: 0.5, Y: 0.3},
			Params: timeline.GraphicsParams{Text: "how it feels", Style: "title_lowercase", Shadow: "strong"}},
		{ID: "g8", Template: timeline.TemplateOutroCard, AtMs: 14000, DurationMs: 2000, Anchor: "center",
			Params: timeline.GraphicsParams{Text: "SAVE FOR\nBACK DAY", Scale: ptr64(0.9)}},
	}
}

func resolveAll(t *testing.T, items []timeline.GraphicsItem) []Resolved {
	t.Helper()
	var out []Resolved
	for _, it := range items {
		if field, err := it.CheckParams(); err != nil {
			t.Fatalf("%s: %s: %v", it.ID, field, err)
		}
		r, err := Resolve(out916, it, nil)
		if err != nil {
			t.Fatalf("%s: %v", it.ID, err)
		}
		out = append(out, r)
	}
	return out
}

// sampleJob is the job of sampleItems at the preview size.
func sampleJob(t *testing.T) Job {
	t.Helper()
	j, err := BuildJob(out916, 30, 16*30, resolveAll(t, sampleItems()))
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestEveryTemplateIsInTheCatalogTheBundleAndTheSchema(t *testing.T) {
	names := timeline.GraphicsTemplates()
	if len(Templates()) != len(names) {
		t.Fatalf("the catalog has %d templates, the timeline %d", len(Templates()), len(names))
	}
	scene, err := fs.ReadFile(Bundle(), "scene.html")
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range names {
		tpl, ok := Lookup(n)
		if !ok || Templates()[i].Name != n {
			t.Fatalf("template %s is not in the catalog in schema order", n)
		}
		if tpl.Description == "" || tpl.Use == "" || tpl.Params == "" || tpl.Accents == "" || len(tpl.Example) == 0 {
			t.Errorf("template %s lacks its description for the editor", n)
		}
		src, err := fs.ReadFile(Bundle(), "templates/"+n+".js")
		if err != nil {
			t.Fatalf("template %s has no file in the bundle: %v", n, err)
		}
		if !bytes.Contains(src, []byte("__cfMotion.register('"+n+"', 1,")) {
			t.Errorf("templates/%s.js does not register %s version 1", n, n)
		}
		if !bytes.Contains(scene, []byte(`<script src="templates/`+n+`.js"></script>`)) {
			t.Errorf("scene.html does not load templates/%s.js", n)
		}
		if png, ok := PreviewPNG(n); !ok || !bytes.HasPrefix(png, []byte("\x89PNG")) {
			t.Errorf("template %s has no preview (go test ./internal/motion -run IntegrationTemplates -docker-image media-tools:dev -update)", n)
		}
	}
}

// The templates are our code, and the only data in them is the item's params. These are the rules that keep it that way, checked
// on the source: text only through textContent, no markup parsing, no code from strings, no network, a CSP that allows nothing but
// the bundle itself.
func TestBundleSourceRules(t *testing.T) {
	scene, _ := fs.ReadFile(Bundle(), "scene.html")
	csp := regexp.MustCompile(`http-equiv="Content-Security-Policy" content="([^"]+)"`).FindSubmatch(scene)
	if csp == nil {
		t.Fatal("scene.html has no Content-Security-Policy")
	}
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "connect-src 'none'", "object-src 'none'", "base-uri 'none'", "worker-src 'none'"} {
		if !strings.Contains(string(csp[1]), want) {
			t.Errorf("the CSP lacks %q: %s", want, csp[1])
		}
	}
	if strings.Contains(string(csp[1]), "unsafe") {
		t.Errorf("the CSP allows unsafe sources: %s", csp[1])
	}
	// Markup and code from strings, loading, and style or attributes from strings (a template sets style properties one by one,
	// as numbers it computed, and never an attribute).
	banned := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|\beval\s*\(|new Function|\bimport\s*\(|\bfetch\s*\(|XMLHttpRequest|WebSocket|` +
		`setAttribute\(|setAttributeNS\(|\.src\s*=|srcdoc|url\(|cssText|setProperty|DOMParser|createContextualFragment|RTCPeerConnection\s*\(`)
	for _, bad := range []string{`el.style.cssText = s`, `el.style.setProperty('--x', v)`, `el.setAttribute('class', c)`, `f.srcdoc = h`,
		`new DOMParser().parseFromString(h, 'text/html')`, `range.createContextualFragment(h)`, `new RTCPeerConnection()`, `el.innerHTML = t`} {
		if !banned.MatchString(bad) {
			t.Errorf("the banned list misses %q", bad)
		}
	}
	_ = fs.WalkDir(Bundle(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		src, _ := fs.ReadFile(Bundle(), p)
		for i, line := range strings.Split(string(src), "\n") {
			code := line
			if k := strings.Index(code, "//"); k >= 0 {
				code = code[:k]
			}
			if p == "runtime.js" && strings.Contains(code, "refuse(name)") {
				continue
			}
			if m := banned.FindString(code); m != "" && (p != "runtime.js" || !strings.Contains(code, "'fetch', 'XMLHttpRequest', 'WebSocket'")) {
				t.Errorf("%s:%d uses %q: templates write text with textContent and load nothing", p, i+1, m)
			}
		}
		return nil
	})
}

func TestTitlePopLandsEachWordOnItsAccent(t *testing.T) {
	it := sampleItems()[0]
	r := resolveAll(t, []timeline.GraphicsItem{it})[0]
	f := FeelOf(it)
	if len(r.Timing.EnterMs) != 3 {
		t.Fatalf("enter %v, want one per word", r.Timing.EnterMs)
	}
	for i, s := range r.Timing.EnterMs {
		peak := s + int64(math.Round(0.6*float64(f.DurMs)))
		if d := peak - (it.Params.AccentsMs[i] - it.AtMs); d < -1 || d > 1 {
			t.Errorf("word %d peaks at %d ms, its accent is at %d", i, peak, it.Params.AccentsMs[i]-it.AtMs)
		}
	}
	if len(r.Timing.HitsMs) != 1 || r.Timing.HitsMs[0] != 1936 {
		t.Errorf("the accent after the last word should be a hit: %v", r.Timing.HitsMs)
	}
}

func TestStaggersAreInTheRevealRange(t *testing.T) {
	for name, f := range Feels {
		if f.StaggerMs < 80 || f.StaggerMs > 350 {
			t.Errorf("%s stagger %d ms is outside 80 to 350", name, f.StaggerMs)
		}
	}
	it := timeline.GraphicsItem{ID: "g", Template: timeline.TemplateTitlePop, DurationMs: 3000, Anchor: "upper_third", Params: timeline.GraphicsParams{Text: "ONE TWO THREE FOUR"}}
	tim := Schedule(it, 1)
	for i := 1; i < len(tim.EnterMs); i++ {
		if d := tim.EnterMs[i] - tim.EnterMs[i-1]; d != Feels[timeline.EnergyLively].StaggerMs {
			t.Errorf("words %d and %d are %d ms apart", i-1, i, d)
		}
	}
	if tim.ReadableMs() < 1200 {
		t.Errorf("a 3 s title is readable for only %d ms", tim.ReadableMs())
	}
}

func TestEveryTemplateEntersBeforeItLeaves(t *testing.T) {
	for _, r := range resolveAll(t, sampleItems()) {
		tm := r.Timing
		if tm.FullyInMs <= 0 || tm.ExitMs <= 0 || tm.ExitMs+tm.ExitDurMs != r.Item.DurationMs {
			t.Errorf("%s (%s): timing %+v does not end with the item (%d ms)", r.Item.ID, r.Item.Template, tm, r.Item.DurationMs)
		}
		for _, s := range tm.EnterMs {
			if s < 0 || s >= r.Item.DurationMs {
				t.Errorf("%s: an entrance at %d ms is outside the item", r.Item.ID, s)
			}
		}
		for _, ink := range r.InkTimesMs() {
			if ink < r.Item.AtMs || ink >= r.Item.EndMs() {
				t.Errorf("%s: ink check at %d ms is outside the item", r.Item.ID, ink)
			}
		}
	}
}

func TestTheBoxIsTheTextItemsBox(t *testing.T) {
	for _, r := range resolveAll(t, sampleItems()) {
		pl, err := timeline.PlaceText(out916, r.Item.AsText())
		if err != nil {
			t.Fatal(err)
		}
		if pl.Box != r.Box {
			t.Errorf("%s: the overlay's box %v is not the text item's %v", r.Item.ID, r.Box, pl.Box)
		}
		zone, _ := out916.Zone()
		if !zone.Inner.Contains(r.Box) {
			t.Errorf("%s: box %v is outside the safe zone", r.Item.ID, r.Box)
		}
		if len(r.Params.Lines) == 0 || r.Params.FontPx <= 0 || r.Params.LinePx <= 0 {
			t.Errorf("%s: page params %+v", r.Item.ID, r.Params)
		}
	}
}

func TestOverlaysKeepOffTheSubject(t *testing.T) {
	it := sampleItems()[0]
	// A head right where the upper-third title goes, with room above it.
	head := rect(0, 330, 1080, 520)
	sub := []timeline.Subject{{Head: head, Body: rect(0, 330, 1080, 1920)}}
	r, err := Resolve(out916, it, sub)
	if err != nil {
		t.Fatal(err)
	}
	// The avoidance clears the ink of the letters (the empty top of the line box may reach over), as for a text item.
	if ink := r.Placement.Layout.InkBox(r.Box); ink.Overlaps(head) {
		t.Errorf("the title's ink %v covers the head %v", ink, head)
	}
	if !r.Avoid.Cleared {
		t.Errorf("avoidance %+v did not clear the subject", r.Avoid)
	}
}

func TestSegmentsMergeAndClipEvenly(t *testing.T) {
	j := sampleJob(t)
	if j.Width != 1080 || j.CSSWidth != 1080 || len(j.Items) != 8 || len(j.Fonts) == 0 {
		t.Fatalf("job %+v", j)
	}
	// The items follow each other end to start, so the frame ranges touch and become one segment.
	if len(j.Segments) != 1 || j.Segments[0].StartFrame != 0 || j.Segments[0].EndFrame != 16*30 {
		t.Fatalf("segments %+v", j.Segments)
	}
	c := j.Segments[0].Clip
	if c.X%2 != 0 || c.Y%2 != 0 || c.W%2 != 0 || c.H%2 != 0 || c.X+c.W > 1080 || c.Y+c.H > 1920 {
		t.Errorf("clip %+v", c)
	}
	// Two items far apart are two segments, each clipped to its own box.
	items := resolveAll(t, []timeline.GraphicsItem{sampleItems()[0], sampleItems()[5]})
	items[1].Item.AtMs = 10_000
	j2, err := BuildJob(out916, 30, 20*30, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(j2.Segments) != 2 || j2.Segments[0].Clip == j2.Segments[1].Clip {
		t.Fatalf("segments %+v", j2.Segments)
	}
	if j2.Segments[0].EndFrame != 72 || j2.Segments[1].StartFrame != 300 {
		t.Errorf("segments %+v", j2.Segments)
	}
}

func TestBuildJobIsDeterministic(t *testing.T) {
	a, _ := sampleJob(t).Encode()
	b, _ := sampleJob(t).Encode()
	if !bytes.Equal(a, b) {
		t.Fatal("the same items gave two different jobs")
	}
}

// The job is the contract with the runner, a separate module (packages/media-tools/motion-runner). This test writes the job of the
// sample items into the runner's testdata (with -update) or checks it is unchanged; the runner's own test parses that file with its
// strict decoder, so a field added on one side only fails one of the two.
func TestJobMatchesRunnerContract(t *testing.T) {
	data, err := sampleJob(t).Encode()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "..", "..", "packages", "media-tools", "motion-runner", "testdata", "job.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil { //nolint:gosec // test data
			t.Fatal(err)
		}
	}
	have, err := os.ReadFile(path) //nolint:gosec // test data
	if err != nil {
		t.Fatalf("%v (run go test ./internal/motion -run TestJobMatchesRunnerContract -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(data)) {
		t.Fatalf("the runner's testdata/job.json is not the job this package builds: run go test ./internal/motion -run TestJobMatchesRunnerContract -update and check the runner still parses it")
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
}

func TestLuminanceAndOnColour(t *testing.T) {
	if l := Luminance("#FFFFFF"); math.Abs(l-1) > 1e-9 {
		t.Errorf("white %v", l)
	}
	if l := Luminance("#000000"); l != 0 {
		t.Errorf("black %v", l)
	}
	if onColour("#FFD60A") != "#111111" || onColour("#1A1AFF") != "#FFFFFF" {
		t.Error("the colour on an accent is not the readable one")
	}
}
