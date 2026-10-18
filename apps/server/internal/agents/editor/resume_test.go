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
	if text := textOf(r.ok(few, ToolRenderPreview, `{}`)); !strings.Contains(text, "Left of your budget: $0.10, 5 tool calls and 2 minutes") || !strings.Contains(text, "finish soon") {
		t.Errorf("a preview near the limit:\n%s", text)
	}
	plenty := r.next()
	plenty.Left = runtime.BudgetLeft{Known: true, ToolCalls: 40, Wall: 12 * time.Minute, Micros: 4_000_000}
	r.smp.series = renderedFlat([3]float64{3000, 13, 27}, [3]float64{3000, 13, 27}) // crushed
	text := textOf(r.ok(plenty, ToolCheckEdit, `{}`))
	if !strings.Contains(text, "Left of your budget: $4.00, 40 tool calls and 12 minutes") || strings.Contains(text, "finish soon") || !strings.Contains(text, "your one revision pass") {
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

	// The same after a change of worker: the preview and the failed checks are in the events, so the note still names them.
	r.newWorker()
	if v, failed := r.deps.previews.lastCheck(r.run.ID); v != 0 || failed != nil {
		t.Fatal("the last check is still in memory")
	}
	out, ok = ag.OnLimit(ctx, r.rc, runtime.LimitWallClock)
	if !ok {
		t.Fatal("a change of worker lost the preview")
	}
	for _, want := range []string{"ran out of time", "version 2", "The final checks still found something to improve", "crushed"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("after a change of worker the note lacks %q: %s", want, out)
		}
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

func TestBudgetLowCountsTheDollars(t *testing.T) {
	left := func(micros int64) runtime.BudgetLeft {
		return runtime.BudgetLeft{Known: true, ToolCalls: 40, Wall: 12 * time.Minute, Micros: micros}
	}
	// A call that has cost $0.10 so far (perCall): a revision pass is four of them, $0.40.
	const perCall = 100_000
	for _, c := range []struct {
		name    string
		l       runtime.BudgetLeft
		perCall int64
		wantLo  bool
	}{
		{"plenty of everything", left(2_000_000), perCall, false},
		{"a revision pass left exactly", left(400_000), perCall, false},
		{"less than a revision pass left", left(399_000), perCall, true},
		{"no cost known: dollars do not count", left(1_000), 0, false},
		{"few tool calls", runtime.BudgetLeft{Known: true, ToolCalls: 7, Wall: 12 * time.Minute, Micros: 2_000_000}, perCall, true},
		{"little time", runtime.BudgetLeft{Known: true, ToolCalls: 40, Wall: 2 * time.Minute, Micros: 2_000_000}, perCall, true},
		{"not known", runtime.BudgetLeft{}, perCall, false},
	} {
		if got := budgetLow(c.l, c.perCall); got != c.wantLo {
			t.Errorf("%s: low = %v, want %v", c.name, got, c.wantLo)
		}
	}
	if got := budgetNote(left(150_000), perCall); !strings.Contains(got, "Left of your budget: $0.15, 40 tool calls and 12 minutes.") || !strings.Contains(got, "finish soon") {
		t.Errorf("a note near the dollar limit: %q", got)
	}
	if got := budgetNote(left(3_500_000), perCall); !strings.Contains(got, "$3.50") || strings.Contains(got, "finish soon") {
		t.Errorf("a note with room: %q", got)
	}
	if got := budgetNote(runtime.BudgetLeft{}, 0); got != "" {
		t.Errorf("an unknown budget: %q", got)
	}
}

// What a call costs is what the run's recent calls cost (not the first, which writes the whole prompt to the cache), raised by half for the
// longer conversation later calls read, and never under the floor that holds before the run has shown anything. Without ledger rows the
// run's cost over its tool calls stands in.
func TestCallCostFollowsWhatTheRecentCallsCost(t *testing.T) {
	for _, c := range []struct {
		name  string
		calls []int64
		cost  int64
		tools int
		want  int64
	}{
		{"before the first call", nil, 0, 0, minCallMicros},
		{"nothing spent yet", nil, 0, 1, minCallMicros},
		{"a cheap first call", []int64{30_000}, 30_000, 1, minCallMicros},
		{"no ledger rows: the run's cost per tool call", nil, 200_000, 2, 150_000},
		{"one big first call is left out of the mean", []int64{233_000, 33_000}, 266_000, 2, minCallMicros},
		{"the last three calls count", []int64{233_000, 900_000, 100_000, 100_000, 100_000}, 1_433_000, 5, 150_000},
		{"two calls after the first", []int64{233_000, 100_000, 200_000}, 533_000, 3, 225_000},
	} {
		if got := callCostMicros(c.calls, c.cost, c.tools); got != c.want {
			t.Errorf("%s: callCostMicros = %d, want %d", c.name, got, c.want)
		}
	}
}

// The shape of a live run that was told it was low on money and then ended "out of budget" at $0.57 of $0.96 after seven model calls: the
// first wrote 45,000 tokens to the cache ($0.23), the others cost between $0.025 and $0.106. Judged on the mean of all its calls ($0.08, so
// $0.12 for the next) the $0.39 left was "less than a revision pass" and the check told the editor not to revise; judged on its recent
// calls ($0.048) a revision pass of four calls is $0.19 and it is not low.
func TestARunWithAFatFirstCallAndMoneyLeftIsNotLow(t *testing.T) {
	calls := []int64{232_996, 33_229, 103_771, 106_430, 46_291, 24_977, 24_662}
	var total int64
	for _, c := range calls {
		total += c
	}
	perCall := callCostMicros(calls, total, len(calls))
	left := runtime.BudgetLeft{Known: true, ToolCalls: 53, Wall: 13 * time.Minute, Micros: 960_000 - total}
	if left.Micros < 380_000 || left.Micros > 390_000 {
		t.Fatalf("the test's own arithmetic: %d left", left.Micros)
	}
	if budgetLow(left, perCall) {
		t.Errorf("$0.39 left, recent calls at about $0.03 (per call %d): the run is told it is low", perCall)
	}
	if note := budgetNote(left, perCall); strings.Contains(note, "finish soon") {
		t.Errorf("a run with $0.39 left is told to finish: %q", note)
	}
	// The old figure, all calls and the run's cost per tool call, said low: the bug this test keeps out.
	if old := total / int64(len(calls)); !budgetLow(left, old+old/2) {
		t.Logf("(the mean of all calls, %d, would not have said low here)", old)
	}
}

// The main settings: a first edit has $0.96 and the editor's max_tokens is 24,000 (a worst-case call of $0.48 and more). Judged on the
// worst case it was "low" from the first call on, and the editor was told not to revise. Judged on what the run spent it is not low
// until the money is nearly gone: after one call of $0.06 a fresh first edit has plenty, and with $0.10 left it is low.
func TestAFreshFirstEditIsNotLowAndOneWithTenCentsIs(t *testing.T) {
	perCall := callCostMicros([]int64{60_000}, 60_000, 1) // one call of $0.06 so far
	fresh := runtime.BudgetLeft{Known: true, ToolCalls: 55, Wall: 14 * time.Minute, Micros: 900_000}
	if budgetLow(fresh, perCall) {
		t.Errorf("a fresh first edit ($0.90 left of $0.96, after one call of $0.06) is low; per call %d", perCall)
	}
	if got := budgetNote(fresh, perCall); strings.Contains(got, "finish soon") || strings.Contains(got, "not much") {
		t.Errorf("a fresh first edit is told to finish: %q", got)
	}
	poor := runtime.BudgetLeft{Known: true, ToolCalls: 30, Wall: 10 * time.Minute, Micros: 100_000}
	if !budgetLow(poor, perCall) {
		t.Errorf("a run with $0.10 left is not low; per call %d", perCall)
	}
	if got := budgetNote(poor, perCall); !strings.Contains(got, "finish soon") {
		t.Errorf("a run with $0.10 left is not told to finish: %q", got)
	}
}

// The tools read what the run spent from its row: a fresh run with money left gets no warning, and a failing first check says not to
// revise only when the money left is under a revision pass of calls like the ones so far.
func TestToolsWarnWhenTheDollarsRunLow(t *testing.T) {
	r := newDBRun(t)
	// The run so far: 4 tool calls and $0.40, so a call is $0.10 plus half, $0.15; a revision pass (four calls) is $0.60.
	if _, err := r.pool.Exec(context.Background(), `UPDATE agent_runs SET cost_micros = 400000, tool_calls = 4 WHERE id = $1`, r.run.ID); err != nil {
		t.Fatal(err)
	}
	r.ok(r.next(), ToolPlanEdit, planCall(validPlanArgs(r.clip.String())))
	r.ok(r.next(), ToolEditTimeline, r.opsArgs(r.prj, addClipOp(r.clip, 6000, 9000), addClipOp(r.clip, 9000, 12000)))

	rich := r.next()
	rich.Left = runtime.BudgetLeft{Known: true, ToolCalls: 40, Wall: 12 * time.Minute, Micros: 700_000}
	if text := textOf(r.ok(rich, ToolRenderPreview, `{}`)); !strings.Contains(text, "Left of your budget: $0.70") || strings.Contains(text, "finish soon") {
		t.Errorf("a preview with money left:\n%s", text)
	}
	poor := r.next()
	poor.Left = runtime.BudgetLeft{Known: true, ToolCalls: 40, Wall: 12 * time.Minute, Micros: 100_000}
	r.smp.series = renderedFlat([3]float64{3000, 24, 40}, [3]float64{3000, 24, 40})
	text := textOf(r.ok(poor, ToolCheckEdit, `{}`))
	if !strings.Contains(text, "$0.10") || !strings.Contains(text, "finish soon") || !strings.Contains(text, "do not start a revision") {
		t.Errorf("a check with the dollars nearly gone:\n%s", text)
	}
}
