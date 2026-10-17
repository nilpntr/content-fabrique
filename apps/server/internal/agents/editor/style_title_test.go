package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The recipe's title is the same share of the frame on every format: text sizes scale with the short side, and the check measures
// against a 9:16 frame's height, so the title the recipe writes passes title_size on a 16:9 output too.
func TestTitleSizeIsTheSameShareOnEveryFormat(t *testing.T) {
	p := ginoProfile(t)
	for _, f := range []timeline.Format{timeline.Format9x16, timeline.Format1x1, timeline.Format4x5, timeline.Format16x9} {
		spec := buildingLats(t)
		switch f {
		case timeline.Format1x1:
			spec.Output = timeline.Output{Format: f, Width: 1080, Height: 1080, FPS: 30}
		case timeline.Format4x5:
			spec.Output = timeline.Output{Format: f, Width: 1080, Height: 1350, FPS: 30}
		case timeline.Format16x9:
			spec.Output = timeline.Output{Format: f, Width: 1920, Height: 1080, FPS: 30}
		}
		r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: p})
		for _, d := range r.Style.Dimensions {
			if d.ID == "title_size" && !d.OK {
				t.Errorf("%s: %+v", f, d)
			}
		}
	}
}

// A later title's look is not read when the earliest title cannot be placed.
func TestAnUnplaceableEarliestTitleLeavesNoLookOfALaterOne(t *testing.T) {
	spec := buildingLats(t)
	for _, tr := range spec.Tracks {
		if tt, ok := tr.(*timeline.TextTrack); ok {
			bad := tt.Items[0]
			bad.ID, bad.AtMs, bad.Align = "t0", 0, "top" // an alignment that is not one
			later := tt.Items[0]
			later.ID, later.AtMs, later.Fill = "t5", 5000, "#123456"
			tt.Items = []timeline.TextItem{later, bad}
		}
	}
	r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: ginoProfile(t)})
	for _, d := range r.Style.Dimensions {
		if d.ID == "title_colour" {
			t.Errorf("the colour of the later title was judged: %+v", d)
		}
	}
}

// The size fix is a scale the ops take, whatever the distance.
func TestTheTitleSizeFixIsInRange(t *testing.T) {
	p := ginoProfile(t)
	p.Text.HeightPct = 8
	spec := buildingLatsDefaultTitle(t) // the style's own size, about half of 8 %
	r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: p})
	var fix string
	for _, d := range r.Style.Failed() {
		if d.ID == "title_size" {
			fix = d.Fix
		}
	}
	if !strings.Contains(fix, "scale to 1.4") {
		t.Errorf("fix %q", fix)
	}
	p.Text.HeightPct = 2.0
	r = RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: p})
	for _, d := range r.Style.Failed() {
		if d.ID == "title_size" && !strings.Contains(d.Fix, "scale to 0.6") && !strings.Contains(d.Fix, "scale to 0.5") {
			t.Errorf("fix %q", d.Fix)
		}
	}
}
