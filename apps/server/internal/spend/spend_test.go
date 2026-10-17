package spend_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
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
	g, mem, _ := newGate(store.SpendCaps{WorkspaceDailyMicros: 10_000, PlatformDailyMicros: 10_000})
	w := ws()
	ctx := context.Background()
	h, err := g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 6_000})
	if err != nil || h.Micros() != 6_000 || len(mem.Holds()) != 1 {
		t.Fatalf("reserve: %v, holds %d", err, len(mem.Holds()))
	}
	// 6,000 are held: another 6,000 does not fit, and the refusal is typed.
	_, err = g.Reserve(ctx, spend.Request{Workspace: w, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 6_000})
	var ce *spend.CapError
	if !errors.As(err, &ce) || ce.Cap != store.CapPlatformDaily || ce.Room != 4_000 || !errors.Is(err, spend.ErrCapExceeded) || !errors.Is(err, spend.ErrPlatformPaused) {
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
	// 850 spent: past 80 %.
	if err := g.Record(ctx, w, store.UsageEntry{WorkspaceID: w, Kind: store.KindRender, Stage: "render", Model: "render-vcpu", Unit: store.UnitVCPUMs, Quantity: 1, ListCostMicros: 850, PriceTableVersion: "v", BilledTo: store.BilledToPlatform, IdempotencyKey: "render:1", CreatedAt: day}); err != nil {
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
