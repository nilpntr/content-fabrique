package llm

import (
	"context"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
)

// DailyGuard is the workspace's daily spend cap (DAILY_SPEND_CAP_MICROS) for spend that is not a model call: transcription
// (P2-05). It is the algorithm the model client uses before every call (reserveDaily): the worst-case cost is set aside in
// process, today's ledger is read, and the call is refused when the two together would pass the cap. The ledger sums every kind
// of row, so a day of transcription and a day of editing draw on the same cap.
//
// A guard's in-flight reservations are its own: a model client's calls in flight are not seen by it, and the other way round.
// Both read the same ledger rows, so the overshoot is bounded by what is in flight at one moment.
type DailyGuard struct {
	ledger Ledger
	cap    int64
	now    func() time.Time
	res    reservations
}

// NewDailyGuard returns a guard over the ledger with the cap in micro-dollars.
func NewDailyGuard(ledger Ledger, capMicros int64) *DailyGuard {
	return &DailyGuard{ledger: ledger, cap: capMicros, now: time.Now}
}

// Reserve sets est (the call's worst-case cost in micro-dollars) aside. It returns a *CapError (errors.Is ErrCapExceeded) when the
// cap cannot cover it, and any other error when the ledger cannot be read (the call must not go out then). Call release once
// the call's ledger rows are written, and not before.
func (g *DailyGuard) Reserve(ctx context.Context, ws ids.WorkspaceID, est int64) (release func(), err error) {
	return reserveDaily(ctx, &g.res, g.ledger, ws, g.now(), g.cap, est)
}
