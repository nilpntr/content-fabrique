package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Spend holds and counters (P7-09a, docs/design/spend-control-v1.md sections 6 and 10).
//
// A paid call reserves its worst case here before it is sent (ReserveSpend), and settles after it (SettleSpend: the ledger
// rows, the counters and the removal of the hold in one transaction) or gives the reservation back (ReleaseSpend). The
// reservations live in the database, so every API and worker process sees the same ones; the counters are updated in the
// transaction that writes the ledger rows, so a cap check never sums the ledger.
//
// Lock order. Every transaction that changes a counter takes the platform row of the period first and the workspace's row
// second, so two of them can never wait on each other. ReserveSpend locks the rows FOR UPDATE and checks
// "counter + live holds + estimate <= cap" for each cap that applies; SettleSpend takes the same locks through its upserts.

// The usage_ledger kinds.
const (
	KindLLM     = "llm"
	KindRender  = "render"
	KindMedia   = "media"
	KindStorage = "storage"
	KindEgress  = "egress"
)

// The usage_ledger units: the smallest whole unit of each quantity.
const (
	UnitTokens   = "tokens"
	UnitAudioMs  = "audio_ms"
	UnitVCPUMs   = "vcpu_ms"
	UnitByteDays = "byte_days"
	UnitBytes    = "bytes"
)

// Holds are made for a workspace (spend_holds.scope = 'workspace'). The column also allows a platform-scope hold, with no workspace, for
// spend that only the platform breaker should see; nothing makes one yet, because every ledger row belongs to a workspace.
const spendScopeWorkspace = "workspace"

// DayPeriod is the counter period of the UTC day containing t: "day:2026-10-17". Billing periods ("bill:...") are for later.
func DayPeriod(t time.Time) string { return "day:" + t.UTC().Format("2006-01-02") }

// SpendCap names the cap that refused a reservation.
type SpendCap string

// The caps the database enforces.
const (
	// CapWorkspaceDaily is DAILY_SPEND_CAP_MICROS: the workspace's platform- and test-billed cost per UTC day.
	CapWorkspaceDaily SpendCap = "workspace_daily"
	// CapPlatformDaily is PLATFORM_DAILY_SPEND_CAP_MICROS, the breaker: all platform-billed cost per UTC day.
	CapPlatformDaily SpendCap = "platform_daily"
	// CapWorkspaceAudio is TRANSCRIBE_DAILY_MINUTES: the audio a workspace may have transcribed per UTC day.
	CapWorkspaceAudio SpendCap = "workspace_audio"
	// CapPlatformBusy is a refusal by the breaker's room that is not a trip: this request does not fit next to the live holds (or is
	// large), while the committed spend still leaves room for a normal small call. It is retryable: the holds settle or expire, or a
	// smaller request fits. It never sets the trip flag and never raises the 100 % alert.
	CapPlatformBusy SpendCap = "platform_busy"
)

// DefaultPlatformSmallCallMicros is $0.10: the size of "a normal small call" in the breaker's definition of a trip.
const DefaultPlatformSmallCallMicros int64 = 100_000

// BreakerTripped is the breaker's definition of a real crossing: the committed (settled) platform spend plus a small normal call no
// longer fits under the cap. Live holds do not count, and neither does the size of one request, so a burst of in-flight calls or a
// single large estimate near the cap is "busy", not "tripped". small <= 0 means DefaultPlatformSmallCallMicros; it is at most a tenth
// of the cap, so a small cap is not "tripped" from the first call. Calls smaller than that still fit after a trip; "tripped" means no
// normal-sized call does.
func BreakerTripped(committed, limit, small int64) bool {
	if small <= 0 {
		small = DefaultPlatformSmallCallMicros
	}
	small = max(min(small, limit/10), 1)
	return committed+small > limit
}

// ErrSpendCapExceeded is wrapped by every *SpendCapError.
var ErrSpendCapExceeded = errors.New("store: spend cap exceeded")

// ErrSpendCapNotSet is returned for a reservation that counts against a money cap that has no positive limit: the call must
// not go out on a cap nobody chose.
var ErrSpendCapNotSet = errors.New("store: a spend cap that applies to this reservation is not set")

// SpendCapError says which cap refused a reservation.
type SpendCapError struct {
	Cap SpendCap
	// Requested is what the reservation asked for (micro-dollars, or milliseconds of audio for CapWorkspaceAudio).
	Requested int64
	// Room is what was left under the cap, counting the live holds.
	Room int64
	// HoldsInTheWay is set on a CapPlatformBusy refusal that waiting can cure: the request fits next to the settled spend, and only
	// live holds stand in its way. A busy refusal without it is a request that is too large for the room left; waiting will not help.
	HoldsInTheWay bool
}

func (e *SpendCapError) Error() string {
	return fmt.Sprintf("store: refused: the %s cap would be exceeded (asked %d, room %d)", e.Cap, e.Requested, e.Room)
}

// Is makes errors.Is(err, ErrSpendCapExceeded) true.
func (e *SpendCapError) Is(target error) bool { return target == ErrSpendCapExceeded }

// SpendCaps are the limits a reservation is checked against, in micro-dollars and milliseconds of audio.
type SpendCaps struct {
	// WorkspaceDailyMicros applies to platform- and test-billed reservations. Must be positive when one is made.
	WorkspaceDailyMicros int64
	// PlatformDailyMicros applies to platform-billed reservations (the breaker). Must be positive when one is made.
	PlatformDailyMicros int64
	// WorkspaceDailyAudioMs applies to reservations that carry audio; 0 means no limit.
	WorkspaceDailyAudioMs int64
	// PlatformSmallCallMicros is the size of a normal small call in the breaker's trip rule (BreakerTripped); 0 means
	// DefaultPlatformSmallCallMicros.
	PlatformSmallCallMicros int64
}

// SpendAlert is something worth a log line (type spend.alert) that a reservation or a settle found: the platform breaker passed
// 80 % of its cap, or tripped. Each is returned once per period, to whichever caller saw the crossing.
type SpendAlert struct {
	// Type is "platform_breaker".
	Type string
	// Pct is 80 (warning) or 100 (tripped: the committed spend plus a small call no longer fits under the cap).
	Pct         int
	Period      string
	SpentMicros int64
	CapMicros   int64
}

// The alert type.
const AlertPlatformBreaker = "platform_breaker"

// ReserveSpendParams is one reservation.
type ReserveSpendParams struct {
	// Key identifies the call. A second reservation with the same key returns the first one's hold and sets nothing more aside.
	Key       string
	Workspace ids.WorkspaceID
	// BilledTo is platform, workspace_key or test. A workspace_key reservation counts against no money cap.
	BilledTo string
	Kind     string
	// Micros is the worst-case cost; AudioMs the audio about to be transcribed (0 when none).
	Micros  int64
	AudioMs int64
	Caps    SpendCaps
	Now     time.Time
	// TTL is how long the hold counts: past it a crashed caller stops blocking the caps.
	TTL time.Duration
}

// SpendHold is a reservation that was made. A zero ID means nothing was set aside (the call is free, or no cap applies).
type SpendHold struct {
	ID        uuid.UUID
	Key       string
	Workspace ids.WorkspaceID
	BilledTo  string
	Kind      string
	Period    string
	Micros    int64
	AudioMs   int64
	ExpiresAt time.Time
}

func validKind(k string) bool {
	switch k {
	case KindLLM, KindTranscription, KindRender, KindMedia, KindStorage, KindEgress:
		return true
	}
	return false
}

func validBilledTo(b string) bool {
	switch b {
	case BilledToPlatform, BilledToWorkspaceKey, BilledToTest:
		return true
	}
	return false
}

func validKey(k string) bool { return len(k) >= 1 && len(k) <= 200 }

// ReserveSpend sets the worst case of a call aside, in one transaction that locks the counters in the fixed order, or refuses
// with a *SpendCapError naming the cap. Nothing is set aside on an error.
func (s *Store) ReserveSpend(ctx context.Context, p ReserveSpendParams) (SpendHold, []SpendAlert, error) {
	switch {
	case !validKey(p.Key):
		return SpendHold{}, nil, fmt.Errorf("%w: a spend hold needs a key of 1 to 200 characters", ErrInvalid)
	case p.Workspace.IsZero():
		return SpendHold{}, nil, fmt.Errorf("%w: a spend hold needs a workspace", ErrInvalid)
	case !validBilledTo(p.BilledTo), !validKind(p.Kind):
		return SpendHold{}, nil, fmt.Errorf("%w: billed_to %q or kind %q", ErrInvalid, p.BilledTo, p.Kind)
	case p.Micros < 0 || p.AudioMs < 0 || p.TTL <= 0 || p.Now.IsZero():
		return SpendHold{}, nil, fmt.Errorf("%w: a hold needs non-negative amounts, a time and a TTL", ErrInvalid)
	}
	now := p.Now.UTC()
	period := DayPeriod(now)
	hold := SpendHold{Key: p.Key, Workspace: p.Workspace, BilledTo: p.BilledTo, Kind: p.Kind, Period: period, Micros: p.Micros, AudioMs: p.AudioMs, ExpiresAt: now.Add(p.TTL)}

	// What counts against which cap.
	platformMicros := p.BilledTo == BilledToPlatform && p.Micros > 0
	workspaceMicros := p.BilledTo != BilledToWorkspaceKey && p.Micros > 0
	workspaceAudio := p.AudioMs > 0
	if !platformMicros && !workspaceMicros && !workspaceAudio {
		return hold, nil, nil // free, or paid by the creator's own key: nothing to set aside
	}
	if platformMicros && p.Caps.PlatformDailyMicros <= 0 || workspaceMicros && p.Caps.WorkspaceDailyMicros <= 0 {
		return SpendHold{}, nil, ErrSpendCapNotSet
	}

	var (
		refusal *SpendCapError
		alerts  []SpendAlert
	)
	err := s.InTx(ctx, func(tx *Store) error {
		q := tx.q
		// A repeated reservation (a retried step with the same key) finds its hold; a stale one is replaced.
		if old, err := q.GetSpendHoldByKey(ctx, p.Key); err == nil {
			if !old.WorkspaceID.Valid || old.WorkspaceID.Bytes != p.Workspace.UUID {
				return fmt.Errorf("%w: the hold key belongs to another owner", ErrConflict)
			}
			if old.ExpiresAt.After(now) {
				hold.ID, hold.Period, hold.Micros, hold.ExpiresAt = old.ID, old.Period, old.Micros, old.ExpiresAt
				if old.AudioMs != nil {
					hold.AudioMs = *old.AudioMs
				}
				return nil
			}
			if _, err := deleteHold(ctx, q, p.Workspace, p.Key); err != nil {
				return err
			}
		} else if !errors.Is(err, errNoRows) {
			return err
		}

		// Make sure the rows exist, then lock them: platform first, workspace second.
		if platformMicros {
			if err := q.EnsurePlatformSpendCounter(ctx, period); err != nil {
				return err
			}
		}
		if workspaceMicros || workspaceAudio {
			if err := q.EnsureWorkspaceSpendCounter(ctx, db.EnsureWorkspaceSpendCounterParams{WorkspaceID: nullableWorkspace(p.Workspace), Period: period}); err != nil {
				return err
			}
		}
		var platform, workspace db.SpendCounter
		var err error
		if platformMicros {
			if platform, err = q.LockPlatformSpendCounter(ctx, period); err != nil {
				return err
			}
		}
		if workspaceMicros || workspaceAudio {
			if workspace, err = q.LockWorkspaceSpendCounter(ctx, db.LockWorkspaceSpendCounterParams{WorkspaceID: nullableWorkspace(p.Workspace), Period: period}); err != nil {
				return err
			}
		}

		// What is spent and what is held, for each cap that applies.
		var st SpendState
		if platformMicros {
			held, err := q.SumPlatformSpendHolds(ctx, db.SumPlatformSpendHoldsParams{Period: period, Now: now})
			if err != nil {
				return err
			}
			st.PlatformSpent, st.PlatformHeld = platform.PlatformMicros, held
			if limit := p.Caps.PlatformDailyMicros; platform.PlatformMicros*100 >= limit*80 {
				if n, err := q.MarkPlatformAlerted80(ctx, db.MarkPlatformAlerted80Params{Now: &now, Period: period}); err != nil {
					return err
				} else if n == 1 {
					alerts = append(alerts, SpendAlert{Type: AlertPlatformBreaker, Pct: 80, Period: period, SpentMicros: platform.PlatformMicros, CapMicros: limit})
				}
			}
		}
		if workspaceMicros || workspaceAudio {
			held, err := q.SumWorkspaceSpendHolds(ctx, db.SumWorkspaceSpendHoldsParams{WorkspaceID: nullableWorkspace(p.Workspace), Period: period, Now: now})
			if err != nil {
				return err
			}
			st.WorkspaceSpent, st.WorkspaceHeld = workspace.PlatformMicros+workspace.TestMicros, held.Micros
			st.WorkspaceAudio, st.WorkspaceHeldAudio = workspace.AudioMs, held.AudioMs
		}
		if refusal = CheckSpendCaps(p, st); refusal != nil {
			if refusal.Cap == CapPlatformDaily {
				// A real trip (the committed spend plus a small call no longer fits): record it before the transaction ends, so the
				// alert is logged once however many processes are refused. A busy refusal (CapPlatformBusy) records nothing.
				if n, err := q.MarkPlatformTripped(ctx, db.MarkPlatformTrippedParams{Now: &now, Period: period}); err != nil {
					return err
				} else if n == 1 {
					alerts = append(alerts, SpendAlert{Type: AlertPlatformBreaker, Pct: 100, Period: period, SpentMicros: platform.PlatformMicros, CapMicros: p.Caps.PlatformDailyMicros})
				}
			}
			return nil // committed: the trip flag stays
		}

		var audio *int64
		if p.AudioMs > 0 {
			audio = &p.AudioMs
		}
		row, err := q.InsertSpendHold(ctx, db.InsertSpendHoldParams{
			Scope: spendScopeWorkspace, WorkspaceID: nullableWorkspace(p.Workspace), BilledTo: p.BilledTo, Kind: p.Kind, Period: period,
			Micros: p.Micros, AudioMs: audio, ExpiresAt: hold.ExpiresAt, IdempotencyKey: p.Key,
		})
		if errors.Is(err, errNoRows) {
			// Lost a race on the key to another transaction of the same call: its hold stands.
			old, gerr := q.GetSpendHoldByKey(ctx, p.Key)
			if gerr != nil {
				return gerr
			}
			hold.ID = old.ID
			return nil
		}
		if err != nil {
			return err
		}
		hold.ID = row.ID
		return nil
	})
	if err != nil {
		return SpendHold{}, nil, mapErr(err)
	}
	if refusal != nil {
		return SpendHold{}, alerts, refusal
	}
	return hold, alerts, nil
}

// SpendState is what a reservation is checked against: the settled counters and the live holds of its period.
type SpendState struct {
	PlatformSpent, PlatformHeld int64
	// WorkspaceSpent is the workspace's platform- plus test-billed cost; WorkspaceHeld the live holds of those.
	WorkspaceSpent, WorkspaceHeld      int64
	WorkspaceAudio, WorkspaceHeldAudio int64
}

// CheckSpendCaps is the arithmetic of a reservation, in the order the caps are looked at: the platform breaker, the workspace's
// daily cap, the transcription minutes. Each is "spent + held + asked <= cap". It returns the first cap that would be passed,
// nil when the reservation fits. ReserveSpend does it under the counter locks; the in-memory fake of the tests does it under a mutex.
func CheckSpendCaps(p ReserveSpendParams, st SpendState) *SpendCapError {
	var busy *SpendCapError
	if p.BilledTo == BilledToPlatform && p.Micros > 0 {
		if limit := p.Caps.PlatformDailyMicros; st.PlatformSpent+st.PlatformHeld+p.Micros > limit {
			e := &SpendCapError{Cap: CapPlatformBusy, Requested: p.Micros, Room: max(limit-st.PlatformSpent-st.PlatformHeld, 0),
				HoldsInTheWay: st.PlatformHeld > 0 && st.PlatformSpent+p.Micros <= limit}
			if BreakerTripped(st.PlatformSpent, limit, p.Caps.PlatformSmallCallMicros) {
				e.Cap = CapPlatformDaily
				return e
			}
			busy = e // retryable: a cap that cannot be cured by waiting (below) takes precedence over it
		}
	}
	if p.BilledTo != BilledToWorkspaceKey && p.Micros > 0 {
		if limit := p.Caps.WorkspaceDailyMicros; st.WorkspaceSpent+st.WorkspaceHeld+p.Micros > limit {
			return &SpendCapError{Cap: CapWorkspaceDaily, Requested: p.Micros, Room: max(limit-st.WorkspaceSpent-st.WorkspaceHeld, 0)}
		}
	}
	if p.AudioMs > 0 && p.Caps.WorkspaceDailyAudioMs > 0 {
		if limit := p.Caps.WorkspaceDailyAudioMs; st.WorkspaceAudio+st.WorkspaceHeldAudio+p.AudioMs > limit {
			return &SpendCapError{Cap: CapWorkspaceAudio, Requested: p.AudioMs, Room: max(limit-st.WorkspaceAudio-st.WorkspaceHeldAudio, 0)}
		}
	}
	return busy
}

// SettleSpendParams settles a call: its ledger rows, the counters, and the removal of its hold.
type SettleSpendParams struct {
	// HoldKey is the key of the hold to remove; empty for a cost with no hold (a render, say).
	HoldKey   string
	Workspace ids.WorkspaceID
	// Rows are the ledger rows; every row needs an IdempotencyKey. A row whose key is already in the ledger is skipped, and its
	// cost is not added to the counters again.
	Rows []UsageEntry
	// PlatformCapMicros is the breaker's cap, for the alerts; 0 raises none. PlatformSmallCallMicros is the trip rule's small call
	// (0: DefaultPlatformSmallCallMicros).
	PlatformCapMicros       int64
	PlatformSmallCallMicros int64
	Now                     time.Time
}

// SettleSpend writes the rows, adds their cost to the counters and deletes the hold, in one transaction. It is idempotent on the
// rows' keys: a replayed settle changes nothing. It returns how many rows were new.
func (s *Store) SettleSpend(ctx context.Context, p SettleSpendParams) (int, []SpendAlert, error) {
	if p.Now.IsZero() {
		p.Now = time.Now()
	}
	if p.Workspace.IsZero() {
		return 0, nil, fmt.Errorf("%w: settling needs a workspace", ErrInvalid)
	}
	rows := make([]UsageEntry, len(p.Rows))
	for i, e := range p.Rows {
		if e.WorkspaceID.IsZero() {
			e.WorkspaceID = p.Workspace
		}
		if e.WorkspaceID != p.Workspace {
			return 0, nil, fmt.Errorf("%w: a ledger row is for another workspace than the settle", ErrInvalid)
		}
		if e.Kind == "" {
			e.Kind = KindLLM
		}
		if e.Unit == "" {
			switch e.Kind {
			case KindLLM:
				e.Unit = UnitTokens
			case KindTranscription:
				e.Unit = UnitAudioMs
			}
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = p.Now
		}
		e.CreatedAt = e.CreatedAt.UTC()
		if !validKey(e.IdempotencyKey) || !validKind(e.Kind) || !validBilledTo(e.BilledTo) || e.Unit == "" || e.ListCostMicros < 0 || e.Quantity < 0 {
			return 0, nil, fmt.Errorf("%w: a ledger row needs an idempotency key, a kind, a unit, billed_to and non-negative amounts", ErrInvalid)
		}
		rows[i] = e
	}

	type delta struct{ platform, test, audio int64 }
	var (
		inserted int
		alerts   []SpendAlert
	)
	err := s.InTx(ctx, func(tx *Store) error {
		q := tx.q
		perPeriod := map[string]*delta{}
		for _, e := range rows {
			n, err := q.InsertUsageLedger(ctx, usageParams(e))
			if err != nil {
				return err
			}
			if n == 0 {
				continue // already counted
			}
			inserted++
			d := perPeriod[DayPeriod(e.CreatedAt)]
			if d == nil {
				d = &delta{}
				perPeriod[DayPeriod(e.CreatedAt)] = d
			}
			switch e.BilledTo {
			case BilledToPlatform:
				d.platform += e.ListCostMicros
			case BilledToTest:
				d.test += e.ListCostMicros
			}
			if e.Kind == KindTranscription {
				d.audio += e.AudioMs
			}
		}
		periods := make([]string, 0, len(perPeriod))
		for period := range perPeriod {
			periods = append(periods, period)
		}
		slices.Sort(periods)
		// Platform rows first, then the workspace's: the fixed lock order.
		for _, period := range periods {
			d := perPeriod[period]
			if d.platform == 0 {
				continue
			}
			c, err := q.AddPlatformSpend(ctx, db.AddPlatformSpendParams{Period: period, PlatformMicros: d.platform})
			if err != nil {
				return err
			}
			if limit := p.PlatformCapMicros; limit > 0 {
				if c.PlatformMicros*100 >= limit*80 {
					if n, err := q.MarkPlatformAlerted80(ctx, db.MarkPlatformAlerted80Params{Now: &p.Now, Period: period}); err != nil {
						return err
					} else if n == 1 {
						alerts = append(alerts, SpendAlert{Type: AlertPlatformBreaker, Pct: 80, Period: period, SpentMicros: c.PlatformMicros, CapMicros: limit})
					}
				}
				if BreakerTripped(c.PlatformMicros, limit, p.PlatformSmallCallMicros) {
					if n, err := q.MarkPlatformTripped(ctx, db.MarkPlatformTrippedParams{Now: &p.Now, Period: period}); err != nil {
						return err
					} else if n == 1 {
						alerts = append(alerts, SpendAlert{Type: AlertPlatformBreaker, Pct: 100, Period: period, SpentMicros: c.PlatformMicros, CapMicros: limit})
					}
				}
			}
		}
		for _, period := range periods {
			d := perPeriod[period]
			if d.platform == 0 && d.test == 0 && d.audio == 0 {
				continue
			}
			if _, err := q.AddWorkspaceSpend(ctx, db.AddWorkspaceSpendParams{
				WorkspaceID: nullableWorkspace(p.Workspace), Period: period, PlatformMicros: d.platform, TestMicros: d.test, AudioMs: d.audio,
			}); err != nil {
				return err
			}
		}
		if p.HoldKey != "" {
			if _, err := deleteHold(ctx, q, p.Workspace, p.HoldKey); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, nil, mapErr(err)
	}
	return inserted, alerts, nil
}

// ExtendSpend moves the expiry of a live hold forward to expiresAt (never back): the heartbeat of a call that is still in flight. A
// hold that is gone (settled, released or swept) is not an error and is not brought back.
func (s *Store) ExtendSpend(ctx context.Context, ws ids.WorkspaceID, key string, expiresAt time.Time) error {
	if !validKey(key) {
		return fmt.Errorf("%w: a spend hold needs a key", ErrInvalid)
	}
	_, err := s.q.ExtendSpendHold(ctx, db.ExtendSpendHoldParams{ExpiresAt: expiresAt.UTC(), IdempotencyKey: key, WorkspaceID: nullableWorkspace(ws)})
	return mapErr(err)
}

// ReleaseSpend gives a reservation back without charging anything (the call never went out, or failed with nothing billed). A hold
// that is already gone is not an error.
func (s *Store) ReleaseSpend(ctx context.Context, ws ids.WorkspaceID, key string) error {
	if !validKey(key) {
		return fmt.Errorf("%w: a spend hold needs a key", ErrInvalid)
	}
	_, err := deleteHold(ctx, s.q, ws, key)
	return mapErr(err)
}

// SweepResult is what SweepExpiredSpendHolds removed.
type SweepResult struct {
	Holds   int64
	Micros  int64
	AudioMs int64
}

// SweepExpiredSpendHolds deletes the holds past their expiry. They had stopped counting already; this only keeps the table small.
func (s *Store) SweepExpiredSpendHolds(ctx context.Context, now time.Time) (SweepResult, error) {
	r, err := s.q.DeleteExpiredSpendHolds(ctx, now.UTC())
	return SweepResult{Holds: r.Holds, Micros: r.Micros, AudioMs: r.AudioMs}, mapErr(err)
}

// SpendCounter is a counter row as read back.
type SpendCounter struct {
	Period         string
	PlatformMicros int64
	TestMicros     int64
	AudioMs        int64
	Alerted80At    *time.Time
	TrippedAt      *time.Time
}

// WorkspaceSpend returns the workspace's counter for the period (zeros when there is none yet).
func (s *Store) WorkspaceSpend(ctx context.Context, ws ids.WorkspaceID, period string) (SpendCounter, error) {
	c, err := s.q.GetWorkspaceSpendCounter(ctx, db.GetWorkspaceSpendCounterParams{WorkspaceID: nullableWorkspace(ws), Period: period})
	if errors.Is(err, errNoRows) {
		return SpendCounter{Period: period}, nil
	}
	return counterOf(c), mapErr(err)
}

// PlatformSpend returns the platform's counter for the period (zeros when there is none yet).
func (s *Store) PlatformSpend(ctx context.Context, period string) (SpendCounter, error) {
	c, err := s.q.GetPlatformSpendCounter(ctx, period)
	if errors.Is(err, errNoRows) {
		return SpendCounter{Period: period}, nil
	}
	return counterOf(c), mapErr(err)
}

func counterOf(c db.SpendCounter) SpendCounter {
	return SpendCounter{Period: c.Period, PlatformMicros: c.PlatformMicros, TestMicros: c.TestMicros, AudioMs: c.AudioMs, Alerted80At: c.Alerted80At, TrippedAt: c.TrippedAt}
}

func deleteHold(ctx context.Context, q *db.Queries, ws ids.WorkspaceID, key string) (int64, error) {
	return q.DeleteWorkspaceSpendHold(ctx, db.DeleteWorkspaceSpendHoldParams{IdempotencyKey: key, WorkspaceID: nullableWorkspace(ws)})
}

var errNoRows = pgx.ErrNoRows

func nullableWorkspace(ws ids.WorkspaceID) pgtype.UUID { return pgUUID(ws.UUID) }
