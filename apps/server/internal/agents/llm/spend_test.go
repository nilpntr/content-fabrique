package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The platform breaker (PLATFORM_DAILY_SPEND_CAP_MICROS) refuses managed paid calls once the day's platform-billed cost, held calls
// included, would pass it: before anything is sent, with a typed error, and with its alerts logged once per crossing.
func TestPlatformBreakerRefusesManagedCallsAndAlertsOnce(t *testing.T) {
	rg := newRig(t, map[string]string{"CF_ENV": "staging", "PLATFORM_DAILY_SPEND_CAP_MICROS": "1500", "DAILY_SPEND_CAP_MICROS": "100000000"})
	ws, other := newWorkspaceID(), newWorkspaceID()
	// Another workspace spent 1,300 of the 1,500 today: past 80 %, and no room for a 600 worst case.
	rg.ledger.Seed(store.UsageEntry{WorkspaceID: other, ListCostMicros: 1_300, BilledTo: "platform", CreatedAt: rg.now.Add(-time.Hour)})
	c, billing := rg.client(t, ws, "editor")
	if billing != BilledToPlatform {
		t.Fatalf("billing = %s", billing)
	}
	for i := range 3 {
		_, err := c.Messages(context.Background(), simpleRequest())
		var ce *CapError
		if !errors.As(err, &ce) || ce.Kind != CapPlatform || ce.LimitMicros != 200 || !errors.Is(err, ErrCapExceeded) || !errors.Is(err, spend.ErrPlatformPaused) {
			t.Fatalf("call %d: %#v", i, err)
		}
	}
	if got := len(rg.api.messageCalls()); got != 0 {
		t.Errorf("%d requests were sent while the breaker was open", got)
	}
	if len(rg.ledger.Holds()) != 0 || len(rg.ledger.Rows()) != 1 {
		t.Errorf("a refused call left holds %d or rows %d", len(rg.ledger.Holds()), len(rg.ledger.Rows()))
	}
	logs := rg.logs.String()
	if n := strings.Count(logs, "threshold_pct=80"); n != 1 {
		t.Errorf("the 80 %% alert was logged %d times, want once:\n%s", n, logs)
	}
	if n := strings.Count(logs, "threshold_pct=100"); n != 1 {
		t.Errorf("the 100 %% alert was logged %d times, want once", n)
	}
	if !strings.Contains(logs, "spend.alert") || !strings.Contains(logs, "type=platform_breaker") || !strings.Contains(logs, "cap_micros=1500") {
		t.Errorf("the alert line is missing its fields:\n%s", logs)
	}
	_, err := c.Messages(context.Background(), simpleRequest())
	if status, code, ok := spend.HTTPError(err); !ok || status != 503 || code != "platform_spend_paused" {
		t.Errorf("the breaker must cross the API as 503 platform_spend_paused: %d %s %v", status, code, ok)
	}
}

// Test and workspace-key traffic never counts against the breaker: a developer's local calls keep working however low it is set.
func TestPlatformBreakerIgnoresTestTraffic(t *testing.T) {
	rg := newRig(t, map[string]string{"PLATFORM_DAILY_SPEND_CAP_MICROS": "1"}) // CF_ENV=local: billed_to test
	c, billing := rg.client(t, newWorkspaceID(), "editor")
	if billing != BilledToTest {
		t.Fatalf("billing = %s", billing)
	}
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatalf("a test-billed call was stopped by the platform breaker: %v", err)
	}
	// It still counts against the workspace's own daily cap, as it always did (what a developer relies on locally).
	rg = newRig(t, map[string]string{"DAILY_SPEND_CAP_MICROS": "100"})
	c, _ = rg.client(t, newWorkspaceID(), "editor")
	var ce *CapError
	if _, err := c.Messages(context.Background(), simpleRequest()); !errors.As(err, &ce) || ce.Kind != CapDaily {
		t.Fatalf("the workspace's daily cap does not apply to test traffic: %v", err)
	}
}

// A call that settles takes its hold with it; one the API refused gives it back; one whose ledger row cannot be written keeps it until
// it expires, so the caps stay conservative.
func TestHoldFollowsTheCall(t *testing.T) {
	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	if len(rg.ledger.Holds()) != 0 || rg.ledger.Reserves != 1 || rg.ledger.Releases != 0 {
		t.Errorf("settled call: holds %d reserves %d releases %d", len(rg.ledger.Holds()), rg.ledger.Reserves, rg.ledger.Releases)
	}

	rg = newRig(t, nil)
	rg.api.messages = func(int, recordedCall) fakeReply { return errReply(400, "invalid_request_error", "bad", nil) }
	c, _ = rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err == nil {
		t.Fatal("expected the error")
	}
	if len(rg.ledger.Holds()) != 0 || rg.ledger.Releases != 1 {
		t.Errorf("refused call: holds %d releases %d, want the hold given back", len(rg.ledger.Holds()), rg.ledger.Releases)
	}

	rg = newRig(t, nil)
	rg.ledger.FailSettle = 100
	c, _ = rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); !errors.Is(err, ErrLedger) {
		t.Fatalf("err = %v", err)
	}
	if len(rg.ledger.Holds()) != 1 || rg.ledger.Releases != 0 {
		t.Errorf("unrecorded call: holds %d releases %d, want the hold kept until it expires", len(rg.ledger.Holds()), rg.ledger.Releases)
	}
}

// A settle that wrote its row and then failed (the answer was lost) is retried with the same key: one row, not two.
func TestSettleRetryAfterALostAnswerCountsOnce(t *testing.T) {
	rg := newRig(t, nil)
	rg.ledger.FailSettleAfterWrite = 1
	ws := newWorkspaceID()
	c, _ := rg.client(t, ws, "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	if rows := rg.ledger.Rows(); len(rows) != 1 || rg.ledger.Settles != 2 {
		t.Fatalf("rows %d after %d settles, want 1 after 2", len(rows), rg.ledger.Settles)
	}
	if got := rg.ledger.Counter(ws, store.DayPeriod(*rg.now)).TestMicros; got != 200 {
		t.Errorf("counter = %d, want 200 (counted once)", got)
	}
}

// Fail closed (D-31): an unpriced paid provider never runs outside development and ci, whatever is asked.
func TestUnpricedFailsClosedOutsideDevelopmentAndCI(t *testing.T) {
	for _, env := range []string{"staging", "production", "bench"} {
		if _, err := LoadSettings(func(k string) string {
			return map[string]string{"CF_ENV": env, "LLM_ALLOW_UNPRICED": "1"}[k]
		}); err == nil || !strings.Contains(err.Error(), "LLM_ALLOW_UNPRICED") {
			t.Errorf("%s: LoadSettings accepted LLM_ALLOW_UNPRICED=1: %v", env, err)
		}
		// Even settings built by hand are refused by the factory.
		prices, _ := LoadPriceTable(pricingFS, PriceFile)
		_, err := NewFactory(FactoryOptions{Settings: Settings{Env: env, Mode: ModeLive, AllowUnpriced: true, DailySpendCapMicros: 1, PlatformDailySpendCapMicros: 1, MaxRequestMicros: 1, Concurrency: 1},
			Getenv: env2(nil), Prices: prices, Ledger: &fakeLedger{}})
		if err == nil {
			t.Errorf("%s: NewFactory accepted AllowUnpriced", env)
		}
	}
	for _, env := range []string{"local", "ci"} {
		if _, err := LoadSettings(func(k string) string {
			return map[string]string{"CF_ENV": env, "LLM_ALLOW_UNPRICED": "1"}[k]
		}); err != nil {
			t.Errorf("%s: %v", env, err)
		}
	}
	// An unpriced model on staging is refused before anything is reserved or sent.
	rg := newRig(t, map[string]string{"CF_ENV": "staging"})
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	req.Model = "claude-mystery-9"
	var ue *UnpricedError
	if _, err := c.Messages(context.Background(), req); !errors.As(err, &ue) || !errors.Is(err, ErrUnpriced) {
		t.Fatalf("err = %v", err)
	}
	if len(rg.api.messageCalls()) != 0 || rg.ledger.Reserves != 0 {
		t.Errorf("an unpriced call reached the API (%d) or the holds (%d)", len(rg.api.messageCalls()), rg.ledger.Reserves)
	}
}

func env2(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// fakeClock is a clock the test moves; the spend gate and the heartbeat read it from other goroutines.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// A call can be queued for its key's slot, cooled down by a rate limit and retried for far longer than any fixed guess of its
// duration. Its hold is short and a heartbeat extends it while the call is in flight, so it keeps counting against the caps for as long
// as the call lives, and is gone once the call is over.
func TestHoldOutlivesALongQueueWait(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	rg := newRig(t, nil, func(o *FactoryOptions) { o.now, o.heartbeat = clock.Now, 5*time.Millisecond })
	started, release := make(chan struct{}), make(chan struct{})
	rg.api.messages = func(n int, _ recordedCall) fakeReply {
		if n == 1 {
			close(started)
			<-release
		}
		return okReply("ok", defaultUsage)
	}
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	done := make(chan error, 1)
	go func() { _, err := c.Messages(context.Background(), simpleRequest()); done <- err }()
	<-started
	if len(rg.ledger.Holds()) != 1 {
		t.Fatalf("holds = %d", len(rg.ledger.Holds()))
	}
	// 24 minutes pass with the call still in flight (a hold with a fixed lifetime of its estimate would have lapsed after the first
	// 17): after every 4-minute step the heartbeat has pushed the expiry past the new time.
	for range 6 {
		clock.advance(4 * time.Minute)
		deadline := time.Now().Add(5 * time.Second)
		for {
			h := rg.ledger.Holds()
			if len(h) == 1 && h[0].ExpiresAt.After(clock.Now().Add(3*time.Minute)) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the hold lapsed while the call was in flight: %+v at %v", h, clock.Now())
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	if rg.ledger.Extends == 0 {
		t.Error("nothing extended the hold")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The call is over: the hold went with its settle, and the heartbeat stopped.
	if len(rg.ledger.Holds()) != 0 {
		t.Errorf("holds left: %+v", rg.ledger.Holds())
	}
	n := rg.ledger.Extends
	time.Sleep(30 * time.Millisecond)
	if rg.ledger.Extends != n {
		t.Error("the heartbeat outlived the call")
	}
}

// A call whose ledger row could not be written keeps its hold, but no longer extends it: it expires on its own.
func TestUnrecordedCallsHoldStopsBeingExtended(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	rg := newRig(t, nil, func(o *FactoryOptions) { o.now, o.heartbeat = clock.Now, 5*time.Millisecond })
	rg.ledger.FailSettle = 100
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); !errors.Is(err, ErrLedger) {
		t.Fatalf("err = %v", err)
	}
	h := rg.ledger.Holds()
	if len(h) != 1 {
		t.Fatalf("holds = %+v", h)
	}
	expires := h[0].ExpiresAt
	n := rg.ledger.Extends
	clock.advance(time.Hour)
	time.Sleep(30 * time.Millisecond)
	if rg.ledger.Extends != n || !rg.ledger.Holds()[0].ExpiresAt.Equal(expires) {
		t.Error("an abandoned hold was still being extended")
	}
}

// A request that does not fit next to the live holds while the committed spend still leaves room is busy, not a trip: it is refused
// with a retryable error, sets no trip flag and raises no 100 % alert. An agent run (WaitOnBusy) waits for the holds to settle and
// goes ahead, bounded by its context.
func TestBreakerBusyIsRetryableAndNotATrip(t *testing.T) {
	vars := map[string]string{"CF_ENV": "staging", "PLATFORM_DAILY_SPEND_CAP_MICROS": "100000", "PLATFORM_SPEND_SMALL_CALL_MICROS": "100", "DAILY_SPEND_CAP_MICROS": "100000000"}
	setup := func(t *testing.T) (*rig, Client, ids.WorkspaceID) {
		rg := newRig(t, vars)
		ws := newWorkspaceID()
		// Another caller holds nearly all the room: 99,800 of 100,000, with nothing spent yet.
		if _, _, err := rg.ledger.ReserveSpend(context.Background(), store.ReserveSpendParams{
			Key: "other", Workspace: newWorkspaceID(), BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 99_800,
			Caps: store.SpendCaps{WorkspaceDailyMicros: 1 << 40, PlatformDailyMicros: 100_000, PlatformSmallCallMicros: 100}, Now: *rg.now, TTL: time.Hour,
		}); err != nil {
			t.Fatal(err)
		}
		c, _ := rg.client(t, ws, "editor")
		return rg, c, ws
	}
	old := busyBackoff
	busyBackoff = func(int) time.Duration { return 5 * time.Millisecond }
	t.Cleanup(func() { busyBackoff = old })

	t.Run("refused at once without waiting", func(t *testing.T) {
		rg, c, _ := setup(t)
		_, err := c.Messages(context.Background(), simpleRequest())
		var ce *CapError
		if !errors.As(err, &ce) || ce.Kind != CapPlatformBusy || !errors.Is(err, spend.ErrSpendCapBusy) || errors.Is(err, spend.ErrPlatformPaused) {
			t.Fatalf("err = %#v", err)
		}
		if status, code, ok := spend.HTTPError(err); !ok || status != 503 || code != "spend_cap_busy" {
			t.Errorf("crosses the API as %d %s", status, code)
		}
		if strings.Contains(rg.logs.String(), "threshold_pct=100") || len(rg.api.messageCalls()) != 0 {
			t.Errorf("a busy refusal tripped the breaker or sent a request:\n%s", rg.logs.String())
		}
	})

	t.Run("a run waits for the holds to settle", func(t *testing.T) {
		rg, c, _ := setup(t)
		// The other caller settles after a moment: its hold goes.
		go func() {
			time.Sleep(60 * time.Millisecond)
			rg.ledger.ReleaseAll()
		}()
		req := simpleRequest()
		req.Run = RunInfo{WaitOnBusy: true}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := c.Messages(ctx, req); err != nil {
			t.Fatalf("the run did not get through once the room came back: %v", err)
		}
		if len(rg.api.messageCalls()) != 1 {
			t.Errorf("message calls = %d", len(rg.api.messageCalls()))
		}
	})

	t.Run("the wait is bounded by the context", func(t *testing.T) {
		_, c, _ := setup(t)
		req := simpleRequest()
		req.Run = RunInfo{WaitOnBusy: true}
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		_, err := c.Messages(ctx, req)
		var ce *CapError
		if !errors.As(err, &ce) || ce.Kind != CapPlatformBusy {
			t.Fatalf("err = %#v, want the busy refusal when the wall clock ends the wait", err)
		}
	})
}
