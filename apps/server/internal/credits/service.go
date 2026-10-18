package credits

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// EditorAgent is the name of the agent whose runs are priced as edits (editor.Name; a test keeps the two equal).
const EditorAgent = "editor"

// BillsEdits reports whether runs of the agent are priced as edits and need a credit hold.
func BillsEdits(agent string) bool { return agent == EditorAgent }

// Errors of the service.
var (
	// ErrNoPlan is returned when the workspace has no active plan: no price, no budget. The API's paywall answers first, so this is a
	// race with a revoke, or the CLI on a workspace that was never granted a plan.
	ErrNoPlan = errors.New("credits: the workspace has no active plan")
	// ErrNotConfigured is returned when an editor run is started by a runtime that was given no credits service. It fails closed: no
	// editor run is ever started without a hold.
	ErrNotConfigured = errors.New("credits: this runtime has no credits service, so an editor run cannot be started")
)

// UnchargedFailureReasons are the failure reasons (agent_runs.failure_reason, the runtime's Fail* constants; a runtime test keeps the names
// in step) of a run that failed for a reason of the creator's own, so that it counts toward the cap on runs that end without a charge:
//   - refusal: the model declined to work on the content;
//   - context_window_exceeded: the conversation (the footage, the instructions) no longer fits the model;
//   - invalid_input, context_failed: the instructions or the project's data could not be used.
//
// A failure that is ours does not count, so a bad day of the model API or of our own services never locks a creator out: budget (the run
// budget comes from the plan's price list, so running out of it is our sizing, not the creator's behaviour; budget fitting and OnLimit end
// such a run gracefully once a preview exists), model_error,
// model_access, model_outcome_unknown, max_tokens (the model could not fit its answer), event_too_large, internal_error, ledger_error,
// corrupt_log, unknown_agent, too_many_resumes, unexpected_stop_reason, request_cap, replay_miss, and the spend caps (daily_cap,
// platform_spend_paused, spend_cap_busy), which already stop the spending.
var UnchargedFailureReasons = []string{"refusal", "context_window_exceeded", "invalid_input", "context_failed"}

// ErrTooManyUncharged is wrapped by *TooManyUnchargedError.
var ErrTooManyUncharged = errors.New("credits: too many runs ended without a charge today")

// TooManyUnchargedError says how many runs that ended without a charge the workspace may have per UTC day, and that it has had them.
type TooManyUnchargedError struct{ Cap int }

func (e *TooManyUnchargedError) Error() string {
	return fmt.Sprintf("credits: the workspace has had %d editor runs today that spent money and ended without a charge, which is the most per day", e.Cap)
}

// Is makes errors.Is(err, ErrTooManyUncharged) true.
func (e *TooManyUnchargedError) Is(target error) bool { return target == ErrTooManyUncharged }

// Service decides what an action costs and sets the credits aside for it. The money is in the store.
type Service struct {
	Store *store.Store
	List  *PriceList
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// NewService returns a Service over the store and a price list.
func NewService(st *store.Store, list *PriceList) *Service { return &Service{Store: st, List: list} }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Quote is what an action will cost, decided on the server.
type Quote struct {
	Action  Action
	Credits int
	// BudgetMicros is the hard budget of the run, from the plan's COGS budget for the action. 0 means "keep the caller's budget": a run
	// started without credits (development) is not capped by a plan it may not have.
	BudgetMicros int64
	// Plan is the workspace's plan at the time of the quote ("" when there is none and the run is free).
	Plan        plans.Plan
	ListVersion string
	// UnchargedCap is the plan's cap on runs that end without a charge per UTC day (CheckUncharged); 0 for a free run, which has none.
	UnchargedCap int
	// BillingMode is the plan's mode ("" for a free run): a BYOK run must be billed to the workspace's own key (RequiredBilling).
	BillingMode BillingMode
	// Free is true for a run started without credits: it still gets a hold (of zero credits), so that it is recorded as a charged edit.
	Free bool
}

// QuoteEdit decides which edit this run is, what it costs and what budget it gets. It locks the project's row first (the caller has
// usually done so already), so two requests for one project cannot both be the first edit.
//
// The first edit is the run on a project that has no charged edit yet. A run that failed was released and never charged, so after a failed
// first run the next one is the first edit again; a run that was cancelled after it made a version was charged, so the next one is a revision (a cancel after a preview of earlier work was released).
//
// With free set (cfctl agent run --no-credits) the workspace need not have a plan and the run costs no credits.
func (s *Service) QuoteEdit(ctx context.Context, tx *store.Store, ws ids.WorkspaceID, project ids.ProjectID, free bool) (Quote, error) {
	if _, err := tx.GetProjectForUpdate(ctx, ws, project); err != nil {
		return Quote{}, err
	}
	charged, err := tx.ProjectHasChargedEdit(ctx, ws, project)
	if err != nil {
		return Quote{}, err
	}
	action := EditFirst
	if charged {
		action = EditRevision
	}
	q := Quote{Action: action, ListVersion: s.List.Version, Free: free}
	ent, err := tx.GetEntitlements(ctx, ws)
	if err != nil {
		return Quote{}, err
	}
	if !ent.Active {
		if free {
			return q, nil
		}
		return Quote{}, ErrNoPlan
	}
	if _, ok := s.List.Plan(ent.Plan); !ok {
		return Quote{}, fmt.Errorf("credits: the price list %s has no plan %q", s.List.Version, ent.Plan)
	}
	q.Plan = ent.Plan
	if free {
		return q, nil
	}
	q.BillingMode = s.List.Plans[ent.Plan].BillingMode
	q.UnchargedCap = s.List.Plans[ent.Plan].UnchargedRunsPerDay
	q.Credits = s.List.Credits[action]
	q.BudgetMicros = s.List.Plans[ent.Plan].COGSBudgetMicros[action]
	return q, nil
}

// Hold sets the credits of the quote aside for the run, inside the transaction that creates the run. It returns an error satisfying
// errors.Is(err, store.ErrInsufficientCredits) when the workspace cannot afford it.
func (s *Service) Hold(ctx context.Context, tx *store.Store, q Quote, run store.AgentRun) (store.CreditHold, error) {
	var member ids.UserID
	if run.StartedBy.Valid {
		member = ids.UserID{UUID: uuid.UUID(run.StartedBy.Bytes)}
	}
	return tx.HoldCredits(ctx, store.HoldCreditsParams{
		Workspace: run.WorkspaceID, Action: string(q.Action), ActionID: run.ID, Credits: q.Credits,
		Project: run.ProjectID, Member: member, Plan: string(q.Plan), ListVersion: q.ListVersion,
		Now: s.now(), TTL: store.CreditHoldQueuedTTL,
	})
}

// CheckUncharged refuses the run that was just created (and has its hold) when the workspace has too many runs that end without a charge.
// A run that is cancelled before it made anything, finds nothing to change, or fails for the creator's own reason is released, not
// charged, but it has spent model money. The workspace's runs in flight count too (they may all end that way, and nothing else bounds how
// many it can start at once), and so does the new run itself: the cap is the number of such runs per UTC day including this one. It is
// checked after the hold, so a workspace that is out of credits is told so (402) rather than to wait for tomorrow (429). A free run has no
// cap. The caller rolls the transaction back on an error. It must run after Hold in the same transaction: the hold's lock on the credit
// account is what serialises one workspace's concurrent starts, so that the count it reads includes the runs of the starts before it.
func (s *Service) CheckUncharged(ctx context.Context, tx *store.Store, ws ids.WorkspaceID, q Quote) error {
	if q.UnchargedCap <= 0 {
		return nil
	}
	now := s.now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	n, err := tx.CountUnchargedRuns(ctx, ws, EditorAgent, day, UnchargedFailureReasons)
	if err != nil {
		return err
	}
	if n > q.UnchargedCap {
		return &TooManyUnchargedError{Cap: q.UnchargedCap}
	}
	return nil
}

// Errors of RecordFinalExport.
var (
	// ErrEditNotCharged is returned for a project that has no charged edit.
	ErrEditNotCharged = errors.New("credits: the project has no charged edit, so it cannot be exported")
	// ErrVersionNotCharged is returned for a version that contains agent work that was not charged (a run that failed or was released).
	ErrVersionNotCharged = errors.New("credits: the version contains agent work that was not charged, so it cannot be exported")
)

// RecordFinalExport is called, inside the transaction that queues a final render of a version, for the export.final action. The final
// render of an edit the creator paid for: the project needs a charged edit, and the version must be covered by payment (the last agent
// work in it belongs to a charged run; a person's edits on top of a charged version are free, and previews of any version are never
// refused). It records the export on the ledger at the price of the list (0 credits). It takes no lock on the credit account and never
// refuses for lack of credits: an export costs nothing.
func (s *Service) RecordFinalExport(ctx context.Context, tx *store.Store, ws ids.WorkspaceID, project ids.ProjectID, member ids.UserID, render uuid.UUID, version int) error {
	charged, err := tx.ProjectHasChargedEdit(ctx, ws, project)
	if err != nil {
		return err
	}
	if !charged {
		return ErrEditNotCharged
	}
	covered, err := tx.VersionCovered(ctx, ws, project, version)
	if err != nil {
		return err
	}
	if !covered {
		return ErrVersionNotCharged
	}
	price := s.List.Credits[ExportFinal]
	if price != 0 {
		// The price list says an export is free. A list that charges for it needs the hold-and-settle path; refuse to be quietly wrong.
		return fmt.Errorf("credits: price list %s charges %d credits for %s, which this build records but does not charge", s.List.Version, price, ExportFinal)
	}
	var plan plans.Plan
	if ent, err := tx.GetEntitlements(ctx, ws); err == nil && ent.Active {
		plan = ent.Plan
	}
	return tx.RecordFreeCreditAction(ctx, store.RecordCreditActionParams{
		Workspace: ws, Action: string(ExportFinal), ActionID: render, Project: project, Member: member,
		Plan: string(plan), ListVersion: s.List.Version, Now: s.now(),
	})
}

// SyncAllowance grants the workspace's allowance for the current period (see store.SyncAllowance).
func (s *Service) SyncAllowance(ctx context.Context, ws ids.WorkspaceID) (store.AllowanceResult, error) {
	return s.Store.SyncAllowance(ctx, ws, s.List.AllowanceSpecs(), s.now())
}

// SyncListSwitch is the one-off step after the price list changed under a workspace that already has its allowance (see
// store.TopUpAllowanceToList): the credits the new list adds to the allowance it was granted. Idempotent. It returns the credits added.
func (s *Service) SyncListSwitch(ctx context.Context, ws ids.WorkspaceID) (int, error) {
	return s.Store.TopUpAllowanceToList(ctx, ws, s.List.AllowanceSpecs(), s.now())
}

// TopUpExpiry is when a top-up granted at t expires.
func (s *Service) TopUpExpiry(t time.Time) time.Time {
	return store.AddMonths(t, s.List.TopUpExpiryMonths)
}

// LoadDefault loads the price list the code runs on (ListFile) from the embedded config.
func LoadDefault() (*PriceList, error) { return LoadPriceList(configfiles.Plans(), ListFile) }
