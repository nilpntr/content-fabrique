package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func TestSubjectInCropAdviceMatchesTheCause(t *testing.T) {
	plan := &Plan{ContentType: TypeTalking, TargetLengthS: 15}
	z := 2.5
	// The focus is right (auto-centred) but the zoom crops the person out: the advice is to lower the zoom.
	r := RunChecks(CheckInput{Spec: landscapeEdit(t, &timeline.Reframe{Mode: timeline.ReframeBlurFill, Zoom: &z}), Plan: plan, Subjects: leftQuarter()})
	c := wantStatus(t, r, CheckSubjectInCrop, CheckFail)
	if !strings.Contains(c.Detail, "zoom") || !strings.Contains(c.Detail, "or lower") || strings.Contains(c.Detail, "leave the focus out") {
		t.Errorf("zoom cause: %s", c.Detail)
	}
	// A pass says the framing is static.
	r = RunChecks(CheckInput{Spec: landscapeEdit(t, nil), Plan: plan, Subjects: leftQuarter()})
	if c := wantStatus(t, r, CheckSubjectInCrop, CheckPass); !strings.Contains(c.Detail, "static framing") {
		t.Errorf("pass: %s", c.Detail)
	}
}

func TestSubjectInCropSkipsAClipFarFromEverySample(t *testing.T) {
	far := subject.Index{assetA: {Aspect: 16.0 / 9.0, Samples: []subject.Sample{{AtMs: 20000, X: 0.15, Y: 0.2, W: 0.2, H: 0.7, HeadTopY: 0.22}}}}
	r := RunChecks(CheckInput{Spec: landscapeEdit(t, nil), Plan: &Plan{ContentType: TypeTalking, TargetLengthS: 15}, Subjects: far})
	c := wantStatus(t, r, CheckSubjectInCrop, CheckSkip)
	if !strings.Contains(c.Detail, "not checked") {
		t.Errorf("detail: %s", c.Detail)
	}
}
