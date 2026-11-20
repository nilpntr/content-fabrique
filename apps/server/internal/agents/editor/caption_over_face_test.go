package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// a speaker whose head and shoulders cover the rows top to bottom (fractions of a 9:16 frame), for the asset with this key.
func speakerRows(key string, top, bottom float64) subject.Index {
	return subject.Index{key: {Aspect: 0.5625, Samples: []subject.Sample{{
		AtMs: 1000, X: 0.3, Y: top, W: 0.4, H: min(0.45, 1-top), HeadTopY: top, HeadX: 0.4, HeadW: 0.2, ShoulderY: bottom,
	}}}}
}

func withCaptions(t *testing.T, enabled bool) *timeline.Spec {
	t.Helper()
	spec := buildingLats(t)
	spec.Tracks = append(spec.Tracks, &timeline.CaptionsTrack{ID: "captions", From: "transcript", Style: "word_highlight", Enabled: enabled})
	return spec
}

func TestCaptionOverFace(t *testing.T) {
	run := func(spec *timeline.Spec, ix subject.Index) Report {
		return RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: ginoProfile(t), Subjects: ix})
	}
	// No captions: the check is not part of the report.
	if _, ok := byID(run(withCaptions(t, false), speakerRows(assetA, 0.5, 0.7)))[CheckCaptionOverFace]; ok {
		t.Error("the check ran without captions")
	}
	// No boxes: skipped, with the command that reads them.
	c := wantStatus(t, run(withCaptions(t, true), nil), CheckCaptionOverFace, CheckSkip)
	if !strings.Contains(c.Detail, "quicklook") {
		t.Errorf("skip: %s", c.Detail)
	}
	// The face is high: the captions stay in the lower band.
	c = wantStatus(t, run(withCaptions(t, true), speakerRows(assetA, 0.2, 0.34)), CheckCaptionOverFace, CheckPass)
	if !strings.Contains(c.Detail, "clear of the face") {
		t.Errorf("clear: %s", c.Detail)
	}
	// The face is where the lower band is: the renderer moves the captions up. A title at the top is then on them: a failure that says so.
	c = wantStatus(t, run(withCaptions(t, true), speakerRows(assetA, 0.52, 0.72)), CheckCaptionOverFace, CheckFail)
	if !strings.Contains(c.Detail, "t1 on c1") || !strings.Contains(c.Detail, "upper one") {
		t.Errorf("title on the moved captions: %s", c.Detail)
	}
	// With the title in the lower third the moved captions pass, with a note.
	low := withCaptions(t, true)
	withTitle(t, low, func(it *timeline.TextItem) { it.Anchor = timeline.AnchorLowerThird })
	c = wantStatus(t, run(low, speakerRows(assetA, 0.52, 0.72)), CheckCaptionOverFace, CheckPass)
	if !strings.Contains(c.Detail, "upper band") {
		t.Errorf("moved: %s", c.Detail)
	}
	// A face that fills the frame is in both bands: a failure that names the clips and the fix.
	c = wantStatus(t, run(withCaptions(t, true), speakerRows(assetA, 0.12, 0.95)), CheckCaptionOverFace, CheckFail)
	for _, want := range []string{"c1", "upper band", "set_reframe"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("fail lacks %q: %s", want, c.Detail)
		}
	}
}
