// Package spend is the spend gate of every paid action (P7-09a, docs/design/spend-control-v1.md section 6): it reserves the worst
// case of a call against the caps before the call is made, and settles the call's real cost into the ledger and the counters
// afterwards.
//
// The reservations live in Postgres (store.ReserveSpend), so the API and every worker see the same ones; a Gate adds the caps
// (from the settings), the clock, the typed errors and the spend.alert log lines on top. A call goes
//
//	hold, err := gate.Reserve(ctx, spend.Request{...})   // refused with a *CapError when a cap would be passed
//	... make the call ...
//	hold.Settle(ctx, rows...)                            // the ledger rows, the counters and the hold, in one transaction
//	hold.Release(ctx)                                    // instead, when nothing was billed
//
// A caller that dies between Reserve and Settle leaves a hold that stops counting at its expiry (the TTL of the request) and is
// deleted by the periodic sweep (jobs.SpendHoldsSweepArgs).
package spend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Backend is where the holds and counters live. *store.Store is the real one.
type Backend interface {
	ReserveSpend(ctx context.Context, p store.ReserveSpendParams) (store.SpendHold, []store.SpendAlert, error)
	SettleSpend(ctx context.Context, p store.SettleSpendParams) (int, []store.SpendAlert, error)
	ReleaseSpend(ctx context.Context, ws ids.WorkspaceID, key string) error
}

var _ Backend = (*store.Store)(nil)

// Hold lifetimes. A hold must outlive the call it covers, or the call's cost would be invisible to the caps while it runs; it must
// not outlive a crash by much, or a dead worker would keep blocking budget. Each call kind chooses its own (see the callers); these
// are the margin added to the call's own timeout and the lifetime when the caller does not know the call's duration.
const (
	// HoldMargin is added to a call's longest duration.
	HoldMargin = 2 * time.Minute
	// DefaultHoldTTL is the lifetime when the caller gives none.
	DefaultHoldTTL = 30 * time.Minute
	// MinHoldTTL is the shortest lifetime a hold gets, whatever the caller works out.
	MinHoldTTL = 5 * time.Minute
)

// Options configure a Gate.
type Options struct {
	// Caps are the limits: DAILY_SPEND_CAP_MICROS, PLATFORM_DAILY_SPEND_CAP_MICROS and TRANSCRIBE_DAILY_MINUTES (as milliseconds).
	Caps store.SpendCaps
	// Now replaces time.Now (tests).
	Now func() time.Time
	// Logger receives the spend.alert lines.
	Logger *slog.Logger
}

// Gate reserves and settles spend. It is safe for concurrent use and holds no per-call state.
type Gate struct {
	b    Backend
	caps store.SpendCaps
	now  func() time.Time
	log  *slog.Logger
}

// NewGate returns a gate over the backend.
func NewGate(b Backend, o Options) *Gate {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Gate{b: b, caps: o.Caps, now: o.Now, log: o.Logger}
}

// Caps returns the limits the gate checks.
func (g *Gate) Caps() store.SpendCaps { return g.caps }

// Request is one reservation.
type Request struct {
	Workspace ids.WorkspaceID
	// BilledTo is platform, workspace_key or test; Kind the ledger kind of the call (llm, transcription, ...).
	BilledTo string
	Kind     string
	// Micros is the call's worst-case cost in micro-dollars, AudioMs the audio about to be transcribed.
	Micros  int64
	AudioMs int64
	// Key identifies the call; empty makes a random one. A repeated reservation with the same key returns the same hold.
	Key string
	// TTL is how long the hold counts; 0 means DefaultHoldTTL.
	TTL time.Duration
}

// Hold is a reservation that was made. Settle or Release it exactly once; doing neither lets it expire.
type Hold struct {
	g *Gate
	h store.SpendHold
}

// Key is the hold's key, from which callers build their ledger rows' idempotency keys.
func (h *Hold) Key() string { return h.h.Key }

// Micros is the amount that was set aside.
func (h *Hold) Micros() int64 { return h.h.Micros }

// Reserve sets the worst case aside, or refuses with a *CapError (errors.Is ErrCapExceeded; for the breaker also ErrPlatformPaused).
// Any other error means the database could not be asked: the call must not go out then (fail closed).
func (g *Gate) Reserve(ctx context.Context, r Request) (*Hold, error) {
	if r.Key == "" {
		r.Key = "hold:" + uuid.NewString()
	}
	if r.TTL <= 0 {
		r.TTL = DefaultHoldTTL
	}
	r.TTL = max(r.TTL, MinHoldTTL)
	h, alerts, err := g.b.ReserveSpend(ctx, store.ReserveSpendParams{
		Key: r.Key, Workspace: r.Workspace, BilledTo: r.BilledTo, Kind: r.Kind, Micros: r.Micros, AudioMs: r.AudioMs,
		Caps: g.caps, Now: g.now(), TTL: r.TTL,
	})
	g.alert(ctx, alerts)
	if err != nil {
		var ce *store.SpendCapError
		if errors.As(err, &ce) {
			return nil, &CapError{Cap: ce.Cap, Requested: ce.Requested, Room: ce.Room}
		}
		return nil, fmt.Errorf("spend: reserving: %w", err)
	}
	return &Hold{g: g, h: h}, nil
}

// Settle writes the ledger rows, adds their cost to the counters and removes the hold, in one transaction. It is idempotent on the
// rows' idempotency keys, so a retry after an error that may have committed is safe. Pass a context that outlives the caller's
// cancellation: the call is already paid for.
func (h *Hold) Settle(ctx context.Context, rows ...store.UsageEntry) error {
	return h.g.settle(ctx, h.h.Workspace, h.h.Key, rows)
}

// Release gives the reservation back: nothing was billed.
func (h *Hold) Release(ctx context.Context) error {
	if h.h.ID == uuid.Nil {
		return nil // nothing was set aside
	}
	if err := h.g.b.ReleaseSpend(ctx, h.h.Workspace, h.h.Key); err != nil {
		return fmt.Errorf("spend: releasing: %w", err)
	}
	return nil
}

// Record writes ledger rows for a cost that had no hold (a render, whose CPU is known only afterwards) and adds them to the
// counters. The breaker sees the cost; a cost that is already incurred is never refused.
func (g *Gate) Record(ctx context.Context, ws ids.WorkspaceID, rows ...store.UsageEntry) error {
	return g.settle(ctx, ws, "", rows)
}

func (g *Gate) settle(ctx context.Context, ws ids.WorkspaceID, holdKey string, rows []store.UsageEntry) error {
	_, alerts, err := g.b.SettleSpend(ctx, store.SettleSpendParams{
		HoldKey: holdKey, Workspace: ws, Rows: rows, PlatformCapMicros: g.caps.PlatformDailyMicros, Now: g.now(),
	})
	g.alert(ctx, alerts)
	if err != nil {
		return fmt.Errorf("spend: settling: %w", err)
	}
	return nil
}

// alert logs each alert as a spend.alert line, the event P0-11 turns into an Axiom monitor.
func (g *Gate) alert(ctx context.Context, alerts []store.SpendAlert) {
	for _, a := range alerts {
		level := slog.LevelWarn
		msg := "the platform daily spend breaker passed 80 % of its cap"
		if a.Pct >= 100 {
			level = slog.LevelError
			msg = "the platform daily spend breaker tripped: managed paid calls are refused until the next UTC day"
		}
		g.log.Log(ctx, level, "spend.alert", "type", a.Type, "threshold_pct", a.Pct, "period", a.Period,
			"spent_micros", a.SpentMicros, "cap_micros", a.CapMicros, "detail", msg)
	}
}

// ---- errors -----------------------------------------------------------------------------------------------------

// ErrCapExceeded is wrapped by every *CapError.
var ErrCapExceeded = errors.New("spend: spend cap exceeded")

// ErrPlatformPaused is wrapped by the *CapError of the platform breaker: managed paid calls are refused for the rest of the UTC day.
var ErrPlatformPaused = errors.New("spend: the platform daily spend breaker is open")

// CapError is a reservation that was refused, before anything was sent, by a cap the database enforces.
type CapError struct {
	Cap store.SpendCap
	// Requested is the amount asked for (micro-dollars, or milliseconds for the minutes cap); Room what was left.
	Requested, Room int64
}

func (e *CapError) Error() string {
	return fmt.Sprintf("spend: refused before sending: the %s cap would be exceeded (asked %d, room %d)", e.Cap, e.Requested, e.Room)
}

// Is makes errors.Is(err, ErrCapExceeded) true, and errors.Is(err, ErrPlatformPaused) for the breaker.
func (e *CapError) Is(target error) bool {
	return target == ErrCapExceeded || (target == ErrPlatformPaused && e.Cap == store.CapPlatformDaily)
}

// The error codes a spend refusal maps to where it crosses the API.
const (
	// CodePlatformSpendPaused is the 503 body code of the breaker.
	CodePlatformSpendPaused = "platform_spend_paused"
	// CodeDailySpendCap is the 429 body code of a workspace's daily cap.
	CodeDailySpendCap = "daily_spend_cap"
)

// HTTPError maps a refusal to the status and code it crosses the API with: 503 platform_spend_paused for the breaker, 429
// daily_spend_cap for a workspace's own cap. ok is false for any other error.
func HTTPError(err error) (status int, code string, ok bool) {
	var ce *CapError
	if !errors.As(err, &ce) {
		return 0, "", false
	}
	if ce.Cap == store.CapPlatformDaily {
		return http.StatusServiceUnavailable, CodePlatformSpendPaused, true
	}
	return http.StatusTooManyRequests, CodeDailySpendCap, true
}
