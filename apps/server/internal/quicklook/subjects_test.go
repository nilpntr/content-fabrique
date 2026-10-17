package quicklook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/subject"
)

func answerWith(subjects string) json.RawMessage {
	return json.RawMessage(`{"title":"Barbell row","description":"A lifter rows.","tags":["gym"],"subjects":` + subjects + `}`)
}

// The subject boxes are read as numbers and left out when they are not complete; the labels are kept either way.
func TestValidateReadsSubjectsAndLeavesOutIncompleteOnes(t *testing.T) {
	out, err := Validate(answerWith(`[{"frame":1,"present":true,"x":0.2,"y":0.2,"w":0.5,"h":0.7,"head_top_y":0.22},{"frame":2,"present":true,"x":0.2}]`))
	if err != nil || len(out.Subjects) != 1 || out.Subjects[0].Frame != 1 || !out.Subjects[0].Present || out.Subjects[0].HeadTopY != 0.22 {
		t.Fatalf("%+v %v", out, err)
	}
	// No subjects member at all (a model that dropped it): the labels stand.
	out, err = Validate(json.RawMessage(`{"title":"Row","description":"A row.","tags":[]}`))
	if err != nil || len(out.Subjects) != 0 {
		t.Errorf("%+v %v", out, err)
	}
	// A subject entry of the wrong shape is an invalid answer (strict parsing), a text in a number field too.
	for _, bad := range []string{`[{"frame":"one","present":true,"x":0,"y":0,"w":1,"h":1,"head_top_y":0}]`, `[{"frame":1,"present":true,"x":"0.2","y":0,"w":1,"h":1,"head_top_y":0}]`, `[{"frame":1,"extra":1}]`, `{"a":1}`} {
		if _, err := Validate(answerWith(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	// At most MaxSubjects entries are read.
	many := strings.Repeat(`{"frame":1,"present":true,"x":0.1,"y":0.1,"w":0.5,"h":0.5,"head_top_y":0.1},`, 40)
	out, err = Validate(answerWith("[" + strings.TrimSuffix(many, ",") + "]"))
	if err != nil || len(out.Subjects) != MaxSubjects {
		t.Errorf("%d subjects, %v", len(out.Subjects), err)
	}
}

// The model's numbers are bounded before they are stored: a box that is not inside the frame, a head that is not inside its box, a frame
// that does not exist or twice, a box that is a speck, each is dropped (never repaired), and a clip with none left has no track.
func TestSubjectTrackBoundsWhatTheModelSaid(t *testing.T) {
	fr := frameSet{jpegs: [][]byte{fixtureFrame(1), fixtureFrame(4), fixtureFrame(7), fixtureFrame(10)}, at: []int64{1500, 4500, 7500, 10500}}
	good := SubjectRead{Frame: 1, Present: true, X: 0.2, Y: 0.2, W: 0.5, H: 0.7, HeadTopY: 0.23}
	tr := subjectTrack([]SubjectRead{
		good,
		{Frame: 2, Present: true, X: 0.2, Y: 0.2, W: 0.9, H: 0.7, HeadTopY: 0.23},    // runs out of the frame
		{Frame: 3, Present: true, X: 0.2, Y: 0.2, W: 0.5, H: 0.7, HeadTopY: 0.95},    // the head is under the box's top 60 %
		{Frame: 4, Present: true, X: -0.2, Y: 0.2, W: 0.5, H: 0.7, HeadTopY: 0.23},   // left of the frame
		{Frame: 9, Present: true, X: 0.2, Y: 0.2, W: 0.5, H: 0.7, HeadTopY: 0.23},    // no such frame
		{Frame: 1, Present: true, X: 0.3, Y: 0.3, W: 0.4, H: 0.5, HeadTopY: 0.33},    // frame 1 twice: the first stays
		{Frame: 0, Present: true, X: 0.2, Y: 0.2, W: 0.5, H: 0.7, HeadTopY: 0.23},    // frames count from 1
		{Frame: 2, Present: false, X: 0.2, Y: 0.2, W: 0.5, H: 0.7, HeadTopY: 0.23},   // nobody there
		{Frame: 3, Present: true, X: 0.2, Y: 0.2, W: 0.01, H: 0.7, HeadTopY: 0.23},   // a speck
		{Frame: 3, Present: true, X: 1e308, Y: 0.2, W: 0.5, H: 0.7, HeadTopY: 0.23},  // a number out of range
		{Frame: 4, Present: true, X: 0.2, Y: 0.2, W: 1e-300, H: 0.7, HeadTopY: 0.23}, // another
		{Frame: 4, Present: true, X: 0.2, Y: 0.2, W: 0.5, H: 0.7, HeadTopY: 0.1},     // the head above the box
	}, fr)
	if tr == nil || len(tr.Samples) != 1 {
		t.Fatalf("track %+v", tr)
	}
	s := tr.Samples[0]
	if s.AtMs != 1500 || s.X != 0.2 || s.HeadTopY != 0.23 || tr.Aspect <= 0 {
		t.Errorf("sample %+v aspect %v", s, tr.Aspect)
	}
	if got := subjectTrack([]SubjectRead{{Frame: 1}}, fr); got != nil {
		t.Errorf("a track for nobody: %+v", got)
	}
	if got := subjectTrack(nil, frameSet{}); got != nil {
		t.Errorf("a track without frames: %+v", got)
	}
	if got := subjectTrack([]SubjectRead{good}, frameSet{jpegs: [][]byte{[]byte("not a jpeg")}, at: []int64{0}}); got != nil {
		t.Errorf("a track from a picture that is not one: %+v", got)
	}
}

// The quick look stores the subject boxes in its detail, with the times of the frames the model saw; a forced run with KeepLabels stores
// them without touching the title, description and tags.
func TestTheDetailHoldsTheSubjectsAndKeepLabelsLeavesTheLabels(t *testing.T) {
	r := newRig(t, rigOptions{})
	res := mustRun(t, r, Options{})
	if res.Subjects != 3 {
		t.Fatalf("result %+v", res)
	}
	a := r.store.snapshot()
	tr, ok := subject.FromDetail(a.QuickLook)
	if !ok || len(tr.Samples) != 3 || tr.Samples[0].HeadTopY != 0.22 || tr.Samples[1].AtMs <= tr.Samples[0].AtMs {
		t.Fatalf("stored subjects %+v from %s", tr, a.QuickLook)
	}
	// A later quick look that would label the clip differently, run with KeepLabels.
	r.api.reply = func(int) (int, string) {
		return toolReply(`{"title":"Another title","description":"Something else entirely.","tags":["other"],"subjects":[{"frame":1,"present":true,"x":0.3,"y":0.1,"w":0.4,"h":0.8,"head_top_y":0.12}]}`, 1750, 110)
	}
	res, err := r.svc.Run(context.Background(), r.asset.WorkspaceID, r.asset.ID, Options{Force: true, KeepLabels: true})
	if err != nil || res.Outcome != OutcomeDone {
		t.Fatalf("%+v %v", res, err)
	}
	b := r.store.snapshot()
	if b.Title != a.Title || b.Description != a.Description || strings.Join(b.Tags, ",") != strings.Join(a.Tags, ",") {
		t.Errorf("the labels changed: %q %q %v", b.Title, b.Description, b.Tags)
	}
	tr2, ok := subject.FromDetail(b.QuickLook)
	if !ok || len(tr2.Samples) != 1 || tr2.Samples[0].HeadTopY != 0.12 {
		t.Errorf("the new subjects were not stored: %+v", tr2)
	}
	// Without KeepLabels the labels follow the model.
	if _, err := r.svc.Run(context.Background(), r.asset.WorkspaceID, r.asset.ID, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	if c := r.store.snapshot(); c.Description != "Something else entirely" {
		t.Errorf("a forced run without KeepLabels kept the old description: %q", c.Description)
	}
}

// A run that only adds the boxes to a good quick look leaves the quick look as it is when it fails.
func TestAFailedKeepLabelsRunLeavesTheQuickLookAlone(t *testing.T) {
	r := newRig(t, rigOptions{})
	mustRun(t, r, Options{})
	before := r.store.snapshot()
	r.api.reply = func(int) (int, string) {
		return toolReply(`{"title":"x","description":"Visit www.example.com now","tags":[],"subjects":[]}`, 1750, 110)
	}
	res, err := r.svc.Run(context.Background(), r.asset.WorkspaceID, r.asset.ID, Options{Force: true, KeepLabels: true})
	if err != nil || res.Outcome != OutcomeFailed || res.Reason != FailInvalidOutput {
		t.Fatalf("%+v %v", res, err)
	}
	after := r.store.snapshot()
	if *after.QuickLookStatus != store.QuickLookSucceeded || string(after.QuickLook) != string(before.QuickLook) || after.Title != before.Title {
		t.Errorf("the quick look was touched: %s / %s", after.QuickLook, string(before.QuickLook))
	}
	// Without KeepLabels the same failure is recorded, as it always was.
	if res, _ := r.svc.Run(context.Background(), r.asset.WorkspaceID, r.asset.ID, Options{Force: true}); res.Outcome != OutcomeFailed {
		t.Fatalf("%+v", res)
	}
	if *r.store.snapshot().QuickLookStatus != store.QuickLookFailed {
		t.Error("a failed forced run was not recorded")
	}
}

// A clip with nobody in it was asked and has no boxes: the detail says so, so a backfill does not ask again.
func TestAClipWithNobodyInItIsMarkedAsRead(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.api.reply = func(int) (int, string) {
		return toolReply(`{"title":"Empty gym","description":"An empty gym.","tags":["gym"],"subjects":[{"frame":1,"present":false,"x":0,"y":0,"w":0,"h":0,"head_top_y":0}]}`, 1750, 110)
	}
	if res := mustRun(t, r, Options{}); res.Outcome != OutcomeDone || res.Subjects != 0 {
		t.Fatalf("%+v", res)
	}
	a := r.store.snapshot()
	if _, ok := subject.FromDetail(a.QuickLook); ok {
		t.Error("boxes for nobody")
	}
	if !SubjectsRead(a.QuickLook) {
		t.Errorf("the detail does not say the boxes were asked for: %s", a.QuickLook)
	}
	if SubjectsRead(json.RawMessage(`{"prompt_version":"quicklook.v1"}`)) || SubjectsRead(nil) || SubjectsRead(json.RawMessage(`nope`)) {
		t.Error("SubjectsRead")
	}
}
