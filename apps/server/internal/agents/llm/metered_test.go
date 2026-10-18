package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/spend/spendtest"
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
		ListCostMicros: 30000, PriceTableVersion: "2026-10b", BilledTo: "test", RequestID: resp.RequestID,
	}
	r.CreatedAt = time.Time{}
	// The run is the action; the idempotency key is "llm:" and the call's own id, unique per call.
	if !strings.HasPrefix(r.IdempotencyKey, "llm:") || len(r.IdempotencyKey) < 20 {
		t.Errorf("idempotency key = %q", r.IdempotencyKey)
	}
	want.ActionID, want.IdempotencyKey = run, r.IdempotencyKey
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

func TestRunBudgetReservesThenSettles(t *testing.T) {
	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest() // Haiku: the exact worst case is 1000 counted tokens*0.1 + 1000 max tokens*0.5 = 600 micros
	ub := upperBoundEstimate(t, req)
	if ub <= 600 {
		t.Fatalf("test assumes the local upper bound (%d) is above the exact worst case (600)", ub)
	}

	b := newSpyBudget(50_000)
	req.Run = RunInfo{Budget: b}
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// The worst case was set aside before sending (the local bound; no count_tokens call was needed) and settled at
	// the real cost: defaultUsage on Haiku is 1000*0.1 + 200*0.5 = 200 micros.
	if len(b.reserved) != 1 || b.reserved[0] != ub || len(b.settled) != 1 || b.settled[0] != 200 || b.Spent() != 200 || b.Remaining() != 49_800 {
		t.Fatalf("reserved %v, settled %v, spent %d, remaining %d", b.reserved, b.settled, b.Spent(), b.Remaining())
	}
	if n := len(rg.api.callsTo("/count_tokens")); n != 0 {
		t.Errorf("%d count_tokens calls for a request far under every cap", n)
	}

	// A budget between the exact worst case and the local bound: the bound does not fit, the exact count does.
	tight := newSpyBudget((ub + 600) / 2)
	req.Run = RunInfo{Budget: tight}
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatalf("a budget that covers the exact worst case refused the call: %v", err)
	}
	if len(tight.reserved) != 1 || tight.reserved[0] != 600 || len(rg.api.callsTo("/count_tokens")) != 1 {
		t.Errorf("reserved %v after %d counts; want one 600 reservation after one count", tight.reserved, len(rg.api.callsTo("/count_tokens")))
	}

	// Less than the exact worst case is refused before anything is sent, and leaves nothing reserved.
	short := newSpyBudget(599)
	req.Run = RunInfo{Budget: short}
	sent := len(rg.api.messageCalls())
	_, err := c.Messages(context.Background(), req)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapRun || ce.EstimateMicros != 600 || ce.LimitMicros != 599 || !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("err = %#v", err)
	}
	if len(rg.api.messageCalls()) != sent {
		t.Error("a refused call was sent")
	}
	if short.Remaining() != 599 {
		t.Errorf("a refused call left %d reserved", 599-short.Remaining())
	}
	// An overspent (negative) budget refuses everything; exactly enough is allowed.
	if _, err := c.Messages(context.Background(), withBudget(req, NewRunBudget(-5))); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("negative budget: %v", err)
	}
	if _, err := c.Messages(context.Background(), withBudget(req, NewRunBudget(600))); err != nil {
		t.Fatalf("exact budget refused: %v", err)
	}
}

func withBudget(req Request, b Budget) Request { req.Run.Budget = b; return req }

// Concurrent calls of one run can never overspend: each reserves its worst case before it is sent, so the second one
// is refused while the first is in flight, and the room comes back (at the real cost) when the first settles.
func TestRunBudgetNeverOverspendsUnderConcurrency(t *testing.T) {
	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	ub := upperBoundEstimate(t, req)
	b := newSpyBudget(ub + 100) // room for one reservation, not two
	req.Run = RunInfo{Budget: b}
	inFlight, release := make(chan struct{}), make(chan struct{})
	rg.api.messages = func(n int, _ recordedCall) fakeReply {
		if n == 1 {
			close(inFlight)
			<-release
		}
		return okReply("ok", defaultUsage)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Messages(context.Background(), req); done <- err }()
	<-inFlight
	_, err := c.Messages(context.Background(), req)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapRun {
		t.Fatalf("second concurrent call: %#v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if b.Spent() != 200 || b.Remaining() != ub+100-200 {
		t.Errorf("spent %d, remaining %d", b.Spent(), b.Remaining())
	}
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatalf("after the first settled: %v", err)
	}

	// Many goroutines against a small budget: the total settled never exceeds it, whatever the interleaving.
	rg2 := newRig(t, nil)
	rg2.api.messages = nil
	c2, _ := rg2.client(t, newWorkspaceID(), "editor")
	small := NewRunBudget(2_000) // each call reserves ub (~700) and settles at 200
	r2 := withBudget(simpleRequest(), small)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c2.Messages(context.Background(), r2) }()
	}
	wg.Wait()
	if small.Spent() > 2_000 || small.Remaining() < 0 {
		t.Errorf("overspent: spent %d, remaining %d", small.Spent(), small.Remaining())
	}
	if small.Spent() == 0 {
		t.Error("no call went through")
	}
}

func TestPerRequestCapRefusesBeforeSending(t *testing.T) {
	// 500 micros is under the local bound (so the call is counted) and under the exact worst case of 600.
	rg := newRig(t, map[string]string{"LLM_MAX_REQUEST_MICROS": "500"})
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	_, err := c.Messages(context.Background(), simpleRequest())
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapRequest || ce.EstimateMicros != 600 || ce.LimitMicros != 500 {
		t.Fatalf("err = %#v", err)
	}
	if got := len(rg.api.messageCalls()); got != 0 {
		t.Errorf("%d message calls were sent", got)
	}
	if got := len(rg.api.callsTo("/count_tokens")); got != 1 {
		t.Errorf("%d count_tokens calls, want 1", got)
	}
	if len(rg.ledger.snapshot()) != 0 {
		t.Error("a refused call wrote a ledger row")
	}
	// The default cap is $2: a 1.2 MB prompt on Opus counted at 600k tokens is 2.4M.
	rg = newRig(t, nil)
	rg.api.countTokens = func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: `{"input_tokens": 600000}`} }
	c, _ = rg.client(t, newWorkspaceID(), "editor")
	big := padded(simpleRequest(), 1_200_000)
	big.Model = "claude-opus-5-5"
	if _, err := c.Messages(context.Background(), big); !errors.Is(err, ErrCapExceeded) { // 2.4M + 20k
		t.Fatalf("default cap: %v", err)
	}
	if rg.api.totalCalls() != 1 {
		t.Errorf("%d API calls; only the count should have gone out", rg.api.totalCalls())
	}
}

func TestCacheBreakpointsRaiseTheEstimate(t *testing.T) {
	rg := newRig(t, map[string]string{"LLM_MAX_REQUEST_MICROS": "420000"})
	rg.api.countTokens = func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: `{"input_tokens": 80000}`} }
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	// A 170 KB prompt: its local bound (about 450k micros) is over the cap, so it is counted.
	req := padded(simpleRequest(), 170_000)
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
	rg.ledger.Seed(
		store.UsageEntry{WorkspaceID: ws, ListCostMicros: 4_000, CreatedAt: now.Add(-time.Hour), BilledTo: "test"},        // today
		store.UsageEntry{WorkspaceID: ws, ListCostMicros: 4_000, CreatedAt: now.Add(-13 * time.Hour), BilledTo: "test"},   // yesterday (UTC): not counted
		store.UsageEntry{WorkspaceID: other, ListCostMicros: 9_999_999, CreatedAt: now.Add(-time.Hour), BilledTo: "test"}, // another workspace: not counted
		store.UsageEntry{WorkspaceID: ws, ListCostMicros: 3_500, CreatedAt: now.Add(-time.Minute), BilledTo: "test"},      // today
	)
	c, _ := rg.client(t, ws, "editor")
	req := simpleRequest() // worst case 600 micros (its local bound a little more); 7500 spent today, so 2500 left
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatalf("under the cap: %v", err)
	}
	// That call cost 200 micros: 7700 spent. Raise max_tokens so the worst case no longer fits in the 2300 left. The
	// local bound does not fit either, so the call is counted, and the exact worst case is what the error reports.
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
	rg := newRig(t, map[string]string{"DAILY_SPEND_CAP_MICROS": "1000"}) // room for one worst case, not two
	ws := newWorkspaceID()
	ub := upperBoundEstimate(t, simpleRequest())
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

	// The first call is mid-flight and not in the ledger; its reservation is, so a second one (even at its exact worst
	// case of 600) does not fit next to it.
	_, err := c.Messages(context.Background(), simpleRequest())
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapDaily || ce.LimitMicros != 1000-ub {
		t.Fatalf("second concurrent call: %#v (want room %d)", err, 1000-ub)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Once the first is metered (200 micros), the reservation is gone and the next fits again.
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatalf("after the first finished: %v", err)
	}
	if got := len(rg.ledger.Holds()); got != 0 {
		t.Errorf("%d holds leaked", got)
	}
}

func TestDailyCapFailsClosedWhenTheLedgerCannotBeRead(t *testing.T) {
	rg := newRig(t, nil)
	rg.ledger.FailReserve = spendtest.ErrDown
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
	if !errors.Is(err, ErrUnpriced) || !errors.As(err, &ue) || ue.Model != "claude-mystery-9" || ue.TableVersion != "2026-10b" {
		t.Fatalf("err = %#v", err)
	}
	if rg.api.totalCalls() != 0 {
		t.Error("an unpriced model reached the network")
	}

	// LLM_ALLOW_UNPRICED=1: the call goes out, caps cannot be checked, and the row records cost 0 and says so. (The model must still be
	// described in the capabilities: an unknown one is refused before it is sent.)
	caps, err := DefaultCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	withMystery := Capabilities{"claude-mystery-9": {ThinkingOff: ThinkingOffOmit}}
	for k, v := range caps {
		withMystery[k] = v
	}
	rg = newRig(t, map[string]string{"LLM_ALLOW_UNPRICED": "1"}, func(o *FactoryOptions) { o.Caps = withMystery })
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
	rg.ledger.FailSettle = 100
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	resp, err := c.Messages(context.Background(), simpleRequest())
	var le *LedgerError
	if !errors.Is(err, ErrLedger) || !errors.As(err, &le) {
		t.Fatalf("err = %v", err)
	}
	if resp.Text() != "hello" || resp.Usage.OutputTokens != 200 {
		t.Errorf("the paid response was dropped: %+v", resp)
	}
	if rg.ledger.Settles != ledgerAttempts {
		t.Errorf("insert attempts = %d, want %d", rg.ledger.Settles, ledgerAttempts)
	}
	logs := rg.logs.String()
	if !strings.Contains(logs, "usage ledger write failed") || !strings.Contains(logs, "list_cost_micros=200") {
		t.Errorf("the lost row was not logged for reconciliation:\n%s", logs)
	}
	mustNotContain(t, "logs", logs, testKey)

	// A flaky ledger that recovers loses nothing.
	rg = newRig(t, nil)
	rg.ledger.FailSettle = 2
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
	ws := newWorkspaceID()
	c, _ := rg.client(t, ws, "editor")
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
	// The resolver learns whose request it is working for, so that it can refuse a reference of another workspace.
	if len(res.workspaces) == 0 {
		t.Fatal("the resolver was not called")
	}
	for _, got := range res.workspaces {
		if got != ws {
			t.Errorf("the resolver saw workspace %v, want %v", got, ws)
		}
	}
	// Both the token count and the message carry the bytes (the call above needed no count; ask for one).
	if _, err := c.CountTokens(context.Background(), req); err != nil {
		t.Fatal(err)
	}
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

type fakeResolver struct {
	data       map[string][]byte
	workspaces []ids.WorkspaceID
}

func (f *fakeResolver) ResolveImage(ctx context.Context, ref string) (string, []byte, error) {
	if ws, ok := WorkspaceFromContext(ctx); ok {
		f.workspaces = append(f.workspaces, ws)
	} else {
		f.workspaces = append(f.workspaces, ids.WorkspaceID{})
	}
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
