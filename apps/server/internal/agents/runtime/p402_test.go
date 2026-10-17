package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// P4-02a changes to the runtime: a tool may opt out of strict tool use, a run can be started under a guard, a tool's artifacts
// can be collected in a test, and a run's events have one-line summaries for the API.

func schemaTool(name, schema string, notStrict bool) Tool {
	return NewTool(Spec{Name: name, Description: "A tool for the tests.", Schema: json.RawMessage(schema), NotStrict: notStrict},
		func(context.Context, RunContext, struct{}) (Result, error) { return Text("ok"), nil })
}

const rangedSchema = `{
  "type": "object",
  "properties": {
    "n": {"type": "integer", "minimum": 1, "maximum": 3},
    "o": {"oneOf": [{"properties": {"a": {}}, "required": ["a"]}, {"properties": {"b": {}}, "required": ["b"]}]}
  },
  "additionalProperties": false
}`

func TestNonStrictToolsMayUseWhatStrictCannot(t *testing.T) {
	if _, err := NewRegistry(schemaTool("strict_one", rangedSchema, false)); err == nil {
		t.Fatal("an open object inside a strict tool was accepted")
	}
	reg, err := NewRegistry(schemaTool("lax_one", rangedSchema, true), schemaTool("plain", `{"type":"object","properties":{},"additionalProperties":false}`, false))
	if err != nil {
		t.Fatal(err)
	}
	defs := reg.Definitions()
	if defs[0].Strict || !defs[1].Strict {
		t.Fatalf("strict flags: %v %v", defs[0].Strict, defs[1].Strict)
	}
	// The arguments are still checked against the whole schema.
	if err := reg.Validate("lax_one", json.RawMessage(`{"n":2,"o":{"a":1}}`)); err != nil {
		t.Errorf("valid arguments refused: %v", err)
	}
	if err := reg.Validate("lax_one", json.RawMessage(`{"n":9}`)); err == nil {
		t.Error("a value out of range was accepted")
	}
	if err := reg.Validate("lax_one", json.RawMessage(`{"extra":1}`)); err == nil {
		t.Error("an extra property was accepted")
	}
	// Not even a non-strict tool may leave its top level open, or not be an object.
	for _, schema := range []string{`{"type":"object","properties":{}}`, `{"type":"array"}`, `[]`} {
		if _, err := NewRegistry(schemaTool("bad", schema, true)); err == nil {
			t.Errorf("%s was accepted", schema)
		}
	}
}

func TestRunContextWithArtifacts(t *testing.T) {
	var got []string
	rc := RunContext{ToolUseID: "toolu_1"}.WithArtifacts(func(_ context.Context, p json.RawMessage) error { got = append(got, string(p)); return nil })
	if err := rc.Artifact(context.Background(), "thing", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != `{"data":{"n":1},"kind":"thing","tool_use_id":"toolu_1"}` {
		t.Fatalf("artifacts: %v", got)
	}
	if err := (RunContext{}).Artifact(context.Background(), "thing", 1); err == nil {
		t.Error("a RunContext with no sink took an artifact")
	}
}

func TestStartGuarded(t *testing.T) {
	e := newEnv(t, newScripted())
	input, _ := json.Marshal(Input{Message: "hi"})
	params := StartParams{Agent: "echo", WorkspaceID: e.ws.ID, Input: input}
	count := func() int {
		var n int
		if err := e.st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM agent_runs`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	jobCount := func() int {
		var n int
		if err := e.st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'agent_run'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	refuse := errors.New("not now")
	var sawTx bool
	_, err := e.svc.StartGuarded(context.Background(), params, func(_ context.Context, tx *store.Store) error {
		sawTx = tx != nil
		return refuse
	})
	if !errors.Is(err, refuse) || !sawTx || count() != 0 || jobCount() != 0 {
		t.Fatalf("a refused guard: err %v, %d runs, %d jobs", err, count(), jobCount())
	}
	run, err := e.svc.StartGuarded(context.Background(), params, func(context.Context, *store.Store) error { return nil })
	if err != nil || run.Status != store.AgentQueued || count() != 1 || jobCount() != 1 {
		t.Fatalf("an accepted guard: %+v %v, %d runs, %d jobs", run, err, count(), jobCount())
	}
	if _, err := e.svc.Start(context.Background(), params); err != nil || count() != 2 || jobCount() != 2 {
		t.Fatalf("Start: %v, %d runs, %d jobs", err, count(), jobCount())
	}
}

func TestSummarizeEvent(t *testing.T) {
	ev := func(typ, payload string) store.AgentEvent {
		return store.AgentEvent{Seq: 3, Type: typ, Payload: json.RawMessage(payload)}
	}
	long := strings.Repeat("é", 500)
	cases := []struct {
		name string
		e    store.AgentEvent
		want EventSummary
	}{
		{"status", ev("status", `{"state":"failed","reason":"budget","limit":"dollars"}`), EventSummary{Seq: 3, Type: "status", Text: "failed reason=budget limit=dollars"}},
		{"waiting", ev("status", `{"state":"waiting_input","question":"Which one?"}`), EventSummary{Seq: 3, Type: "status", Text: "waiting_input question=Which one?"}},
		{"context is not shown", ev("status", `{"state":"context","blocks":[{"type":"text","text":"SECRET BRIEF"}]}`), EventSummary{Seq: 3, Type: "status", Text: "context"}},
		{"assistant", ev("assistant_message", `{"content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"Looking."},{"type":"tool_use","id":"t","name":"view_clip","input":{}}]}`),
			EventSummary{Seq: 3, Type: "assistant_message", Text: "Looking. [calls view_clip]"}},
		{"tool call", ev("tool_call", `{"tool_use_id":"t","name":"view_clip","input":{"asset_id":"ast_x"}}`), EventSummary{Seq: 3, Type: "tool_call", Tool: "view_clip", CallID: "t", Text: `view_clip {"asset_id":"ast_x"}`}},
		{"tool result", ev("tool_result", `{"tool_use_id":"t","name":"view_clip","is_error":true,"content":[{"type":"text","text":"no"},{"type":"image","ref":"ws/a/b.jpg"}]}`),
			EventSummary{Seq: 3, Type: "tool_result", Tool: "view_clip", CallID: "t", IsError: true, Text: "no [1 images]"}},
		{"error", ev("error", `{"code":"model_error","message":"boom"}`), EventSummary{Seq: 3, Type: "error", IsError: true, Text: "model_error: boom"}},
		{"artifact", ev("artifact", `{"kind":"timeline_version","data":{"version":2}}`), EventSummary{Seq: 3, Type: "artifact", Text: "timeline_version", Kind: "timeline_version"}},
		{"not json", ev("tool_call", `x`), EventSummary{Seq: 3, Type: "tool_call"}},
	}
	for _, c := range cases {
		if got := SummarizeEvent(c.e); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	// Long text is cut to one line at a rune boundary.
	got := SummarizeEvent(ev("error", `{"code":"x","message":"`+long+`"}`))
	if len(got.Text) > SummaryChars+len("…") || !strings.HasSuffix(got.Text, "…") || strings.ContainsRune(got.Text, '�') {
		t.Errorf("long text: %d bytes %q", len(got.Text), got.Text[len(got.Text)-12:])
	}
	if got := SummarizeEvent(ev("assistant_message", `{"content":[{"type":"text","text":"a\n\n  b\tc"}]}`)); got.Text != "a b c" {
		t.Errorf("white space: %q", got.Text)
	}
}

func TestQuestionAndOutputHelpers(t *testing.T) {
	q := store.AgentEvent{Type: "status", Payload: json.RawMessage(`{"state":"waiting_input","question":"Which?"}`)}
	if QuestionOf(q) != "Which?" {
		t.Error("question not read")
	}
	for _, e := range []store.AgentEvent{
		{Type: "status", Payload: json.RawMessage(`{"state":"running"}`)},
		{Type: "tool_result", Payload: json.RawMessage(`{"state":"waiting_input","question":"x"}`)},
		{Type: "status", Payload: json.RawMessage(`x`)},
	} {
		if QuestionOf(e) != "" {
			t.Errorf("a question in %+v", e)
		}
	}
	if s, v := OutputSummary([]byte(`{"summary":"Done.","timeline_version":4}`)); s != "Done." || v == nil || *v != 4 {
		t.Errorf("OutputSummary = %q %v", s, v)
	}
	if s, v := OutputSummary([]byte(`{"stopped":"budget"}`)); s != "" || v != nil {
		t.Errorf("OutputSummary of a stop = %q %v", s, v)
	}
	if s, v := OutputSummary(nil); s != "" || v != nil {
		t.Error("empty output")
	}
	if s, v := OutputSummary([]byte(`[`)); s != "" || v != nil {
		t.Error("broken output")
	}
}

func TestDefaultAgentsTakeExtras(t *testing.T) {
	extra := NewEchoAgent(nil)
	extra.Name = "second"
	a, err := NewAgents(DefaultAgents(extra)...)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.Names(), ","); got != "echo,second" {
		t.Fatalf("agents: %s", got)
	}
}
