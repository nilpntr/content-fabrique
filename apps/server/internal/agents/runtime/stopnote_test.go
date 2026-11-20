package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// An agent that cannot end gracefully may still say why (live test 2026-11-20, section 4.2: a run that spent its budget and made nothing
// ended with only "the budget was used up"). Its note goes into the failed run's output, next to the runtime's own figures.
func TestAStopNoteIsKeptWithTheBudgetFailure(t *testing.T) {
	noting := NewEchoAgent(&memSink{})
	noting.Name = "noting"
	noting.OnLimit = func(context.Context, RunContext, string) (json.RawMessage, bool) {
		return json.RawMessage(`{"summary":"I ran out of steps before I made a version, so nothing changed.","current_version":3,"limit":"forged"}`), false
	}
	e := newEnv(t, newScripted(use(toolUse("toolu_1", "note", `{"text":"a"}`), toolUse("toolu_2", "note", `{"text":"b"}`))), func(c *envConfig) { c.extra = []Agent{noting} })
	run := e.create("noting", "hello", Budgets{ToolCalls: 1})
	e.execute(run)
	r := e.get(run)
	if r.Status != store.AgentFailed || r.FailureReason == nil || *r.FailureReason != FailBudget {
		t.Fatalf("run = %s reason %v", r.Status, r.FailureReason)
	}
	out := outputOf(t, r)
	if out["summary"] != "I ran out of steps before I made a version, so nothing changed." || out["current_version"] != float64(3) || out["limit"] != LimitToolCalls || out["stopped"] != FailBudget {
		t.Fatalf("output %v", out)
	}
}

// A budget that cannot pay for the first call stops the run before it starts, and says so in numbers rather than "the budget was used up"
// with nothing spent (live test 2026-11-20, section 4.3). The agent's own note is not asked for: no work was done.
func TestAFirstCallTheBudgetCannotPayForIsSaidPlainly(t *testing.T) {
	asked := false
	noting := NewEchoAgent(&memSink{})
	noting.Name = "noting"
	noting.OnLimit = func(context.Context, RunContext, string) (json.RawMessage, bool) {
		asked = true
		return json.RawMessage(`{"summary":"I ran out of budget while studying the clips."}`), false
	}
	e := newEnv(t, newScripted(endTurn("never")), func(c *envConfig) { c.extra = []Agent{noting} })
	run := e.create("noting", "hello", Budgets{Micros: 100})
	e.execute(run)
	assertBudgetStop(t, e, run, LimitDollars)
	out := outputOf(t, e.get(run))
	if s, _ := out["summary"].(string); !strings.Contains(s, "cannot pay for the first step") || !strings.Contains(s, "Nothing of its budget was spent") || out["first_call_refused"] != true {
		t.Fatalf("output %v", out)
	}
	if asked {
		t.Error("the agent was asked for a note about work that never started")
	}
}

func TestWithNoteKeepsTheRuntimesFigures(t *testing.T) {
	sum := json.RawMessage(`{"stopped":"budget","summary":"Stopped before finishing: the dollars budget was used up.","cost_micros":5}`)
	got := map[string]any{}
	if err := json.Unmarshal(withNote(sum, json.RawMessage(`{"summary":"plain words","cost_micros":0,"made_version":false}`)), &got); err != nil {
		t.Fatal(err)
	}
	if got["summary"] != "plain words" || got["cost_micros"] != float64(5) || got["made_version"] != false || got["stopped"] != "budget" {
		t.Errorf("%v", got)
	}
	for _, note := range []string{"", "null", "[1]", "{}"} {
		if string(withNote(sum, json.RawMessage(note))) != string(sum) {
			t.Errorf("note %q changed the summary", note)
		}
	}
}
