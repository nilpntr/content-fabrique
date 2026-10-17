package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// An agent whose work is usable when a limit comes may end the run gracefully: the run succeeds with the agent's output, and a status
// event names the limit. Without the hook, or when the hook says no, the run fails with reason budget as before.
func TestAgentMayEndGracefullyAtALimit(t *testing.T) {
	var seen []string
	var left BudgetLeft
	graceful := NewEchoAgent(&memSink{})
	graceful.Name = "graceful"
	graceful.OnLimit = func(_ context.Context, rc RunContext, limit string) (json.RawMessage, bool) {
		seen = append(seen, limit)
		left = rc.Left
		return json.RawMessage(`{"summary":"stopped at the limit, the work is usable","implicit_finish":true}`), true
	}
	refusing := NewEchoAgent(&memSink{})
	refusing.Name = "refusing"
	refusing.OnLimit = func(context.Context, RunContext, string) (json.RawMessage, bool) { return nil, false }

	for name, tc := range map[string]struct {
		budgets Budgets
		script  func() *scripted
		limit   string
	}{
		"tool calls": {Budgets{ToolCalls: 1}, func() *scripted {
			return newScripted(use(toolUse("toolu_1", "note", `{"text":"a"}`), toolUse("toolu_2", "note", `{"text":"b"}`)))
		}, LimitToolCalls},
		"tokens": {Budgets{Tokens: 100}, func() *scripted { return newScripted(use(toolUse("toolu_1", "note", `{"text":"a"}`))) }, LimitTokens},
	} {
		seen = nil
		e := newEnv(t, tc.script(), func(c *envConfig) { c.extra = []Agent{graceful, refusing} })
		run := e.create("graceful", "hello", tc.budgets)
		e.execute(run)
		r := e.get(run)
		if r.Status != store.AgentSucceeded || r.FailureReason != nil {
			t.Fatalf("%s: run = %s %v", name, r.Status, r.FailureReason)
		}
		if out := outputOf(t, r); out["implicit_finish"] != true || len(seen) != 1 || seen[0] != tc.limit {
			t.Fatalf("%s: output %v, limits %v", name, out, seen)
		}
		if !left.Known {
			t.Errorf("%s: the hook's RunContext does not know the budget left", name)
		}
		events := e.events(run)
		last := events[len(events)-1]
		var p statusPayload
		_ = json.Unmarshal(last.Payload, &p)
		if last.Type != store.EventStatus || p.State != stateSucceeded || p.Limit != tc.limit {
			t.Errorf("%s: last event %s %s", name, last.Type, last.Payload)
		}
		if len(eventsOf(events, store.EventError)) != 0 {
			t.Errorf("%s: a graceful end wrote an error event", name)
		}

		// The same limit, an agent that says no: the run fails for the budget.
		e = newEnv(t, tc.script(), func(c *envConfig) { c.extra = []Agent{graceful, refusing} })
		run = e.create("refusing", "hello", tc.budgets)
		e.execute(run)
		assertBudgetStop(t, e, run, tc.limit)
	}
}

// A tool sees what is left of the budgets, as of its call.
func TestToolsSeeTheBudgetLeft(t *testing.T) {
	var got BudgetLeft
	probe := NewEchoAgent(&memSink{})
	probe.Name = "probe"
	probe.Tools = append(probe.Tools, NewTool(Spec{Name: "probe", Description: "reports the budget left", Schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		func(_ context.Context, rc RunContext, _ struct{}) (Result, error) {
			got = rc.Left
			return Text("ok"), nil
		}))
	e := newEnv(t, newScripted(use(toolUse("toolu_1", "probe", `{}`)), use(toolUse("toolu_2", "finish", `{"summary":"x"}`))), func(c *envConfig) { c.extra = []Agent{probe} })
	run := e.create("probe", "hello", Budgets{ToolCalls: 10, WallClock: 5 * time.Minute})
	e.execute(run)
	if !got.Known || got.ToolCalls < 8 || got.ToolCalls > 10 || got.Wall < 4*time.Minute || got.Wall > 5*time.Minute || got.Micros <= 0 {
		t.Errorf("the tool saw %+v", got)
	}
}
