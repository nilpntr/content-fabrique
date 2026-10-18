package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// A test fixture shaped like a posing hook with a high head (a lat spread against a dark wall: the box is most of the frame wide, the
// head narrow, about 30 px of band above it once the living camera moves). A long hook title has no room above the head at any legible
// size; with the head measured on its own (quicklook.v3) it stacks beside it, in the hook. Without it there is no room, and check_edit
// fails with the room in numbers and a concrete fix.
func poseIndex(withHead bool) subject.Index {
	s := subject.Sample{AtMs: 1000, X: 0.09, Y: 0.195, W: 0.81, H: 0.805, HeadTopY: 0.2}
	if withHead {
		s.HeadX, s.HeadW, s.ShoulderY = 0.42, 0.165, 0.32
	}
	return subject.Index{assetA: {Aspect: 0.5625, Samples: []subject.Sample{s}}}
}

func longHookTitle(t *testing.T) *timeline.Spec {
	spec := withTitle(t, buildingLats(t), func(it *timeline.TextItem) {
		sc := 0.7
		it.Text, it.Scale, it.DurationMs = "UPPER BACK THICKNESS 101", &sc, 1850
	})
	main, _, _ := timeline.MainTrack(spec)
	for i := range main.Items {
		main.Items[i].Motion = &timeline.Motion{Preset: timeline.MotionHandheld}
	}
	return spec
}

func TestALongHookTitleStaysInTheHookBesideAMeasuredHead(t *testing.T) {
	r := RunChecks(CheckInput{Spec: longHookTitle(t), Plan: workoutPlan(), Style: ginoProfile(t), Subjects: poseIndex(true)})
	c := wantStatus(t, r, CheckTextOverSubject, CheckPass)
	if !strings.Contains(c.Detail, "stacks it beside the head") || !strings.Contains(c.Detail, "lines") {
		t.Errorf("beside: %s", c.Detail)
	}
}

func TestALongHookTitleThatCannotStayInTheHookFailsWithAConcreteFix(t *testing.T) {
	r := RunChecks(CheckInput{Spec: longHookTitle(t), Plan: workoutPlan(), Style: ginoProfile(t), Subjects: poseIndex(false)})
	c := wantStatus(t, r, CheckTextOverSubject, CheckFail)
	for _, want := range []string{"tried: above the head (a band of", "beside it (at most", "smallest legible scale 0.56",
		"characters a line in up to 3 lines", "cfctl assets quicklook --subjects"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("lacks %q: %s", want, c.Detail)
		}
	}
}
