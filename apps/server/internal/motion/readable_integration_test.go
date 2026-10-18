package motion

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// liveReadableItems are the graphics overlay_readable failed in the live runs of editor v2.10 (Gino's style), at time 0: a lively
// title_pop that leaves before the drop and two hype chapter cards that start before their cut.
func liveReadableItems() []timeline.GraphicsItem {
	one := 1
	look := func(text, energy string, acc ...int64) timeline.GraphicsParams {
		return timeline.GraphicsParams{Text: text, Style: "title_series", Fill: "#F5F0A0", Shadow: "none", Energy: energy, AccentsMs: acc}
	}
	card := func(text string, acc ...int64) timeline.GraphicsParams {
		p := look(text, timeline.EnergyHype, acc...)
		p.Number, p.Accent = &one, "#F5F0A0"
		return p
	}
	return []timeline.GraphicsItem{
		{ID: "g1", Template: timeline.TemplateTitlePop, AtMs: 0, DurationMs: 2050, Anchor: "upper_third", Params: look("UPPER BACK 101", timeline.EnergyLively, 3, 180, 360)},
		{ID: "g2", Template: timeline.TemplateChapterCard, AtMs: 0, DurationMs: 2320, Anchor: "upper_third", Params: card("CABLE ROW", 270, 775)},
		{ID: "g3", Template: timeline.TemplateChapterCard, AtMs: 0, DurationMs: 2500, Anchor: "upper_third", Params: card("PULLDOWNS", 482, 996)},
	}
}

// ink is a frame's ink: the pixels with alpha over half, their count and their bounds.
type ink struct {
	px     map[image.Point]bool
	bounds image.Rectangle
}

func readInk(t *testing.T, name string) ink {
	t.Helper()
	data, err := os.ReadFile(name) //nolint:gosec // test output
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	k := ink{px: map[image.Point]bool{}}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0x8000 {
				p := image.Pt(x, y)
				k.px[p] = true
				k.bounds = k.bounds.Union(image.Rectangle{Min: p, Max: p.Add(image.Pt(1, 1))})
			}
		}
	}
	return k
}

// complete says whether a frame shows the whole settled text: its ink spans at least 95 % of the settled width and 90 % of the
// settled height (no word missing), and its inked area is at least 85 % of the settled area (not faded or blurred away).
func complete(k, ref ink) (bool, string) {
	w := float64(k.bounds.Dx()) / float64(ref.bounds.Dx())
	h := float64(k.bounds.Dy()) / float64(ref.bounds.Dy())
	a := float64(len(k.px)) / float64(len(ref.px))
	return w >= 0.95 && h >= 0.9 && a >= 0.85, fmt.Sprintf("width %.2f, height %.2f, ink %.2f of the settled text", w, h, a)
}

// The readable window Schedule computes (from the templates' curves) holds on the rendered frames: in every frame from LegibleMs to
// LeavingMs the whole text is on screen. And the graphics overlay_readable failed in the live runs read for 1.2 s and more.
func TestIntegrationReadableWindow(t *testing.T) {
	runner, root := dockerRunner(t)
	items := liveReadableItems()
	for _, e := range []string{timeline.EnergyCalm, timeline.EnergyLively, timeline.EnergyHype} {
		for _, it := range sampleItems() {
			it.Params.Energy = e
			it.AtMs, it.Params.AccentsMs = 0, shift(it.Params.AccentsMs, -it.AtMs)
			items = append(items, it)
		}
	}
	for i, it := range items {
		r := resolveAll(t, []timeline.GraphicsItem{it})[0]
		job, err := BuildJob(out916, 30, timeline.Frame(it.DurationMs, 30), []Resolved{r})
		if err != nil {
			t.Fatal(err)
		}
		work, _, _ := renderJob(t, runner, root, fmt.Sprintf("readable-%d", i), job)
		seg := job.Segments[0]
		frame := func(n int64) string {
			return filepath.Join(work, OutDir, "s0", fmt.Sprintf("f%06d.png", n-seg.StartFrame))
		}
		tm := r.Timing
		ref := readInk(t, frame(timeline.Frame((tm.FullyInMs+tm.ExitMs)/2, 30)))
		// The frames whose whole duration lies inside the window.
		from, to := timeline.Frame(tm.LegibleMs+33, 30), timeline.Frame(tm.LeavingMs, 30)-1
		for n := from; n <= to; n++ {
			if ok, why := complete(readInk(t, frame(n)), ref); !ok {
				t.Errorf("%s %s (%s): frame %d (%d ms) is inside the readable window %d-%d ms but not the whole text: %s",
					it.ID, it.Template, it.Params.EnergyOrDefault(), n, n*1000/30, tm.LegibleMs, tm.LeavingMs, why)
			}
		}
		t.Logf("%s %s (%s): readable %d-%d ms (%d ms); was fully in %d to exit %d (%d ms)", it.ID, it.Template, it.Params.EnergyOrDefault(),
			tm.LegibleMs, tm.LeavingMs, tm.ReadableMs(), tm.FullyInMs, tm.ExitMs, tm.ExitMs-tm.FullyInMs)
		if i < len(liveReadableItems()) && tm.ReadableMs() < 1200 {
			t.Errorf("%s %s of the live runs reads for %d ms", it.ID, it.Template, tm.ReadableMs())
		}
	}
}
