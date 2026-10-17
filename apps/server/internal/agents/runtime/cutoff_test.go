package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// lastUser returns the content of the last message of a request, which must be from the user.
func lastUser(t *testing.T, req llm.Request) []llm.Block {
	t.Helper()
	msgs := req.Messages
	m := msgs[len(msgs)-1]
	if m.Role != llm.RoleUser {
		t.Fatalf("the request ends on a %s message", m.Role)
	}
	return m.Content
}

// An answer that stops at max_tokens does not fail the run: its tool calls are cut off, so each is answered with an error that says
// so (none is run), and the model goes on.
func TestCutOffToolCallsAreAnsweredWithAnError(t *testing.T) {
	model := newScripted(
		say("max_tokens", toolUse("toolu_1", "note", `{"text":"a"}`), toolUse("toolu_2", "note", `{"text":"b"}`)),
		use(toolUse("toolu_3", "finish", `{"summary":"done"}`)),
	)
	e := newEnv(t, model)
	run := e.create("echo", "hello", Budgets{})
	e.execute(run)
	if r := e.get(run); r.Status != store.AgentSucceeded {
		t.Fatalf("run = %s %v", r.Status, r.FailureReason)
	}
	if e.sink.effects() != 0 {
		t.Error("a cut-off call was run")
	}
	got := lastUser(t, model.request(1))
	if len(got) != 2 {
		t.Fatalf("%d results, want one per cut-off call", len(got))
	}
	for i, b := range got {
		if b.Type != llm.BlockToolResult || !b.IsError || b.ToolUseID != []string{"toolu_1", "toolu_2"}[i] || !strings.Contains(textOfBlocks(b.Content), "cut off at the output limit") {
			t.Errorf("result %d: %+v", i, b)
		}
	}
	// The cut-off call was paid for like any other.
	if r := e.get(run); r.CostMicros != 200 || r.ToolCalls != 1 {
		t.Errorf("cost %d, tool calls %d: the two calls cost 100 each and only finish is a call", r.CostMicros, r.ToolCalls)
	}
}

func textOfBlocks(bs []llm.Block) string {
	var s string
	for _, b := range bs {
		s += b.Text
	}
	return s
}

// A cut-off answer with no tool call is followed by a message from the user, so the conversation never ends on the assistant.
func TestCutOffTextGetsAUserMessage(t *testing.T) {
	model := newScripted(say("max_tokens", llm.TextBlock("I was thinking about")), use(toolUse("toolu_1", "finish", `{"summary":"done"}`)))
	e := newEnv(t, model)
	run := e.create("echo", "hello", Budgets{})
	e.execute(run)
	if r := e.get(run); r.Status != store.AgentSucceeded {
		t.Fatalf("run = %s %v", r.Status, r.FailureReason)
	}
	got := lastUser(t, model.request(1))
	if len(got) != 1 || got[0].Type != llm.BlockText || !strings.Contains(got[0].Text, "Send it again, shorter") {
		t.Errorf("the message after the cut-off: %+v", got)
	}
}

// Two recoveries, then the run fails with max_tokens as before.
func TestCutOffRecoveriesAreLimited(t *testing.T) {
	n := 0
	cut := func() scriptStep {
		n++
		return say("max_tokens", toolUse("toolu_"+string(rune('a'+n)), "note", `{"text":"a"}`))
	}
	model := newScripted(cut(), cut(), cut(), use(toolUse("toolu_f", "finish", `{"summary":"never"}`)))
	e := newEnv(t, model)
	run := e.create("echo", "hello", Budgets{})
	e.execute(run)
	r := e.get(run)
	if r.Status != store.AgentFailed || r.FailureReason == nil || *r.FailureReason != FailMaxTokens {
		t.Fatalf("run = %s %v", r.Status, r.FailureReason)
	}
	if model.calls() != 3 {
		t.Errorf("%d model calls, want 3 (two recoveries and the answer that failed the run)", model.calls())
	}

	// Two cut-offs in a row and then a good answer is a run that succeeds.
	model = newScripted(cut(), say("max_tokens", llm.TextBlock("...")), use(toolUse("toolu_f", "finish", `{"summary":"ok"}`)))
	e = newEnv(t, model)
	run = e.create("echo", "hello", Budgets{})
	e.execute(run)
	if r := e.get(run); r.Status != store.AgentSucceeded {
		t.Fatalf("two recoveries: run = %s %v", r.Status, r.FailureReason)
	}
}

// A worker that dies after the cut-off answer was stored (or after some of its results) resumes without answering anything twice
// and without leaving a tool call unanswered.
func TestCutOffRecoveryIsResumeSafe(t *testing.T) {
	for _, point := range []string{"assistant_persisted", "cut_off_result"} {
		model := newScripted(
			say("max_tokens", toolUse("toolu_1", "note", `{"text":"a"}`), toolUse("toolu_2", "note", `{"text":"b"}`)),
			use(toolUse("toolu_3", "finish", `{"summary":"done"}`)),
		)
		e := newEnv(t, model)
		run := e.create("echo", "hello", Budgets{})
		if !e.crashAt(run, point) {
			t.Fatalf("%s: the crash point was not reached", point)
		}
		e.ageHeartbeat(run)
		e.execute(run)
		if r := e.get(run); r.Status != store.AgentSucceeded {
			t.Fatalf("%s: run = %s %v", point, r.Status, r.FailureReason)
		}
		results := eventsOf(e.events(run), store.EventToolResult)
		seen := map[string]int{}
		for _, ev := range results {
			var p toolResultPayload
			_ = json.Unmarshal(ev.Payload, &p)
			seen[p.ToolUseID]++
		}
		if seen["toolu_1"] != 1 || seen["toolu_2"] != 1 || seen["toolu_3"] != 1 {
			t.Errorf("%s: results per call %v, want each answered once", point, seen)
		}
		if got := lastUser(t, model.request(model.calls()-1)); len(got) != 2 {
			t.Errorf("%s: the last request carries %d blocks", point, len(got))
		}
	}

	// The same for a cut-off turn with no tool call: one message, not two.
	model := newScripted(say("max_tokens", llm.TextBlock("...")), use(toolUse("toolu_1", "finish", `{"summary":"done"}`)))
	e := newEnv(t, model)
	run := e.create("echo", "hello", Budgets{})
	if !e.crashAt(run, "assistant_persisted") {
		t.Fatal("the crash point was not reached")
	}
	e.ageHeartbeat(run)
	e.execute(run)
	if r := e.get(run); r.Status != store.AgentSucceeded {
		t.Fatalf("run = %s %v", r.Status, r.FailureReason)
	}
	n := 0
	for _, ev := range eventsOf(e.events(run), store.EventStatus) {
		if strings.Contains(string(ev.Payload), stateCutOff) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d cut-off messages stored, want 1", n)
	}
}
