package subject

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The live test of 2026-11-20 (docs/design/live-test-2026-11-20.md, section 4.1): Gino's back pose (a rear lat spread, the hands at the
// waist, dark wall above the head) was read with a box that starts 10 % of the frame above the head. That gap read as a raised arm, so the
// whole box became torso, and the hook title "BACK 101" fitted nowhere on the pose in seven edits, even when asked for in so many words. The
// samples below are the stored ones of that clip, no camera move, the title as Gino's settings make it. quicklook.v5 asks the reader
// whether a hand or an arm is above the head (ArmsUp).
func ginoPose(armsUp *bool) []Sample {
	return []Sample{
		{AtMs: 2348, X: 0.15, Y: 0.1, W: 0.6, H: 0.9, HeadTopY: 0.2, HeadX: 0.42, HeadW: 0.15, ShoulderY: 0.27, ArmsUp: armsUp},
		{AtMs: 5165, X: 0.08, Y: 0.1, W: 0.75, H: 0.9, HeadTopY: 0.18, HeadX: 0.4, HeadW: 0.16, ShoulderY: 0.27, ArmsUp: armsUp},
		{AtMs: 7982, X: 0.08, Y: 0.1, W: 0.75, H: 0.9, HeadTopY: 0.2, HeadX: 0.41, HeadW: 0.16, ShoulderY: 0.29, ArmsUp: armsUp},
	}
}

func ginoHook() (*timeline.Spec, timeline.TextItem) {
	spec := specWithClip(poseClip, 4000, 6100, nil)
	it := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 2000, Text: "BACK 101", Style: "title_series", Anchor: timeline.AnchorUpperThird, Fill: "#F2E3B3", Shadow: "none"}
	return spec, it
}

func TestAHookTitleFitsOnABackPoseWhoseHandsAreDown(t *testing.T) {
	no := false
	spec, it := ginoHook()
	lookup := Index{poseClip: {Aspect: 0.563, Samples: ginoPose(&no)}}.Lookup
	fit, err := FitText(spec, lookup, it, 5100)
	if err != nil {
		t.Fatal(err)
	}
	if fit.How != FitInPlace || !fit.Avoidance.Cleared {
		t.Fatalf("the hook title did not stay on the pose: %s %+v", fit.How, fit.Avoidance)
	}
	ink := fit.Placement.Layout.InkBox(fit.Placement.Box)
	for _, r := range fit.Regions {
		if ink.Overlaps(r.Head) || ink.Overlaps(r.Torso) {
			t.Errorf("the ink %+v covers the head %+v or the torso %+v", ink, r.Head, r.Torso)
		}
	}
	t.Logf("placed %q at scale %v, box %+v", fit.Avoidance.Where, fit.Avoidance.Scale, fit.Placement.Box)
}

// Without the reader's answer (an older quick look) the padded box still reads as a raised arm, as before: the title is moved off the pose.
// And when the reader says a hand is up, the whole box is kept clear.
func TestARaisedArmStillKeepsTheTitleOffTheBox(t *testing.T) {
	spec, it := ginoHook()
	yes := true
	for name, up := range map[string]*bool{"not read": nil, "arms up": &yes} {
		lookup := Index{poseClip: {Aspect: 0.563, Samples: ginoPose(up)}}.Lookup
		fit, err := FitText(spec, lookup, it, 5100)
		if err != nil {
			t.Fatal(err)
		}
		if fit.How == FitInPlace {
			t.Errorf("%s: the title stayed over the raised arms: %+v", name, fit.Avoidance)
		}
	}
}

func TestArmsUpDecidesTheTorso(t *testing.T) {
	no, yes := false, true
	s := Sample{X: 0.1, Y: 0.1, W: 0.8, H: 0.9, HeadTopY: 0.2, ShoulderY: 0.28}
	s.ArmsUp = &no
	if _, y0, _, _ := s.Torso(); y0 != 0.28 {
		t.Errorf("hands down: the torso starts at %v, want the shoulder line", y0)
	}
	if _, y0, _, _ := s.Body(); y0 != 0.2 {
		t.Errorf("hands down: the body starts at %v, want the head top", y0)
	}
	s.ArmsUp = &yes
	if _, y0, _, _ := s.Torso(); y0 != 0.1 {
		t.Errorf("arms up: the torso starts at %v, want the box top", y0)
	}
}
