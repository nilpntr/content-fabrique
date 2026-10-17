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
	// BillingMode is the plan's mode ("" for a free run): a BYOK run must be billed to the workspace's own key (RequiredBilling).
	BillingMode BillingMode
	// Free is true for a run started without credits: it still gets a hold (of zero credits), so that it is recorded as a charged edit.
	Free bool
}

// QuoteEdit decides which edit this run is, what it costs and what budget it gets. It locks the project's row first (the caller has
// usually done so already), so two requests for one project cannot both be the first edit.
//
// The first edit is the run on a project that has no charged edit yet. A run that failed was released and never charged, so after a failed
// first run the next one is the first edit again; a run that was cancelled after a preview was charged, so the next one is a revision.
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

// TopUpExpiry is when a top-up granted at t expires.
func (s *Service) TopUpExpiry(t time.Time) time.Time {
	return store.AddMonths(t, s.List.TopUpExpiryMonths)
}

// LoadDefault loads the price list the code runs on (ListFile) from the embedded config.
func LoadDefault() (*PriceList, error) { return LoadPriceList(configfiles.Plans(), ListFile) }
