package editor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/hooklab"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The hook lab in the editor: check_edit's hook_score on synthetic edits (no database), and plan_edit's lab step and
// HookLabForProject over the database.

// labPlan is a plan that ran the hook lab, which is what puts hook_score in the report.
func labPlan(contentType string) *Plan {
	return &Plan{ContentType: contentType, TargetLengthS: 20, HookLab: &PlanHookLab{Pick: "a", Kind: hooklab.KindBestMoment, AssetID: assetA, InMs: 5000, OutMs: 7000, Total: 1, Variants: 3, WeightsFrom: hooklab.FromDefaults}}
}

func hookShots() []shot {
	return []shot{{asset: assetA, in: 2000, out: 3500}, {asset: assetA, in: 5000, out: 8000}, {asset: assetA, in: 2000, out: 5000}}
}

func TestHookScoreIsOnlyInAReportWhosePlanRanTheLab(t *testing.T) {
	in := CheckInput{Spec: specOf(t, hookShots(), title(0), nil), Assets: map[string]AssetAnalysis{assetA: repsClip()}}
	if _, ok := byID(RunChecks(in))[CheckHookScore]; ok {
		t.Error("hook_score is in a report without a plan")
	}
	in.Plan = &Plan{ContentType: TypeWorkout, TargetLengthS: 20}
	if _, ok := byID(RunChecks(in))[CheckHookScore]; ok {
		t.Error("hook_score is in the report of a plan from before the lab")
	}
	in.Plan = labPlan(TypeWorkout)
	r := RunChecks(in)
	c := wantStatus(t, r, CheckHookScore, CheckPass)
	// A title from the first frame, on live picture: the hook lands, reads, and the first frame is alive.
	if r.Metrics.HookScore != 1 || !strings.Contains(c.Detail, "scores 1.00") || !strings.Contains(c.Detail, "3 of 3 criteria pass") {
		t.Errorf("score %v: %s", r.Metrics.HookScore, c.Detail)
	}
}

func TestHookScoreFailsAWeakOpening(t *testing.T) {
	// No title, no speech, and the first shot is dead time: nothing hooks. The plan's pick is named as the way out.
	dead := []shot{{asset: assetA, in: 0, out: 1500}, {asset: assetA, in: 5000, out: 8000}}
	r := RunChecks(CheckInput{Spec: specOf(t, dead, nil, nil), Plan: labPlan(TypeWorkout), Assets: map[string]AssetAnalysis{assetA: repsClip()}})
	c := wantStatus(t, r, CheckHookScore, CheckFail)
	for _, want := range []string{"scores 0.00", hooklab.InFirst2s, hooklab.FirstFrame, "dead time", "picked variant a", "from 5000 ms", "scored 1.00"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("the detail lacks %q: %s", want, c.Detail)
		}
	}
	// The same opening on the pick's own shot is not told to open on the pick.
	onPick := []shot{{asset: assetA, in: 5100, out: 6600}, {asset: assetA, in: 2000, out: 5000}}
	c = wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, onPick, nil, nil), Plan: labPlan(TypeWorkout), Assets: map[string]AssetAnalysis{assetA: repsClip()}}), CheckHookScore, CheckFail)
	if strings.Contains(c.Detail, "picked variant") {
		t.Errorf("an opening on the pick is sent to the pick: %s", c.Detail)
	}
	// A title that comes on after two seconds is not the hook's text; a late title inside them is weak but passes with the rest.
	late := specOf(t, hookShots(), title(2600), nil)
	c = wantStatus(t, RunChecks(CheckInput{Spec: late, Plan: labPlan(TypeWorkout), Assets: map[string]AssetAnalysis{assetA: repsClip()}}), CheckHookScore, CheckFail)
	if !strings.Contains(c.Detail, "no text and no spoken line") {
		t.Errorf("late title: %s", c.Detail)
	}
	wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, hookShots(), title(1500), nil), Plan: labPlan(TypeWorkout), Assets: map[string]AssetAnalysis{assetA: repsClip()}}), CheckHookScore, CheckPass)
	// Nothing known about the opening at all: not applicable, not a failure.
	bare := RunChecks(CheckInput{Spec: specOf(t, hookShots(), nil, nil), Plan: labPlan(TypeWorkout), Style: noTitleProfile(t)})
	wantStatus(t, bare, CheckHookScore, CheckSkip)
	if bare.Metrics.HookScore != 0 {
		t.Errorf("score of an opening that was not measured: %v", bare.Metrics.HookScore)
	}
}

// noTitleProfile is a firm profile of a creator whose videos have no title.
func noTitleProfile(t *testing.T) *styleprofile.Profile {
	t.Helper()
	p := *ginoProfile(t)
	text := *p.Text
	text.TitleShare = 0
	p.Text = &text
	return &p
}

func TestHookScoreOnSpeech(t *testing.T) {
	sp := talkingClip()
	// The edit opens on the first word of the first sentence, with a title: everything passes.
	clean := talkingInput(t, sp, []shot{{asset: assetA, in: 320, out: 1550}, {asset: assetA, in: 3020, out: 4000}})
	clean.Plan = labPlan(TypeTalking)
	c := wantStatus(t, RunChecks(clean), CheckHookScore, CheckPass)
	if !strings.Contains(c.Detail, "4 of 4 criteria pass") {
		t.Errorf("clean: %s", c.Detail)
	}
	// It opens on the "Um," of the second sentence: weak (the filler), but the opening as a whole passes.
	filler := talkingInput(t, sp, []shot{{asset: assetA, in: 1500, out: 2900}, {asset: assetA, in: 3020, out: 4000}})
	filler.Plan = labPlan(TypeTalking)
	c = wantStatus(t, RunChecks(filler), CheckHookScore, CheckPass)
	if !strings.Contains(c.Detail, "weak: "+hooklab.NoFillerStart) {
		t.Errorf("filler: %s", c.Detail)
	}
	// What it says never quotes the speaker or the title.
	for _, in := range []CheckInput{clean, filler} {
		d := strings.ToLower(byID(RunChecks(in))[CheckHookScore].Detail)
		for _, word := range []string{"truth", "hurts", "heavy"} {
			if strings.Contains(d, word) {
				t.Errorf("the detail quotes %q: %s", word, d)
			}
		}
	}
	// Captions that are on are text from the first word: an opening shot without speech in it is then not "nothing hooks".
	spec := specOf(t, []shot{{asset: assetB, in: 0, out: 1500}, {asset: assetA, in: 320, out: 1550}}, nil, nil)
	spec.Tracks = append(spec.Tracks, &timeline.CaptionsTrack{ID: "captions", From: "transcript", Style: "caption_plain", Enabled: true})
	wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: labPlan(TypeTalking), Speech: map[string]*Speech{assetA: sp}}), CheckHookScore, CheckSkip)
}

func TestHookScoreReadsTheTextAgainstThePerson(t *testing.T) {
	// The long hook title over a posing shot with no room for it: the renderer shows it later, so the text is not off the body here.
	in := CheckInput{Spec: longHookTitle(t), Plan: workoutPlan(), Style: ginoProfile(t), Subjects: poseIndex(false)}
	in.Plan.HookLab = labPlan(TypeWorkout).HookLab
	c := byID(RunChecks(in))[CheckHookScore]
	if !strings.Contains(c.Detail, hooklab.TextOffBody) {
		t.Errorf("no room: %s (%s)", c.Detail, c.Status)
	}
	// With the head measured on its own it stacks beside the head: adjusted, which passes.
	in.Subjects = poseIndex(true)
	if c := byID(RunChecks(in))[CheckHookScore]; strings.Contains(c.Detail, hooklab.TextOffBody) {
		t.Errorf("beside the head: %s", c.Detail)
	}
	// textPlace names every outcome it can tell apart.
	spec := longHookTitle(t)
	texts := textItems(spec)
	if got := textPlace(spec, nil, texts[0], timeline.TotalDuration(spec)); got != "" {
		t.Errorf("no lookup: %q", got)
	}
	if got := textPlace(spec, subject.Index{}.Lookup, texts[0], timeline.TotalDuration(spec)); got != "" {
		t.Errorf("no boxes: %q", got)
	}
	off := false
	onBody := texts[0]
	onBody.AvoidSubject, onBody.Anchor = &off, timeline.AnchorCenter
	if got := textPlace(spec, poseIndex(false).Lookup, onBody, timeline.TotalDuration(spec)); got != hooklab.TextOnBody && got != hooklab.TextOnFace {
		t.Errorf("a text set not to avoid the person, in the middle of them: %q", got)
	}
}

func TestHookScoreTakesItsWeightsAndSharpness(t *testing.T) {
	// A soft first frame under a good title: weak by default, and it fails when the weights say the first frame is what counts.
	in := CheckInput{Spec: specOf(t, hookShots(), title(0), nil), Plan: labPlan(TypeWorkout), Assets: map[string]AssetAnalysis{assetA: repsClip()},
		Sharpness: map[string][]SharpSpan{assetA: {{0, 12000, 0.9}, {2000, 5000, 0.5}}}}
	c := wantStatus(t, RunChecks(in), CheckHookScore, CheckPass)
	if !strings.Contains(c.Detail, "soft") {
		t.Errorf("soft frame: %s", c.Detail)
	}
	heavy := hooklab.Resolved{Weights: hooklab.Weights{hooklab.FirstFrame: 30}, From: "test"}
	in.HookWeights = &heavy
	wantStatus(t, RunChecks(in), CheckHookScore, CheckFail)
	// The shortest stretch that holds the time decides; a time outside every stretch is not measured.
	if s, ok := sharpnessAt(in.Sharpness[assetA], 2500); !ok || s != 0 {
		t.Errorf("inside the rep: %v %v", s, ok)
	}
	if s, ok := sharpnessAt(in.Sharpness[assetA], 9000); !ok || s != 1 {
		t.Errorf("in the shot only: %v %v", s, ok)
	}
	if _, ok := sharpnessAt(in.Sharpness[assetA], 12000); ok {
		t.Error("past the end is measured")
	}
}

// idsOf are the ids of labProject.
type idsOf struct {
	talker, set, wide, foreign ids.AssetID
	prj                        ids.ProjectID
}

// labProject seeds a project with a talking clip, a set with two reps and a wide shot nothing is known about.
func labProject(t *testing.T) (*env, idsOf) {
	t.Helper()
	e := newEnv(t)
	var x idsOf
	x.talker = e.asset(e.wsA, clip{title: "Talker", durMs: 6000})
	x.set = e.asset(e.wsA, clip{title: "Lat pulldown", durMs: 12000, silent: true})
	x.wide = e.asset(e.wsA, clip{title: "Gym wide", durMs: 15000, silent: true})
	x.foreign = e.asset(e.wsB, clip{title: "Someone else's"})
	e.saveSpeech(e.wsA, x.talker, true, talkingClip().Words)
	e.saveSpeech(e.wsB, x.foreign, true, talk("Secret:0", "plans.:40"))
	res := store.AnalysisResult{Version: 1, StepMs: 100, DurationMs: 12000, Series: store.PackedSeries{}, Shots: []store.NewShot{{Idx: 0, StartMs: 0, EndMs: 12000, Signals: []byte(`{}`)}},
		Summary: []byte(`{"version":1,"duration_ms":12000,"shots":1,"sets":1,"reps":2,"clean_reps":2,"period_ms":3000,"dead_ms":6000}`),
		Moments: []store.NewMoment{
			{Kind: store.MomentRep, StartMs: 2000, EndMs: 5000, Score: 0.81, Attrs: []byte(`{"sharpness_rel":0.95}`), Description: "rep 1 of 2"},
			{Kind: store.MomentRep, StartMs: 5000, EndMs: 8000, Score: 0.88, Attrs: []byte(`{"sharpness_rel":0.95}`), Description: "rep 2 of 2"},
			{Kind: store.MomentDead, StartMs: 0, EndMs: 2000, Attrs: []byte(`{}`), Description: "dead time"},
			{Kind: store.MomentDead, StartMs: 8000, EndMs: 12000, Attrs: []byte(`{}`), Description: "dead time"},
		}}
	if err := e.st.SaveAnalysis(context.Background(), e.wsA.ID, x.set, res); err != nil {
		t.Fatal(err)
	}
	x.prj = e.project(e.wsA, "9:16", "", x.talker, x.set, x.wide)
	return e, x
}

func TestPlanEditRunsTheHookLab(t *testing.T) {
	e, x := labProject(t)
	var artifacts []json.RawMessage
	rc := e.runCtx(e.wsA, x.prj, "").WithArtifacts(func(_ context.Context, p json.RawMessage) error {
		artifacts = append(artifacts, p)
		return nil
	})
	text := textOf(e.ok(rc, ToolPlanEdit, planCall(validPlanArgs(x.set.String()))))
	for _, want := range []string{
		"Hook lab: 3 ways to open this video", "weights: defaults",
		"1. variant a, a spoken line: " + x.talker.String() + " from 320 to 1600 ms", "It opens on the line from 400 to 1520 ms",
		"the best moment of the picture: " + x.set.String() + " from 5000 to", "the clip your plan opens on: " + x.set.String() + " from 2000 to",
		`<untrusted_data kind="transcript">`, "variant a: Here is the truth.", "Pick: variant a.", "check_edit's hook_score",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer lacks %q", want)
		}
	}
	if t.Failed() {
		t.Logf("answer:\n%s", text[:min(len(text), 4000)])
	}
	// The speaker's words are inside the data block and nowhere else, and nothing of another workspace is there.
	head, _, _ := strings.Cut(text, "<untrusted_data")
	if strings.Contains(head, "truth") || strings.Contains(text, "Secret") {
		t.Error("the transcript is outside its data block, or another workspace's is in the answer")
	}
	// The lab is recorded before the plan, whose HookLab is its pick.
	if len(artifacts) != 2 {
		t.Fatalf("%d artifacts", len(artifacts))
	}
	var lab struct {
		Kind string     `json:"kind"`
		Data HookRecord `json:"data"`
	}
	if err := json.Unmarshal(artifacts[0], &lab); err != nil || lab.Kind != HookArtifact || lab.Data.RecordVersion != HookRecordVersion || len(lab.Data.Ranked) != 3 || lab.Data.Pick != "a" {
		t.Fatalf("hook_lab artifact: %v %s", err, artifacts[0])
	}
	if best := lab.Data.Ranked[0]; best.Line == nil || best.Total != 1 || best.Variant.Kind != hooklab.KindSpokenLine {
		t.Errorf("the pick: %+v", best)
	}
	var plan struct {
		Kind string `json:"kind"`
		Data Plan   `json:"data"`
	}
	if err := json.Unmarshal(artifacts[1], &plan); err != nil || plan.Kind != PlanArtifact || plan.Data.HookLab == nil {
		t.Fatalf("plan artifact: %v %s", err, artifacts[1])
	}
	if h := plan.Data.HookLab; h.Pick != "a" || h.AssetID != x.talker.String() || h.InMs != 320 || h.Total != 1 || h.Variants != 3 || h.WeightsFrom != hooklab.FromDefaults {
		t.Errorf("the plan's hook lab: %+v", h)
	}
	if p, ok := e.deps.previews.plan(rc.RunID); !ok || p.HookLab == nil {
		t.Error("the plan kept for the run has no hook lab")
	}

	// A playbook moves the weights, and the answer says where they come from.
	e.deps.HookPlaybook = hooklab.StaticPlaybook{Version: "2026-47", Findings: []hooklab.Finding{{ID: "f1", Criterion: hooklab.FirstFrame, Effect: 1.8, Confidence: hooklab.ConfidenceHigh}}}
	e.rebuild()
	if text := textOf(e.ok(e.runCtx(e.wsA, x.prj, ""), ToolPlanEdit, planCall(validPlanArgs(x.set.String())))); !strings.Contains(text, "weights: playbook 2026-47") {
		t.Error("the playbook's weights are not used")
	}
}

func TestHookLabForProject(t *testing.T) {
	e, x := labProject(t)
	ctx := context.Background()
	svc := e.svc
	// Before there is a timeline: the proposals alone.
	rec, err := HookLabForProject(ctx, svc, e.st, e.wsA.ID, x.prj, HookLabOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Version != 0 || len(rec.Ranked) != 3 || rec.Pick != "a" || rec.Ranked[0].Variant.AssetID != x.talker.String() {
		t.Fatalf("before a timeline: %+v", rec)
	}
	// With a timeline that opens on dead time and has no title, the edit as it is ranks last.
	rc := e.runCtx(e.wsA, x.prj, "")
	e.ok(rc, ToolEditTimeline, e.opsArgs(x.prj, addClipOp(x.set, 0, 3000), addClipOp(x.set, 5000, 8000)))
	rec, err = HookLabForProject(ctx, svc, e.st, e.wsA.ID, x.prj, HookLabOptions{})
	if err != nil {
		t.Fatal(err)
	}
	last := rec.Ranked[len(rec.Ranked)-1]
	if rec.Version != 1 || len(rec.Ranked) != 4 || last.Variant.ID != hooklab.KindCurrent || last.Variant.Kind != hooklab.KindCurrent || last.Total != 0 {
		t.Fatalf("with a timeline: version %d, %d ranked, last %+v", rec.Version, len(rec.Ranked), last)
	}
	facts, lines := HookLabText(rec.Lab)
	if !strings.Contains(facts, "the edit as it is") || !strings.Contains(facts, "fails "+hooklab.FirstFrame) || !strings.Contains(lines, "variant a: Here is the truth.") || strings.Contains(facts, "truth") {
		t.Errorf("text:\n%s\n%s", facts, lines)
	}
	// Another workspace sees no such project.
	if _, err := HookLabForProject(ctx, svc, e.st, e.wsB.ID, x.prj, HookLabOptions{}); err == nil {
		t.Error("another workspace ran the lab on the project")
	}
	if _, err := HookLabForProject(ctx, svc, e.st, e.wsA.ID, x.prj, HookLabOptions{Version: 9}); err == nil {
		t.Error("a version that does not exist was scored")
	}
}
