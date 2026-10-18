package evals

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// liveCase is a live case with these sample scores (nil: a failed sample).
func liveResult(id string, scores ...*float64) CaseResult {
	c := CaseResult{Case: id, Mode: "live", HeldOut: strings.HasPrefix(id, "held")}
	for i, s := range scores {
		smp := SampleResult{N: i + 1, Score: s}
		if s == nil {
			smp.Failed, smp.Reason = true, "budget"
		}
		c.Samples = append(c.Samples, smp)
	}
	aggregate(&c)
	return c
}

func run(cases ...CaseResult) *RunResult {
	r := &RunResult{Cases: cases}
	summarise(r)
	return r
}

func TestAggregateCountsFailuresAsZero(t *testing.T) {
	c := liveResult("a", sc(80), nil, sc(90))
	if c.N != 3 || *c.Score != 56.7 || c.SD == nil || len(c.Failures) != 1 || c.FailureRate < 0.33 || c.FailureRate > 0.34 {
		t.Errorf("aggregate: n %d score %v sd %v failures %v rate %v", c.N, *c.Score, c.SD, c.Failures, c.FailureRate)
	}
	// An unjudged sample is left out when another one was judged, and counts when none was.
	c = CaseResult{Samples: []SampleResult{{N: 1, Score: sc(80)}, {N: 2, Score: sc(60), Unjudged: true}}}
	aggregate(&c)
	if c.N != 1 || *c.Score != 80 {
		t.Errorf("unjudged left in: %v n %d", *c.Score, c.N)
	}
	c = CaseResult{Samples: []SampleResult{{N: 1, Score: sc(60), Unjudged: true}}}
	aggregate(&c)
	if c.N != 1 || *c.Score != 60 {
		t.Errorf("only unjudged: %v", c.Score)
	}
	r := run(liveResult("a", sc(80), sc(84)), liveResult("b", sc(90), nil), liveResult("held", sc(70), sc(72)))
	if r.Samples != 6 || r.Failures != 1 || r.MeanSE == nil || *r.Mean != 63.5 || *r.HeldOut != 71 {
		t.Errorf("summary: samples %d failures %d mean %v ± %v held %v", r.Samples, r.Failures, *r.Mean, r.MeanSE, *r.HeldOut)
	}
}

// The director's numbers: the same code scored 91.0, 85.0 and 83.7 on one case. A gain of a point or two is noise, and is refused; a
// large, consistent gain is kept; a failure rate that rises is refused whatever the mean says.
func TestAcceptIsNoiseAware(t *testing.T) {
	rule := Rule{MinGain: 0.5, MaxRegression: 5, HeldOutSlack: 1}
	before := run(liveResult("a", sc(91), sc(85)), liveResult("b", sc(84), sc(88)), liveResult("held", sc(89.3), sc(84.2)))
	noisy := run(liveResult("a", sc(93), sc(86)), liveResult("b", sc(85), sc(90)), liveResult("held", sc(90.1), sc(85)))
	d := Accept(before, noisy, rule)
	if d.Accepted || !strings.Contains(strings.Join(d.Reasons, "; "), "within the noise") || d.GainSE <= 0 {
		t.Errorf("a small gain inside the noise was kept: %+v", d)
	}
	better := run(liveResult("a", sc(97), sc(96)), liveResult("b", sc(96), sc(97)), liveResult("held", sc(88), sc(87)))
	if d := Accept(before, better, rule); !d.Accepted || d.Gain < 9 {
		t.Errorf("a clear gain was refused: %+v", d)
	}
	// A case that drops 4 points inside a noise of about ±4 is not a regression; the same drop with no noise (stored scores) is, past 5.
	drop := run(liveResult("a", sc(98), sc(99)), liveResult("b", sc(80), sc(84)), liveResult("held", sc(88), sc(87)))
	if d := Accept(before, drop, rule); strings.Contains(strings.Join(d.Reasons, "; "), "case b dropped") {
		t.Errorf("a drop inside the noise counted as a regression: %v", d.Reasons)
	}
	// More failures: refused even with a higher mean.
	failing := run(liveResult("a", sc(99), sc(99)), liveResult("b", sc(99), nil), liveResult("held", sc(88), sc(87)))
	if d := Accept(before, failing, rule); d.Accepted || !strings.Contains(strings.Join(d.Reasons, "; "), "failure rate rose") {
		t.Errorf("a rising failure rate was kept: %+v", d)
	}
	// One live sample: the noise sd stands in for its spread.
	one := run(liveResult("a", sc(92)), liveResult("b", sc(88)))
	if v := caseVar(one.Cases[0], DefaultNoiseSD); v != DefaultNoiseSD*DefaultNoiseSD {
		t.Errorf("a single live sample's variance %v", v)
	}
}

func TestRunRepeatsLiveCasesAndScoresFailures(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fb.yaml", "feedback:\n  - {id: music, text: music only, detector: music_only}\n")
	path := write(t, dir, "s.yaml", "suite: s\nfeedback: fb.yaml\ndefaults: {workspace: wks_x, budget_usd: 1}\ncases:\n  - id: one\n    footage: [ast_a]\n")
	s, err := LoadSuite(path, os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	ws := ids.WorkspaceID{UUID: uuid.New()}
	calls := 0
	phx := &phoenixFake{datasets: map[string]bool{}}
	srv := httptest.NewServer(phx)
	defer srv.Close()
	d := Deps{
		Edit: func(context.Context, Case, int64) (Target, error) {
			calls++
			t := Target{Workspace: ws, Project: ids.ProjectID{UUID: uuid.New()}, Version: 1, RunID: uuid.New(), EditMicros: 100_000}
			if calls == 2 {
				return t, errors.New("the editor run run_x failed: budget")
			}
			return t, nil
		},
		Measure: func(context.Context, Target) (Measurement, error) {
			return Measurement{Report: editor.Report{Checks: []editor.Check{{ID: "a", Status: editor.CheckPass}}}, Spec: specOf([]timeline.VideoItem{clip("c1", 0, 2000)}, true)}, nil
		},
		Phoenix: &Phoenix{BaseURL: srv.URL},
	}
	res, err := Run(context.Background(), s, d, Options{Live: true, Repeat: 3, MaxMicros: 2_000_000, OutDir: dir, NoJudge: true})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Cases[0]
	if calls != 3 || len(c.Samples) != 3 || c.N != 3 || len(c.Failures) != 1 || !c.Samples[1].Failed || c.Samples[1].Score != nil {
		t.Fatalf("samples %+v failures %v", c.Samples, c.Failures)
	}
	if *c.Score != 66.7 || c.SD == nil || c.CostMicros != 300_000 || res.FailureRate < 0.33 {
		t.Errorf("case %v ± %v, cost %d, failure rate %v", *c.Score, c.SD, c.CostMicros, res.FailureRate)
	}
	back, err := LoadResults(res.JSONL)
	if err != nil || len(back.Cases[0].Samples) != 3 || back.Repeat != 3 || back.Failures != 1 {
		t.Errorf("results file: %+v %v", back, err)
	}
	runs := 0
	for _, p := range phx.paths {
		if strings.HasSuffix(p, "/runs") {
			runs++
		}
	}
	if runs != 3 {
		t.Errorf("%d experiment runs, want one a sample", runs)
	}
}

func TestBaselineCacheByCodeHash(t *testing.T) {
	root := t.TempDir()
	for _, p := range HashedPaths {
		if err := os.MkdirAll(filepath.Join(root, p, "testdata"), 0o750); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(root, p), "a.go", "package x\n")
	}
	write(t, root, "fb.yaml", "feedback: []\n")
	suitePath := write(t, root, "s.yaml", "suite: s\nfeedback: fb.yaml\ncases:\n  - id: one\n")
	s, err := LoadSuite(suitePath, os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	key := RunKey{Live: true, Repeat: 2, Judge: true}
	h1, err := CodeHash(root, s, key)
	if err != nil {
		t.Fatal(err)
	}
	// A test or test data does not change the hash; code, the suite and the options do.
	write(t, filepath.Join(root, HashedPaths[0]), "x_test.go", "package x\n")
	write(t, filepath.Join(root, HashedPaths[0], "testdata"), "t.json", "{}")
	if h, _ := CodeHash(root, s, key); h != h1 {
		t.Error("a test file changed the hash")
	}
	write(t, filepath.Join(root, HashedPaths[0]), "a.go", "package x\n// changed\n")
	h2, _ := CodeHash(root, s, key)
	if h2 == h1 {
		t.Error("a code change kept the hash")
	}
	if h3, _ := CodeHash(root, s, RunKey{Live: true, Repeat: 3, Judge: true}); h3 == h2 {
		t.Error("another repeat count kept the hash")
	}

	out := t.TempDir()
	now := time.Now()
	r := &RunResult{Suite: "s", RunID: now.UTC().Format("20060102-150405") + "-x", CodeHash: h2}
	f := filepath.Join(out, "s", "r.jsonl")
	if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(`{"summary":{"suite":"s","suite_run":"`+r.RunID+`","code_hash":"`+h2+`","mean":80}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.JSONL = f
	if err := RecordBaseline(out, r, now); err != nil {
		t.Fatal(err)
	}
	if got, ok := LookupBaseline(out, "s", h2, 24*time.Hour, now.Add(time.Hour)); !ok || *got.Mean != 80 {
		t.Errorf("the baseline was not found: %v", ok)
	}
	if _, ok := LookupBaseline(out, "s", h2, 24*time.Hour, now.Add(25*time.Hour)); ok {
		t.Error("a stale baseline was reused")
	}
	if _, ok := LookupBaseline(out, "s", h1, 24*time.Hour, now); ok {
		t.Error("a baseline of other code was reused")
	}
}

func TestEstimateFromRecentRuns(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fb.yaml", "feedback: []\n")
	s, err := LoadSuite(write(t, dir, "s.yaml", `suite: s
feedback: fb.yaml
defaults: {workspace: wks_x, budget_usd: 1.5}
cases:
  - id: a
    footage: [ast_a]
  - id: b
    footage: [ast_b]
  - id: stored
    stored: {project: prj_x, version: 1}
`), os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	hist := []*RunResult{{Cases: []CaseResult{{Case: "a", Samples: []SampleResult{{EditMicros: 400_000, JudgeMicros: 25_000}, {EditMicros: 600_000, JudgeMicros: 25_000}}}}}}
	e := EstimateRun(s, Options{Live: true, Repeat: 2}, true, hist)
	// a: 2 × (0.50 + 0.025); b: 2 × (1.50 budget + 0.025); stored: 1 × 0.025.
	if want := int64(2*(500_000+25_000) + 2*(1_500_000+25_000) + 25_000); e.Micros != want {
		t.Errorf("estimate %d, want %d: %v", e.Micros, want, e.Lines)
	}
	if !strings.Contains(strings.Join(e.Lines, "\n"), "its budget") {
		t.Errorf("lines %v", e.Lines)
	}
}

// The calibration anchor: the judge sees the human-rated edit and its ranges before the edit it judges, never its own case's anchor, and
// a verdict's distance from the human ranges is reported.
func TestJudgeAnchorsAndCalibrationError(t *testing.T) {
	anchor := Anchor{CaseID: "calibration-v4", Sheet: Picture{Label: "sheet", JPEG: testJPEG(t, 50)}, Note: "sick, except the transitions",
		Expect: map[string]Bnd{DimHook: {4, 5}, DimEnergy: {4, 5}}}
	in := judgeIn(t)
	in.Anchors = []Anchor{anchor}
	req := BuildJudgeRequest(in)
	first := req.Messages[0].Content
	if !strings.Contains(first[0].Text, "sick, except the transitions") || !strings.Contains(first[0].Text, "hook 4-5, energy_pace 4-5") ||
		first[1].Type != llm.BlockImage {
		t.Errorf("the anchor does not come first: %q", first[0].Text)
	}
	if judgeIn(t).Key("m") == in.Key("m") {
		t.Error("the anchor is not part of the cache key")
	}
	s := &Suite{Name: "s"}
	d := Deps{Visuals: func(context.Context, Target, Measurement) (*Picture, []Picture, error) {
		return &Picture{Label: "sheet", JPEG: testJPEG(t, 1)}, nil, nil
	}}
	got, err := judgeInput(context.Background(), Case{ID: "calibration-v4"}, Target{}, Measurement{}, s, d, []Anchor{anchor})
	if err != nil || len(got.Anchors) != 0 {
		t.Errorf("a calibration case was shown its own anchor: %d", len(got.Anchors))
	}
	got, _ = judgeInput(context.Background(), Case{ID: "other"}, Target{}, Measurement{}, s, d, []Anchor{anchor})
	if len(got.Anchors) != 1 {
		t.Error("another case was not shown the anchor")
	}
	res := Calibrate(&Calibration{Expect: map[string]Bnd{DimHook: {4, 5}, DimEnergy: {4, 5}}}, &Verdict{Scores: map[string]Score{DimHook: {Score: 3}, DimEnergy: {Score: 4}}}, nil)
	if !res.Judged || res.Agrees || res.Error != 0.5 {
		t.Errorf("calibration %+v", res)
	}
}
