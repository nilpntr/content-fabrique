package motion

import (
	"bytes"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The templates draw the letters at the size Go laid them out at: the capitals Chrome inks are the style's capital height (what
// style_match compares with the creator's measured title height), within 6 %. Flat capitals ("HEH", no round letters that overshoot),
// no shadow and no outline, on the frame just after the item is fully on screen.
func TestIntegrationCapHeightInDocker(t *testing.T) {
	runner, root := dockerRunner(t)
	one := 1
	for _, it := range []timeline.GraphicsItem{
		{ID: "g1", Template: timeline.TemplateTitlePop, AtMs: 0, DurationMs: 2400, Anchor: "upper_third",
			Params: timeline.GraphicsParams{Text: "HEH", Style: "title_series", Fill: "#F2E9A0", Shadow: "none", Energy: timeline.EnergyCalm}},
		{ID: "g2", Template: timeline.TemplateChapterCard, AtMs: 0, DurationMs: 2500, Anchor: "upper_third",
			Params: timeline.GraphicsParams{Text: "HEH", Number: &one, Style: "title_series", Fill: "#F2E9A0", Shadow: "none", Energy: timeline.EnergyCalm}},
	} {
		r := resolveAll(t, []timeline.GraphicsItem{it})[0]
		job, err := BuildJob(out916, 30, timeline.Frame(it.DurationMs, 30), []Resolved{r})
		if err != nil {
			t.Fatal(err)
		}
		work, _, _ := renderJob(t, runner, root, "cap-"+it.ID, job)
		n := timeline.Frame(r.InkTimesMs()[0], job.FPS) - job.Segments[0].StartFrame
		data, err := os.ReadFile(filepath.Join(work, OutDir, "s0", fmt.Sprintf("f%06d.png", n))) //nolint:gosec // test output
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		b := img.Bounds()
		top, bottom := -1, -1
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if _, _, _, a := img.At(x, y).RGBA(); a > 0x8000 {
					if top < 0 {
						top = y
					}
					bottom = y
					break
				}
			}
		}
		if top < 0 {
			t.Fatalf("%s: no ink", it.ID)
		}
		inked := float64(bottom - top + 1)
		want := r.Placement.Style.CapHeightPct(out916.ShortSide(), out916.Height) / 100 * float64(out916.Height)
		t.Logf("%s: capitals %.0f px inked, %.1f px laid out (%.2f %% of the frame)", it.ID, inked, want, 100*inked/float64(out916.Height))
		if math.Abs(inked-want)/want > 0.06 {
			t.Errorf("%s: the capitals are %.0f px high on the frame, the layout's are %.1f px", it.ID, inked, want)
		}
	}
}
