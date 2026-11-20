package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The per-edit spend ceiling (the cost guard), in replay mode over the echo agent's committed cassettes: no API, no key, no cost.

// A run whose budget is above the ceiling is made with the ceiling as its budget, and stops at it, saying the ceiling is why.
func TestSpendCeilingCapsTheBudgetOfANewRun(t *testing.T) {
	f := llmFactory(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": cassetteDir}, nil)
	e := newEnv(t, nil, withFactory(f))
	e.svc.Ceilings = spend.Ceilings{Default: 2_000_000}

	// Under the ceiling: the caller's budget stands.
	under := e.create("echo", echoMessage, Budgets{Micros: 500_000})
	if under.BudgetMicros != 500_000 {
		t.Fatalf("a $0.50 budget under a $2 ceiling became %d", under.BudgetMicros)
	}
	// Over it: the budget is the ceiling.
	over := e.create("echo", echoMessage, Budgets{Micros: 5_000_000})
	if over.BudgetMicros != 2_000_000 {
		t.Fatalf("a $5 budget over a $2 ceiling became %d", over.BudgetMicros)
	}

	// A ceiling the first replayed call cannot fit under: the call is refused before it is sent, and the run says the ceiling stopped it.
	e.svc.Ceilings = spend.Ceilings{Default: 2_000_000, Workspaces: map[uuid.UUID]int64{e.ws.ID.UUID: 1}}
	e.runner.deps.Ceilings = e.svc.Ceilings
	tiny := e.create("echo", echoMessage, Budgets{})
	if tiny.BudgetMicros != 1 {
		t.Fatalf("budget %d, want the 1 micro-dollar ceiling", tiny.BudgetMicros)
	}
	e.execute(tiny)
	assertBudgetStop(t, e, tiny, LimitDollars)
	out := outputOf(t, e.get(tiny))
	if !strings.Contains(fmt.Sprint(out["summary"]), "spend ceiling of $0.0000") || out["ceiling_source"] != "workspace" || out["ceiling_micros"] != float64(1) {
		t.Fatalf("the summary does not say the ceiling stopped the run: %v", out)
	}
	errs := eventsOf(e.events(tiny), store.EventError)
	if !strings.Contains(string(errs[0].Payload), "spend ceiling") {
		t.Fatalf("the error event does not name the ceiling: %s", errs[0].Payload)
	}
	if e.sink.effects() != 0 {
		t.Fatalf("effects = %d: a refused call ran a tool", e.sink.effects())
	}
}

// Who a stop at the dollar limit is blamed on.
func TestCeilingBinds(t *testing.T) {
	for _, c := range []struct {
		ceiling spend.Ceiling
		budget  int64
		want    bool
	}{
		{spend.Ceiling{}, 1_000_000, false},
		{spend.Ceiling{Micros: 500_000, Source: "default"}, 960_000, true},        // lowered after the run was made
		{spend.Ceiling{Micros: 2_000_000, Source: "default"}, 2_000_000, false},   // a plan's own $2 budget, not the default ceiling
		{spend.Ceiling{Micros: 800_000, Source: "plan creator"}, 800_000, true},   // a deliberate setting capped it
		{spend.Ceiling{Micros: 3_000_000, Source: "workspace"}, 2_000_000, false}, // above the budget
	} {
		if got := ceilingBinds(c.ceiling, c.budget); got != c.want {
			t.Errorf("ceilingBinds(%+v, %d) = %v", c.ceiling, c.budget, got)
		}
	}
}

// The runtime enforces the ceiling mid-run: a run that made its first replayed call and ran its tool, then was taken over by a worker
// whose ceiling is below what the run has already spent, stops before the next model call, with the ceiling as the reason, and the
// tool's effect is not repeated.
func TestSpendCeilingStopsARunMidRun(t *testing.T) {
	f := llmFactory(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": cassetteDir}, nil)
	e := newEnv(t, nil, withFactory(f))
	run := e.create("echo", echoMessage, Budgets{})
	if !e.crashAt(run, "tool_run_done") { // the first call answered, the note ran
		t.Fatal("not reached")
	}
	spent := e.get(run).CostMicros
	if spent <= 0 || e.sink.effects() != 1 {
		t.Fatalf("before the takeover: cost %d, effects %d", spent, e.sink.effects())
	}
	// The ceiling of this workspace is now below the run's cost so far (a workspace override wins over the default).
	e.runner.deps.Ceilings = spend.Ceilings{Default: 2_000_000, Workspaces: map[uuid.UUID]int64{e.ws.ID.UUID: spent - 1}}
	e.ageHeartbeat(run)
	if got := e.execute(run); got != OutcomeFinished {
		t.Fatalf("resume = %s", got)
	}
	assertBudgetStop(t, e, run, LimitDollars)
	r := e.get(run)
	out := outputOf(t, r)
	if out["ceiling_source"] != "workspace" || !strings.Contains(fmt.Sprint(out["summary"]), "spend ceiling") {
		t.Fatalf("the summary does not say the workspace's ceiling stopped the run: %v", out)
	}
	if r.CostMicros != spent || e.sink.effects() != 1 {
		t.Fatalf("after the stop: cost %d (was %d), effects %d", r.CostMicros, spent, e.sink.effects())
	}
}
