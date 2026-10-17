package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Credits (P7-01a, docs/design/spend-control-v1.md sections 3 to 5 and 10; the model is described in migration 00018).
//
// The balance is a sum over an append-only ledger. Sources (an allowance, a top-up, a positive adjustment) add credits; every charge,
// refund, expiry and negative adjustment names the source it took from or gave back to (grant_id), so the credits left in a source are
// exact and its expiry takes exactly what is left. Active holds are subtracted from what is available.
//
// Every function that changes the balance takes the workspace's credit_accounts row FOR UPDATE first, so two requests for the last
// credits take turns and exactly one wins.
//
// Lock order. A transaction takes at most one of the following kinds of locks, in this order: (1) the project row (the start of an
// edit; the export), (2) the run row (the end of a run), (3) the workspace's credit_accounts row. The spend counters (spend.go) are only
// ever locked in other transactions, never in one that holds a credit account: a credit hold is taken in the request that creates the
// run, a spend reservation in the worker's model call, a credit settle in the transaction that ends the run. Nothing nests them, so they
// cannot deadlock.

// The credit_ledger kinds.
const (
	CreditGrantAllowance = "grant_allowance"
	CreditGrantTopUp     = "grant_topup"
	CreditCharge         = "charge"
	CreditRefund         = "refund"
	CreditExpire         = "expire"
	CreditAdjust         = "adjust"
)

// The actions the ledger names (the price list's keys, package credits).
const (
	ActionEditFirst    = "edit.first"
	ActionEditRevision = "edit.revision"
	ActionExportFinal  = "export.final"
	ActionIdeationRun  = "ideation.run"
)

// How long the hold of a run lasts, and when it is renewed. The hold follows its run (the heartbeat moves its expiry forward), so these
// are about how long a run can go without a sign of life, not about how long a run takes.
const (
	// CreditHoldQueuedTTL is how long a hold made at the start of a run lasts while the run waits in the queue for a worker.
	CreditHoldQueuedTTL = 2 * time.Hour
	// CreditHoldRunTTL is how long a hold lasts after the run's last heartbeat.
	CreditHoldRunTTL = 30 * time.Minute
	// CreditHoldWaitingTTL is how long the hold of a run that waits for its creator's answer lasts.
	CreditHoldWaitingTTL = 7 * 24 * time.Hour
	// creditHoldRenewBelow: the heartbeat writes the hold only when less than this is left, so a heartbeat every few seconds writes about
	// once in ten minutes.
	creditHoldRenewBelow = 20 * time.Minute
	// creditHoldStaleAfter: a hold whose run is still "active" is kept by the sweep until it has been expired this long.
	creditHoldStaleAfter = 30 * 24 * time.Hour
)

// ErrInsufficientCredits is wrapped by *InsufficientCreditsError.
var ErrInsufficientCredits = errors.New("store: not enough credits")

// InsufficientCreditsError says how many credits were asked for and how many were available.
type InsufficientCreditsError struct{ Needed, Available int }

func (e *InsufficientCreditsError) Error() string {
	return fmt.Sprintf("store: not enough credits (needed %d, available %d)", e.Needed, e.Available)
}

// Is makes errors.Is(err, ErrInsufficientCredits) true.
func (e *InsufficientCreditsError) Is(target error) bool { return target == ErrInsufficientCredits }

// CreditLedgerRow is a row of credit_ledger as read back.
type CreditLedgerRow = db.CreditLedger

// CreditSource is a grant (or a positive adjustment) that can still be used, with what is left of it.
type CreditSource struct {
	ID        uuid.UUID
	Kind      string
	Granted   int
	Remaining int
	// ExpiresAt is nil for a source that never expires (a trial's allowance).
	ExpiresAt *time.Time
	Plan      string
	CreatedAt time.Time
}

// CreditTopUp is a usable source that is not the allowance: what is left of it and when it expires.
type CreditTopUp struct {
	Credits   int
	ExpiresAt time.Time
}

// CreditBalance is what a workspace can spend right now.
type CreditBalance struct {
	// Available is what a new action can use: what is left in the usable sources minus the active holds, never below zero.
	Available int
	// Held is what the active holds set aside.
	Held int
	// Allowance is the size of the allowance of the current period (0 when the workspace has none); AllowanceRemaining what is left of it.
	Allowance          int
	AllowanceRemaining int
	// PeriodEnd is when the current allowance expires; nil for none, or for a trial's, which does not.
	PeriodEnd *time.Time
	// TopUps are the other usable sources, earliest expiry first.
	TopUps []CreditTopUp
	// Plan is the plan the current allowance belongs to ("" before the first grant).
	Plan string
}

func sourceOf(r db.ListCreditSourcesRow) CreditSource {
	s := CreditSource{ID: r.ID, Kind: r.Kind, Granted: int(r.Granted), Remaining: int(r.Remaining), ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt}
	if r.Plan != nil {
		s.Plan = *r.Plan
	}
	return s
}

// creditSources lists the sources that have not expired at now (by time, or by an expire row), in the order of use, empty ones included
// (the balance shows an allowance that is all used up; a charge skips it).
func (s *Store) creditSources(ctx context.Context, ws ids.WorkspaceID, now time.Time) ([]CreditSource, error) {
	at := now.UTC()
	rows, err := s.q.ListCreditSources(ctx, db.ListCreditSourcesParams{WorkspaceID: ws, Now: &at})
	if err != nil {
		return nil, err
	}
	out := make([]CreditSource, 0, len(rows))
	for _, r := range rows {
		if r.Expired {
			continue // taken by an expire row (a plan change replaced it, or the job ran): nothing is left, and it is not shown
		}
		out = append(out, sourceOf(r))
	}
	return out, nil
}

func balanceOf(sources []CreditSource, held int) CreditBalance {
	b := CreditBalance{Held: held}
	total := 0
	for _, src := range sources {
		total += src.Remaining
		if src.Kind == CreditGrantAllowance {
			b.Allowance += src.Granted
			b.AllowanceRemaining += src.Remaining
			if src.ExpiresAt != nil && (b.PeriodEnd == nil || src.ExpiresAt.Before(*b.PeriodEnd)) {
				e := *src.ExpiresAt
				b.PeriodEnd = &e
			}
			if b.Plan == "" {
				b.Plan = src.Plan
			}
			continue
		}
		if src.ExpiresAt != nil && src.Remaining > 0 {
			b.TopUps = append(b.TopUps, CreditTopUp{Credits: src.Remaining, ExpiresAt: *src.ExpiresAt})
		}
	}
	b.Available = max(total-held, 0)
	return b
}

// CreditBalance returns what the workspace can spend at now. It reads without a lock (it is a display, not a decision); a hold or a
// charge in flight may or may not be counted.
func (s *Store) CreditBalance(ctx context.Context, ws ids.WorkspaceID, now time.Time) (CreditBalance, error) {
	sources, err := s.creditSources(ctx, ws, now)
	if err != nil {
		return CreditBalance{}, mapErr(err)
	}
	held, err := s.q.SumActiveCreditHolds(ctx, db.SumActiveCreditHoldsParams{WorkspaceID: ws, Now: now.UTC()})
	if err != nil {
		return CreditBalance{}, mapErr(err)
	}
	b := balanceOf(sources, int(held))
	if acct, err := s.q.GetCreditAccount(ctx, ws); err == nil {
		b.Plan = acct.Plan
	}
	return b, nil
}

// CreditAccount is the bookkeeping row of a workspace's credits.
type CreditAccount struct {
	Plan           string
	PeriodAnchor   *time.Time
	AllowanceUntil *time.Time
}

// GetCreditAccount returns the workspace's account, or the zero value when it has none yet.
func (s *Store) GetCreditAccount(ctx context.Context, ws ids.WorkspaceID) (CreditAccount, error) {
	a, err := s.q.GetCreditAccount(ctx, ws)
	if errors.Is(err, errNoRows) {
		return CreditAccount{}, nil
	}
	return CreditAccount{Plan: a.Plan, PeriodAnchor: a.PeriodAnchor, AllowanceUntil: a.AllowanceUntil}, mapErr(err)
}

// CreditLedger returns the workspace's newest ledger rows, newest first.
func (s *Store) CreditLedger(ctx context.Context, ws ids.WorkspaceID, limit int) ([]CreditLedgerRow, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	rows, err := s.q.ListCreditLedger(ctx, db.ListCreditLedgerParams{WorkspaceID: ws, RowLimit: int32(limit)}) //nolint:gosec // checked above
	return rows, mapErr(err)
}

// lockCreditAccount makes sure the account exists and locks it for the rest of the transaction.
func (s *Store) lockCreditAccount(ctx context.Context, ws ids.WorkspaceID) (db.CreditAccount, error) {
	if err := s.q.EnsureCreditAccount(ctx, ws); err != nil {
		return db.CreditAccount{}, err
	}
	return s.q.LockCreditAccount(ctx, ws)
}

// ---- Holds ------------------------------------------------------------------------------------------------------

// HoldCreditsParams is a request to set credits aside for an action.
type HoldCreditsParams struct {
	Workspace ids.WorkspaceID
	// Action is one of the Action constants; ActionID identifies this occurrence (the agent run's id for an edit). A second hold for the
	// same ActionID returns the first one and sets nothing more aside.
	Action   string
	ActionID uuid.UUID
	// Credits is the price from the price list. 0 is allowed: the hold then exists (no editor run is ever without one) and needs nothing.
	Credits int
	// Project and Member attribute the action (both optional).
	Project ids.ProjectID
	Member  ids.UserID
	// Plan and ListVersion are recorded on the charge.
	Plan, ListVersion string
	Now               time.Time
	TTL               time.Duration
}

// CreditHold is a hold as read back.
type CreditHold struct {
	ID          uuid.UUID
	Workspace   ids.WorkspaceID
	Action      string
	ActionID    uuid.UUID
	Credits     int
	Project     ids.ProjectID
	Member      ids.UserID
	Plan        string
	ListVersion string
	ExpiresAt   time.Time
}

func holdOf(h db.CreditHold) CreditHold {
	out := CreditHold{ID: h.ID, Workspace: h.WorkspaceID, Action: h.Action, ActionID: h.ActionID, Credits: int(h.Credits), ExpiresAt: h.ExpiresAt}
	if h.ProjectID.Valid {
		out.Project = ids.ProjectID{UUID: h.ProjectID.Bytes}
	}
	if h.MemberUserID.Valid {
		out.Member = ids.UserID{UUID: h.MemberUserID.Bytes}
	}
	if h.Plan != nil {
		out.Plan = *h.Plan
	}
	if h.PlanListVersion != nil {
		out.ListVersion = *h.PlanListVersion
	}
	return out
}

func validCreditAction(a string) bool {
	switch a {
	case ActionEditFirst, ActionEditRevision, ActionExportFinal, ActionIdeationRun:
		return true
	}
	return false
}

func optText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// The longest actor and reason the ledger takes (the table checks the same, in characters).
const (
	maxActorLen  = 200
	maxReasonLen = 1000
)

// HoldCredits sets the credits of an action aside, in one transaction that locks the workspace's account, or returns an
// *InsufficientCreditsError. Nothing is set aside on an error. Called from inside the transaction that creates the run, so the hold
// and the run exist together or not at all.
func (s *Store) HoldCredits(ctx context.Context, p HoldCreditsParams) (CreditHold, error) {
	switch {
	case p.Workspace.IsZero():
		return CreditHold{}, fmt.Errorf("%w: a credit hold needs a workspace", ErrInvalid)
	case !validCreditAction(p.Action), p.ActionID == uuid.Nil:
		return CreditHold{}, fmt.Errorf("%w: a credit hold needs a known action and an action id", ErrInvalid)
	case p.Credits < 0, p.TTL <= 0, p.Now.IsZero():
		return CreditHold{}, fmt.Errorf("%w: a credit hold needs a non-negative amount, a time and a TTL", ErrInvalid)
	}
	now := p.Now.UTC()
	var hold CreditHold
	err := s.inTx(ctx, func(tx *Store) error {
		if _, err := tx.lockCreditAccount(ctx, p.Workspace); err != nil {
			return err
		}
		// A repeated request for the same action finds its hold.
		old, err := tx.q.GetCreditHoldByAction(ctx, db.GetCreditHoldByActionParams{WorkspaceID: p.Workspace, ActionID: p.ActionID})
		if err == nil {
			hold = holdOf(old)
			return nil
		}
		if !errors.Is(err, errNoRows) {
			return err
		}
		if p.Credits > 0 {
			sources, err := tx.creditSources(ctx, p.Workspace, now)
			if err != nil {
				return err
			}
			held, err := tx.q.SumActiveCreditHolds(ctx, db.SumActiveCreditHoldsParams{WorkspaceID: p.Workspace, Now: now})
			if err != nil {
				return err
			}
			if avail := balanceOf(sources, int(held)).Available; avail < p.Credits {
				return &InsufficientCreditsError{Needed: p.Credits, Available: avail}
			}
		}
		row, err := tx.q.InsertCreditHold(ctx, db.InsertCreditHoldParams{
			WorkspaceID: p.Workspace, Action: p.Action, ActionID: p.ActionID, Credits: int32(p.Credits), //nolint:gosec // a price list value
			ProjectID: pgUUID(p.Project.UUID), MemberUserID: pgUUID(p.Member.UUID), Plan: optText(p.Plan), PlanListVersion: optText(p.ListVersion),
			ExpiresAt: now.Add(p.TTL), IdempotencyKey: "hold:" + p.ActionID.String(),
		})
		if err != nil {
			return err // a conflict cannot happen: the account is locked and the hold was looked for above
		}
		hold = holdOf(row)
		return nil
	})
	if err != nil {
		var ins *InsufficientCreditsError
		if errors.As(err, &ins) {
			return CreditHold{}, err
		}
		return CreditHold{}, mapErr(err)
	}
	return hold, nil
}

// The credits of one run, as the run resource shows them.
const (
	RunCreditsHeld     = "held"     // the run is in progress (or its hold has not been settled): these credits are set aside
	RunCreditsCharged  = "charged"  // the run was charged
	RunCreditsRefunded = "refunded" // the run was charged and the charge was given back
)

// RunCredits is what a run cost in credits: its state, the action (edit.first or edit.revision) and the credits.
type RunCredits struct {
	State   string
	Action  string
	Credits int
}

// RunCreditsOf reads the credits of a run from the ledger and the holds: charged (the charge rows), refunded (a refund of them; Credits
// is then what was given back, which is less than the charge when a source had expired), or held (the hold). ok is false when the run
// has neither: it was released (a failed or empty run is not charged and leaves no row), or it never took credits. Only the actions of an
// edit are reported.
func (s *Store) RunCreditsOf(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID) (rc RunCredits, ok bool, err error) {
	// The settle writes the charge and deletes the hold in one transaction, but these reads are separate: charges first, then the hold,
	// then the charges again when the hold is gone, so a settle in between is seen as a charge and never as nothing.
	for attempt := 0; attempt < 2; attempt++ {
		charges, err := s.q.ListCreditChargesOfAction(ctx, db.ListCreditChargesOfActionParams{WorkspaceID: ws, ActionID: pgUUID(run)})
		if err != nil {
			return RunCredits{}, false, mapErr(err)
		}
		if len(charges) > 0 {
			return s.chargedRunCredits(ctx, ws, run, charges)
		}
		hold, err := s.GetCreditHold(ctx, ws, run)
		switch {
		case err == nil:
			rc = RunCredits{State: RunCreditsHeld, Action: hold.Action, Credits: hold.Credits}
			return rc, isEditAction(rc.Action), nil
		case !errors.Is(err, ErrNotFound):
			return RunCredits{}, false, err
		}
	}
	return RunCredits{}, false, nil
}

func isEditAction(a string) bool { return a == ActionEditFirst || a == ActionEditRevision }

func (s *Store) chargedRunCredits(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, charges []db.CreditLedger) (RunCredits, bool, error) {
	rc := RunCredits{State: RunCreditsCharged}
	for _, c := range charges {
		rc.Credits -= int(c.Credits)
		if c.Action != nil {
			rc.Action = *c.Action
		}
	}
	refunds, err := s.q.ListCreditRefundsOfAction(ctx, db.ListCreditRefundsOfActionParams{WorkspaceID: ws, ActionID: pgUUID(run)})
	if err != nil {
		return RunCredits{}, false, mapErr(err)
	}
	if len(refunds) > 0 {
		rc.State, rc.Credits = RunCreditsRefunded, 0
		for _, r := range refunds {
			rc.Credits += int(r.Credits)
		}
	}
	return rc, isEditAction(rc.Action), nil
}

// GetCreditHold returns the hold of an action, or ErrNotFound.
func (s *Store) GetCreditHold(ctx context.Context, ws ids.WorkspaceID, actionID uuid.UUID) (CreditHold, error) {
	h, err := s.q.GetCreditHoldByAction(ctx, db.GetCreditHoldByActionParams{WorkspaceID: ws, ActionID: actionID})
	if err != nil {
		return CreditHold{}, mapErr(err)
	}
	return holdOf(h), nil
}

// ExtendCreditHold moves the expiry of an action's hold forward to expiresAt. It never moves it back, and a hold that is gone is not
// recreated.
func (s *Store) ExtendCreditHold(ctx context.Context, ws ids.WorkspaceID, actionID uuid.UUID, expiresAt time.Time) error {
	expiresAt = expiresAt.UTC()
	_, err := s.q.ExtendCreditHold(ctx, db.ExtendCreditHoldParams{ExpiresAt: expiresAt, WorkspaceID: ws, ActionID: actionID, RenewBelow: expiresAt})
	return mapErr(err)
}

// RenewCreditHold is the heartbeat of the run that owns a hold: it moves the expiry to now plus CreditHoldRunTTL, but only when less
// than twenty minutes of it are left, so a heartbeat every few seconds writes about once in ten minutes.
func (s *Store) RenewCreditHold(ctx context.Context, ws ids.WorkspaceID, actionID uuid.UUID, now time.Time) error {
	now = now.UTC()
	_, err := s.q.ExtendCreditHold(ctx, db.ExtendCreditHoldParams{
		ExpiresAt: now.Add(CreditHoldRunTTL), WorkspaceID: ws, ActionID: actionID, RenewBelow: now.Add(creditHoldRenewBelow),
	})
	return mapErr(err)
}

// CreditSettlement says what SettleCredits did.
type CreditSettlement struct {
	// Held is true when the action had a hold; false when it had none (it was never billed, or was settled already).
	Held bool
	// Charged is the credits taken from the sources (0 for a release, a replay, or a zero-credit action).
	Charged int
	// Short is how many credits of the hold could not be taken because the sources ran out (an allowance that expired while the run was
	// working). It is recorded on the charge row and absorbed: the creator is never charged below zero.
	Short int
	// Replayed is true when the action had been charged already; nothing was added.
	Replayed bool
	// Released is true when the hold was removed without a charge.
	Released bool
}

// SettleCredits ends the hold of an action, in one transaction: with charge it writes the charge rows (one per source it takes from)
// and deletes the hold; without it only deletes the hold. It is idempotent on the action id, so a replayed settle charges once.
//
// Called from the transaction that ends the run (EndAgentRun), so the run's end and its charge commit together.
func (s *Store) SettleCredits(ctx context.Context, ws ids.WorkspaceID, actionID uuid.UUID, charge bool, now time.Time) (CreditSettlement, error) {
	if ws.IsZero() || actionID == uuid.Nil {
		return CreditSettlement{}, fmt.Errorf("%w: settling needs a workspace and an action id", ErrInvalid)
	}
	// Most runs have no hold (the echo agent): do not lock anything for them. A hold is created before its run can end, so a missing hold
	// here is final.
	if _, err := s.q.GetCreditHoldByAction(ctx, db.GetCreditHoldByActionParams{WorkspaceID: ws, ActionID: actionID}); errors.Is(err, errNoRows) {
		return CreditSettlement{}, nil
	} else if err != nil {
		return CreditSettlement{}, mapErr(err)
	}
	now = now.UTC()
	var res CreditSettlement
	err := s.inTx(ctx, func(tx *Store) error {
		res = CreditSettlement{}
		if _, err := tx.lockCreditAccount(ctx, ws); err != nil {
			return err
		}
		// A source whose period has ended and that the expiry job has not got to yet is expired now, with the credits this hold rests on
		// kept in it: a run that ends in the minute after a period ends is charged to the old allowance like one that ends later.
		if err := tx.expireDueOfWorkspace(ctx, ws, now); err != nil {
			return err
		}
		hold, err := tx.q.GetCreditHoldByAction(ctx, db.GetCreditHoldByActionParams{WorkspaceID: ws, ActionID: actionID})
		if errors.Is(err, errNoRows) {
			return nil // settled by someone else while we waited for the lock
		}
		if err != nil {
			return err
		}
		res.Held = true
		charged, err := tx.q.ListCreditChargesOfAction(ctx, db.ListCreditChargesOfActionParams{WorkspaceID: ws, ActionID: pgUUID(actionID)})
		if err != nil {
			return err
		}
		if len(charged) > 0 {
			res.Replayed = true // charged already: only the hold was left behind
			if err := tx.expireFrozen(ctx, hold, now); err != nil {
				return err
			}
		} else if !charge {
			res.Released = true
			if err := tx.expireFrozen(ctx, hold, now); err != nil {
				return err
			}
		} else {
			if err := tx.chargeHold(ctx, hold, now, &res); err != nil {
				return err
			}
		}
		_, err = tx.q.DeleteCreditHold(ctx, db.DeleteCreditHoldParams{WorkspaceID: ws, ActionID: actionID})
		return err
	})
	if err != nil {
		return CreditSettlement{}, mapErr(err)
	}
	return res, nil
}

// ReleaseCredits removes the hold of an action without charging. A hold that is gone is not an error.
func (s *Store) ReleaseCredits(ctx context.Context, ws ids.WorkspaceID, actionID uuid.UUID, now time.Time) (CreditSettlement, error) {
	return s.SettleCredits(ctx, ws, actionID, false, now)
}

// chargeHold writes the charge rows of a hold, taking from the sources in the order of use. The account is locked.
func (s *Store) chargeHold(ctx context.Context, hold db.CreditHold, now time.Time, res *CreditSettlement) error {
	sources, err := s.creditSources(ctx, hold.WorkspaceID, now)
	if err != nil {
		return err
	}
	need := int(hold.Credits)
	type take struct {
		source CreditSource
		n      int
	}
	var takes []take
	// The part of the hold that rests on a source that expired while the run worked is charged to that source first: it was kept for this
	// hold, and the live allowance of the new period must not pay for it again. What is left of the frozen part (the hold was for more
	// than the price now, which does not happen today) expires.
	if hold.FrozenCredits > 0 && hold.FrozenSourceID.Valid {
		frozen, err := s.q.GetCreditSource(ctx, db.GetCreditSourceParams{WorkspaceID: hold.WorkspaceID, ID: hold.FrozenSourceID.Bytes})
		if err != nil {
			return err
		}
		n := min(int(frozen.Remaining), int(hold.FrozenCredits), need)
		if n > 0 {
			takes = append(takes, take{CreditSource{ID: frozen.ID, Kind: frozen.Kind, Remaining: int(frozen.Remaining)}, n})
			need -= n
		}
		if left := int(hold.FrozenCredits) - n; left > 0 {
			reason := "the frozen credits of a hold that were not charged"
			if err := s.expireFrozenPart(ctx, hold, left, now, &reason); err != nil {
				return err
			}
		}
	}
	for _, src := range sources {
		if need == 0 {
			break
		}
		n := min(src.Remaining, need)
		if n <= 0 {
			continue
		}
		takes = append(takes, take{src, n})
		need -= n
	}
	res.Charged = int(hold.Credits) - need
	res.Short = need
	reason := (*string)(nil)
	if need > 0 {
		r := fmt.Sprintf("short by %d: the credits ran out (an allowance expired) before the action ended", need)
		reason = &r
	}
	// The work the charge paid for: the newest timeline version the run authored (an edit's run id is the author id of its versions).
	var version *int32
	if hold.Action == ActionEditFirst || hold.Action == ActionEditRevision {
		v, err := s.q.AgentRunTimelineVersion(ctx, db.AgentRunTimelineVersionParams{WorkspaceID: hold.WorkspaceID, AuthorID: ids.Encode(ids.PrefixRun, hold.ActionID)})
		if err != nil {
			return err
		}
		if v > 0 {
			version = &v
		}
	}
	insert := func(i int, credits int32, grant pgtype.UUID) error {
		_, err := s.q.InsertCreditLedger(ctx, db.InsertCreditLedgerParams{
			WorkspaceID: hold.WorkspaceID, Kind: CreditCharge, Credits: credits, GrantID: grant, Action: &hold.Action, ActionID: pgUUID(hold.ActionID),
			ProjectID: hold.ProjectID, MemberUserID: hold.MemberUserID, Plan: hold.Plan, PlanListVersion: hold.PlanListVersion, TimelineVersion: version,
			Reason: reason, IdempotencyKey: fmt.Sprintf("charge:%s:%d", hold.ActionID, i), CreatedAt: now,
		})
		if errors.Is(err, errNoRows) {
			return nil // already there
		}
		return err
	}
	if len(takes) == 0 {
		// A free action, or nothing left to take: still one row, because it records that the action happened (a charged edit).
		return insert(0, 0, pgtype.UUID{})
	}
	for i, t := range takes {
		if err := insert(i, -int32(t.n), pgtype.UUID{Bytes: t.source.ID, Valid: true}); err != nil { //nolint:gosec // a price list value
			return err
		}
	}
	return nil
}

// CreditSweepResult is what SweepExpiredCreditHolds removed.
type CreditSweepResult struct {
	Holds   int64
	Credits int64
}

// SweepExpiredCreditHolds deletes the holds past their expiry (a run whose worker and retries are gone). They had stopped counting
// already; this gives the table its size back and tells the caller what crashed runs left behind.
func (s *Store) SweepExpiredCreditHolds(ctx context.Context, now time.Time) (CreditSweepResult, error) {
	now = now.UTC()
	stale := now.Add(-creditHoldStaleAfter)
	var out CreditSweepResult
	// The holds with frozen credits go one at a time, under their workspace's lock, so that the frozen credits expire with them.
	frozen, err := s.q.ListExpiredCreditHoldsWithFrozen(ctx, db.ListExpiredCreditHoldsWithFrozenParams{Now: now, StaleBefore: stale, RowLimit: 200})
	if err != nil {
		return out, mapErr(err)
	}
	var firstErr error // a hold that fails must not keep the rest from being swept
	for _, f := range frozen {
		err := s.inTx(ctx, func(tx *Store) error {
			if _, err := tx.lockCreditAccount(ctx, f.WorkspaceID); err != nil {
				return err
			}
			h, err := tx.q.DeleteExpiredCreditHoldByID(ctx, db.DeleteExpiredCreditHoldByIDParams{WorkspaceID: f.WorkspaceID, ID: f.ID, Now: now, StaleBefore: stale})
			if errors.Is(err, errNoRows) {
				return nil // settled, renewed or swept by someone else
			}
			if err != nil {
				return err
			}
			out.Holds++
			out.Credits += int64(h.Credits)
			return tx.expireFrozen(ctx, h, now)
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	r, err := s.q.DeleteExpiredCreditHolds(ctx, db.DeleteExpiredCreditHoldsParams{Now: now, StaleBefore: stale})
	out.Holds += r.Holds
	out.Credits += r.Credits
	if firstErr != nil {
		return out, mapErr(firstErr)
	}
	return out, mapErr(err)
}

// ---- Grants, adjustments and refunds ----------------------------------------------------------------------------

// GrantTopUpParams is a top-up: credits that last until ExpiresAt.
type GrantTopUpParams struct {
	Workspace ids.WorkspaceID
	Credits   int
	ExpiresAt time.Time
	// Actor and Reason say who granted and why (the purchase, the operator).
	Actor, Reason string
	// Key makes the grant idempotent: a second grant with the same key adds nothing. Empty gets a random key.
	Key               string
	Plan, ListVersion string
	Now               time.Time
}

// GrantTopUp adds a top-up to the workspace. It returns the id of the grant row, and false when the key was used already.
func (s *Store) GrantTopUp(ctx context.Context, p GrantTopUpParams) (uuid.UUID, bool, error) {
	switch {
	case p.Workspace.IsZero():
		return uuid.Nil, false, fmt.Errorf("%w: a top-up needs a workspace", ErrInvalid)
	case p.Credits < 1:
		return uuid.Nil, false, fmt.Errorf("%w: a top-up needs a positive number of credits", ErrInvalid)
	case p.Now.IsZero() || !p.ExpiresAt.After(p.Now):
		return uuid.Nil, false, fmt.Errorf("%w: a top-up needs an expiry in the future", ErrInvalid)
	case len(p.Actor) > maxActorLen || len(p.Reason) > maxReasonLen:
		return uuid.Nil, false, fmt.Errorf("%w: an actor of at most %d and a reason of at most %d bytes", ErrInvalid, maxActorLen, maxReasonLen)
	}
	key := p.Key
	if key == "" {
		key = "topup:" + uuid.NewString()
	}
	if !validKey(key) {
		return uuid.Nil, false, fmt.Errorf("%w: a ledger key of 1 to 200 characters", ErrInvalid)
	}
	var (
		id      uuid.UUID
		created bool
	)
	err := s.inTx(ctx, func(tx *Store) error {
		if _, err := tx.lockCreditAccount(ctx, p.Workspace); err != nil {
			return err
		}
		exp := p.ExpiresAt.UTC()
		var err error
		id, err = tx.q.InsertCreditLedger(ctx, db.InsertCreditLedgerParams{
			WorkspaceID: p.Workspace, Kind: CreditGrantTopUp, Credits: int32(p.Credits), //nolint:gosec // bounded by the caller
			Plan: optText(p.Plan), PlanListVersion: optText(p.ListVersion), ExpiresAt: &exp,
			Actor: optText(strings.TrimSpace(p.Actor)), Reason: optText(strings.TrimSpace(p.Reason)), IdempotencyKey: key, CreatedAt: p.Now.UTC(),
		})
		if errors.Is(err, errNoRows) {
			old, gerr := tx.q.GetCreditLedgerByKey(ctx, db.GetCreditLedgerByKeyParams{WorkspaceID: p.Workspace, IdempotencyKey: key})
			if gerr != nil {
				return gerr
			}
			id = old.ID
			return nil
		}
		created = err == nil
		return err
	})
	if err != nil {
		return uuid.Nil, false, mapErr(err)
	}
	return id, created, nil
}

// AdjustCreditsParams is an operator's correction.
type AdjustCreditsParams struct {
	Workspace ids.WorkspaceID
	// Credits is signed: positive adds credits that last until ExpiresAt (which is then required), negative takes credits away from the
	// sources in the order of use, and fails with *InsufficientCreditsError when the workspace has fewer.
	Credits   int
	ExpiresAt time.Time
	// Actor and Reason are required: every adjustment says who made it and why.
	Actor, Reason string
	Key           string
	Now           time.Time
}

// AdjustCredits writes an adjustment. It returns the credits that moved (positive or negative).
func (s *Store) AdjustCredits(ctx context.Context, p AdjustCreditsParams) (int, error) {
	actor, reason := strings.TrimSpace(p.Actor), strings.TrimSpace(p.Reason)
	switch {
	case p.Workspace.IsZero():
		return 0, fmt.Errorf("%w: an adjustment needs a workspace", ErrInvalid)
	case p.Credits == 0:
		return 0, fmt.Errorf("%w: an adjustment of zero credits", ErrInvalid)
	case actor == "" || reason == "":
		return 0, fmt.Errorf("%w: an adjustment needs an actor and a reason", ErrInvalid)
	case len(actor) > maxActorLen || len(reason) > maxReasonLen:
		return 0, fmt.Errorf("%w: an actor of at most %d and a reason of at most %d bytes", ErrInvalid, maxActorLen, maxReasonLen)
	case p.Now.IsZero():
		return 0, fmt.Errorf("%w: an adjustment needs a time", ErrInvalid)
	case p.Credits > 0 && !p.ExpiresAt.After(p.Now):
		return 0, fmt.Errorf("%w: credits added by an adjustment need an expiry in the future", ErrInvalid)
	}
	key := p.Key
	if key == "" {
		key = "adjust:" + uuid.NewString()
	}
	if !validKey(key) {
		return 0, fmt.Errorf("%w: a ledger key of 1 to 200 characters", ErrInvalid)
	}
	now := p.Now.UTC()
	err := s.inTx(ctx, func(tx *Store) error {
		if _, err := tx.lockCreditAccount(ctx, p.Workspace); err != nil {
			return err
		}
		row := db.InsertCreditLedgerParams{
			WorkspaceID: p.Workspace, Kind: CreditAdjust, Credits: int32(p.Credits), //nolint:gosec // bounded by the caller
			Actor: &actor, Reason: &reason, IdempotencyKey: key, CreatedAt: now,
		}
		if p.Credits > 0 {
			exp := p.ExpiresAt.UTC()
			row.ExpiresAt = &exp
			_, err := tx.q.InsertCreditLedger(ctx, row)
			if errors.Is(err, errNoRows) {
				return nil // replayed
			}
			return err
		}
		if _, err := tx.q.GetCreditLedgerByKey(ctx, db.GetCreditLedgerByKeyParams{WorkspaceID: p.Workspace, IdempotencyKey: key + ":0"}); err == nil {
			return nil // replayed
		} else if !errors.Is(err, errNoRows) {
			return err
		}
		sources, err := tx.creditSources(ctx, p.Workspace, now)
		if err != nil {
			return err
		}
		held, err := tx.q.SumActiveCreditHolds(ctx, db.SumActiveCreditHoldsParams{WorkspaceID: p.Workspace, Now: now})
		if err != nil {
			return err
		}
		need := -p.Credits
		// Only what is free can be taken: credits that a run in progress holds will be charged to it.
		if avail := balanceOf(sources, int(held)).Available; avail < need {
			return &InsufficientCreditsError{Needed: need, Available: avail}
		}
		rows := 0
		for _, src := range sources {
			if need == 0 {
				break
			}
			n := min(src.Remaining, need)
			if n <= 0 {
				continue
			}
			// The first row is always ":0", which is what the replay check above looks for.
			row.Credits, row.GrantID, row.IdempotencyKey = -int32(n), pgtype.UUID{Bytes: src.ID, Valid: true}, fmt.Sprintf("%s:%d", key, rows) //nolint:gosec // bounded above
			if _, err := tx.q.InsertCreditLedger(ctx, row); err != nil {
				return err
			}
			need -= n
			rows++
		}
		return nil
	})
	if err != nil {
		var ins *InsufficientCreditsError
		if errors.As(err, &ins) {
			return 0, err
		}
		return 0, mapErr(err)
	}
	return p.Credits, nil
}

// RefundAction gives an action's charge back: for each charge row, a refund row to the same source, if that source has not expired
// (credits that would have expired are not brought back). It is idempotent. It returns the credits given back and the credits that
// could not be because their source had expired.
func (s *Store) RefundAction(ctx context.Context, ws ids.WorkspaceID, actionID uuid.UUID, actor, reason string, now time.Time) (refunded, lost int, err error) {
	actor, reason = strings.TrimSpace(actor), strings.TrimSpace(reason)
	if ws.IsZero() || actionID == uuid.Nil || actor == "" || reason == "" || now.IsZero() || len(actor) > maxActorLen || len(reason) > maxReasonLen {
		return 0, 0, fmt.Errorf("%w: a refund needs a workspace, an action, an actor (at most %d bytes), a reason (at most %d) and a time", ErrInvalid, maxActorLen, maxReasonLen)
	}
	now = now.UTC()
	err = s.inTx(ctx, func(tx *Store) error {
		refunded, lost = 0, 0
		if _, err := tx.lockCreditAccount(ctx, ws); err != nil {
			return err
		}
		charges, err := tx.q.ListCreditChargesOfAction(ctx, db.ListCreditChargesOfActionParams{WorkspaceID: ws, ActionID: pgUUID(actionID)})
		if err != nil {
			return err
		}
		for _, c := range charges {
			// Every charge row gets a refund row, so that the action stops counting as a charged edit: one that gives the credits back to
			// their source, or a zero-credit one for a free charge and for credits whose source has expired (they are not brought back).
			row := db.InsertCreditLedgerParams{
				WorkspaceID: ws, Kind: CreditRefund, Action: c.Action, ActionID: c.ActionID,
				ProjectID: c.ProjectID, MemberUserID: c.MemberUserID, Plan: c.Plan, PlanListVersion: c.PlanListVersion,
				Actor: &actor, Reason: &reason, IdempotencyKey: fmt.Sprintf("refund:%s:%s", actionID, c.ID), CreatedAt: now,
			}
			back := 0
			if c.Credits < 0 && c.GrantID.Valid {
				src, err := tx.q.GetCreditSource(ctx, db.GetCreditSourceParams{WorkspaceID: ws, ID: c.GrantID.Bytes})
				if err != nil {
					return err
				}
				if !src.Expired && (src.ExpiresAt == nil || src.ExpiresAt.After(now)) {
					row.Credits, row.GrantID = -c.Credits, c.GrantID
					back = int(-c.Credits)
				}
			}
			switch _, err := tx.q.InsertCreditLedger(ctx, row); {
			case errors.Is(err, errNoRows):
				continue // refunded already
			case err != nil:
				return err
			}
			if back > 0 {
				refunded += back
			} else if c.Credits < 0 {
				lost += int(-c.Credits)
			}
		}
		return nil
	})
	return refunded, lost, mapErr(err)
}

// VersionCovered says whether a final render of the version may be made as far as payment goes: the newest agent-authored version at or
// before it (the last agent work it contains; a person's edit on top of it is free) must belong to a run that was charged and not
// refunded. A version with no agent work in its history is covered. A run that failed or was released made versions that are not, and
// neither are the person's edits made on top of them, until a later run is charged. The project's charged edit is checked separately.
func (s *Store) VersionCovered(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (bool, error) {
	n, err := s.q.NearestAgentVersion(ctx, db.NearestAgentVersionParams{WorkspaceID: ws, ProjectID: project, Version: int32(version)}) //nolint:gosec // a version number the caller checked
	if errors.Is(err, errNoRows) {
		return true, nil
	}
	if err != nil {
		return false, mapErr(err)
	}
	run, err := ids.Decode(ids.PrefixRun, n.AuthorID)
	if err != nil {
		return false, nil // an agent author that is not a run id: nothing paid for it, fail closed
	}
	charged, err := s.q.ActionIsCharged(ctx, db.ActionIsChargedParams{WorkspaceID: ws, ActionID: pgUUID(run)})
	return charged, mapErr(err)
}

// CountUnchargedRuns counts the runs of the agent that the workspace has in flight, plus those that finished since the given time having
// spent money and ended without a charge: cancelled before they made anything, succeeded saying there was nothing to change, or failed
// for one of failureReasons. A finished run that cost nothing (cancelled in the queue) and a run that was charged do not count. It is the
// abuse control of credits.Service.CheckUncharged. Only runs created within unchargedLookback before the given time are looked at.
func (s *Store) CountUnchargedRuns(ctx context.Context, ws ids.WorkspaceID, agent string, since time.Time, failureReasons []string) (int, error) {
	t := since.UTC()
	if failureReasons == nil {
		failureReasons = []string{}
	}
	n, err := s.q.CountUnchargedRuns(ctx, db.CountUnchargedRunsParams{WorkspaceID: ws, Agent: agent, MinID: uuidV7Floor(t.Add(-unchargedLookback)), Since: &t, Reasons: failureReasons})
	return int(n), mapErr(err)
}

// unchargedLookback is how far before the day's start a run can have been created and still count: a run created yesterday that is still
// in flight, or that finished today, belongs to today's count. It is longer than the longest a run can stay active (a run parked for its
// creator's answer holds its credits for 7 days), and it also absorbs any difference between the app's clock and the database's, which
// mints the run ids.
const unchargedLookback = 8 * 24 * time.Hour

// uuidV7Floor is the smallest UUIDv7 a row created at t can have: the 48-bit millisecond timestamp, the version, zeros. Row ids are
// UUIDv7 (the database's uuidv7()), so they sort by creation time and a range on them is a range of time.
func uuidV7Floor(t time.Time) uuid.UUID {
	ms := uint64(max(t.UnixMilli(), 0)) //nolint:gosec // non-negative above
	var u uuid.UUID
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], ms)
	copy(u[:6], b[2:])
	u[6], u[8] = 0x70, 0x80
	return u
}

// ProjectHasChargedEdit says whether the project has an edit that was charged and not refunded: the first edit, or a revision. A run that
// failed was released, not charged, so it does not count. This decides first or revision, and whether a final export may be made.
func (s *Store) ProjectHasChargedEdit(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID) (bool, error) {
	ok, err := s.q.ProjectHasChargedEdit(ctx, db.ProjectHasChargedEditParams{WorkspaceID: ws, ProjectID: pgUUID(project.UUID)})
	return ok, mapErr(err)
}

// RecordCreditActionParams records an action that costs nothing but must be on the ledger (a final export).
type RecordCreditActionParams struct {
	Workspace         ids.WorkspaceID
	Action            string
	ActionID          uuid.UUID
	Project           ids.ProjectID
	Member            ids.UserID
	Plan, ListVersion string
	Now               time.Time
}

// RecordFreeCreditAction writes a zero-credit charge row for the action. It is idempotent on the action id, and needs no lock: it moves
// no credits.
func (s *Store) RecordFreeCreditAction(ctx context.Context, p RecordCreditActionParams) error {
	if p.Workspace.IsZero() || !validCreditAction(p.Action) || p.ActionID == uuid.Nil || p.Now.IsZero() {
		return fmt.Errorf("%w: a free action needs a workspace, a known action, an action id and a time", ErrInvalid)
	}
	_, err := s.q.InsertCreditLedger(ctx, db.InsertCreditLedgerParams{
		WorkspaceID: p.Workspace, Kind: CreditCharge, Credits: 0, Action: &p.Action, ActionID: pgUUID(p.ActionID),
		ProjectID: pgUUID(p.Project.UUID), MemberUserID: pgUUID(p.Member.UUID), Plan: optText(p.Plan), PlanListVersion: optText(p.ListVersion),
		IdempotencyKey: fmt.Sprintf("charge:%s:0", p.ActionID), CreatedAt: p.Now.UTC(),
	})
	if errors.Is(err, errNoRows) {
		return nil
	}
	return mapErr(err)
}

// ---- Allowances and expiry --------------------------------------------------------------------------------------

// AllowanceSpec is what a plan's allowance is, from the price list.
type AllowanceSpec struct {
	Plan    plans.Plan
	Credits int
	// Refill is true for a plan that gets its allowance again every period; a trial gets it once.
	Refill      bool
	ListVersion string
}

// AllowanceResult says what SyncAllowance did.
type AllowanceResult struct {
	// Granted is the credits of the allowance written by this call (0 when the period had been granted already, or the workspace has no
	// active plan).
	Granted int
	Plan    plans.Plan
	// PeriodStart and PeriodEnd are the period the allowance belongs to; zero for a trial's, which has none.
	PeriodStart, PeriodEnd time.Time
	// Replaced is the credits of the old plan's allowance that a change of plan expired.
	Replaced int
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// AddMonths adds n months to t in UTC, keeping the day of the month when the month has it and its last day when not (31 January plus one
// month is 28 February). Periods are computed from the anchor each time, not chained, so the day never drifts down.
func AddMonths(t time.Time, n int) time.Time {
	t = t.UTC()
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	d = min(d, daysIn(first.Year(), first.Month()))
	return time.Date(first.Year(), first.Month(), d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// ManualPeriod returns the monthly period of a manual plan that contains now: the periods start at the anchor and are a month long.
func ManualPeriod(anchor, now time.Time) (start, end time.Time) {
	anchor, now = anchor.UTC(), now.UTC()
	n := (now.Year()-anchor.Year())*12 + int(now.Month()) - int(anchor.Month())
	if AddMonths(anchor, n).After(now) {
		n--
	}
	n = max(n, 0)
	return AddMonths(anchor, n), AddMonths(anchor, n+1)
}

// merchantRenewalGap is how far the end of a merchant plan's period must move for it to count as a renewal (a new period with a new
// allowance) rather than the same billing period with a changed end.
const merchantRenewalGap = 15 * 24 * time.Hour

// SyncAllowance grants the workspace's allowance for the period that contains now, if it has not been granted. It is idempotent: the
// grant's key names the workspace, the plan and the period, so a second call, or a second worker, adds nothing.
//
//   - The plan is the one in workspace_plans, when it is active at now; a workspace with no active plan gets nothing.
//   - A plan managed by the merchant of record has the period that ends at its current_period_end and starts a month earlier. Any other
//     plan (a manual grant) has monthly periods from the day this account first got the plan.
//   - A trial's allowance is granted once and never refilled; it does not expire by itself.
//   - A different plan than the one the account last got replaces the allowance: what is left of the old one expires (an expire row),
//     and the new plan's allowance is granted in full. A manual plan's periods start again from now.
func (s *Store) SyncAllowance(ctx context.Context, ws ids.WorkspaceID, specs map[plans.Plan]AllowanceSpec, now time.Time) (AllowanceResult, error) {
	if ws.IsZero() || now.IsZero() {
		return AllowanceResult{}, fmt.Errorf("%w: an allowance needs a workspace and a time", ErrInvalid)
	}
	now = now.UTC()
	var res AllowanceResult
	err := s.inTx(ctx, func(tx *Store) error {
		res = AllowanceResult{}
		acct, err := tx.lockCreditAccount(ctx, ws)
		if err != nil {
			return err
		}
		wp, err := tx.q.GetWorkspacePlan(ctx, ws)
		if err != nil {
			return err
		}
		plan := plans.Plan(wp.Plan)
		if !plan.Grantable() || wp.Status != string(StatusActive) || (wp.CurrentPeriodEnd != nil && !wp.CurrentPeriodEnd.After(now)) {
			return nil // no active plan at now: no allowance
		}
		spec, ok := specs[plan]
		if !ok {
			return fmt.Errorf("store: the price list has no allowance for plan %q", plan)
		}
		res.Plan = plan

		changed := acct.Plan != "" && acct.Plan != string(plan)
		generation := acct.PlanGeneration
		if changed {
			generation++
			sources, err := tx.creditSources(ctx, ws, now)
			if err != nil {
				return err
			}
			for _, src := range sources {
				if src.Kind != CreditGrantAllowance {
					continue
				}
				reason := fmt.Sprintf("the plan changed from %s to %s", acct.Plan, plan)
				expired, err := tx.expireSourceKeepingHolds(ctx, ws, src.ID, now, now, &reason)
				if err != nil {
					return err
				}
				res.Replaced += expired
			}
		}

		var (
			anchor            = acct.PeriodAnchor
			until             *time.Time
			keyTail           string
			expires           *time.Time
			sameBillingPeriod bool
		)
		switch {
		case !spec.Refill:
			keyTail = "once"
		case PlanSource(wp.Source) == SourceMerchant && wp.CurrentPeriodEnd != nil:
			end := wp.CurrentPeriodEnd.UTC()
			res.PeriodStart, res.PeriodEnd = AddMonths(end, -1), end
			until, expires, keyTail = &end, &end, "until:"+end.Format(time.RFC3339)
			// The period end moved by less than a renewal (a billing-date change, a proration, a pause and resume): still the billing
			// period that was granted, not a new one. A second allowance for it would double the credits of one payment.
			if !changed && acct.Plan == string(plan) && acct.AllowanceUntil != nil && end.Sub(*acct.AllowanceUntil) < merchantRenewalGap {
				sameBillingPeriod = true
			}
		default:
			if anchor == nil || changed {
				a := now
				anchor = &a
			}
			res.PeriodStart, res.PeriodEnd = ManualPeriod(*anchor, now)
			end := res.PeriodEnd
			until, expires, keyTail = &end, &end, "until:"+end.Format(time.RFC3339)
		}
		if generation > 0 && spec.Refill { // a trial is once ever, whatever happened in between
			// After a change of plan the key says so: the plan that comes back within the same period (a downgrade after an upgrade) is
			// granted again, because its replaced allowance was expired.
			keyTail = fmt.Sprintf("g%d:%s", generation, keyTail)
		}
		p := string(plan)
		version := spec.ListVersion
		if !sameBillingPeriod {
			_, err = tx.q.InsertCreditLedger(ctx, db.InsertCreditLedgerParams{
				WorkspaceID: ws, Kind: CreditGrantAllowance, Credits: int32(spec.Credits), //nolint:gosec // a price list value
				Plan: &p, PlanListVersion: &version, ExpiresAt: expires,
				IdempotencyKey: fmt.Sprintf("allowance:%s:%s:%s", ws.UUID, plan, keyTail), CreatedAt: now,
			})
			switch {
			case err == nil:
				res.Granted = spec.Credits
			case errors.Is(err, errNoRows): // this period was granted already
			default:
				return err
			}
		}
		return tx.q.SetCreditAccountPeriod(ctx, db.SetCreditAccountPeriodParams{WorkspaceID: ws, Plan: p, PeriodAnchor: anchor, AllowanceUntil: until, PlanGeneration: generation})
	})
	if err != nil {
		return AllowanceResult{}, mapErr(err)
	}
	return res, nil
}

// expireSource writes the expire row of a source: it takes what is left, exactly (remaining may be 0, which still marks the source as
// expired). The account is locked.
func (s *Store) expireSource(ctx context.Context, ws ids.WorkspaceID, source uuid.UUID, remaining int, at time.Time, reason *string) error {
	_, err := s.q.InsertCreditLedger(ctx, db.InsertCreditLedgerParams{
		WorkspaceID: ws, Kind: CreditExpire, Credits: -int32(max(remaining, 0)), GrantID: pgtype.UUID{Bytes: source, Valid: true}, //nolint:gosec // what a source had
		Reason: reason, IdempotencyKey: "expire:" + source.String(), CreatedAt: at.UTC(),
	})
	if errors.Is(err, errNoRows) {
		return nil
	}
	return err
}

// expireFrozen expires the frozen credits of a hold that is released, replayed or swept: the source they rest on has expired, and nothing
// will take them now. Never more than the source has left (a hold that outlived its own charge has nothing frozen any more). The account
// is locked.
func (s *Store) expireFrozen(ctx context.Context, hold db.CreditHold, now time.Time) error {
	if hold.FrozenCredits == 0 || !hold.FrozenSourceID.Valid {
		return nil
	}
	src, err := s.q.GetCreditSource(ctx, db.GetCreditSourceParams{WorkspaceID: hold.WorkspaceID, ID: hold.FrozenSourceID.Bytes})
	if err != nil {
		return err
	}
	n := min(int(hold.FrozenCredits), int(src.Remaining))
	if n <= 0 {
		return nil
	}
	reason := "the run that held these credits ended without being charged, after their period"
	return s.expireFrozenPart(ctx, hold, n, now, &reason)
}

// expireDueOfWorkspace expires the sources of the workspace whose expiry has passed and that have no expire row yet, keeping what active
// holds rest on (the periodic job does the same for every workspace; a settle does it for its own first). The account is locked.
func (s *Store) expireDueOfWorkspace(ctx context.Context, ws ids.WorkspaceID, now time.Time) error {
	at := now.UTC()
	due, err := s.q.ListDueCreditExpiriesOfWorkspace(ctx, db.ListDueCreditExpiriesOfWorkspaceParams{WorkspaceID: ws, Now: &at})
	if err != nil {
		return err
	}
	reason := "expired"
	for _, d := range due {
		if _, err := s.expireSourceKeepingHolds(ctx, ws, d.ID, *d.ExpiresAt, at, &reason); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) expireFrozenPart(ctx context.Context, hold db.CreditHold, credits int, at time.Time, reason *string) error {
	_, err := s.q.InsertCreditLedger(ctx, db.InsertCreditLedgerParams{
		WorkspaceID: hold.WorkspaceID, Kind: CreditExpire, Credits: -int32(credits), GrantID: hold.FrozenSourceID, //nolint:gosec // what a hold froze
		Reason: reason, IdempotencyKey: fmt.Sprintf("expire:%s:%s", uuid.UUID(hold.FrozenSourceID.Bytes), hold.ActionID), CreatedAt: at.UTC(),
	})
	if errors.Is(err, errNoRows) {
		return nil
	}
	return err
}

// expireSourceKeepingHolds expires what is left of a source at the moment `at` it stopped being usable (its expiry, or the plan change that
// replaced it), except the credits that active holds made before then rest on: those stay in the source, frozen for their holds, and are
// charged to it when the run is charged or expire when it is released (see migration 00020). The holds rest on the sources in the order of
// use, so a hold rests on this one only as far as the sources before it in that order cannot cover the holds. now is when it is done (a
// hold that has ended since is not active any more). It returns the credits it expired. The account is locked.
func (s *Store) expireSourceKeepingHolds(ctx context.Context, ws ids.WorkspaceID, source uuid.UUID, at, now time.Time, reason *string) (int, error) {
	rem, earlier := 0, 0
	sources, err := s.creditSources(ctx, ws, at.Add(-time.Microsecond))
	if err != nil {
		return 0, err
	}
	found := false
	for _, src := range sources {
		if src.ID == source {
			rem, found = src.Remaining, true
			break
		}
		earlier += src.Remaining
	}
	if !found { // not in the list (it had expired by the clock before `at`, or has nothing): all of what is left goes
		src, err := s.q.GetCreditSource(ctx, db.GetCreditSourceParams{WorkspaceID: ws, ID: source})
		if err != nil {
			return 0, err
		}
		rem = int(src.Remaining)
		if err := s.expireSource(ctx, ws, source, rem, at, reason); err != nil {
			return 0, err
		}
		return rem, nil
	}
	holds, err := s.q.ListActiveCreditHolds(ctx, db.ListActiveCreditHoldsParams{WorkspaceID: ws, Now: now.UTC()})
	if err != nil {
		return 0, err
	}
	// The holds that can rest on this source: made before it stopped, with something not frozen yet, and not frozen on another source.
	eligible := func(h db.ListActiveCreditHoldsRow) bool {
		return h.CreatedAt.Before(at) && h.Credits > h.FrozenCredits && (!h.FrozenSourceID.Valid || h.FrozenSourceID.Bytes == source)
	}
	need := 0
	for _, h := range holds {
		if eligible(h) {
			need += int(h.Credits - h.FrozenCredits)
		}
	}
	left := min(max(need-earlier, 0), rem)
	allocated := 0
	for _, h := range holds {
		if left == 0 {
			break
		}
		if !eligible(h) {
			continue
		}
		uncovered := int(h.Credits - h.FrozenCredits)
		f := min(uncovered, left)
		n, err := s.q.FreezeCreditHold(ctx, db.FreezeCreditHoldParams{WorkspaceID: ws, ID: h.ID, SourceID: pgtype.UUID{Bytes: source, Valid: true}, Credits: int32(f)}) //nolint:gosec // a price
		if err != nil {
			return 0, err
		}
		if n == 1 {
			left -= f
			allocated += f
		}
	}
	expired := rem - allocated
	if err := s.expireSource(ctx, ws, source, expired, at, reason); err != nil {
		return 0, err
	}
	return expired, nil
}

// ExpireDueCredits writes the expire row of every source whose expiry has passed and that has none, taking exactly what is left of it.
// It does at most limit sources, oldest expiry first, and returns how many it expired and how many credits they held. Safe to run twice
// and from several workers: each source is handled under its workspace's account lock and the row's key is unique.
func (s *Store) ExpireDueCredits(ctx context.Context, now time.Time, limit int) (sources, credits int, err error) {
	if limit < 1 {
		return 0, 0, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	now = now.UTC()
	due, err := s.q.ListDueCreditExpiries(ctx, db.ListDueCreditExpiriesParams{Now: &now, RowLimit: int32(limit)}) //nolint:gosec // checked above
	if err != nil {
		return 0, 0, mapErr(err)
	}
	for _, d := range due {
		var left int
		err := s.inTx(ctx, func(tx *Store) error {
			left = 0
			if _, err := tx.lockCreditAccount(ctx, d.WorkspaceID); err != nil {
				return err
			}
			if _, err := tx.q.GetCreditLedgerByKey(ctx, db.GetCreditLedgerByKeyParams{WorkspaceID: d.WorkspaceID, IdempotencyKey: "expire:" + d.ID.String()}); err == nil {
				return nil // expired by someone else while we waited
			} else if !errors.Is(err, errNoRows) {
				return err
			}
			reason := "expired"
			// The row is dated at the moment the source stopped being usable, so a period's report lines up with its end. Credits that a run
			// still working holds are left in it until that run is charged or released.
			var err error
			left, err = tx.expireSourceKeepingHolds(ctx, d.WorkspaceID, d.ID, *d.ExpiresAt, now, &reason)
			return err
		})
		if err != nil {
			return sources, credits, mapErr(err)
		}
		sources++
		credits += left
	}
	return sources, credits, nil
}

// WorkspacesNeedingAllowance lists up to limit workspaces whose allowance may need granting at now, with an id above after (the zero ID
// starts the list; pass the last id of a full page to get the next one): SyncAllowance decides for each. Paging by id means a workspace
// that stays on the list (one that fails every time) cannot hide the ones behind it.
func (s *Store) WorkspacesNeedingAllowance(ctx context.Context, now time.Time, after ids.WorkspaceID, limit int) ([]ids.WorkspaceID, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	now = now.UTC()
	rows, err := s.q.ListWorkspacesNeedingAllowance(ctx, db.ListWorkspacesNeedingAllowanceParams{Now: &now, After: after, RowLimit: int32(limit)}) //nolint:gosec // checked above
	if err != nil {
		return nil, mapErr(err)
	}
	return rows, nil
}

// ---- The end of a run -------------------------------------------------------------------------------------------

// runAuthoredVersion says the run made a timeline version (the author id of the editor's versions is the run's TypeID).
func (s *Store) runAuthoredVersion(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID) (bool, error) {
	v, err := s.q.AgentRunTimelineVersion(ctx, db.AgentRunTimelineVersionParams{WorkspaceID: ws, AuthorID: ids.Encode(ids.PrefixRun, run)})
	return v > 0, err
}

// settleRunCredits settles the credit hold of a run that has just reached a final status, in the transaction that put it there: success
// charges, failure releases, and a cancel charges when the run authored a timeline version and releases when it did not. A run with no hold (the
// echo agent) is left alone. A run that parks for input keeps its hold, with a long expiry. s must be a transaction Store.
func (s *Store) settleRunCredits(ctx context.Context, ws ids.WorkspaceID, run AgentRun, now time.Time) error {
	var charge bool
	switch run.Status {
	case AgentSucceeded:
		charge = true
		// A run that ended by saying there was nothing to change (the editor's finish with no_edit_needed) made no version: the creator
		// gets nothing for the credits, so the hold is released, not charged.
		var out struct {
			NoEditNeeded bool `json:"no_edit_needed"`
		}
		if len(run.Output) > 0 && json.Unmarshal(run.Output, &out) == nil && out.NoEditNeeded {
			made, err := s.runAuthoredVersion(ctx, ws, run.ID)
			if err != nil {
				return err
			}
			charge = made
		}
	case AgentFailed:
		charge = false
	case AgentCancelled:
		// The creator keeps what the run made: a cancel is charged when the run authored a timeline version (the work is in the project,
		// and a cancel before the preview must not make a revision free), and released when it did not. A preview alone does not count:
		// render_preview can look at a version the run did not make (an earlier one), and then there is nothing the run gave the creator.
		var err error
		if charge, err = s.runAuthoredVersion(ctx, ws, run.ID); err != nil {
			return err
		}
	case AgentWaitingInput:
		return s.ExtendCreditHold(ctx, ws, run.ID, now.Add(CreditHoldWaitingTTL))
	default:
		return nil
	}
	_, err := s.SettleCredits(ctx, ws, run.ID, charge, now)
	return err
}
