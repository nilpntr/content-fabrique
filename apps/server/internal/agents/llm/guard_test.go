package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// DailyGuard is the daily cap for spend that is not a model call (transcription): the same ledger, the same reservation of what
// is in flight, the same refusal, and it fails closed.
func TestDailyGuardReservesAgainstTheLedgerAndWhatIsInFlight(t *testing.T) {
	ledger := &fakeLedger{}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	g := NewDailyGuard(ledger, 10_000)
	g.now = func() time.Time { return now }
	ws, other := newWorkspaceID(), newWorkspaceID()
	ledger.rows = append(ledger.rows,
		store.UsageEntry{WorkspaceID: ws, ListCostMicros: 4_000, CreatedAt: now.Add(-time.Hour)},      // today, a model call
		store.UsageEntry{WorkspaceID: ws, ListCostMicros: 3_000, CreatedAt: now.Add(-13 * time.Hour)}, // yesterday: not counted
		store.UsageEntry{WorkspaceID: other, ListCostMicros: 9_999_999, CreatedAt: now.Add(-time.Hour)},
	)
	ctx := context.Background()

	r1, err := g.Reserve(ctx, ws, 3_000) // 4000 spent + 3000 reserved
	if err != nil {
		t.Fatal(err)
	}
	var ce *CapError
	if _, err := g.Reserve(ctx, ws, 3_500); !errors.As(err, &ce) || ce.Kind != CapDaily || ce.EstimateMicros != 3_500 || ce.LimitMicros != 3_000 || !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("a second call next to the first: %#v", err)
	}
	if len(g.res.m) != 1 {
		t.Errorf("a refused call left a reservation: %v", g.res.m)
	}
	r2, err := g.Reserve(ctx, ws, 3_000) // exactly fills the cap
	if err != nil {
		t.Fatalf("a call that fits exactly: %v", err)
	}
	r1()
	r2()
	if len(g.res.m) != 0 {
		t.Errorf("reservations leaked: %v", g.res.m)
	}

	// Another workspace has its own cap; the next UTC day starts clean.
	if _, err := g.Reserve(ctx, other, 5_000); !errors.As(err, &ce) {
		t.Errorf("workspace B is over its own cap: %v", err)
	}
	now = now.Add(12*time.Hour + time.Minute)
	rel, err := g.Reserve(ctx, ws, 9_000)
	if err != nil {
		t.Fatalf("next day: %v", err)
	}
	rel()
}

func TestDailyGuardFailsClosedWhenTheLedgerCannotBeRead(t *testing.T) {
	ledger := &fakeLedger{failSum: true}
	g := NewDailyGuard(ledger, 10_000)
	ws := newWorkspaceID()
	_, err := g.Reserve(context.Background(), ws, 1)
	var ce *CapError
	if err == nil || errors.As(err, &ce) {
		t.Fatalf("err = %v: an unreadable ledger is an error, not a cap", err)
	}
	if len(g.res.m) != 0 {
		t.Errorf("reservations leaked: %v", g.res.m)
	}
}
