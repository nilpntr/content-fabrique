package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// An agent's EndTurn hook can answer a turn that ended without a tool call with a nudge: the nudge is stored, sent as a user
// message after the assistant's text, and the hook is told how often it has nudged. A second end_turn it accepts, with its own
// output.
func TestEndTurnNudge(t *testing.T) {
	var seen []int
	a := NewEchoAgent(nil)
	a.Name = "nudger"
	a.EndTurn = func(_ context.Context, _ RunContext, text string, nudges int) (EndTurn, error) {
		seen = append(seen, nudges)
		if nudges == 0 {
			return EndTurn{Nudge: "You stopped too early: call finish."}, nil
		}
		return EndTurn{Output: json.RawMessage(`{"summary":"` + text + `","implicit":true}`)}, nil
	}
	model := newScripted(endTurn("first try"), endTurn("second try"))
	e := newEnv(t, model, func(c *envConfig) { c.extra = []Agent{a} })
	run := e.create("nudger", "hi", Budgets{})
	if got := e.execute(run); got != OutcomeFinished {
		t.Fatalf("outcome = %s", got)
	}
	r := e.get(run)
	if r.Status != store.AgentSucceeded || outputOf(t, r)["summary"] != "second try" || outputOf(t, r)["implicit"] != true {
		t.Fatalf("run = %s %s", r.Status, r.Output)
	}
	if fmt.Sprint(seen) != "[0 1]" {
		t.Errorf("the hook saw %v", seen)
	}
	req := model.request(1)
	if len(req.Messages) != 3 || req.Messages[1].Role != llm.RoleAssistant || req.Messages[2].Role != llm.RoleUser ||
		req.Messages[2].Content[0].Text != "You stopped too early: call finish." {
		t.Fatalf("second request: %+v", req.Messages)
	}
	// The conversation is rebuilt from the events the same way (a resumed run sends the same nudge).
	c, err := rebuild(e.events(run))
	if err != nil || c.nudges != 1 {
		t.Fatalf("rebuild: %v", err)
	}
	if msgs := c.messages(nil); len(msgs) != 4 || msgs[2].Content[0].Text != "You stopped too early: call finish." {
		t.Fatalf("rebuilt messages: %+v", msgs)
	}
}

// A run with no hook keeps ending with the text as its summary.
func TestEndTurnWithoutAHookEndsTheRun(t *testing.T) {
	e := newEnv(t, newScripted(endTurn("done")))
	run := e.create("echo", "hi", Budgets{})
	e.execute(run)
	if r := e.get(run); r.Status != store.AgentSucceeded || outputOf(t, r)["summary"] != "done" {
		t.Fatalf("run = %s %s", r.Status, r.Output)
	}
}
