package editor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// dbRun is a run of the editor that really exists in the database, with its artifact events written there, so that a test can throw
// away what the worker keeps in memory (as a worker that takes the run over has none) and see what the events give back.
type dbRun struct {
	*gateEnv
	run store.AgentRun
	rc  runtime.RunContext
}

func newDBRun(t *testing.T) *dbRun {
	t.Helper()
	g := newGateEnv(t)
	g.deps.noPlanGate = false
	g.rebuild()
	run, err := g.st.CreateAgentRun(context.Background(), g.wsA.ID, store.NewAgentRun{
		Agent: Name, Stage: Stage, ProjectID: g.prj.UUID, RoutingProfile: "dev-cheap", Model: "claude-haiku-5-5", Effort: "medium", MaxTokens: 1000,
		BudgetMicros: 5_000_000, BudgetTokens: 1_000_000, BudgetToolCalls: 60, BudgetWallClockMs: 15 * 60 * 1000, BilledTo: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	rc := runtime.RunContext{WorkspaceID: g.wsA.ID, RunID: run.ID, ProjectID: g.prj.UUID, Agent: Name, Logger: quiet}
	rc = rc.WithArtifacts(func(ctx context.Context, p json.RawMessage) error {
		_, err := g.st.AppendAgentEventUnfenced(ctx, g.wsA.ID, run.ID, store.EventArtifact, p)
		return err
	})
	return &dbRun{gateEnv: g, run: run, rc: rc}
}

// call is the next tool call of the run.
func (r *dbRun) next() runtime.RunContext { return r.nextCall(r.rc) }

// worker change: what a worker that takes the run over has is nothing in memory.
func (r *dbRun) newWorker() { r.deps.previews.forget(r.run.ID) }

// The plan, the preview and the check are in the run's events, so a worker that takes the run over does not ask the model to plan, to
// preview or to check again.
func TestGatesSurviveAWorkerChange(t *testing.T) {
	r := newDBRun(t)

	// The plan: written, then forgotten by the worker, and the first edit is allowed all the same.
	r.ok(r.next(), ToolPlanEdit, planCall(validPlanArgs(r.clip.String())))
	r.newWorker()
	if _, ok := r.deps.previews.plan(r.run.ID); ok {
		t.Fatal("the plan is still in memory")
	}
	r.ok(r.next(), ToolEditTimeline, r.opsArgs(r.prj, addClipOp(r.clip, 6000, 9000), addClipOp(r.clip, 9000, 12000)))

	// Another run of the project, with no plan of its own, is still stopped: the events are the run's own.
	stranger := r.runCtx(r.wsA, r.prj, "")
	if msg := r.toolErr(stranger, ToolEditTimeline, r.opsArgs(r.prj, `{"op":"set_speed","id":"c1","speed":1.2}`)); !strings.Contains(msg, "plan_edit") {
		t.Errorf("a run with no plan: %q", msg)
	}

	// The preview and the check: written, forgotten, and finish succeeds.
	r.ok(r.next(), ToolRenderPreview, `{}`)
	r.smp.series = renderedFlat([3]float64{3000, 24, 40}, [3]float64{3000, 24, 40})
	r.ok(r.next(), ToolCheckEdit, `{}`)
	r.newWorker()
	if v, _ := r.deps.previews.checkState(r.run.ID); v != 0 {
		t.Fatal("the check is still in memory")
	}
	if res := r.ok(r.next(), ToolFinish, `{"summary":"Done."}`); !res.Final {
		t.Fatalf("finish after a change of worker: %+v", res)
	}

	// A version made after the check needs its own preview and check, in the events as in memory.
	r.ok(r.next(), ToolEditTimeline, `{"base_version":1,"ops":[{"op":"set_speed","id":"c1","speed":1.2}]}`)
	r.newWorker()
	if res, _ := r.call2(ToolFinish); !res.IsError || !strings.Contains(textOf(res), "version 2") {
		t.Fatalf("finish on a version nobody previewed: %+v", res)
	}
	r.ok(r.next(), ToolRenderPreview, `{}`)
	r.newWorker()
	if res, _ := r.call2(ToolFinish); !res.IsError || !strings.Contains(textOf(res), "check_edit on version 2") {
		t.Fatalf("finish on a version nobody checked: %+v", res)
	}
	// The count of checks comes from the events too: the second check of the run is the one after the revision.
	text := textOf(r.ok(r.next(), ToolCheckEdit, `{}`))
	if !strings.Contains(text, "Do not revise again") {
		t.Errorf("the second check was counted as the first:\n%s", text)
	}
	var nths []int
	for _, art := range runArtifacts(context.Background(), r.st, r.wsA.ID, r.run.ID, CheckArtifact) {
		var c struct {
			Nth int `json:"nth"`
		}
		_ = json.Unmarshal(art.Data, &c)
		nths = append(nths, c.Nth)
	}
	if len(nths) != 2 || nths[0] != 1 || nths[1] != 2 {
		t.Errorf("the checks of the run are numbered %v", nths)
	}
}

func (r *dbRun) call2(tool string) (runtime.Result, error) {
	return r.call(r.next(), tool, `{"summary":"Done."}`)
}

func TestBudgetNoteAndGracefulEnd(t *testing.T) {
	r := newDBRun(t)
	ctx := context.Background()
	ag, err := NewAgent(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	// No version made: the limit fails the run as before.
	if out, ok := ag.OnLimit(ctx, r.rc, runtime.LimitToolCalls); ok {
		t.Fatalf("a run with no version ended gracefully: %s", out)
	}

	r.ok(r.next(), ToolPlanEdit, planCall(validPlanArgs(r.clip.String())))
	r.ok(r.next(), ToolEditTimeline, r.opsArgs(r.prj, addClipOp(r.clip, 6000, 9000), addClipOp(r.clip, 9000, 12000)))
	// A version but no preview: nothing to stand behind.
	if out, ok := ag.OnLimit(ctx, r.rc, runtime.LimitWallClock); ok {
		t.Fatalf("a run that never previewed ended gracefully: %s", out)
	}

	// The preview and the check tell the model what is left, and say so when it is not much.
	few := r.next()
	few.Left = runtime.BudgetLeft{Known: true, ToolCalls: 5, Wall: 2 * time.Minute, Micros: 100_000}
	if text := textOf(r.ok(few, ToolRenderPreview, `{}`)); !strings.Contains(text, "Left of your budget: 5 tool calls and 2 minutes") || !strings.Contains(text, "finish soon") {
		t.Errorf("a preview near the limit:\n%s", text)
	}
	plenty := r.next()
	plenty.Left = runtime.BudgetLeft{Known: true, ToolCalls: 40, Wall: 12 * time.Minute, Micros: 4_000_000}
	r.smp.series = renderedFlat([3]float64{3000, 13, 27}, [3]float64{3000, 13, 27}) // crushed
	text := textOf(r.ok(plenty, ToolCheckEdit, `{}`))
	if !strings.Contains(text, "Left of your budget: 40 tool calls and 12 minutes") || strings.Contains(text, "finish soon") || !strings.Contains(text, "your one revision pass") {
		t.Errorf("a check with room:\n%s", text)
	}
	low := r.next()
	low.Left = runtime.BudgetLeft{Known: true, ToolCalls: 4, Wall: 9 * time.Minute, Micros: 4_000_000}
	// (a second check is the one after the revision, so make a new version and check that one near the limit)
	r.ok(r.next(), ToolEditTimeline, `{"base_version":1,"ops":[{"op":"set_speed","id":"c1","speed":1.05}]}`)
	r.ok(r.next(), ToolRenderPreview, `{}`)
	text = textOf(r.ok(low, ToolCheckEdit, `{}`))
	if !strings.Contains(text, "do not start a revision") && !strings.Contains(text, "Do not revise again") {
		t.Errorf("a failing check near the limit:\n%s", text)
	}

	// Now the limit comes: the version is previewed and was checked, so the run ends with the finish's output and says what was left.
	out, ok := ag.OnLimit(ctx, r.rc, runtime.LimitToolCalls)
	if !ok {
		t.Fatal("a previewed version was not finished gracefully")
	}
	var o struct {
		Summary  string `json:"summary"`
		Version  int    `json:"timeline_version"`
		Implicit bool   `json:"implicit_finish"`
		Limit    string `json:"limit"`
	}
	if err := json.Unmarshal(out, &o); err != nil || o.Version != 2 || !o.Implicit || o.Limit != runtime.LimitToolCalls {
		t.Fatalf("output: %v %s", err, out)
	}
	for _, want := range []string{"ran out of steps", "version 2", "crushed"} {
		if !strings.Contains(o.Summary, want) {
			t.Errorf("the note lacks %q: %s", want, o.Summary)
		}
	}
	summary, version := runtime.OutputSummary(out)
	if summary != o.Summary || version == nil || *version != 2 {
		t.Errorf("the runtime reads the output as %q %v", summary, version)
	}

	// The same after a change of worker: the preview is in the events.
	r.newWorker()
	if _, ok := ag.OnLimit(ctx, r.rc, runtime.LimitWallClock); !ok {
		t.Error("a change of worker lost the preview")
	}
}

// A version edited after its preview is still ended gracefully when an earlier version was previewed, and the note says it was not
// previewed.
func TestGracefulEndOfAnUnpreviewedLastEdit(t *testing.T) {
	r := newDBRun(t)
	ag, err := NewAgent(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	r.ok(r.next(), ToolPlanEdit, planCall(validPlanArgs(r.clip.String())))
	r.ok(r.next(), ToolEditTimeline, r.opsArgs(r.prj, addClipOp(r.clip, 6000, 9000), addClipOp(r.clip, 9000, 12000)))
	r.ok(r.next(), ToolRenderPreview, `{}`)
	r.ok(r.next(), ToolEditTimeline, `{"base_version":1,"ops":[{"op":"set_speed","id":"c1","speed":1.05}]}`)
	out, ok := ag.OnLimit(context.Background(), r.rc, runtime.LimitWallClock)
	if !ok || !strings.Contains(string(out), "was not previewed") || !strings.Contains(string(out), "ran out of time") {
		t.Fatalf("%v %s", ok, out)
	}
}
