package spend_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/spend/spendtest"
	"github.com/nilpntr/content-fabrique/internal/store"
)

var day = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func newGate(caps store.SpendCaps) (*spend.Gate, *spendtest.Memory, *bytes.Buffer) {
	mem := &spendtest.Memory{}
	logs := &bytes.Buffer{}
	return spend.NewGate(mem, spend.Options{Caps: caps, Now: func() time.Time { return day }, Logger: slog.New(slog.NewTextHandler(logs, nil))}), mem, logs
}

func ws() ids.WorkspaceID { return ids.WorkspaceID{UUID: uuid.New()} }

func TestReserveSettleRelease(t *testing.T) {
	g, mem, _ := newGate(store.SpendCaps{WorkspaceDailyMicros: 100_000, PlatformDailyMicros: 10_000})
	w := ws()
	ctx := context.Background()
	h, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 6_000})
	if err != nil || h.Micros() != 6_000 || len(mem.Holds()) != 1 {
		t.Fatalf("reserve: %v, holds %d", err, len(mem.Holds()))
	}
	// 6,000 are held: another 6,000 does not fit, and the refusal is typed.
	_, err = g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 6_000})
	var ce *spend.CapError
	if !errors.As(err, &ce) || ce.Cap != store.CapPlatformBusy || ce.Room != 4_000 || !ce.HoldsInTheWay || !errors.Is(err, spend.ErrCapExceeded) || !errors.Is(err, spend.ErrSpendCapBusy) || errors.Is(err, spend.ErrPlatformPaused) {
		t.Fatalf("second reserve: %#v", err)
	}
	row := store.UsageEntry{WorkspaceID: w, Stage: "editor", Model: "m", ListCostMicros: 2_500, PriceTableVersion: "v", BilledTo: store.BilledToPlatform, IdempotencyKey: "llm:" + h.Key()}
	for range 2 {
		if err := h.Settle(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if len(mem.Holds()) != 0 || len(mem.Rows()) != 1 {
		t.Errorf("after settling twice: holds %d rows %d", len(mem.Holds()), len(mem.Rows()))
	}
	// 2,500 spent, nothing held: 7,500 fit, and releasing a hold gives it all back.
	h2, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 7_500})
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.Release(ctx); err != nil || len(mem.Holds()) != 0 {
		t.Fatalf("release: %v, holds %d", err, len(mem.Holds()))
	}
	// A free call and a call on the creator's key set nothing aside; releasing them is a no-op.
	for _, r := range []spend.Request{
		{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM},
		{Workspace: w, BilledTo: store.BilledToWorkspaceKey, Kind: store.KindLLM, Micros: 1 << 40},
	} {
		h, err := g.Reserve(ctx, r)
		if err != nil || len(mem.Holds()) != 0 || h.Release(ctx) != nil {
			t.Fatalf("%+v: %v", r, err)
		}
	}
}

func TestAlertsAreLoggedOnceAsSpendAlert(t *testing.T) {
	g, mem, logs := newGate(store.SpendCaps{WorkspaceDailyMicros: 1 << 40, PlatformDailyMicros: 1_000})
	ctx := context.Background()
	w := ws()
	// 950 spent of 1,000: past 80 %, and a small call (a tenth of the cap, 100) no longer fits: the breaker trips as the cost lands.
	if err := g.Record(ctx, w, store.UsageEntry{WorkspaceID: w, Kind: store.KindRender, Stage: "render", Model: "render-vcpu", Unit: store.UnitVCPUMs, Quantity: 1, ListCostMicros: 950, PriceTableVersion: "v", BilledTo: store.BilledToPlatform, IdempotencyKey: "render:1", CreatedAt: day}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 500}); !errors.Is(err, spend.ErrPlatformPaused) {
			t.Fatalf("reserve: %v", err)
		}
	}
	out := logs.String()
	if strings.Count(out, "spend.alert") != 2 || strings.Count(out, "threshold_pct=80") != 1 || strings.Count(out, "threshold_pct=100") != 1 {
		t.Errorf("alerts:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "level=ERROR") {
		t.Errorf("the 80 %% alert is a warning and the trip an error:\n%s", out)
	}
	_ = mem
}

func TestFailsClosedWhenTheDatabaseCannotBeAsked(t *testing.T) {
	g, mem, _ := newGate(store.SpendCaps{WorkspaceDailyMicros: 1000, PlatformDailyMicros: 1000})
	mem.FailReserve = spendtest.ErrDown
	_, err := g.Reserve(context.Background(), spend.Request{Workspace: ws(), BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 1})
	var ce *spend.CapError
	if err == nil || errors.As(err, &ce) || !errors.Is(err, spendtest.ErrDown) {
		t.Fatalf("err = %v: an unreachable database is an error, not a cap", err)
	}
	// And a cap that is not set refuses instead of passing.
	g, _, _ = newGate(store.SpendCaps{WorkspaceDailyMicros: 1000})
	if _, err := g.Reserve(context.Background(), spend.Request{Workspace: ws(), BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 1}); !errors.Is(err, store.ErrSpendCapNotSet) {
		t.Errorf("no platform cap: %v", err)
	}
}

func TestHTTPErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&spend.CapError{Cap: store.CapPlatformDaily}, http.StatusServiceUnavailable, "platform_spend_paused"},
		{&spend.CapError{Cap: store.CapWorkspaceDaily}, http.StatusTooManyRequests, "daily_spend_cap"},
		{&spend.CapError{Cap: store.CapWorkspaceAudio}, http.StatusTooManyRequests, "daily_spend_cap"},
	} {
		if status, code, ok := spend.HTTPError(tc.err); !ok || status != tc.status || code != tc.code {
			t.Errorf("%v: %d %s %v", tc.err, status, code, ok)
		}
	}
	if _, _, ok := spend.HTTPError(errors.New("boom")); ok {
		t.Error("an unrelated error was mapped")
	}
}

func TestComputeMeterRecordsOnceAndPricesFromTheTable(t *testing.T) {
	g, mem, _ := newGate(store.SpendCaps{WorkspaceDailyMicros: 1 << 40, PlatformDailyMicros: 1 << 40})
	w := ws()
	m := &spend.ComputeMeter{
		Gate: g, Cost: func(ms int64) (int64, error) { return (ms*20 + 999) / 1000, nil }, PriceVersion: "2026-10b", BilledTo: store.BilledToTest,
		VCPUs: map[string]float64{"ingest": 2}, Now: func() time.Time { return day },
	}
	if got := m.VCPUMsOfWall("ingest", 1500*time.Millisecond); got != 3000 {
		t.Errorf("1.5 s on 2 vCPUs = %d ms", got)
	}
	if got := m.VCPUMsOfWall("unknown", time.Second); got != 0 {
		t.Errorf("an unconfigured stage metered %d", got)
	}
	project, member, action := ids.ProjectID{UUID: uuid.New()}, ids.UserID{UUID: uuid.New()}, uuid.New()
	u := spend.ComputeUse{Workspace: w, Kind: store.KindMedia, Stage: "ingest", VCPUMs: 3000, Key: "media:ingest:1:1", Project: project, Member: member, Action: action}
	m.Record(context.Background(), u)
	m.Record(context.Background(), u) // a retried write
	rows := mem.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Kind != store.KindMedia || r.Unit != store.UnitVCPUMs || r.Quantity != 3000 || r.ListCostMicros != 60 || r.PriceTableVersion != "2026-10b" ||
		r.ProjectID != project || r.MemberUserID != member || r.ActionID != action || r.BilledTo != store.BilledToTest {
		t.Errorf("row = %+v", r)
	}
	// A nil meter and an empty use record nothing and do not panic.
	var none *spend.ComputeMeter
	none.Record(context.Background(), u)
	m.Record(context.Background(), spend.ComputeUse{Workspace: w, Kind: store.KindMedia, Stage: "ingest", Key: "media:ingest:2:1"})
	if len(mem.Rows()) != 1 {
		t.Errorf("rows = %d", len(mem.Rows()))
	}
	// Compute without a price is recorded at cost 0 and says "unpriced", not lost.
	m.Cost = func(int64) (int64, error) { return 0, errors.New("no price") }
	m.Record(context.Background(), spend.ComputeUse{Workspace: w, Kind: store.KindMedia, Stage: "ingest", VCPUMs: 10, Key: "media:ingest:3:1"})
	if rows := mem.Rows(); len(rows) != 2 || rows[1].PriceTableVersion != "unpriced" || rows[1].ListCostMicros != 0 {
		t.Errorf("rows = %+v", rows)
	}
}

// A hold is short and a heartbeat extends it while its call is in flight: it keeps counting however long the call lives, and a caller
// that dies (stops beating) stops blocking the caps one TTL after its last beat.
func TestKeepAliveOutlivesLongCallsAndACrashedCallersHoldExpires(t *testing.T) {
	clk := &spendtest.Clock{T: day}
	mem := &spendtest.Memory{}
	g := spend.NewGate(mem, spend.Options{
		Caps: store.SpendCaps{WorkspaceDailyMicros: 1_000, PlatformDailyMicros: 1_000}, Now: clk.Now,
		HoldTTL: 5 * time.Minute, Heartbeat: 5 * time.Millisecond,
	})
	w := ws()
	ctx := context.Background()
	full := spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 1_000}
	blocked := func() bool {
		_, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 1, Key: "probe-" + time.Now().String()})
		return errors.Is(err, spend.ErrCapExceeded)
	}

	// A caller that heartbeats: 40 minutes pass in 4-minute steps and its hold still counts.
	h, err := g.Reserve(ctx, full)
	if err != nil {
		t.Fatal(err)
	}
	stop := h.KeepAlive(ctx)
	for range 10 {
		clk.Advance(4 * time.Minute)
		deadline := time.Now().Add(5 * time.Second)
		for len(mem.Holds()) == 1 && !mem.Holds()[0].ExpiresAt.After(clk.Now().Add(3*time.Minute)) {
			if time.Now().After(deadline) {
				t.Fatalf("no heartbeat reached the hold at %v: %+v", clk.Now(), mem.Holds())
			}
			time.Sleep(time.Millisecond)
		}
		if !blocked() {
			t.Fatalf("a hold of a live call stopped counting at %v", clk.Now())
		}
	}
	stop()
	// The caller crashes: it never settles and never beats again. The hold counts until its TTL after the last beat, then it does not.
	clk.Advance(2 * time.Minute)
	if !blocked() {
		t.Fatal("the hold lapsed before its TTL")
	}
	clk.Advance(4 * time.Minute)
	if blocked() {
		t.Fatal("a crashed caller's hold still blocks the caps after its TTL")
	}

	// A failing heartbeat is logged and does not stop the loop; stopping twice and a hold with nothing set aside are fine.
	mem.FailExtend = spendtest.ErrDown
	h2, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 10})
	if err != nil {
		t.Fatal(err)
	}
	stop2 := h2.KeepAlive(ctx)
	time.Sleep(30 * time.Millisecond)
	stop2()
	stop2()
	free, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM})
	if err != nil {
		t.Fatal(err)
	}
	free.KeepAlive(ctx)()
}

// A beat that cannot get through for a whole TTL means the hold has lapsed while its call is still in flight: that is logged as an
// error, once per outage, and a beat that works again resets it.
func TestKeepAliveSaysSoWhenTheHoldHasLapsed(t *testing.T) {
	clk := &spendtest.Clock{T: day}
	mem := &spendtest.Memory{}
	logs := &syncBuf{}
	g := spend.NewGate(mem, spend.Options{
		Caps: store.SpendCaps{WorkspaceDailyMicros: 1_000, PlatformDailyMicros: 1_000}, Now: clk.Now, Logger: slog.New(slog.NewTextHandler(logs, nil)),
		HoldTTL: 5 * time.Minute, Heartbeat: 5 * time.Millisecond,
	})
	h, err := g.Reserve(context.Background(), spend.Request{Workspace: ws(), BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 100})
	if err != nil {
		t.Fatal(err)
	}
	mem.FailExtend = spendtest.ErrDown
	stop := h.KeepAlive(context.Background())
	time.Sleep(30 * time.Millisecond)
	if strings.Contains(logs.String(), "lapsed") {
		t.Fatal("escalated before a whole TTL had passed")
	}
	clk.Advance(6 * time.Minute)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(logs.String(), "lapsed") == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	stop()
	if n := strings.Count(logs.String(), "lapsed"); n != 1 || !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("escalation logged %d times (want once, as an error):\n%s", n, logs.String())
	}
}

// A hold whose ledger row could not be written is kept to the end of the UTC day.
func TestRetainKeepsAnUnrecordedCallsHoldToTheEndOfTheDay(t *testing.T) {
	clk := &spendtest.Clock{T: day}
	mem := &spendtest.Memory{}
	g := spend.NewGate(mem, spend.Options{Caps: store.SpendCaps{WorkspaceDailyMicros: 1_000, PlatformDailyMicros: 1_000}, Now: clk.Now})
	h, err := g.Reserve(context.Background(), spend.Request{Workspace: ws(), BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Retain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := mem.Holds()[0].ExpiresAt, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("expires at %v, want %v", got, want)
	}
}

// syncBuf is a log sink the heartbeat's goroutine and the test can both touch.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// Crossing midnight with the fake clock: a hold made on day D and retained after midnight counts on D+1.
func TestRetainAcrossMidnightCountsOnTheNewDay(t *testing.T) {
	clk := &spendtest.Clock{T: time.Date(2026, 10, 17, 23, 59, 0, 0, time.UTC)}
	mem := &spendtest.Memory{}
	g := spend.NewGate(mem, spend.Options{Caps: store.SpendCaps{WorkspaceDailyMicros: 1_000, PlatformDailyMicros: 1_000}, Now: clk.Now})
	ctx := context.Background()
	w := ws()
	h, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute) // 00:01 on the 18th; the call's ledger write fails now
	if err := h.Retain(ctx); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	if _, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 1}); !errors.Is(err, spend.ErrCapExceeded) {
		t.Fatalf("the unrecorded cost is not counted on the day it was found: %v", err)
	}
	clk.Advance(23 * time.Hour) // the 19th
	if _, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 1}); err != nil {
		t.Fatalf("the retained hold outlived its day: %v", err)
	}
}
