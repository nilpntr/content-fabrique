package subject

import (
	"math"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// A hook title over a posing shot whose head is high in the frame (a test fixture shaped like the founder's back-pose clip: a lat spread
// against a dark wall, the arms out, so the person's box is most of the frame's width while the head itself is narrow). With the living
// camera on, the head reaches about 14 % of the frame and leaves about 30 px above it inside the safe zone: no title fits above it at a
// legible size. The wall beside the head is empty. The title must stay in the hook, stacked beside the head, never over the body.
const poseClip = "ast_posefixture"

// poseSample is the person of the fixture as the quick look reads it: the box (arms out), the head top, and (quicklook.v3) the head
// itself and the shoulder line.
func poseSample(withHead bool) Sample {
	s := Sample{AtMs: 6000, X: 0.09, Y: 0.195, W: 0.81, H: 0.805, HeadTopY: 0.2}
	if withHead {
		s.HeadX, s.HeadW, s.ShoulderY = 0.42, 0.165, 0.32
	}
	return s
}

func poseSpec(text string) (*timeline.Spec, timeline.TextItem) {
	spec := specWithClip(poseClip, 7800, 10012, nil)
	main, _ := spec.Tracks[0].(*timeline.VideoTrack)
	main.Items[0].Motion = &timeline.Motion{Preset: timeline.MotionHandheld}
	scale := 0.7
	it := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 1850, Text: text, Style: "title_series", Anchor: timeline.AnchorUpperThird, Scale: &scale, Fill: "#F5F0A0", Shadow: "none"}
	return spec, it
}

func lookupOf(s Sample) Lookup {
	return Index{poseClip: {Aspect: 0.5625, Samples: []Sample{s}}}.Lookup
}

func TestAHookTitleOverAHighHeadStacksBesideItInTheHook(t *testing.T) {
	spec, it := poseSpec("UPPER BACK THICKNESS 101")
	lookup := lookupOf(poseSample(true))
	regions := Regions(spec, lookup, it.AtMs, it.AtMs+it.DurationMs)
	if len(regions) != 1 {
		t.Fatalf("regions %+v", regions)
	}
	zone, _ := spec.Output.Zone()
	head := regions[0].Head
	if pct := float64(head.Top) / float64(spec.Output.Height) * 100; pct < 13 || pct > 15.5 {
		t.Fatalf("the fixture's head (with the camera) is at %.1f %%, want about 14 %%", pct)
	}
	if band := head.Top - zone.Inner.Top; band < 15 || band > 50 {
		t.Fatalf("the band above the head is %d px, want about 30", band)
	}

	fit, err := FitText(spec, lookup, it, 2212)
	if err != nil {
		t.Fatal(err)
	}
	av := fit.Avoidance
	if fit.How != FitInPlace || !av.Cleared || av.Where != timeline.WhereBeside {
		t.Fatalf("the hook title was not kept in the hook beside the head: %s %+v", fit.How, av)
	}
	if fit.AtMs != it.AtMs || fit.DurationMs != it.DurationMs {
		t.Errorf("retimed to %d+%d", fit.AtMs, fit.DurationMs)
	}
	lay := fit.Placement.Layout
	t.Logf("beside the head at scale %v in %d lines %q, box %+v, head %+v", av.Scale, len(lay.Lines), lay.Lines, fit.Placement.Box, head)
	if n := len(lay.Lines); n < 2 || n > timeline.MaxBesideLines {
		t.Errorf("%d lines %q, want 2 or 3", n, lay.Lines)
	}
	if strings.Join(lay.Lines, " ") != it.Text {
		t.Errorf("the words were broken: %q", lay.Lines)
	}
	// Legible: the capitals at least 2.2 % of the frame height.
	if pct := lay.Style.CapHeightPct(spec.Output.ShortSide(), spec.Output.Height); pct < style.LegibleCapPct-0.01 {
		t.Errorf("the capitals are %.2f %% of the frame, under the legible %.1f %%", pct, style.LegibleCapPct)
	}
	// Off the head and the torso, with every frame of the camera, and inside the zone.
	ink := lay.InkBox(fit.Placement.Box)
	for _, r := range fit.Regions {
		if ink.Overlaps(r.Head) || ink.Overlaps(r.Torso) {
			t.Errorf("the ink %+v covers the head %+v or the torso %+v", ink, r.Head, r.Torso)
		}
	}
	if !fit.Placement.Zone.Safe(fit.Placement.Box) {
		t.Errorf("the box %+v is outside the zone", fit.Placement.Box)
	}
	// Over the wall, beside the head: wholly to one side of the head.
	if ink.Right > head.Left && ink.Left < head.Right {
		t.Errorf("the ink %+v is not beside the head %+v", ink, head)
	}
}

// The order: a title that fits above the head at a smaller legible size goes there before it is stacked beside it; the scales tried go
// under the spec's smallest scale (0.6) down to the legibility floor and never under it.
func TestFitScalesGoDownToTheLegibilityFloor(t *testing.T) {
	spec, it := poseSpec("BACK 101")
	scales := timeline.FitScales(spec.Output, it)
	st, _ := style.Lookup("title_series")
	floor := st.FitFloor(spec.Output.ShortSide(), spec.Output.Height)
	if scales[0] != 0.7 || math.Abs(scales[len(scales)-1]-floor) > 1e-9 {
		t.Errorf("scales %v, floor %v", scales, floor)
	}
	if floor >= style.MinScale || floor < style.MinFitScale {
		t.Errorf("the floor of title_series on 9:16 is %v: want under the spec minimum and at least %v", floor, style.MinFitScale)
	}
	if pct := st.Shrunk(floor).CapHeightPct(spec.Output.ShortSide(), spec.Output.Height); pct < style.LegibleCapPct-0.01 {
		t.Errorf("the floor %v gives capitals of %.2f %%", floor, pct)
	}
	for i := 1; i < len(scales); i++ {
		if scales[i] >= scales[i-1] {
			t.Errorf("not descending: %v", scales)
		}
	}
}

// Without the head measured (a quick look before quicklook.v3), the head is the middle 60 % of a box that is most of the frame wide:
// there is no room beside it for this title, so it cannot stay in the hook. The renderer moves it, and check_edit fails with the room in
// numbers (internal/agents/editor).
func TestWithoutAMeasuredHeadTheLongTitleCannotStayInTheHook(t *testing.T) {
	spec, it := poseSpec("UPPER BACK THICKNESS 101")
	fit, err := FitText(spec, lookupOf(poseSample(false)), it, 2212)
	if err != nil {
		t.Fatal(err)
	}
	if fit.How == FitInPlace {
		t.Fatalf("placed in the hook %+v", fit.Avoidance)
	}
	if tr := fit.Avoidance.Tried; tr.AboveBandPx > 60 || tr.FloorScale == 0 {
		t.Errorf("tried %+v", tr)
	}
}

func TestAMeasuredHeadIsNarrowerThanTheBox(t *testing.T) {
	s := poseSample(true)
	x0, y0, x1, y1 := s.Head()
	if x0 < 0.37 || x1 > 0.63 || y0 != 0.2 || y1 != 0.32 {
		t.Errorf("head %v %v %v %v", x0, y0, x1, y1)
	}
	if _, ty, _, _ := s.Torso(); ty != 0.32 {
		t.Errorf("the torso starts at %v, not the shoulder line", ty)
	}
	// A head outside the box, or a shoulder line above the head, is not read: the sample keeps its box and the old head.
	bad := s
	bad.HeadX, bad.ShoulderY = 0.9, 0.1
	c := bad.Clamp()
	if c.HeadW != 0 || c.HeadX != 0 || c.ShoulderY != 0 || !c.Valid() {
		t.Errorf("clamped %+v", c)
	}
	nan := s
	nan.HeadW = math.NaN()
	if c := nan.Clamp(); c.HeadW != 0 || !c.Valid() {
		t.Errorf("NaN head %+v", c)
	}
}
