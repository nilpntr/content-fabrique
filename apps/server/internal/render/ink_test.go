package render

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"math/rand/v2"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// These tests render generated scripts with ffmpeg and libass onto a flat grey frame and measure the pixels the text
// changed, to prove that what the layout says (the box the validator checks) is what libass draws: the ink of every text lies
// inside its layout box, so a box inside the safe zone means ink inside the safe zone.

const inkBackground = 0x80

// renderScript burns a script into a flat grey frame of the given size and returns the frame at time ts (seconds).
func renderScript(t *testing.T, ffmpeg string, plan Plan, w, h int, ts float64) image.Image {
	t.Helper()
	dir := t.TempDir()
	if err := stageSubtitles(plan, dir, ""); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.png")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y", //nolint:gosec // test helper
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x%02X%02X%02X:s=%dx%d:r=30:d=5", inkBackground, inkBackground, inkBackground, w, h),
		"-vf", plan.Subtitles.filter(), "-ss", fmt.Sprintf("%.3f", ts), "-frames:v", "1", out)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s\n%s", err, stderr.String(), plan.Subtitles.ASS)
	}
	return readPNG(t, out)
}

// inkBox returns the bounding box of the pixels that differ from the background by more than a few levels.
func inkBox(img image.Image) (box style.Rect, found bool) {
	b := img.Bounds()
	box = style.Rect{Left: b.Max.X, Top: b.Max.Y, Right: -1, Bottom: -1}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if diff(r) > 12 || diff(g) > 12 || diff(bl) > 12 {
				box.Left, box.Right = min(box.Left, x), max(box.Right, x+1)
				box.Top, box.Bottom = min(box.Top, y), max(box.Bottom, y+1)
				found = true
			}
		}
	}
	return box, found
}

func diff(c uint32) int {
	d := int(c>>8) - inkBackground
	if d < 0 {
		d = -d
	}
	return d
}

// textPlan compiles a one-clip spec with a single text item (the 5 s test clip; the picture is replaced by flat grey in
// renderScript, only the script is used) and returns the plan and the placement.
func textPlan(t *testing.T, f timeline.Format, it timeline.TextItem) (Plan, timeline.TextPlacement) {
	t.Helper()
	w, h, _ := f.Size()
	spec := &timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: f, Width: w, Height: h, FPS: 30, Background: "#000000"},
		Tracks: []timeline.Track{
			&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{{ID: "c1", AssetID: "ast_01j3er3kwcgrasdgd4aznmxxe0", Source: timeline.Source{InMs: 0, OutMs: 5000}, AtMs: 0}}},
			&timeline.TextTrack{ID: "text", Items: []timeline.TextItem{it}},
		},
	}
	plan, err := Compile(spec, Final, plainResolver())
	if err != nil {
		t.Fatal(err)
	}
	pl, err := timeline.PlaceText(spec.Output, it)
	if err != nil {
		t.Fatal(err)
	}
	return plan, pl
}

func within(inner, outer style.Rect, slack int) bool {
	return inner.Left >= outer.Left-slack && inner.Right <= outer.Right+slack && inner.Top >= outer.Top-slack && inner.Bottom <= outer.Bottom+slack
}

// Every style on every format and anchor draws its ink inside the layout box, which is inside the safe zone and clear of the
// blocked area. Typical and awkward texts: wrapped, several lines, wide capitals, accents.
func TestInkIsInsideTheBoxAndTheSafeZone(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	texts := []string{"Never skip leg day", "WWWWWWWWWWWWWWWWWWWWWW WWWW", "Drive through\nyour heels\nevery rep", "Ünïcödé ÀÉÎÕÜ gjpqy", "5x5"}
	for _, f := range []timeline.Format{timeline.Format9x16, timeline.Format1x1, timeline.Format4x5, timeline.Format16x9} {
		w, h, _ := f.Size()
		zone, _ := style.ZoneFor(string(f), w, h)
		for _, sty := range style.IDs() {
			for i, text := range texts {
				anchor := []string{timeline.AnchorUpperThird, timeline.AnchorCenter, timeline.AnchorLowerThird}[i%3]
				it := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 3000, Text: text, Style: sty, Anchor: anchor, Animation: "none"}
				name := fmt.Sprintf("%s/%s/%s/%d", f, sty, anchor, i)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					toolSem <- struct{}{}
					defer func() { <-toolSem }()
					plan, pl := textPlan(t, f, it)
					if !zone.Safe(pl.Box) {
						t.Skipf("the validator rejects this one (box %+v)", pl.Box)
					}
					ink, ok := inkBox(renderScript(t, ffmpeg, plan, w, h, 1.0))
					if !ok {
						t.Fatal("nothing was drawn")
					}
					if !within(ink, pl.Box, 0) {
						t.Errorf("ink %+v is outside the layout box %+v", ink, pl.Box)
					}
					if !zone.Safe(ink) {
						t.Errorf("ink %+v is outside the safe zone %+v", ink, zone)
					}
					// The box is not wildly bigger than the ink: the layout is tight to within the padding and line gaps.
					if ink.Width()*10 < pl.Box.Width()*6 && len(pl.Layout.Lines) == 1 {
						t.Errorf("ink %+v is much narrower than the box %+v: the measured widths are off", ink, pl.Box)
					}
				})
			}
		}
	}
}

// Randomised version of the same property over positions, which can be unsafe: whenever the validator accepts the item, the ink
// is inside the safe zone; the animations are rendered at their start too.
func TestInkRandomItemsStayInTheSafeZone(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	r := rand.New(rand.NewPCG(3, 5))
	formats := []timeline.Format{timeline.Format9x16, timeline.Format1x1, timeline.Format4x5, timeline.Format16x9}
	anims := []string{"none", "pop_in", "fade", "typewriter", "slide_up"}
	words := []string{"Back", "day", "WWWW", "i", "Squat", "deeper!", "Ünï", "5x5", "gjpqy", "supercalifragilistic"}
	checked := 0
	for i := 0; i < 80; i++ {
		f := formats[r.IntN(len(formats))]
		w, h, _ := f.Size()
		zone, _ := style.ZoneFor(string(f), w, h)
		var ws []string
		for n, k := 0, 1+r.IntN(6); n < k; n++ {
			ws = append(ws, words[r.IntN(len(words))])
		}
		it := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 3000, Text: strings.Join(ws, " "), Style: style.IDs()[r.IntN(len(style.IDs()))], Animation: anims[r.IntN(len(anims))]}
		it.Position = &timeline.Point{X: 0.2 + 0.6*r.Float64(), Y: 0.15 + 0.6*r.Float64()}
		plan, pl := textPlan(t, f, it)
		if !zone.Safe(pl.Box) {
			continue
		}
		checked++
		// Frames at the start of the animation (a frame in), at the end of it and in the middle of the event.
		for _, ts := range []float64{0.034, 0.1, 1.5} {
			img := renderScript(t, ffmpeg, plan, w, h, ts)
			ink, ok := inkBox(img)
			if !ok {
				continue // typewriter and fades may show nothing yet
			}
			if !zone.Safe(ink) {
				t.Errorf("case %d (%s %s %q at %.3f s): ink %+v is outside the safe zone %+v (box %+v)", i, it.Style, it.Animation, it.Text, ts, ink, zone, pl.Box)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("only %d safe cases were checked", checked)
	}
}

// Text that tries to inject override tags is drawn literally: it is as wide as the layout says, which it would not be if libass had
// read {\fs200} or {\an9} as tags, and nothing is drawn where \pos(0,0) would have put it.
func TestInjectedTagsAreDrawnLiterally(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	for _, text := range []string{`{\pos(0,0)}hi`, `{\fs200\an9}hi`, `a\Nb`, `x\{\pos(0,0)\}`, `}{\an7}`} {
		for _, anim := range []string{"none", "typewriter"} {
			it := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 3000, Text: text, Style: "caption_plain", Anchor: "center", Animation: anim}
			plan, pl := textPlan(t, timeline.Format9x16, it)
			ink, ok := inkBox(renderScript(t, ffmpeg, plan, 1080, 1920, 2.5))
			if !ok {
				t.Fatalf("%q: nothing drawn", text)
			}
			if !within(ink, pl.Box, 0) {
				t.Errorf("%q (%s): ink %+v is outside the layout box %+v: a tag was read", text, anim, ink, pl.Box)
			}
			if len(pl.Layout.Lines) != 1 {
				t.Fatalf("%q: laid out in %d lines", text, len(pl.Layout.Lines))
			}
			// The text is as wide as measured (the ink is the advance widths minus side bearings, plus the outline).
			if lw, iw := pl.Layout.Widths[0], ink.Width(); iw < lw*8/10 || iw > pl.Box.Width() {
				t.Errorf("%q (%s): the line is %d px wide by the layout and %d px of ink", text, anim, lw, iw)
			}
		}
	}
}

// The preview watermark is drawn inside the safe zone on every format.
func TestWatermarkIsInsideTheSafeZone(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	for _, f := range []timeline.Format{timeline.Format9x16, timeline.Format1x1, timeline.Format4x5, timeline.Format16x9} {
		w, h, _ := f.Size()
		spec := &timeline.Spec{
			Schema: timeline.SchemaVersion,
			Output: timeline.Output{Format: f, Width: w, Height: h, FPS: 30, Background: "#000000"},
			Tracks: []timeline.Track{&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{{ID: "c1", AssetID: "ast_01j3er3kwcgrasdgd4aznmxxe0", Source: timeline.Source{InMs: 0, OutMs: 5000}, AtMs: 0}}}},
		}
		plan, err := Compile(spec, Preview, plainResolver())
		if err != nil {
			t.Fatal(err)
		}
		if plan.Subtitles == nil || !plan.Subtitles.Watermark {
			t.Fatalf("%s: no watermark in %+v", f, plan.Subtitles)
		}
		ink, ok := inkBox(renderScript(t, ffmpeg, plan, w, h, 2.0))
		zone, _ := style.ZoneFor(string(f), w, h)
		if !ok || !zone.Safe(ink) {
			t.Errorf("%s: watermark ink %+v (drawn %v), safe zone %+v", f, ink, ok, zone)
		}
	}
}
