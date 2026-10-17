package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const distinctPrompt = "PROMPT-TEXT-THAT-MUST-NOT-APPEAR-IN-THE-SUMMARY"

func cassetteRequest() Request {
	req := fullRequest()
	req.Messages[0].Content[0] = TextBlock(distinctPrompt)
	return req
}

func readCassettes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		out[p] = string(b)
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRecordThenReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	content := `[{"type":"thinking","thinking":"hmm","signature":"sig1"},{"type":"text","text":"Plan: cut it."},{"type":"tool_use","id":"toolu_1","name":"finish","input":{"summary":"done"}}]`

	// Record against the fake API.
	rec := newRig(t, map[string]string{"LLM_MODE": "record", "LLM_CASSETTE_DIR": dir, "CF_AIG_TOKEN": testToken, "ANTHROPIC_BASE_URL": gatewayURL})
	rec.api.messages = func(int, recordedCall) fakeReply {
		return fakeReply{Status: 200, Body: messageJSON(content, "tool_use", apiUsage{Input: 900, CacheWrite5m: 100, CacheRead: 50, Output: 77})}
	}
	ws := newWorkspaceID()
	c, _ := rec.client(t, ws, "editor")
	want, err := c.Messages(context.Background(), cassetteRequest())
	if err != nil {
		t.Fatal(err)
	}
	if rows := rec.ledger.snapshot(); len(rows) != 1 || rows[0].BilledTo != "test" {
		t.Errorf("a recorded call must write a test-billed ledger row: %+v", rows)
	}
	key, err := RequestKey(cassetteRequest())
	if err != nil {
		t.Fatal(err)
	}
	files := readCassettes(t, dir)
	path := filepath.Join(dir, "editor", key+".json")
	if len(files) != 1 || files[path] == "" {
		t.Fatalf("cassettes = %v, want only %s", keys(files), path)
	}

	// The cassette holds neither secrets nor prompt text.
	body := files[path]
	mustNotContain(t, "cassette", body, testKey, testToken, distinctPrompt)
	for _, banned := range []string{"x-api-key", "authorization", "cf-aig", "gateway.ai.cloudflare.com", "acct123"} {
		if strings.Contains(strings.ToLower(body), banned) {
			t.Errorf("cassette mentions %q", banned)
		}
	}
	var cf cassetteFile
	if err := json.Unmarshal([]byte(body), &cf); err != nil {
		t.Fatal(err)
	}
	if cf.Version != 1 || cf.Stage != "editor" || cf.Key != key || cf.Request.Model != "claude-opus-5-5" || len(cf.Request.Images) != 2 || cf.Request.Tools[0] != "view_clip" {
		t.Errorf("cassette = %+v", cf)
	}
	mustNotContain(t, "record logs", rec.logs.String(), testKey, testToken)

	// Replay with no key and a network that fails the test if touched.
	rp := newRig(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": dir, "ANTHROPIC_API_KEY": "", "CF_ENV": "ci"})
	rp.api.messages = func(int, recordedCall) fakeReply { t.Error("replay called the API"); return fakeReply{Status: 500} }
	rp.api.countTokens = func(int, recordedCall) fakeReply {
		t.Error("replay called count_tokens")
		return fakeReply{Status: 500}
	}
	rc, billing := rp.client(t, ws, "editor")
	if billing != BilledToTest {
		t.Errorf("billing = %q", billing)
	}
	got, err := rc.Messages(context.Background(), cassetteRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got.Text() != want.Text() || got.StopReason != "tool_use" || got.Usage != want.Usage || len(got.Content) != 3 ||
		got.Content[0].Signature != "sig1" || string(got.ToolUses()[0].Input) != `{"summary":"done"}` {
		t.Errorf("replayed = %+v\nrecorded = %+v", got, want)
	}
	if rp.api.totalCalls() != 0 {
		t.Errorf("replay made %d network calls", rp.api.totalCalls())
	}
	// Nothing was spent, so nothing was metered.
	if len(rp.ledger.snapshot()) != 0 {
		t.Error("replay wrote a ledger row")
	}
	// The run budget is still charged, so budget behaviour matches live.
	b := newSpyBudget(1_000_000)
	req := cassetteRequest()
	req.Run = RunInfo{Budget: b}
	if _, err := rc.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Opus: 900*4 + 100*5 + 50*0.2 + 77*20 = 3600+500+10+1540 = 5650.
	if len(b.settled) != 1 || b.settled[0] != 5650 || b.Spent() != 5650 {
		t.Errorf("replay settled %v, spent %d", b.settled, b.Spent())
	}
	// This cassette was recorded without a count (the request was far under every cap, so none was needed): CountTokens
	// in replay falls back to a local estimate, and is not an API call.
	if n, err := rc.CountTokens(context.Background(), cassetteRequest()); err != nil || n < 100 {
		t.Errorf("replay CountTokens = %d, %v", n, err)
	}
}

// count_tokens results are stored in the cassette at record time and served in replay, so cap decisions come out as
// they did live.
func TestReplayServesTheRecordedTokenCount(t *testing.T) {
	dir := t.TempDir()
	// 650 micros is under the local bound of this request, so recording has to count it.
	vars := map[string]string{"LLM_MODE": "record", "LLM_CASSETTE_DIR": dir, "LLM_MAX_REQUEST_MICROS": "650"}
	rec := newRig(t, vars)
	rec.api.countTokens = func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: `{"input_tokens": 1234}`} }
	ws := newWorkspaceID()
	c, _ := rec.client(t, ws, "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.api.callsTo("/count_tokens")); n != 1 {
		t.Fatalf("recording made %d counts, want 1", n)
	}
	key, _ := RequestKey(simpleRequest())
	body := readCassettes(t, dir)[filepath.Join(dir, "editor", key+".json")]
	if !strings.Contains(body, `"count_tokens": 1234`) {
		t.Fatalf("the count was not stored:\n%s", body)
	}

	rp := newRig(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": dir, "LLM_MAX_REQUEST_MICROS": "650"})
	rc, _ := rp.client(t, ws, "editor")
	if n, err := rc.CountTokens(context.Background(), simpleRequest()); err != nil || n != 1234 {
		t.Fatalf("replayed count = %d, %v; want the recorded 1234", n, err)
	}
	// The same decisions as live: the call is counted (1234 tokens -> 123+500 = 624 micros <= 650) and goes through.
	if _, err := rc.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	// A cap the recorded count does not clear refuses the replay, exactly as live would.
	tight := newRig(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": dir, "LLM_MAX_REQUEST_MICROS": "600"})
	tc, _ := tight.client(t, ws, "editor")
	if _, err := tc.Messages(context.Background(), simpleRequest()); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("tight replay: %v", err)
	}
	// And a request with no cassette at all is a miss on the count too.
	other := simpleRequest()
	other.MaxTokens = 7
	if _, err := rc.CountTokens(context.Background(), other); !errors.Is(err, ErrReplayMiss) {
		t.Fatalf("count without cassette: %v", err)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestReplayMissNamesStageAndKeyAndNeverCallsTheAPI(t *testing.T) {
	dir := t.TempDir()
	rp := newRig(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": dir})
	c, _ := rp.client(t, newWorkspaceID(), "editor")
	_, err := c.Messages(context.Background(), simpleRequest())
	var miss *ReplayMissError
	if !errors.Is(err, ErrReplayMiss) || !errors.As(err, &miss) {
		t.Fatalf("err = %v", err)
	}
	key, _ := RequestKey(simpleRequest())
	if miss.Stage != "editor" || miss.Key != key || !strings.Contains(err.Error(), "editor") || !strings.Contains(err.Error(), key) {
		t.Errorf("miss = %+v / %v", miss, err)
	}
	if rp.api.totalCalls() != 0 {
		t.Error("a replay miss fell back to the API")
	}
	// Even with a key present, replay never goes out.
	if miss.Path != filepath.Join(dir, "editor", key+".json") {
		t.Errorf("path = %s", miss.Path)
	}
}

func TestCassetteDirIsPerStage(t *testing.T) {
	dir := t.TempDir()
	rec := newRig(t, map[string]string{"LLM_MODE": "record", "LLM_CASSETTE_DIR": dir})
	ws := newWorkspaceID()
	c, _ := rec.client(t, ws, "ideation")
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	// The same request replayed under another stage is a miss: cassettes are filed by stage.
	rp := newRig(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": dir})
	other, _ := rp.client(t, ws, "editor")
	if _, err := other.Messages(context.Background(), simpleRequest()); !errors.Is(err, ErrReplayMiss) {
		t.Fatalf("err = %v", err)
	}
	same, _ := rp.client(t, ws, "ideation")
	if _, err := same.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
}

func TestRequestKey(t *testing.T) {
	base := func() Request {
		r := fullRequest()
		return r
	}
	k0, err := RequestKey(base())
	if err != nil || len(k0) != 64 {
		t.Fatalf("key = %q, %v", k0, err)
	}
	same := func(name string, mutate func(*Request)) {
		t.Helper()
		r := base()
		mutate(&r)
		if k, _ := RequestKey(r); k != k0 {
			t.Errorf("%s changed the key", name)
		}
	}
	differs := func(name string, mutate func(*Request)) {
		t.Helper()
		r := base()
		mutate(&r)
		if k, err := RequestKey(r); err != nil || k == k0 {
			t.Errorf("%s did not change the key (%v)", name, err)
		}
	}
	// Not part of the key: who is asking.
	same("run info", func(r *Request) { r.Run = RunInfo{ID: newWorkspaceID().UUID, Profile: "x", Budget: NewRunBudget(1)} })
	// An image's identity is its bytes: the reference that produced them does not matter.
	same("image ref", func(r *Request) { r.Messages[0].Content[1].Image.Ref = "r2://elsewhere" })
	// JSON key order in a tool input or schema does not matter either.
	same("input key order", func(r *Request) {
		r.Messages[1].Content[1].Input = json.RawMessage(`{ "asset_id" : "ast_1" }`)
		r.Tools[0].InputSchema = json.RawMessage(`{"additionalProperties":false,"required":["asset_id"],"properties":{"at_ms":{"items":{"type":"integer"},"type":"array"},"asset_id":{"type":"string"}},"type":"object"}`)
	})
	differs("image bytes", func(r *Request) { r.Messages[0].Content[1].Image.Data = []byte("other bytes") })
	differs("image media type", func(r *Request) { r.Messages[0].Content[1].Image.MediaType = "image/webp" })
	differs("model", func(r *Request) { r.Model = "claude-haiku-5-5" })
	differs("text", func(r *Request) { r.Messages[0].Content[0].Text += "!" })
	differs("system", func(r *Request) { r.System[0].Text += "!" })
	differs("max_tokens", func(r *Request) { r.MaxTokens++ })
	differs("effort", func(r *Request) { r.Effort = "high" })
	differs("thinking", func(r *Request) { r.Thinking = nil })
	differs("tools", func(r *Request) { r.Tools = r.Tools[:1] })
	differs("strict", func(r *Request) { r.Tools[0].Strict = false })
	differs("cache control", func(r *Request) { r.System[1].Cache = nil })
	differs("cache ttl", func(r *Request) { r.System[1].Cache = &CacheControl{TTL: "1h"} })
	differs("tool choice", func(r *Request) { r.ToolChoice = &ToolChoice{Type: "any"} })
	differs("tool input", func(r *Request) { r.Messages[1].Content[1].Input = json.RawMessage(`{"asset_id":"ast_2"}`) })
	differs("tool result error flag", func(r *Request) { r.Messages[2].Content[0].IsError = true })
	// An unresolved reference has no identity yet.
	r := base()
	r.Messages[0].Content[1].Image = &Image{Ref: "r2://x"}
	if _, err := RequestKey(r); err == nil {
		t.Error("a request with an unresolved image reference got a key")
	}
}

func TestCorruptOrForeignCassettesAreErrorsNotSilentMisses(t *testing.T) {
	dir := t.TempDir()
	rp := newRig(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": dir})
	c, _ := rp.client(t, newWorkspaceID(), "editor")
	key, _ := RequestKey(simpleRequest())
	if err := os.MkdirAll(filepath.Join(dir, "editor"), 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "editor", key+".json")
	for name, body := range map[string]string{"junk": "{nope", "wrong key": `{"version":1,"key":"abc"}`, "old version": `{"version":0,"key":"` + key + `"}`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := c.Messages(context.Background(), simpleRequest())
		if err == nil || errors.Is(err, ErrReplayMiss) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestStageIsValidatedForPaths(t *testing.T) {
	for _, stage := range []string{"..", "../x", "a/b", ".hidden", ""} {
		if _, err := newCassetteClient(nil, ModeReplay, t.TempDir(), stage, nil); err == nil {
			t.Errorf("stage %q accepted", stage)
		}
	}
}

func TestLiveModeWritesNoCassettes(t *testing.T) {
	dir := t.TempDir()
	rg := newRig(t, map[string]string{"LLM_CASSETTE_DIR": dir})
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	if files := readCassettes(t, dir); len(files) != 0 {
		t.Errorf("live mode wrote %v", keys(files))
	}
}

func TestRecordDoesNotRecordFailures(t *testing.T) {
	dir := t.TempDir()
	rg := newRig(t, map[string]string{"LLM_MODE": "record", "LLM_CASSETTE_DIR": dir})
	rg.api.messages = func(int, recordedCall) fakeReply { return errReply(400, "invalid_request_error", "bad", nil) }
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err == nil {
		t.Fatal("expected the error")
	}
	if files := readCassettes(t, dir); len(files) != 0 {
		t.Errorf("a failed call was recorded: %v", keys(files))
	}
}

// The repository's own cassette directory convention: testdata/cassettes/<stage>/<key>.json.
func TestDefaultCassetteDir(t *testing.T) {
	s, err := LoadSettings(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.CassetteDir != "testdata/cassettes" {
		t.Fatalf("default = %q", s.CassetteDir)
	}
}
