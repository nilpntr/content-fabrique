package render

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// The ink box of a layout holds the ink libass draws, and is not much taller than it: it is what a head is tested against, so a letter
// must never be outside it (a title would be on a head the check calls clear), and it must be tight enough to be worth using.
func TestTheInkBoxHoldsTheInk(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	texts := []string{
		"BACK DAY 101", "BACK DAY\nBLUEPRINT", "Never skip leg day", "Drive through\nyour heels", "Ünïcödé ÀÉÎÕÜ", "5x5 gjpqy",
		// the glyphs that draw highest above the capitals: brackets, a dollar sign, a slash, a bar, an at sign, a caret, a tilde and an
		// asterisk, and letters with a ring, a stacked accent or a tall mark
		"(BACK) [DAY] {101}", "$5 / 10 | @home ^~*", "(row) [pull] {push} |", "ÅNGSTRÖM Åå Ůů", "Ǻ Ế Ǘ Ǻ", "Éclair Ñandú Öl",
	}
	var checked atomic.Int32
	t.Run("cases", func(t *testing.T) {
		for _, sty := range style.IDs() {
			for i, text := range texts {
				for _, scale := range []float64{1, 0.6} {
					anchor := []string{timeline.AnchorUpperThird, timeline.AnchorCenter, timeline.AnchorLowerThird}[i%3]
					it := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 3000, Text: text, Style: sty, Anchor: anchor, Animation: "none", Scale: &scale}
					t.Run(fmt.Sprintf("%s/%d/%g", sty, i, scale), func(t *testing.T) {
						t.Parallel()
						toolSem <- struct{}{}
						defer func() { <-toolSem }()
						plan, pl := textPlan(t, timeline.Format9x16, it)
						if !pl.Zone.Safe(pl.Box) {
							t.Skipf("the validator rejects this one (box %+v)", pl.Box)
						}
						checked.Add(1)
						ink, ok := inkBox(renderScript(t, ffmpeg, plan, 1080, 1920, 1.0))
						if !ok {
							t.Fatal("nothing was drawn")
						}
						box := pl.Layout.InkBox(pl.Box)
						if ink.Top < box.Top || ink.Bottom > box.Bottom || ink.Left < box.Left || ink.Right > box.Right {
							t.Errorf("ink %+v is outside the ink box %+v (layout box %+v, %d lines)", ink, box, pl.Box, len(pl.Layout.Lines))
						}
						// Tight: the ink box is no more than a third of a line taller than the ink, above and below.
						slack := pl.Layout.LineHeight / 3
						if box.Top < ink.Top-slack || box.Bottom > ink.Bottom+slack {
							t.Errorf("loose: ink %+v, ink box %+v, layout box %+v", ink, box, pl.Box)
						}
					})
				}
			}
		}
	})
	// The skip above must not hide the test: most of the cases are ones the validator accepts.
	if n := checked.Load(); n < 100 {
		t.Errorf("only %d cases were checked", n)
	}
}
