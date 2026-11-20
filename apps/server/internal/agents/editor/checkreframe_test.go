package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// a landscape clip with the person on the left quarter of the frame (centre x 0.25).
func leftQuarter() subject.Index {
	return subject.Index{assetA: {Aspect: 16.0 / 9.0, Samples: []subject.Sample{{AtMs: 1000, X: 0.15, Y: 0.2, W: 0.2, H: 0.7, HeadTopY: 0.22}}}}
}

func landscapeEdit(t *testing.T, rf *timeline.Reframe) *timeline.Spec {
	t.Helper()
	spec := specOf(t, []shot{{asset: assetA, in: 0, out: 3000}}, nil, nil)
	main, _, _ := timeline.MainTrack(spec)
	main.Items[0].Reframe = rf
	return spec
}

func TestSubjectInCrop(t *testing.T) {
	plan := &Plan{ContentType: TypeTalking, TargetLengthS: 15}
	run := func(rf *timeline.Reframe) Report {
		return RunChecks(CheckInput{Spec: landscapeEdit(t, rf), Plan: plan, Subjects: leftQuarter()})
	}
	// No focus: the crop is centred on the person, who stays inside.
	wantStatus(t, run(nil), CheckSubjectInCrop, CheckPass)
	wantStatus(t, run(&timeline.Reframe{Mode: timeline.ReframeCover}), CheckSubjectInCrop, CheckPass)
	// A wrong focus crops the person out: the check fails and names the fix, which passes.
	c := wantStatus(t, run(&timeline.Reframe{Mode: timeline.ReframeCover, Focus: &timeline.Point{X: 0.9, Y: 0.5}}), CheckSubjectInCrop, CheckFail)
	if !strings.Contains(c.Detail, "focus x 0.25") {
		t.Errorf("fix: %s", c.Detail)
	}
	wantStatus(t, run(&timeline.Reframe{Mode: timeline.ReframeCover, Focus: &timeline.Point{X: 0.25, Y: 0.5}}), CheckSubjectInCrop, CheckPass)
	// blur_fill shows the whole clip.
	wantStatus(t, run(&timeline.Reframe{Mode: timeline.ReframeBlurFill}), CheckSubjectInCrop, CheckPass)
	// A person too wide for the crop: the check suggests blur_fill.
	wide := subject.Index{assetA: {Aspect: 16.0 / 9.0, Samples: []subject.Sample{{AtMs: 1000, X: 0.2, Y: 0.2, W: 0.6, H: 0.7, HeadTopY: 0.22}}}}
	r := RunChecks(CheckInput{Spec: landscapeEdit(t, nil), Plan: plan, Subjects: wide})
	if c := wantStatus(t, r, CheckSubjectInCrop, CheckFail); !strings.Contains(c.Detail, "blur_fill") {
		t.Errorf("wide: %s", c.Detail)
	}
	// No subject data: the check does not apply.
	r = RunChecks(CheckInput{Spec: landscapeEdit(t, nil), Plan: plan})
	for _, ck := range r.Checks {
		if ck.ID == CheckSubjectInCrop {
			t.Errorf("listed without subject boxes: %+v", ck)
		}
	}
}
