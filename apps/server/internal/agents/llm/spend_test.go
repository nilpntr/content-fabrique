package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// A hold must outlive the call it covers and not much longer: the request's timeout, the retry wait and a margin, but not past the
// run's wall clock (the context's deadline) plus the margin.
func TestHoldTTLFollowsTheCall(t *testing.T) {
	req := simpleRequest()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	long := holdTTL(context.Background(), req, now)
	want := requestTimeout(req.Model, req.MaxTokens) + defaultMaxTotalWait + callMargin
	if long != want || long < requestTimeout(req.Model, req.MaxTokens) {
		t.Errorf("ttl = %v, want %v (and at least the request timeout)", long, want)
	}
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(3*time.Minute))
	defer cancel()
	if got := holdTTL(ctx, req, now); got != 3*time.Minute+callMargin {
		t.Errorf("with a 3-minute wall clock: %v", got)
	}
	ctx2, cancel2 := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel2()
	if got := holdTTL(ctx2, req, now); got != spend.MinHoldTTL {
		t.Errorf("a nearly expired context still gets the minimum: %v", got)
	}
}
