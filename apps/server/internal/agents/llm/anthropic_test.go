package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const gatewayURL = "https://gateway.ai.cloudflare.com/v1/acct123/cf-dev/anthropic"

func directClient(api *fakeAPI, baseURL string) *anthropicClient {
	return newAnthropicClient(anthropicConfig{APIKey: testKey, BaseURL: baseURL, GatewayToken: testToken, HTTPClient: api.client(), Workspace: "wks_test", Stage: "editor"})
}

var strictSchema = json.RawMessage(`{"type":"object","properties":{"asset_id":{"type":"string"},"at_ms":{"type":"array","items":{"type":"integer"}}},"required":["asset_id"],"additionalProperties":false}`)

func fullRequest() Request {
	png := []byte("\x89PNG-not-really-a-png")
	return Request{
		Model:     "claude-opus-5-5",
		MaxTokens: 4096,
		Effort:    "medium",
		Thinking:  &Thinking{Mode: ThinkingAdaptive, Display: "summarized"},
		System: []Block{
			TextBlock("You are the editor."),
			{Type: BlockText, Text: "Stable rules.", Cache: &CacheControl{}},
		},
		Tools: []Tool{
			{Name: "view_clip", Description: "Look at frames", InputSchema: strictSchema, Strict: true},
			{Name: "finish", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Strict: true, Cache: &CacheControl{TTL: "1h"}},
		},
		ToolChoice: &ToolChoice{Type: "auto"},
		Messages: []Message{
			{Role: RoleUser, Content: []Block{
				TextBlock("Cut a 9:16 short."),
				{Type: BlockImage, Image: &Image{MediaType: "image/png", Data: png}, Cache: &CacheControl{TTL: "1h"}},
			}},
			{Role: RoleAssistant, Content: []Block{
				{Type: BlockThinking, Thinking: "hmm", Signature: "sig123"},
				{Type: BlockToolUse, ID: "toolu_1", Name: "view_clip", Input: json.RawMessage(`{"asset_id":"ast_1"}`)},
			}},
			{Role: RoleUser, Content: []Block{
				{Type: BlockToolResult, ToolUseID: "toolu_1", Content: []Block{
					TextBlock("frames follow"),
					{Type: BlockImage, Image: &Image{MediaType: "image/jpeg", Data: []byte("jpegbytes")}},
				}, Cache: &CacheControl{}},
				{Type: BlockToolResult, ToolUseID: "toolu_2", IsError: true, Content: []Block{TextBlock("no such asset")}},
			}},
		},
	}
}

func TestRequestConstruction(t *testing.T) {
	api := &fakeAPI{}
	c := directClient(api, "")
	if _, err := c.Messages(context.Background(), fullRequest()); err != nil {
		t.Fatal(err)
	}
	calls := api.messageCalls()
	if len(calls) != 1 || calls[0].Method != "POST" {
		t.Fatalf("calls = %+v", calls)
	}
	body := calls[0].json(t)

	if body["model"] != "claude-opus-5-5" || body["max_tokens"] != float64(4096) {
		t.Errorf("model/max_tokens = %v/%v", body["model"], body["max_tokens"])
	}
	if oc, _ := body["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Errorf("output_config = %v", body["output_config"])
	}
	if th, _ := body["thinking"].(map[string]any); th["type"] != "adaptive" || th["display"] != "summarized" {
		t.Errorf("thinking = %v", body["thinking"])
	}
	if tc, _ := body["tool_choice"].(map[string]any); tc["type"] != "auto" {
		t.Errorf("tool_choice = %v", body["tool_choice"])
	}

	// System blocks: only the second carries cache_control.
	sys := asSlice(body["system"])
	if len(sys) != 2 || asMap(sys[0])["cache_control"] != nil {
		t.Fatalf("system = %v", sys)
	}
	if cc, _ := asMap(sys[1])["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("system[1] cache_control = %v", sys[1])
	}

	// Tools: strict, with additionalProperties false kept, required and properties in place.
	tools := asSlice(body["tools"])
	t0 := asMap(tools[0])
	if t0["name"] != "view_clip" || t0["strict"] != true || t0["description"] != "Look at frames" {
		t.Errorf("tool 0 = %v", t0)
	}
	is := asMap(t0["input_schema"])
	if is["type"] != "object" || is["additionalProperties"] != false || is["properties"] == nil || asSlice(is["required"])[0] != "asset_id" {
		t.Errorf("input_schema = %v", is)
	}
	t1 := asMap(tools[1])
	if cc, _ := t1["cache_control"].(map[string]any); cc["ttl"] != "1h" {
		t.Errorf("tool 1 cache_control = %v", t1["cache_control"])
	}

	// Messages: image as base64 with the 1h breakpoint, thinking and tool_use round trip, tool_result content.
	msgs := asSlice(body["messages"])
	u0 := asSlice(asMap(msgs[0])["content"])
	img := asMap(u0[1])
	src := asMap(img["source"])
	if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" {
		t.Errorf("image block = %v", img)
	}
	if got, _ := base64.StdEncoding.DecodeString(fmt.Sprint(src["data"])); string(got) != "\x89PNG-not-really-a-png" {
		t.Errorf("image bytes did not round trip")
	}
	if cc, _ := img["cache_control"].(map[string]any); cc["ttl"] != "1h" {
		t.Errorf("image cache_control = %v", img["cache_control"])
	}
	a := asSlice(asMap(msgs[1])["content"])
	if asMap(a[0])["type"] != "thinking" || asMap(a[0])["signature"] != "sig123" {
		t.Errorf("thinking block = %v", a[0])
	}
	tu := asMap(a[1])
	if tu["type"] != "tool_use" || tu["id"] != "toolu_1" || asMap(tu["input"])["asset_id"] != "ast_1" {
		t.Errorf("tool_use = %v", tu)
	}
	r := asSlice(asMap(msgs[2])["content"])
	tr := asMap(r[0])
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "toolu_1" || len(asSlice(tr["content"])) != 2 {
		t.Errorf("tool_result = %v", tr)
	}
	if cc, _ := tr["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("tool_result cache_control = %v", tr["cache_control"])
	}
	if inner := asMap(asSlice(tr["content"])[1]); inner["type"] != "image" {
		t.Errorf("tool_result image = %v", inner)
	}
	if er := asMap(r[1]); er["is_error"] != true {
		t.Errorf("error tool_result = %v", er)
	}
}

func TestCountTokensConstruction(t *testing.T) {
	api := &fakeAPI{countTokens: func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: `{"input_tokens": 4321}`} }}
	c := directClient(api, "")
	req := fullRequest()
	req.MaxTokens = 0 // count_tokens has no max_tokens
	n, err := c.CountTokens(context.Background(), req)
	if err != nil || n != 4321 {
		t.Fatalf("CountTokens = %d, %v", n, err)
	}
	calls := api.callsTo("/v1/messages/count_tokens")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	body := calls[0].json(t)
	if body["model"] != "claude-opus-5-5" || body["max_tokens"] != nil || len(asSlice(body["system"])) != 2 || len(asSlice(body["tools"])) != 2 {
		t.Errorf("count body = %v", body)
	}
}

func TestRequestValidation(t *testing.T) {
	api := &fakeAPI{}
	c := directClient(api, "")
	mutations := map[string]func(*Request){
		"no model":      func(r *Request) { r.Model = "" },
		"no max_tokens": func(r *Request) { r.MaxTokens = 0 },
		"no messages":   func(r *Request) { r.Messages = nil },
		"bad role":      func(r *Request) { r.Messages[0].Role = "system" },
		"bad block":     func(r *Request) { r.Messages[0].Content = []Block{{Type: "video"}} },
		"image no bytes": func(r *Request) {
			r.Messages[0].Content = []Block{{Type: BlockImage, Image: &Image{MediaType: "image/png", Ref: "r2://x"}}}
		},
		"bad cache ttl":   func(r *Request) { r.System = []Block{{Type: BlockText, Text: "x", Cache: &CacheControl{TTL: "2h"}}} },
		"non-strict json": func(r *Request) { r.Tools = []Tool{{Name: "t", InputSchema: json.RawMessage(`[1]`)}} },
		"strict, open": func(r *Request) {
			r.Tools = []Tool{{Name: "t", Strict: true, InputSchema: json.RawMessage(`{"type":"object"}`)}}
		},
		"thinking budget": func(r *Request) { r.Thinking = &Thinking{Mode: ThinkingEnabled, BudgetTokens: 10} },
		"thinking mode":   func(r *Request) { r.Thinking = &Thinking{Mode: "deep"} },
		"tool_choice":     func(r *Request) { r.ToolChoice = &ToolChoice{Type: "tool"} },
	}
	for name, mutate := range mutations {
		req := simpleRequest()
		mutate(&req)
		if _, err := c.Messages(context.Background(), req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if api.totalCalls() != 0 {
		t.Errorf("an invalid request reached the network (%d calls)", api.totalCalls())
	}
}

func TestGatewayHeadersOnlyWithAGateway(t *testing.T) {
	run := uuid.MustParse("0198a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b")
	req := simpleRequest()
	req.Run = RunInfo{ID: run, Profile: "v1-start"}

	// Direct (empty base URL): no gateway headers at all, not even the token.
	api := &fakeAPI{}
	if _, err := directClient(api, "").Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	h := api.messageCalls()[0].Header
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "cf-aig") {
			t.Errorf("direct call carries %s", k)
		}
	}
	if h.Get("X-Api-Key") != testKey {
		t.Error("the SDK did not send the key it was given")
	}
	// An explicit api.anthropic.com base URL is direct too.
	api = &fakeAPI{}
	if _, err := directClient(api, "https://api.anthropic.com").Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for k := range api.messageCalls()[0].Header {
		if strings.HasPrefix(strings.ToLower(k), "cf-aig") {
			t.Errorf("api.anthropic.com call carries %s", k)
		}
	}

	// Through a gateway: the path is under the gateway URL and the headers are there.
	api = &fakeAPI{}
	c := directClient(api, gatewayURL)
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CountTokens(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, call := range api.calls {
		if !strings.HasPrefix(call.Path, "/v1/acct123/cf-dev/anthropic/v1/messages") {
			t.Errorf("path = %s", call.Path)
		}
		if call.Header.Get("cf-aig-collect-log-payload") != "false" {
			t.Errorf("%s: cf-aig-collect-log-payload = %q", call.Path, call.Header.Get("cf-aig-collect-log-payload"))
		}
		if call.Header.Get("cf-aig-authorization") != "Bearer "+testToken {
			t.Errorf("%s: cf-aig-authorization missing", call.Path)
		}
		var meta map[string]string
		if err := json.Unmarshal([]byte(call.Header.Get("cf-aig-metadata")), &meta); err != nil {
			t.Fatalf("cf-aig-metadata is not a JSON object: %v", err)
		}
		if len(meta) > 5 {
			t.Errorf("cf-aig-metadata has %d entries; the gateway allows 5", len(meta))
		}
		want := map[string]string{"workspace_id": "wks_test", "run_id": run.String(), "stage": "editor", "profile": "v1-start"}
		for k, v := range want {
			if meta[k] != v {
				t.Errorf("cf-aig-metadata[%s] = %q, want %q", k, meta[k], v)
			}
		}
	}
	// Without a run the metadata has no run_id; without a token there is no cf-aig-authorization.
	api = &fakeAPI{}
	c = newAnthropicClient(anthropicConfig{APIKey: testKey, BaseURL: gatewayURL, HTTPClient: api.client(), Workspace: "wks_test", Stage: "editor"})
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	call := api.messageCalls()[0]
	if call.Header.Get("cf-aig-authorization") != "" || strings.Contains(call.Header.Get("cf-aig-metadata"), "run_id") {
		t.Errorf("headers = %v", call.Header)
	}
}

func TestResponseConversion(t *testing.T) {
	content := `[{"type":"thinking","thinking":"let me see","signature":"sigABC"},{"type":"text","text":"Looking."},{"type":"tool_use","id":"toolu_9","name":"view_clip","input":{"asset_id":"ast_2","at_ms":[0,2000]}}]`
	api := &fakeAPI{messages: func(int, recordedCall) fakeReply {
		return fakeReply{Status: 200, Body: messageJSON(content, "tool_use", apiUsage{Input: 11, CacheWrite5m: 20, CacheWrite1h: 30, CacheRead: 400, Output: 55})}
	}}
	resp, err := directClient(api, "").Messages(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || resp.ID != "msg_fake" || resp.RequestID != "req_fake_1" || resp.Model != "claude-haiku-5-5" {
		t.Errorf("response = %+v", resp)
	}
	want := Usage{InputTokens: 11, CacheWriteTokens: 50, CacheWrite1hTokens: 30, CacheReadTokens: 400, OutputTokens: 55}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
	if len(resp.Content) != 3 || resp.Content[0].Type != BlockThinking || resp.Content[0].Signature != "sigABC" || resp.Text() != "Looking." {
		t.Errorf("content = %+v", resp.Content)
	}
	uses := resp.ToolUses()
	if len(uses) != 1 || uses[0].ID != "toolu_9" || uses[0].Name != "view_clip" {
		t.Fatalf("tool uses = %+v", uses)
	}
	var in map[string]any
	if err := json.Unmarshal(uses[0].Input, &in); err != nil || in["asset_id"] != "ast_2" {
		t.Errorf("tool input = %s", uses[0].Input)
	}
}

func TestAPIErrorsAreTypedAndScrubbed(t *testing.T) {
	// A hostile or buggy upstream echoes the key and the gateway token in its error message.
	api := &fakeAPI{messages: func(int, recordedCall) fakeReply {
		return errReply(401, "authentication_error", "invalid x-api-key "+testKey+" and token "+testToken, map[string]string{"retry-after": "3"})
	}}
	_, err := directClient(api, "").Messages(context.Background(), simpleRequest())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 || ae.Type != "authentication_error" || !ae.IsAuth() || ae.Retryable() {
		t.Fatalf("err = %#v", err)
	}
	if ae.RetryAfter != 3*time.Second || ae.RequestID != "req_fake_1" {
		t.Errorf("retry-after/request-id = %v/%q", ae.RetryAfter, ae.RequestID)
	}
	mustNotContain(t, "error text", err.Error(), testKey, testToken)
	mustNotContain(t, "api error message", ae.Message, testKey, testToken)

	for status, typ := range map[int]string{408: "", 429: "rate_limit_error", 500: "api_error", 502: "", 503: "", 504: "", 529: "overloaded_error"} {
		if e := (&APIError{Status: status, Type: typ}); !e.Retryable() {
			t.Errorf("%d is not retryable", status)
		}
	}
	for _, status := range []int{400, 401, 403, 404, 409, 413, 422} {
		if e := (&APIError{Status: status, Type: "invalid_request_error"}); e.Retryable() {
			t.Errorf("%d is retryable", status)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"": 0, "2": 2 * time.Second, "0.5": 500 * time.Millisecond, "-1": 0, "junk": 0, "999999999": 0,
		now.Add(90 * time.Second).Format("Mon, 02 Jan 2006 15:04:05 GMT"): 90 * time.Second,
		now.Add(-time.Hour).Format("Mon, 02 Jan 2006 15:04:05 GMT"):       0,
	} {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
