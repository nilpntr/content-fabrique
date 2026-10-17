package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func TestFactoryNeedsAKey(t *testing.T) {
	rg := newRig(t, map[string]string{"ANTHROPIC_API_KEY": ""})
	_, _, err := rg.factory.For(context.Background(), newWorkspaceID(), "editor")
	var nk *NoKeyError
	if !errors.Is(err, ErrNoKey) || !errors.As(err, &nk) || nk.Variable != "ANTHROPIC_API_KEY" || nk.Mode != ModeLive {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("message does not name the variable: %v", err)
	}
	// Whitespace is not a key.
	rg = newRig(t, map[string]string{"ANTHROPIC_API_KEY": "  \n"})
	if _, _, err := rg.factory.For(context.Background(), newWorkspaceID(), "editor"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("blank key: %v", err)
	}
	// Record needs a key too.
	rg = newRig(t, map[string]string{"ANTHROPIC_API_KEY": "", "LLM_MODE": "record", "LLM_CASSETTE_DIR": t.TempDir()})
	if _, _, err := rg.factory.For(context.Background(), newWorkspaceID(), "editor"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("record without key: %v", err)
	}
}

func TestFactoryValidation(t *testing.T) {
	rg := newRig(t, nil)
	for _, stage := range []string{"", "Editor", "../etc", "a/b", "x y", strings.Repeat("a", 65)} {
		if _, _, err := rg.factory.For(context.Background(), newWorkspaceID(), stage); err == nil {
			t.Errorf("stage %q accepted", stage)
		}
	}
	var zero = newWorkspaceID()
	zero.UUID = uuid.Nil
	if _, _, err := rg.factory.For(context.Background(), zero, "editor"); err == nil {
		t.Error("zero workspace accepted")
	}
	prices, _ := LoadPriceTable(pricingFS, PriceFile)
	for name, o := range map[string]FactoryOptions{
		"no prices": {Settings: Settings{Env: "local", Mode: ModeLive}, Getenv: env(nil), Ledger: &fakeLedger{}},
		"no getenv": {Settings: Settings{Env: "local", Mode: ModeLive}, Prices: prices, Ledger: &fakeLedger{}},
		"no ledger": {Settings: Settings{Env: "local", Mode: ModeLive}, Prices: prices, Getenv: env(nil)},
		"bad env":   {Settings: Settings{Env: "prod", Mode: ModeLive}, Prices: prices, Getenv: env(nil), Ledger: &fakeLedger{}},
	} {
		if _, err := NewFactory(o); err == nil {
			t.Errorf("%s: NewFactory succeeded", name)
		}
	}
	// Replay needs no ledger.
	if _, err := NewFactory(FactoryOptions{Settings: Settings{Env: "ci", Mode: ModeReplay}, Prices: prices, Getenv: env(nil)}); err != nil {
		t.Errorf("replay without ledger: %v", err)
	}
}

func TestBilledToByEnvironment(t *testing.T) {
	for envName, want := range map[string]Billing{"local": BilledToTest, "ci": BilledToTest, "bench": BilledToTest, "staging": BilledToPlatform, "production": BilledToPlatform} {
		rg := newRig(t, map[string]string{"CF_ENV": envName})
		ws := newWorkspaceID()
		c, billing := rg.client(t, ws, "editor")
		if billing != want {
			t.Errorf("%s: billing = %q, want %q", envName, billing, want)
		}
		if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
			t.Fatal(err)
		}
		rows := rg.ledger.snapshot()
		if len(rows) != 1 || rows[0].BilledTo != string(want) {
			t.Errorf("%s: ledger rows = %+v, want one billed_to %s", envName, rows, want)
		}
	}
}

func TestEveryCallWritesOneLedgerRow(t *testing.T) {
	rg := newRig(t, nil)
	rg.api.messages = func(int, recordedCall) fakeReply {
		return okReply("hi", apiUsage{Input: 1000, CacheWrite5m: 2000, CacheWrite1h: 500, CacheRead: 10000, Output: 500})
	}
	ws, run := newWorkspaceID(), uuid.New()
	c, _ := rg.client(t, ws, "editor")
	req := simpleRequest()
	req.Model = "claude-opus-5-5"
	req.Run = RunInfo{ID: run}
	resp, err := c.Messages(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	rows := rg.ledger.snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	// Opus 5.5: see TestCost: 4000+ (2500 cache writes: 2000*5... ) computed from the same usage there = 27500 + the 5m part.
	// 1000*4 + 2000*5 + 500*8 + 10000*0.2 + 500*20 = 4000+10000+4000+2000+10000 = 30000.
	want := store.UsageEntry{
		WorkspaceID: ws, RunID: run, Stage: "editor", Kind: "llm", Model: "claude-opus-5-5",
		InputTokens: 1000, CacheWriteTokens: 2500, CacheWrite1hTokens: 500, CacheReadTokens: 10000, OutputTokens: 500,
		ListCostMicros: 30000, PriceTableVersion: "2026-10", BilledTo: "test", RequestID: resp.RequestID,
	}
	r.CreatedAt = time.Time{}
	if r != want {
		t.Errorf("row =\n %+v\nwant\n %+v", r, want)
	}
	if resp.RequestID == "" {
		t.Error("no request id on the response")
	}
}

func TestUsageOnAnErrorStillWritesARow(t *testing.T) {
	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	m := asMetered(t, c)
	m.inner = clientFunc{messages: func(context.Context, Request) (Response, error) {
		return Response{Usage: Usage{InputTokens: 100, OutputTokens: 50}, RequestID: "req_x"}, errors.New("stream cut off after tokens were consumed")
	}, count: func() int64 { return 10 }}
	_, err := c.Messages(context.Background(), simpleRequest())
	if err == nil {
		t.Fatal("expected the error")
	}
	if rows := rg.ledger.snapshot(); len(rows) != 1 || rows[0].InputTokens != 100 || rows[0].RequestID != "req_x" {
		t.Fatalf("rows = %+v", rows)
	}
	// No usage, no row.
	m.inner = clientFunc{messages: func(context.Context, Request) (Response, error) { return Response{}, errors.New("connection refused") }, count: func() int64 { return 10 }}
	if _, err := c.Messages(context.Background(), simpleRequest()); err == nil {
		t.Fatal("expected the error")
	}
	if rows := rg.ledger.snapshot(); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
}

type clientFunc struct {
	messages func(context.Context, Request) (Response, error)
	count    func() int64
}

func (c clientFunc) Messages(ctx context.Context, r Request) (Response, error) {
	return c.messages(ctx, r)
}
func (c clientFunc) CountTokens(context.Context, Request) (int64, error) { return c.count(), nil }

func TestRunBudgetIsChargedAndEnforced(t *testing.T) {
	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	b := &fixedBudget{left: 50_000}
	req := simpleRequest() // Haiku: 1000 counted tokens*0.1 + 1000 max tokens*0.5 = 100+500 = 600 micros worst case
	req.Run = RunInfo{Budget: b}
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// defaultUsage on Haiku: 1000*0.1 + 200*0.5 = 200 micros.
	if len(b.spends) != 1 || b.spends[0] != 200 || b.Remaining() != 49_800 {
		t.Fatalf("spends = %v, remaining %d", b.spends, b.Remaining())
	}
	// A run with less left than the worst case is refused before anything is sent.
	b.left = 599
	sent := len(rg.api.messageCalls())
	_, err := c.Messages(context.Background(), req)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapRun || ce.EstimateMicros != 600 || ce.LimitMicros != 599 || !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("err = %#v", err)
	}
	if len(rg.api.messageCalls()) != sent {
		t.Error("a refused call was sent")
	}
	// An overspent (negative) budget refuses everything.
	b.left = -5
	if _, err := c.Messages(context.Background(), req); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("negative budget: %v", err)
	}
	// Exactly enough is allowed.
	b.left = 600
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatalf("exact budget refused: %v", err)
	}
}

func TestPerRequestCapRefusesBeforeSending(t *testing.T) {
	rg := newRig(t, map[string]string{"LLM_MAX_REQUEST_MICROS": "1000000"}) // $1
	rg.api.countTokens = func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: `{"input_tokens": 400000}`} }
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	req.Model = "claude-opus-5-5" // 400k*4 + 1000*20 = 1.6M + 20k micros
	_, err := c.Messages(context.Background(), req)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapRequest || ce.EstimateMicros != 1_620_000 || ce.LimitMicros != 1_000_000 {
		t.Fatalf("err = %#v", err)
	}
	if got := len(rg.api.messageCalls()); got != 0 {
		t.Errorf("%d message calls were sent", got)
	}
	if len(rg.ledger.snapshot()) != 0 {
		t.Error("a refused call wrote a ledger row")
	}
	// The default cap is $2.
	rg = newRig(t, nil)
	rg.api.countTokens = func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: `{"input_tokens": 600000}`} }
	c, _ = rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), req); !errors.Is(err, ErrCapExceeded) { // 2.4M + 20k
		t.Fatalf("default cap: %v", err)
	}
}

func TestCacheBreakpointsRaiseTheEstimate(t *testing.T) {
	rg := newRig(t, map[string]string{"LLM_MAX_REQUEST_MICROS": "420000"})
	rg.api.countTokens = func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: `{"input_tokens": 80000}`} }
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	req.Model, req.MaxTokens = "claude-opus-5-5", 5000 // plain: 80000*4+5000*20 = 420000: allowed
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatalf("plain request refused: %v", err)
	}
	req.System = []Block{{Type: BlockText, Text: "rules", Cache: &CacheControl{}}} // 5m write: 80000*5+100000 = 500000
	if _, err := c.Messages(context.Background(), req); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("cached request: %v", err)
	}
}

func TestDailyCapUsesTodaysLedger(t *testing.T) {
	rg := newRig(t, map[string]string{"DAILY_SPEND_CAP_MICROS": "10000"})
	ws, other := newWorkspaceID(), newWorkspaceID()
	now := *rg.now
	rg.ledger.rows = append(rg.ledger.rows,
		store.UsageEntry{WorkspaceID: ws, ListCostMicros: 4_000, CreatedAt: now.Add(-time.Hour)},                     // today
		store.UsageEntry{WorkspaceID: ws, ListCostMicros: 4_000, CreatedAt: now.Add(-13 * time.Hour)},                // yesterday (UTC): not counted
		store.UsageEntry{WorkspaceID: other, ListCostMicros: 9_999_999, CreatedAt: now.Add(-time.Hour)},              // another workspace: not counted
		store.UsageEntry{WorkspaceID: ws, ListCostMicros: 3_500, CreatedAt: now.Add(-time.Minute), BilledTo: "test"}, // today
	)
	c, _ := rg.client(t, ws, "editor")
	req := simpleRequest() // worst case 600 micros; 7500 spent today, so 2500 left
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatalf("under the cap: %v", err)
	}
	// That call cost 200 micros: 7700 spent. Raise max_tokens so the worst case no longer fits in the 2300 left.
	req.MaxTokens = 5000 // 100 + 2500 = 2600
	_, err := c.Messages(context.Background(), req)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapDaily || ce.EstimateMicros != 2600 || ce.LimitMicros != 2300 {
		t.Fatalf("err = %#v", err)
	}
	// The next UTC day starts clean.
	*rg.now = now.Add(12*time.Hour + time.Minute)
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatalf("next day: %v", err)
	}
}

// Calls in flight are not in the ledger yet, so a burst must not all pass the check on the same stale total.
func TestDailyCapCountsCallsInFlight(t *testing.T) {
	rg := newRig(t, map[string]string{"DAILY_SPEND_CAP_MICROS": "1000"}) // room for one 600-micro worst case, not two
	ws := newWorkspaceID()
	inFlight, release := make(chan struct{}), make(chan struct{})
	rg.api.messages = func(n int, _ recordedCall) fakeReply {
		if n == 1 {
			close(inFlight)
			<-release
		}
		return okReply("ok", defaultUsage)
	}
	c, _ := rg.client(t, ws, "editor")
	done := make(chan error, 1)
	go func() { _, err := c.Messages(context.Background(), simpleRequest()); done <- err }()
	<-inFlight

	// The first call is mid-flight and unmetered; a second one would be 600 + 600 > 1000.
	_, err := c.Messages(context.Background(), simpleRequest())
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapDaily || ce.LimitMicros != 400 {
		t.Fatalf("second concurrent call: %#v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Once the first is metered (200 micros), the reservation is gone and the next fits again (200 + 600 <= 1000).
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatalf("after the first finished: %v", err)
	}
	if got := len(rg.factory.reserve.m); got != 0 {
		t.Errorf("%d reservations leaked", got)
	}
}

func TestDailyCapFailsClosedWhenTheLedgerCannotBeRead(t *testing.T) {
	rg := newRig(t, nil)
	rg.ledger.failSum = true
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err == nil {
		t.Fatal("a call was sent without knowing today's spend")
	}
	if got := len(rg.api.messageCalls()); got != 0 {
		t.Errorf("%d message calls were sent", got)
	}
}

func TestUnpricedModelsAreRefusedUnlessAllowed(t *testing.T) {
	req := simpleRequest()
	req.Model = "claude-mystery-9"

	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	_, err := c.Messages(context.Background(), req)
	var ue *UnpricedError
	if !errors.Is(err, ErrUnpriced) || !errors.As(err, &ue) || ue.Model != "claude-mystery-9" || ue.TableVersion != "2026-10" {
		t.Fatalf("err = %#v", err)
	}
	if rg.api.totalCalls() != 0 {
		t.Error("an unpriced model reached the network")
	}

	// LLM_ALLOW_UNPRICED=1: the call goes out, caps cannot be checked, and the row records cost 0 and says so.
	rg = newRig(t, map[string]string{"LLM_ALLOW_UNPRICED": "1"})
	c, _ = rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	rows := rg.ledger.snapshot()
	if len(rows) != 1 || rows[0].ListCostMicros != 0 || rows[0].PriceTableVersion != UnpricedVersion || rows[0].InputTokens != 1000 {
		t.Fatalf("rows = %+v", rows)
	}
	if !strings.Contains(rg.logs.String(), "unpriced") {
		t.Error("an unpriced call was not logged")
	}
}

func TestLedgerFailureReturnsThePaidResponse(t *testing.T) {
	rg := newRig(t, nil)
	rg.ledger.failInserts = 100
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	resp, err := c.Messages(context.Background(), simpleRequest())
	var le *LedgerError
	if !errors.Is(err, ErrLedger) || !errors.As(err, &le) {
		t.Fatalf("err = %v", err)
	}
	if resp.Text() != "hello" || resp.Usage.OutputTokens != 200 {
		t.Errorf("the paid response was dropped: %+v", resp)
	}
	if rg.ledger.inserts != ledgerAttempts {
		t.Errorf("insert attempts = %d, want %d", rg.ledger.inserts, ledgerAttempts)
	}
	logs := rg.logs.String()
	if !strings.Contains(logs, "usage ledger write failed") || !strings.Contains(logs, "list_cost_micros=200") {
		t.Errorf("the lost row was not logged for reconciliation:\n%s", logs)
	}
	mustNotContain(t, "logs", logs, testKey)

	// A flaky ledger that recovers loses nothing.
	rg = newRig(t, nil)
	rg.ledger.failInserts = 2
	c, _ = rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatalf("recovering ledger: %v", err)
	}
	if len(rg.ledger.snapshot()) != 1 {
		t.Error("row lost")
	}
}

func TestLedgerWriteSurvivesACancelledContext(t *testing.T) {
	rg := newRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	asMetered(t, c).inner = clientFunc{count: func() int64 { return 10 }, messages: func(context.Context, Request) (Response, error) {
		cancel() // the run is cancelled just as the reply lands
		return Response{Usage: Usage{InputTokens: 100, OutputTokens: 50}, RequestID: "req_c"}, nil
	}}
	if _, err := c.Messages(ctx, simpleRequest()); err != nil {
		t.Fatal(err)
	}
	if len(rg.ledger.snapshot()) != 1 {
		t.Fatal("a paid call from a cancelled run was not metered")
	}
}

func TestImageReferencesAreResolvedBeforeSending(t *testing.T) {
	res := &fakeResolver{data: map[string][]byte{"r2://k/1": []byte("frame-one")}}
	rg := newRig(t, nil, func(o *FactoryOptions) { o.Images = res })
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	req.Messages[0].Content = append(req.Messages[0].Content,
		Block{Type: BlockImage, Image: &Image{Ref: "r2://k/1"}},
		Block{Type: BlockToolResult, ToolUseID: "t", Content: []Block{{Type: BlockImage, Image: &Image{Ref: "r2://k/1"}}}},
	)
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.Messages[0].Content[1].Image.Data != nil {
		t.Error("the caller's request was changed")
	}
	// Both the token count and the message carried the bytes.
	for _, call := range append(rg.api.messageCalls(), rg.api.callsTo("/count_tokens")...) {
		if strings.Count(string(call.Body), "ZnJhbWUtb25l") != 2 { // base64("frame-one")
			t.Errorf("%s did not carry both resolved images: %s", call.Path, call.Body)
		}
	}
	// Failures are errors, not silent drops.
	req.Messages[0].Content[1].Image.Ref = "r2://missing"
	if _, err := c.Messages(context.Background(), req); err == nil {
		t.Error("an unresolvable reference was sent")
	}
	rg2 := newRig(t, nil)
	c2, _ := rg2.client(t, newWorkspaceID(), "editor")
	if _, err := c2.Messages(context.Background(), req); err == nil {
		t.Error("a reference without a resolver was sent")
	}
}

type fakeResolver struct{ data map[string][]byte }

func (f *fakeResolver) ResolveImage(_ context.Context, ref string) (string, []byte, error) {
	d, ok := f.data[ref]
	if !ok {
		return "", nil, errors.New("not found")
	}
	return "image/jpeg", d, nil
}

// A second seam for P4-00b: a custom KeyResolver decides the key and who pays.
func TestKeyResolverSeam(t *testing.T) {
	rg := newRig(t, map[string]string{"ANTHROPIC_API_KEY": ""}, func(o *FactoryOptions) { o.Keys = byokKeys{} })
	ws := newWorkspaceID()
	c, billing := rg.client(t, ws, "editor")
	if billing != BilledToWorkspaceKey {
		t.Fatalf("billing = %q", billing)
	}
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	if got := rg.api.messageCalls()[0].Header.Get("X-Api-Key"); got != "sk-ant-byok-key" {
		t.Errorf("the resolved key was not used")
	}
	if rows := rg.ledger.snapshot(); len(rows) != 1 || rows[0].BilledTo != "workspace_key" {
		t.Errorf("rows = %+v", rows)
	}
}

type byokKeys struct{}

func (byokKeys) ResolveKey(context.Context, ids.WorkspaceID) (string, Billing, error) {
	return "sk-ant-byok-key", BilledToWorkspaceKey, nil
}
