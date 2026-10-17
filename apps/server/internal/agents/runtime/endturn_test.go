package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

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

// The hook failing (a database error, say) fails the attempt; the retry asks the hook again instead of sending the model a
// conversation that ends on its own answer.
func TestEndTurnHookFailureIsRetriedWithoutCallingTheModel(t *testing.T) {
	asked := 0
	a := NewEchoAgent(nil)
	a.Name = "flaky"
	a.EndTurn = func(context.Context, RunContext, string, int) (EndTurn, error) {
		asked++
		if asked == 1 {
			return EndTurn{}, errors.New("the database is away")
		}
		if asked == 2 {
			return EndTurn{Nudge: "call finish"}, nil
		}
		return EndTurn{}, nil
	}
	model := newScripted(endTurn("I am done"), endTurn("really done"))
	e := newEnv(t, model, func(c *envConfig) { c.extra = []Agent{a} })
	run := e.create("flaky", "hi", Budgets{})
	if _, err := e.runner.Execute(context.Background(), e.ws.ID, run.ID); err == nil {
		t.Fatal("the failing hook did not fail the attempt")
	}
	if model.calls() != 1 {
		t.Fatalf("%d model calls after the failed attempt", model.calls())
	}
	if got := e.execute(run); got != OutcomeFinished {
		t.Fatalf("outcome = %s", got)
	}
	if model.calls() != 2 {
		t.Fatalf("%d model calls: the retry should ask the hook, then the model once", model.calls())
	}
	msgs := model.request(1).Messages
	if last := msgs[len(msgs)-1]; last.Role != llm.RoleUser || last.Content[0].Text != "call finish" {
		t.Fatalf("the second request ends with %+v", last)
	}
	// A run that was stored with its answer and not ended (the worker died) is ended by the next attempt without a model call.
	if r := e.get(run); r.Status != store.AgentSucceeded || outputOf(t, r)["summary"] != "really done" {
		t.Fatalf("run = %s %s", r.Status, r.Output)
	}
}

// An assistant message with no content is never sent: it gets a placeholder, and the run still ends as before when no nudge is due.
func TestEmptyAnswerGetsAPlaceholder(t *testing.T) {
	a := NewEchoAgent(nil)
	a.Name = "quiet"
	a.EndTurn = func(_ context.Context, _ RunContext, _ string, nudges int) (EndTurn, error) {
		if nudges == 0 {
			return EndTurn{Nudge: "say something"}, nil
		}
		return EndTurn{}, nil
	}
	model := newScripted(say("end_turn"), endTurn("fine"))
	e := newEnv(t, model, func(c *envConfig) { c.extra = []Agent{a} })
	run := e.create("quiet", "hi", Budgets{})
	if got := e.execute(run); got != OutcomeFinished {
		t.Fatalf("outcome = %s", got)
	}
	msgs := model.request(1).Messages
	if len(msgs) != 3 || len(msgs[1].Content) == 0 || msgs[1].Content[0].Text != emptyReply || msgs[2].Role != llm.RoleUser {
		t.Fatalf("second request: %+v", msgs)
	}
	// Without a hook, an empty answer ends the run as it always did.
	e2 := newEnv(t, newScripted(say("end_turn")))
	run2 := e2.create("echo", "hi", Budgets{})
	e2.execute(run2)
	if r := e2.get(run2); r.Status != store.AgentSucceeded {
		t.Fatalf("run = %s %s", r.Status, r.Output)
	}
}

// OnEnd is called once a run has ended, whatever way it ended.
func TestOnEndIsCalledWhenTheRunEnds(t *testing.T) {
	var ended []uuid.UUID
	a := NewEchoAgent(nil)
	a.Name = "tidy"
	a.OnEnd = func(id uuid.UUID) { ended = append(ended, id) }
	e := newEnv(t, newScripted(endTurn("done")), func(c *envConfig) { c.extra = []Agent{a} })
	run := e.create("tidy", "hi", Budgets{})
	e.execute(run)
	if len(ended) != 1 || ended[0] != run.ID {
		t.Fatalf("OnEnd calls: %v, run %s", ended, run.ID)
	}
}
