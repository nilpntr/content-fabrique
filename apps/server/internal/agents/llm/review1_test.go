package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// ---- a call that was sent and lost: recorded at its worst case --------------------------------------------------

func TestSentButUnansweredCallIsRecordedAtItsWorstCase(t *testing.T) {
	rg := newRig(t, nil)
	rg.api.messages = func(int, recordedCall) fakeReply {
		return fakeReply{TransportErr: errors.New("read: connection reset by peer"), WroteRequest: true}
	}
	ws, run := newWorkspaceID(), uuid.New()
	c, _ := rg.client(t, ws, "editor")
	req := simpleRequest()
	ub := upperBoundEstimate(t, req)
	b := newSpyBudget(10_000)
	req.Run = RunInfo{ID: run, Budget: b}

	_, err := c.Messages(context.Background(), req)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	if got := len(rg.api.messageCalls()); got != 1 {
		t.Fatalf("the unknown-outcome call was retried (%d requests)", got)
	}
	rows := rg.ledger.snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if !r.Estimated || r.ListCostMicros != ub || r.InputTokens < 1 || r.OutputTokens != 1000 || r.RunID != run || r.Stage != "editor" ||
		r.BilledTo != "test" || r.PriceTableVersion != "2026-10b" || r.RequestID != "" {
		t.Errorf("row = %+v (worst case %d)", r, ub)
	}
	// The run's budget was charged the worst case, not nothing.
	if b.Spent() != ub || len(b.settled) != 1 || b.settled[0] != ub {
		t.Errorf("budget settled %v, spent %d; want %d", b.settled, b.Spent(), ub)
	}
	// And the daily cap sees it: the row is part of today's sum.
	sum := rg.ledger.Counter(ws, store.DayPeriod(*rg.now)).TestMicros
	if sum != ub {
		t.Errorf("today's sum = %d, want %d", sum, ub)
	}
	if len(rg.ledger.Holds()) != 0 {
		t.Error("a hold leaked")
	}
	mustNotContain(t, "error", err.Error(), testKey)
}

// A definite refusal (an error answer) is not paid for: nothing is recorded and the budget is not charged.
func TestErrorAnswerChargesNothing(t *testing.T) {
	rg := newRig(t, nil)
	rg.api.messages = func(int, recordedCall) fakeReply { return errReply(400, "invalid_request_error", "bad", nil) }
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	b := newSpyBudget(10_000)
	if _, err := c.Messages(context.Background(), withBudget(simpleRequest(), b)); err == nil {
		t.Fatal("expected the error")
	}
	if len(rg.ledger.snapshot()) != 0 || b.Spent() != 0 || b.Remaining() != 10_000 {
		t.Errorf("rows %d, spent %d, remaining %d", len(rg.ledger.snapshot()), b.Spent(), b.Remaining())
	}
}

// ---- timeouts ----------------------------------------------------------------------------------------------------

func TestRequestTimeoutFollowsMaxTokensAndModel(t *testing.T) {
	for _, c := range []struct {
		model  string
		tokens int
		min    time.Duration
		max    time.Duration
	}{
		{"claude-opus-5-5", 16_000, 30 * time.Minute, 60 * time.Minute},
		{"claude-opus-5-5", 64_000, 60 * time.Minute, 60 * time.Minute},
		{"claude-opus-5-5", 1_000, 10 * time.Minute, 10 * time.Minute},
		{"claude-sonnet-5-5", 16_000, 10 * time.Minute, 30 * time.Minute},
		{"claude-haiku-5-5", 16_000, 10 * time.Minute, 20 * time.Minute},
		{"claude-unknown", 16_000, 30 * time.Minute, 60 * time.Minute}, // unknown is treated as the slowest
	} {
		if got := requestTimeout(c.model, c.tokens); got < c.min || got > c.max {
			t.Errorf("requestTimeout(%s, %d) = %v, want between %v and %v", c.model, c.tokens, got, c.min, c.max)
		}
	}
}

func TestMessagesCarryTheDerivedTimeout(t *testing.T) {
	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	req.Model, req.MaxTokens = "claude-opus-5-5", 16_000
	start := time.Now()
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	dl := rg.api.messageCalls()[0].Deadline
	if d := dl.Sub(start); d < 30*time.Minute || d > 60*time.Minute+time.Minute {
		t.Errorf("a large Opus call got a %v timeout", d)
	}
	// count_tokens gets a short one.
	if _, err := c.CountTokens(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if d := rg.api.callsTo("/count_tokens")[0].Deadline.Sub(start); d > 3*time.Minute {
		t.Errorf("count_tokens timeout = %v", d)
	}
}

// ---- pricing fails closed ---------------------------------------------------------------------------------------

func TestEstimateUsesTheLongTierNearTheThreshold(t *testing.T) {
	pt, err := LoadPriceTable(pricingFS, PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	short, _ := pt.EstimateMax("claude-haiku-5-5", 50_000, 16_000, CacheMarks{})
	// 50000*0.10 + 16000*0.50 = 5000 + 8000
	if short != 13_000 {
		t.Errorf("well under the threshold = %d, want 13000", short)
	}
	// 95,000 counted + 16,000 of output could cross 100,000 prompt tokens if the count is a little low: long tier.
	near, _ := pt.EstimateMax("claude-haiku-5-5", 95_000, 16_000, CacheMarks{})
	// 95000*0.50 + 16000*2.50 = 47500 + 40000
	if near != 87_500 {
		t.Errorf("near the threshold = %d, want 87500", near)
	}
	// Models without a tier are unaffected.
	if got, _ := pt.EstimateMax("claude-opus-5-5", 95_000, 16_000, CacheMarks{}); got != 95_000*4+16_000*20 {
		t.Errorf("opus = %d", got)
	}
}

func TestMissingCacheSplitIsPricedAtTheOneHourRateWhenRequested(t *testing.T) {
	// The response reports 10,000 cache-write tokens but no cache_creation split (a gateway dropped it).
	body := `{"id":"msg_x","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":0,"cache_creation_input_tokens":10000,"cache_read_input_tokens":0,"output_tokens":0}}`
	run := func(oneHour bool) (cost int64, usage Usage) {
		rg := newRig(t, nil)
		rg.api.messages = func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: body} }
		c, _ := rg.client(t, newWorkspaceID(), "editor")
		req := simpleRequest()
		req.Model = "claude-opus-5-5"
		ttl := ""
		if oneHour {
			ttl = "1h"
		}
		req.System = []Block{{Type: BlockText, Text: "rules", Cache: &CacheControl{TTL: ttl}}}
		resp, err := c.Messages(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		rows := rg.ledger.snapshot()
		if len(rows) != 1 {
			t.Fatalf("rows = %d", len(rows))
		}
		return rows[0].ListCostMicros, resp.Usage
	}
	cost5m, u5 := run(false)
	cost1h, u1 := run(true)
	// Opus: 10000 tokens at $5 (5m) = 50000 micros; at $8 (1h) = 80000.
	if cost5m != 50_000 || cost1h != 80_000 {
		t.Errorf("costs = %d (5m request), %d (1h request); want 50000 and 80000", cost5m, cost1h)
	}
	if !u5.CacheWriteSplitMissing || !u1.CacheWriteSplitMissing || u5.CacheWrite1hTokens != 0 || u1.CacheWrite1hTokens != 10_000 {
		t.Errorf("usage = %+v / %+v", u5, u1)
	}
	// A response with a split that does add up is trusted as it is.
	rg := newRig(t, nil)
	rg.api.messages = func(int, recordedCall) fakeReply {
		return okReply("ok", apiUsage{CacheWrite5m: 4000, CacheWrite1h: 6000})
	}
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	req.Model = "claude-opus-5-5"
	resp, _ := c.Messages(context.Background(), req)
	if resp.Usage.CacheWriteSplitMissing || resp.Usage.CacheWrite1hTokens != 6000 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

// Images cost a size-dependent number of tokens (thousands at full size), so a request with an image is always counted,
// even when a local bound would clear every cap, and the caps see the real count.
func TestRequestsWithImagesAreAlwaysCounted(t *testing.T) {
	withImage := func() Request {
		r := simpleRequest()
		r.Messages[0].Content = append(r.Messages[0].Content, Block{Type: BlockImage, Image: &Image{MediaType: "image/png", Data: []byte("not-really-a-png")}})
		return r
	}
	// Text only: no count (the bound clears the generous default caps).
	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	if n := len(rg.api.callsTo("/count_tokens")); n != 0 {
		t.Fatalf("a text request was counted %d times", n)
	}
	// With an image, in the user turn or inside a tool result: counted, once, before sending.
	nested := simpleRequest()
	nested.Messages = append(nested.Messages, Message{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: "t", Content: []Block{{Type: BlockImage, Image: &Image{MediaType: "image/png", Data: []byte("x")}}}}}})
	for name, req := range map[string]Request{"user image": withImage(), "image in a tool result": nested} {
		rg = newRig(t, nil)
		c, _ = rg.client(t, newWorkspaceID(), "editor")
		if _, err := c.Messages(context.Background(), req); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n := len(rg.api.callsTo("/count_tokens")); n != 1 {
			t.Errorf("%s: %d count_tokens calls, want 1", name, n)
		}
	}
	// The count decides the caps: 4,800 tokens for one full-size image would pass a bound of 2,000, but the API's real
	// count (here 400,000) is over the per-request cap, so the call is refused before it is sent.
	rg = newRig(t, map[string]string{"LLM_MAX_REQUEST_MICROS": "100000"})
	rg.api.countTokens = func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: `{"input_tokens": 400000}`} }
	c, _ = rg.client(t, newWorkspaceID(), "editor")
	req := withImage()
	req.Model = "claude-opus-5-5"
	var ce *CapError
	if _, err := c.Messages(context.Background(), req); !errors.As(err, &ce) || ce.Kind != CapRequest || len(rg.api.messageCalls()) != 0 {
		t.Fatalf("err = %v, message calls = %d", err, len(rg.api.messageCalls()))
	}
}

// ---- the daily cap against a stale ledger -----------------------------------------------------------------------

// The ledger always says "nothing spent" (a replica that lags, say): the in-flight reservations alone must hold the
// cap for a burst of concurrent calls.
func TestBurstCannotPassTheDailyCapOnAStaleTotal(t *testing.T) {
	rg := newRig(t, map[string]string{"DAILY_SPEND_CAP_MICROS": "2000"})
	ub := upperBoundEstimate(t, simpleRequest()) // ~713; the exact worst case is 600
	proceed := make(chan struct{})
	started := make(chan struct{}, 16)
	rg.api.messages = func(int, recordedCall) fakeReply {
		started <- struct{}{}
		<-proceed
		return okReply("ok", defaultUsage)
	}
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted, refused := 0, 0
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Messages(context.Background(), simpleRequest())
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, ErrCapExceeded) {
				refused++
			} else if err == nil {
				admitted++
			}
		}()
	}
	// Wait until every call has either been admitted (and is blocked in flight) or refused, then let them finish.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := refused
		mu.Unlock()
		if done+len(started) >= 10 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	inFlight := len(started)
	close(proceed)
	wg.Wait()
	// Each admitted call holds at least its exact worst case (600), so at most 3 fit under 2000.
	if maxFit := 2000 / 600; inFlight > maxFit || inFlight == 0 {
		t.Errorf("%d calls were in flight under a 2000-micro cap (ub %d); at most %d may be", inFlight, ub, maxFit)
	}
	if admitted+refused != 10 {
		t.Errorf("admitted %d + refused %d != 10", admitted, refused)
	}
}

// ---- unknown blocks round-trip ----------------------------------------------------------------------------------

const serverToolContent = `[` +
	`{"type":"text","text":"Searching."},` +
	`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"vertical video trends"}},` +
	`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"EqgfCioIARgB"}]}` +
	`]`

func TestUnknownResponseBlocksRoundTrip(t *testing.T) {
	api := &fakeAPI{messages: func(int, recordedCall) fakeReply {
		return fakeReply{Status: 200, Body: messageJSON(serverToolContent, "pause_turn", defaultUsage)}
	}}
	c := directClient(api, "")
	resp, err := c.Messages(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "pause_turn" || len(resp.Content) != 3 {
		t.Fatalf("response = %+v", resp)
	}
	for _, i := range []int{1, 2} {
		b := resp.Content[i]
		if len(b.Raw) == 0 || string(b.Type) != []string{"", "server_tool_use", "web_search_tool_result"}[i] {
			t.Fatalf("block %d = %+v", i, b)
		}
	}

	// Append the assistant turn exactly as it came and send it back: the raw blocks go out verbatim.
	next := simpleRequest()
	next.Messages = append(next.Messages, Message{Role: RoleAssistant, Content: resp.Content})
	if _, err := c.Messages(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	body := api.messageCalls()[1].json(t)
	assistant := asSlice(asMap(asSlice(body["messages"])[1])["content"])
	if len(assistant) != 3 {
		t.Fatalf("assistant turn = %v", assistant)
	}
	su := asMap(assistant[1])
	if su["type"] != "server_tool_use" || su["id"] != "srvtoolu_1" || asMap(su["input"])["query"] != "vertical video trends" {
		t.Errorf("server_tool_use = %v", su)
	}
	wr := asMap(assistant[2])
	res := asMap(asSlice(wr["content"])[0])
	if wr["type"] != "web_search_tool_result" || wr["tool_use_id"] != "srvtoolu_1" || res["encrypted_content"] != "EqgfCioIARgB" {
		t.Errorf("web_search_tool_result = %v", wr)
	}

	// A raw block that lies about its type, or is not JSON, is an error and not sent.
	for name, b := range map[string]Block{
		"type mismatch": {Type: "server_tool_use", Raw: json.RawMessage(`{"type":"web_search_tool_result"}`)},
		"not json":      {Type: "server_tool_use", Raw: json.RawMessage(`{nope`)},
		"unknown, bare": {Type: "mystery"},
	} {
		bad := simpleRequest()
		bad.Messages = append(bad.Messages, Message{Role: RoleAssistant, Content: []Block{b}})
		if _, err := c.Messages(context.Background(), bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestUnknownBlocksSurviveRecordReplayAndKeyTheRequest(t *testing.T) {
	dir := t.TempDir()
	rec := newRig(t, map[string]string{"LLM_MODE": "record", "LLM_CASSETTE_DIR": dir})
	rec.api.messages = func(int, recordedCall) fakeReply {
		return fakeReply{Status: 200, Body: messageJSON(serverToolContent, "pause_turn", defaultUsage)}
	}
	ws := newWorkspaceID()
	c, _ := rec.client(t, ws, "research")
	want, err := c.Messages(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	rp := newRig(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": dir})
	rc, _ := rp.client(t, ws, "research")
	got, err := rc.Messages(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Content) != 3 || !jsonEqual(t, got.Content[2].Raw, want.Content[2].Raw) || !jsonEqual(t, got.Content[1].Raw, want.Content[1].Raw) {
		t.Fatalf("replayed blocks = %+v", got.Content)
	}
	// The assistant turn built from the replayed blocks keys the next request, and differs when a block differs.
	mk := func(raw string) string {
		r := simpleRequest()
		blocks := append([]Block(nil), got.Content...)
		blocks[2].Raw = json.RawMessage(raw)
		r.Messages = append(r.Messages, Message{Role: RoleAssistant, Content: blocks})
		k, err := RequestKey(r)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	same := mk(string(got.Content[2].Raw))
	reordered := mk(`{ "content":[{"url":"https://example.com/a","title":"A","encrypted_content":"EqgfCioIARgB","type":"web_search_result"}], "tool_use_id":"srvtoolu_1","type":"web_search_tool_result"}`)
	other := mk(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}`)
	if same != reordered || same == other {
		t.Errorf("raw blocks key wrongly: same=%v reordered=%v other=%v", same == reordered, same, other)
	}
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}

// ---- default-equivalent requests share a key --------------------------------------------------------------------

func TestRequestKeyNormalisesDefaults(t *testing.T) {
	key := func(mutate func(*Request)) string {
		r := simpleRequest()
		r.Tools = []Tool{{Name: "t", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Strict: true}}
		mutate(&r)
		k, err := RequestKey(r)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	base := key(func(*Request) {})
	same := map[string]func(*Request){
		"empty stop sequences":     func(r *Request) { r.StopSequences = []string{} },
		"nil system":               func(r *Request) { r.System = nil },
		"empty system":             func(r *Request) { r.System = []Block{} },
		"tool_choice auto":         func(r *Request) { r.ToolChoice = &ToolChoice{Type: "auto"} },
		"tool_choice empty":        func(r *Request) { r.ToolChoice = &ToolChoice{} },
		"name on a non-tool":       func(r *Request) { r.ToolChoice = &ToolChoice{Type: "auto", Name: "t"} },
		"empty tool description":   func(r *Request) { r.Tools[0].Description = "" },
		"5m spelled out":           func(r *Request) { r.System = nil; r.Tools[0].Cache = nil },
		"empty message list entry": func(r *Request) { r.Messages[0].Content = append([]Block(nil), r.Messages[0].Content...) },
	}
	for name, mutate := range same {
		if got := key(mutate); got != base {
			t.Errorf("%s changed the key", name)
		}
	}
	// A cache lifetime spelled "" and "5m" is one lifetime.
	a := key(func(r *Request) { r.System = []Block{{Type: BlockText, Text: "x", Cache: &CacheControl{}}} })
	b := key(func(r *Request) { r.System = []Block{{Type: BlockText, Text: "x", Cache: &CacheControl{TTL: "5m"}}} })
	if a != b {
		t.Error(`cache ttl "" and "5m" key differently`)
	}
	// tool_use with no input and with {} are the same call.
	withInput := func(in json.RawMessage) string {
		return key(func(r *Request) {
			r.Messages = append(r.Messages, Message{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: "a", Name: "t", Input: in}}})
		})
	}
	if withInput(nil) != withInput(json.RawMessage(`{}`)) {
		t.Error("tool_use input nil and {} key differently")
	}
	// Real differences still show.
	for name, mutate := range map[string]func(*Request){
		"tool_choice any":  func(r *Request) { r.ToolChoice = &ToolChoice{Type: "any"} },
		"tool_choice none": func(r *Request) { r.ToolChoice = &ToolChoice{Type: "none"} },
		"stop sequence":    func(r *Request) { r.StopSequences = []string{"END"} },
	} {
		if key(mutate) == base {
			t.Errorf("%s did not change the key", name)
		}
	}
	toolKey := func(name string) string {
		return key(func(r *Request) { r.ToolChoice = &ToolChoice{Type: "tool", Name: name} })
	}
	if toolKey("a") == toolKey("b") {
		t.Error("tool_choice tool names key alike")
	}
}

// ---- a disabled stage -------------------------------------------------------------------------------------------

func TestRouterRefusesDisabledStages(t *testing.T) {
	r, err := NewRouter(routingFS, "v1-start")
	if err != nil {
		t.Fatal(err)
	}
	// editor.subagents.inspector is `enabled: false` in v1-start (a benchmark variant only).
	_, err = r.Resolve("", "editor.subagents.inspector")
	if !errors.Is(err, ErrStageDisabled) {
		t.Fatalf("err = %v, want ErrStageDisabled", err)
	}
	if !strings.Contains(err.Error(), "editor.subagents.inspector") {
		t.Errorf("message = %v", err)
	}
	// Its parent still resolves.
	if _, err := r.Resolve("", "editor"); err != nil {
		t.Fatal(err)
	}
}
