package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/llm/routing"
)

// toolRequest is a call that asks for one call of a tool with thinking off: what the judge, the improver, the quick look and the style
// reads send.
func toolRequest(model, effort string) Request {
	return Request{
		Model: model, MaxTokens: 1000, Effort: effort,
		Messages:   []Message{{Role: RoleUser, Content: []Block{TextBlock("score it")}}},
		Tools:      []Tool{{Name: "score_edit", InputSchema: strictSchema}, {Name: "other", InputSchema: strictSchema, Strict: true}},
		ToolChoice: &ToolChoice{Type: "tool", Name: "score_edit"},
		Thinking:   &Thinking{Mode: ThinkingDisabled},
	}
}

// sent is the JSON of the newest request to a path.
func sent(t *testing.T, api *fakeAPI, suffix string) map[string]any {
	t.Helper()
	calls := api.callsTo(suffix)
	if len(calls) == 0 {
		t.Fatalf("no call to %s", suffix)
	}
	return calls[len(calls)-1].json(t)
}

// The request each model is sent, for Messages and count_tokens alike (models.yaml).
func TestRequestShapePerModel(t *testing.T) {
	for _, c := range []struct {
		model, effort string
		thinking      any // the thinking field sent; nil: none
		forced        bool
	}{
		// Haiku 5.5: disabled, and a forced tool choice, as the quick look and the style reads have always sent.
		{"claude-haiku-5-5", "", map[string]any{"type": "disabled"}, true},
		// Sonnet 5.5: between_tools and nothing else; auto with the tool strict, one call at a time.
		{"claude-sonnet-5-5", "medium", map[string]any{"type": "between_tools"}, false},
		// Opus 5.5: no thinking field at all (it cannot be turned off); auto with the tool strict.
		{"claude-opus-5-5", "medium", nil, false},
	} {
		api := &fakeAPI{}
		cl := directClient(api, "")
		req := toolRequest(c.model, c.effort)
		if _, err := cl.Messages(context.Background(), req); err != nil {
			t.Fatalf("%s: %v", c.model, err)
		}
		if _, err := cl.CountTokens(context.Background(), req); err != nil {
			t.Fatalf("%s count_tokens: %v", c.model, err)
		}
		for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
			body := sent(t, api, path)
			th, has := body["thinking"]
			switch {
			case c.thinking == nil && has:
				t.Errorf("%s %s: thinking %v sent, want none", c.model, path, th)
			case c.thinking != nil:
				if raw, _ := json.Marshal(th); string(raw) != mustJSONString(c.thinking) {
					t.Errorf("%s %s: thinking %s, want %s", c.model, path, raw, mustJSONString(c.thinking))
				}
			}
			tc := asMap(body["tool_choice"])
			if c.forced {
				if tc["type"] != "tool" || tc["name"] != "score_edit" {
					t.Errorf("%s %s: tool_choice %v", c.model, path, tc)
				}
				continue
			}
			if tc["type"] != "auto" || tc["disable_parallel_tool_use"] != true || tc["name"] != nil {
				t.Errorf("%s %s: tool_choice %v, want auto with disable_parallel_tool_use", c.model, path, tc)
			}
			tools := asSlice(body["tools"])
			if len(tools) != 2 || asMap(tools[0])["name"] != "score_edit" || asMap(tools[0])["strict"] != true {
				t.Errorf("%s %s: the named tool is not strict: %v", c.model, path, tools)
			}
		}
		// The caller's request is not changed.
		if req.ToolChoice.Type != "tool" || req.Tools[0].Strict || req.Thinking.Mode != ThinkingDisabled {
			t.Errorf("%s: the caller's request was modified", c.model)
		}
	}
}

func mustJSONString(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// Refused before anything is sent: thinking off above the model's effort limit, and a model the catalogue does not describe.
func TestUnsupportedRequestsAreRefused(t *testing.T) {
	api := &fakeAPI{}
	cl := directClient(api, "")
	for _, c := range []struct {
		req  Request
		want string
	}{
		{toolRequest("claude-sonnet-5-5", "max"), "effort high or below"},
		{toolRequest("claude-sonnet-5-5", "xhigh"), "effort high or below"},
		{toolRequest("claude-haiku-5-5", "max"), "effort high or below"},
		{toolRequest("claude-unknown-9", ""), "not in config/routing/models.yaml"},
	} {
		_, err := cl.Messages(context.Background(), c.req)
		var ue *UnsupportedError
		if !errors.Is(err, ErrUnsupported) || !errors.As(err, &ue) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s effort %q: %v", c.req.Model, c.req.Effort, err)
		}
		if _, err := cl.CountTokens(context.Background(), c.req); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s count_tokens: %v", c.req.Model, err)
		}
	}
	if api.totalCalls() != 0 {
		t.Errorf("%d requests reached the network", api.totalCalls())
	}
	// Opus has no effort limit (nothing is turned off), and thinking that is not "off" is sent as asked on every model.
	req := toolRequest("claude-opus-5-5", "max")
	if _, err := cl.Messages(context.Background(), req); err != nil {
		t.Errorf("opus at max: %v", err)
	}
	req = toolRequest("claude-sonnet-5-5", "max")
	req.Thinking = &Thinking{Mode: ThinkingAdaptive}
	if _, err := cl.Messages(context.Background(), req); err != nil {
		t.Errorf("sonnet adaptive at max: %v", err)
	}
	if th := asMap(sent(t, api, "/v1/messages")["thinking"]); th["type"] != "adaptive" {
		t.Errorf("adaptive sent as %v", th)
	}
}

func TestCatalogueCapabilitiesAreComplete(t *testing.T) {
	caps, err := DefaultCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"} {
		if _, ok := caps[m]; !ok {
			t.Errorf("%s has no capabilities", m)
		}
	}
	yes := true
	for name, cat := range map[string]routing.Catalog{
		"no thinking_off":  {"m": {Cloudflare: "x", ForcedToolChoice: &yes}},
		"bad thinking_off": {"m": {Cloudflare: "x", ThinkingOff: "never", ForcedToolChoice: &yes}},
		"bad effort":       {"m": {Cloudflare: "x", ThinkingOff: "disabled", ThinkingOffMaxEffort: "huge", ForcedToolChoice: &yes}},
		"no forced flag":   {"m": {Cloudflare: "x", ThinkingOff: "disabled"}},
	} {
		if _, err := CapabilitiesFrom(cat); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// scriptClient answers Messages from a list.
type scriptClient struct {
	replies []Response
	reqs    []Request
}

func (s *scriptClient) Messages(_ context.Context, req Request) (Response, error) {
	s.reqs = append(s.reqs, req)
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}
func (s *scriptClient) CountTokens(context.Context, Request) (int64, error) { return 10, nil }

func text(stop, s string) Response {
	return Response{StopReason: stop, Content: []Block{TextBlock(s)}, Usage: Usage{InputTokens: 100, OutputTokens: 10}}
}

func tool(name string) Response {
	return Response{StopReason: "tool_use", Content: []Block{{Type: BlockToolUse, ID: "toolu_1", Name: name, Input: json.RawMessage(`{"a":1}`)}},
		Usage: Usage{InputTokens: 120, OutputTokens: 30}}
}

// A model that cannot be forced may answer in text: CallTool asks once more with a reminder, then gives up with a typed error.
func TestCallToolRetriesOnceThenFails(t *testing.T) {
	req := toolRequest("claude-sonnet-5-5", "medium")

	// The first answer calls the tool: one call.
	sc := &scriptClient{replies: []Response{tool("score_edit")}}
	_, call, ok, err := CallTool(context.Background(), sc, req, "score_edit")
	if err != nil || !ok || call.Name != "score_edit" || len(sc.reqs) != 1 {
		t.Fatalf("first answer: ok %v err %v calls %d", ok, err, len(sc.reqs))
	}

	// Text first, then the call: two calls, the second carries the answer and the reminder, the usage of both is reported.
	sc = &scriptClient{replies: []Response{text("end_turn", "Hook 4, energy 3."), tool("score_edit")}}
	resp, call, ok, err := CallTool(context.Background(), sc, req, "score_edit")
	if err != nil || !ok || call.Name != "score_edit" || len(sc.reqs) != 2 {
		t.Fatalf("retry: ok %v err %v calls %d", ok, err, len(sc.reqs))
	}
	second := sc.reqs[1].Messages
	if len(second) != 3 || second[1].Role != RoleAssistant || second[1].Content[0].Text != "Hook 4, energy 3." ||
		!strings.Contains(second[2].Content[0].Text, "Call score_edit now") {
		t.Errorf("the reminder turn: %+v", second)
	}
	if len(req.Messages) != 1 {
		t.Error("the caller's messages were modified")
	}
	if resp.Usage.InputTokens != 220 || resp.Usage.OutputTokens != 40 {
		t.Errorf("usage %+v, want both calls", resp.Usage)
	}

	// Text twice: a typed error after two calls.
	sc = &scriptClient{replies: []Response{text("end_turn", "a"), text("end_turn", "b")}}
	_, _, ok, err = CallTool(context.Background(), sc, req, "score_edit")
	var nt *NoToolCallError
	if ok || !errors.Is(err, ErrNoToolCall) || !errors.As(err, &nt) || nt.Tool != "score_edit" || len(sc.reqs) != 2 {
		t.Errorf("no call twice: ok %v err %v calls %d", ok, err, len(sc.reqs))
	}

	// Cut off or refused: returned as it is, no retry.
	for _, stop := range []string{"max_tokens", "refusal"} {
		sc = &scriptClient{replies: []Response{text(stop, "x")}}
		resp, _, ok, err := CallTool(context.Background(), sc, req, "score_edit")
		if ok || err != nil || resp.StopReason != stop || len(sc.reqs) != 1 {
			t.Errorf("%s: ok %v err %v calls %d", stop, ok, err, len(sc.reqs))
		}
	}
}
