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

// The live test of 2026-11-20 (section 4.1): Gino's back pose, read with a box 10 % above the head and the hands at the waist. Before
// quicklook.v5 the gap read as a raised arm and the hook title "BACK 101" never fitted on the pose. With the reader's arms_up false the
// title stays in the hook above the head; when the reader did not say (an older quick look) the old rule holds.
func ginoPoseIndex(armsUp *bool) subject.Index {
	s := subject.Sample{AtMs: 1000, X: 0.08, Y: 0.1, W: 0.75, H: 0.9, HeadTopY: 0.2, HeadX: 0.41, HeadW: 0.16, ShoulderY: 0.28, ArmsUp: armsUp}
	return subject.Index{assetA: {Aspect: 0.5625, Samples: []subject.Sample{s}}}
}

func ginoHookTitle(t *testing.T) *timeline.Spec {
	return withTitle(t, buildingLats(t), func(it *timeline.TextItem) {
		it.Text, it.Scale, it.DurationMs, it.Fill = "BACK 101", nil, 1850, "#F2E3B3"
	})
}

func TestTheHookTitleStaysOnABackPoseWithTheHandsDown(t *testing.T) {
	no := false
	r := RunChecks(CheckInput{Spec: ginoHookTitle(t), Plan: workoutPlan(), Style: ginoProfile(t), Subjects: ginoPoseIndex(&no)})
	c := wantStatus(t, r, CheckTextOverSubject, CheckPass)
	if strings.Contains(c.Detail, "renderer shows it at") {
		t.Fatalf("the hook title was moved off the pose: %s", c.Detail)
	}
	unread := RunChecks(CheckInput{Spec: ginoHookTitle(t), Plan: workoutPlan(), Style: ginoProfile(t), Subjects: ginoPoseIndex(nil)})
	wantStatus(t, unread, CheckTextOverSubject, CheckFail)
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
