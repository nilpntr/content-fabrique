package editor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
)

// gateEnv is an environment with the check gate on, a project with one analysed clip (reps from 6 s in steps of 3 s, a low-key
// picture) and a recorder for the artifacts of the run.
type gateEnv struct {
	*env
	prj       ids.ProjectID
	clip      ids.AssetID
	rc        runtime.RunContext
	artifacts *[]json.RawMessage
}

func newGateEnv(t *testing.T) *gateEnv {
	t.Helper()
	e := newEnv(t)
	e.deps.noCheckGate = false
	e.rebuild()
	a := e.asset(e.wsA, clip{durMs: 60000})
	e.saveAnalysisSeries(e.wsA, a, 6, flatSeries(t, 24, 40))
	prj := e.project(e.wsA, "9:16", "", a)
	e.sheet(prj)
	var arts []json.RawMessage
	rc := e.runCtx(e.wsA, prj, "").WithArtifacts(func(_ context.Context, p json.RawMessage) error {
		arts = append(arts, p)
		return nil
	})
	return &gateEnv{env: e, prj: prj, clip: a, rc: rc, artifacts: &arts}
}

func TestCheckEditReadsTheTimelineAndTheAnalysis(t *testing.T) {
	g := newGateEnv(t)
	if msg := g.toolErr(g.rc, ToolCheckEdit, `{}`); !strings.Contains(msg, "no timeline") {
		t.Fatalf("no timeline: %q", msg)
	}
	// Two shots on rep boundaries (6 to 9 s and 9 to 12 s of the analysed clip), 3 s each.
	g.ok(g.nextCall(g.rc), ToolEditTimeline, g.opsArgs(g.prj, addClipOp(g.clip, 6000, 9000), addClipOp(g.clip, 9000, 12000)))
	res := g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`)
	text := textOf(res)
	for _, want := range []string{"check_edit for version 1 (general)", "FAIL hook_length: the first shot", "FAIL hook_title", "PASS rep_boundaries: 4 of 4 shot edges", "PASS brightness_jump", "your one revision pass"} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer lacks %q:\n%s", want, text)
		}
	}
	if len(*g.artifacts) < 2 { // the edit's timeline_version artifact and the check
		t.Fatalf("%d artifacts", len(*g.artifacts))
	}
	var rec struct {
		Kind string      `json:"kind"`
		Data CheckRecord `json:"data"`
	}
	if err := json.Unmarshal((*g.artifacts)[len(*g.artifacts)-1], &rec); err != nil || rec.Kind != CheckArtifact || rec.Data.Nth != 1 || rec.Data.Version != 1 || rec.Data.Failed < 2 || len(rec.Data.Checks) != 12 || rec.Data.RecordVersion != CheckRecordVersion {
		t.Fatalf("the recorded check: %v %+v", err, rec)
	}

	// The second check is the last: no more revising.
	res = g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`)
	if text := textOf(res); !strings.Contains(text, "Do not revise again") {
		t.Errorf("the second check:\n%s", text)
	}
}

func TestCheckEditJudgesAgainstThePlan(t *testing.T) {
	g := newGateEnv(t)
	g.ok(g.rc, ToolPlanEdit, planCall(validPlanArgs(g.clip.String())))
	g.ok(g.nextCall(g.rc), ToolEditTimeline, g.opsArgs(g.prj, addClipOp(g.clip, 6000, 9000), addClipOp(g.clip, 9000, 12000)))
	text := textOf(g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`))
	// The plan is for 20 s, so a 6 s video is short, and its type sets the thresholds.
	for _, want := range []string{"(workout, target 20 s)", "FAIL duration", "under 15.00 s", "FAIL beat_sync: there is no beat grid"} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer lacks %q:\n%s", want, text)
		}
	}
}

func TestFinishNeedsACheckOfTheLatestVersion(t *testing.T) {
	g := newGateEnv(t)
	g.ok(g.nextCall(g.rc), ToolEditTimeline, g.opsArgs(g.prj, addClipOp(g.clip, 6000, 9000), addClipOp(g.clip, 9000, 12000)))
	g.ok(g.nextCall(g.rc), ToolRenderPreview, `{}`)
	res, _ := g.call(g.nextCall(g.rc), ToolFinish, `{"summary":"Done."}`)
	if !res.IsError || res.Final || !strings.Contains(textOf(res), "check_edit on version 1") {
		t.Fatalf("finish before check_edit: %+v %s", res, textOf(res))
	}
	g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`)
	if res := g.ok(g.nextCall(g.rc), ToolFinish, `{"summary":"Done."}`); !res.Final {
		t.Fatalf("finish after the check: %+v", res)
	}

	// A revision is a new version, and it needs a check of its own.
	g.ok(g.nextCall(g.rc), ToolEditTimeline, `{"base_version":1,"ops":[{"op":"set_look","preset":"bw"}]}`)
	g.ok(g.nextCall(g.rc), ToolRenderPreview, `{}`)
	if res, _ := g.call(g.nextCall(g.rc), ToolFinish, `{"summary":"Done."}`); !res.IsError || !strings.Contains(textOf(res), "check_edit on version 2") {
		t.Fatalf("finish after a revision that was not checked: %+v", res)
	}
	g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`)
	if res := g.ok(g.nextCall(g.rc), ToolFinish, `{"summary":"Done."}`); !res.Final {
		t.Fatalf("finish after the second check: %+v", res)
	}

	// Another run's check does not count for this one, and a run that changed nothing needs none.
	other := g.runCtx(g.wsA, g.prj, "")
	g.ok(other, ToolEditTimeline, `{"base_version":2,"ops":[{"op":"set_look","preset":"warm"}]}`)
	g.ok(g.nextCall(other), ToolRenderPreview, `{}`)
	if res, _ := g.call(g.nextCall(other), ToolFinish, `{"summary":"Done."}`); !res.IsError {
		t.Error("a run finished on the strength of another run's check")
	}
	idle := g.runCtx(g.wsA, g.prj, "")
	if res := g.ok(idle, ToolFinish, `{"summary":"Nothing to change.","no_edit_needed":true}`); !res.Final {
		t.Errorf("no_edit_needed: %+v", res)
	}
}

// The check gate does not need previews: with no renderer wired the gate still applies, and a plain-text end of turn is nudged to
// check once.
func TestEndTurnNudgesForTheCheck(t *testing.T) {
	g := newGateEnv(t)
	ag, err := NewAgent(g.deps)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]runtime.Tool{}
	for _, tl := range ag.Tools {
		tools[tl.Name()] = tl
	}
	run := func(rc runtime.RunContext, tool, args string) {
		t.Helper()
		if _, err := tools[tool].Run(context.Background(), rc, json.RawMessage(args)); err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
	}
	ctx := context.Background()
	rc := g.runCtx(g.wsA, g.prj, "")
	run(g.nextCall(rc), ToolEditTimeline, g.opsArgs(g.prj, addClipOp(g.clip, 6000, 9000), addClipOp(g.clip, 9000, 12000)))
	run(g.nextCall(rc), ToolRenderPreview, `{}`)
	// Previewed but not checked: nudged to check, once; after the nudge the text ends the run.
	if d, err := ag.EndTurn(ctx, rc, "Done.", 0); err != nil || !strings.Contains(d.Nudge, "check_edit") || d.Output != nil {
		t.Fatalf("not checked: %+v %v", d, err)
	}
	if d, err := ag.EndTurn(ctx, rc, "Done.", 1); err != nil || d.Nudge != "" || d.Output == nil {
		t.Fatalf("not checked, after the nudge: %+v %v", d, err)
	}
	run(g.nextCall(rc), ToolCheckEdit, `{}`)
	if d, err := ag.EndTurn(ctx, rc, "Done.", 0); err != nil || d.Nudge != "" || d.Output == nil {
		t.Fatalf("checked: %+v %v", d, err)
	}
}

// A call that is run again after a restart answers as it did and writes no second event; and the check is of the run's own newest
// version, not of one a person saved meanwhile.
func TestCheckEditIsRepeatableAndPinnedToTheRunsVersion(t *testing.T) {
	g := newGateEnv(t)
	g.ok(g.nextCall(g.rc), ToolEditTimeline, g.opsArgs(g.prj, addClipOp(g.clip, 6000, 9000), addClipOp(g.clip, 9000, 12000)))
	first := g.nextCall(g.rc)
	g.ok(first, ToolCheckEdit, `{}`)
	events := len(*g.artifacts)
	text := textOf(g.ok(again(first), ToolCheckEdit, `{}`))
	if !strings.Contains(text, "your one revision pass") || len(*g.artifacts) != events {
		t.Errorf("the repeated call: %d events after %d, text:\n%s", len(*g.artifacts), events, text)
	}
	if _, calls := g.deps.previews.checkState(first.RunID); calls != 1 {
		t.Errorf("the repeated call counted as check %d", calls)
	}

	// Another run saves version 2; this run's check is still of its own version 1.
	g.ok(g.runCtx(g.wsA, g.prj, ""), ToolEditTimeline, `{"base_version":1,"ops":[{"op":"set_look","preset":"bw"}]}`)
	text = textOf(g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`))
	if !strings.Contains(text, "check_edit for version 1 ") {
		t.Errorf("the check is not of the run's own version:\n%s", text)
	}
}
