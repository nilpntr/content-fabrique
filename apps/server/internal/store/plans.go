package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Plans and the paywall (P0-16). The workspace_plans table is the one source of truth for what a workspace may use.
// Whether a plan is active is decided in SQL with the database clock when it is read, so an expired grant stops
// working without a job and an application clock cannot disagree with the data.

// PlanSource says who set a plan.
type PlanSource string

// The sources the workspace_plans check constraint allows, plus SourceNone, which is reported (never stored) for a
// workspace that has no record at all.
const (
	// SourceManual is `cfctl plan grant`, until billing exists.
	SourceManual PlanSource = "manual"
	// SourceMerchant is the merchant of record's webhooks (P7-01, D-13).
	SourceMerchant PlanSource = "merchant"
	SourceNone     PlanSource = "none"
)

// PlanStatus is the state of a plan record.
type PlanStatus string

// The statuses the check constraint allows, plus two that are reported and never stored: StatusNone for a workspace
// that has no record at all, and StatusUnknown for an answer the API could not get (the plan could not be read).
const (
	StatusActive   PlanStatus = "active"
	StatusPastDue  PlanStatus = "past_due"
	StatusCanceled PlanStatus = "canceled"
	StatusNone     PlanStatus = "none"
	StatusUnknown  PlanStatus = "unknown"
)

// ErrPlanSourceConflict is returned by GrantPlan and RevokePlan when the current record was not set by hand (its source
// is not manual) and Force was not given: the merchant of record owns it, and a hand edit would be overwritten by its
// next webhook or would disagree with what the customer pays.
var ErrPlanSourceConflict = errors.New("store: the plan is managed by the merchant of record")

// Entitlements is what a workspace may use right now.
type Entitlements struct {
	WorkspaceID ids.WorkspaceID
	Plan        plans.Plan
	Status      PlanStatus
	Source      PlanSource
	// CurrentPeriodEnd is when the paid period ends (exclusive); nil means it does not end.
	CurrentPeriodEnd *time.Time
	GrantedBy        string
	// Active is true when the plan is not none, its status is active and its period has not ended, judged by the
	// database clock at the moment of the read. It is the only field the paywall looks at.
	Active bool
}

// Unknown is the entitlements to report when the plan could not be read: never active.
func Unknown(ws ids.WorkspaceID) Entitlements {
	return Entitlements{WorkspaceID: ws, Plan: plans.None, Status: StatusUnknown, Source: SourceNone}
}

// GetEntitlements returns the entitlements of the workspace: no plan (inactive) when it never had one. ErrNotFound
// means the workspace does not exist or was deleted.
func (s *Store) GetEntitlements(ctx context.Context, ws ids.WorkspaceID) (Entitlements, error) {
	r, err := s.q.GetWorkspacePlan(ctx, ws)
	if err != nil {
		return Entitlements{}, mapErr(err)
	}
	return Entitlements{
		WorkspaceID:      r.WorkspaceID,
		Plan:             plans.Plan(r.Plan),
		Status:           PlanStatus(r.Status),
		Source:           PlanSource(r.Source),
		CurrentPeriodEnd: r.CurrentPeriodEnd,
		GrantedBy:        r.GrantedBy,
		Active:           r.Active,
	}, nil
}

// GrantInput is a plan to set on a workspace.
type GrantInput struct {
	WorkspaceID ids.WorkspaceID
	// Plan must be one of plans.Granted: use RevokePlan to take a plan away.
	Plan plans.Plan
	// PeriodEnd is when the plan ends (exclusive: the plan is active before it and not at it); nil means it does not
	// end. A time in the past is allowed (the plan is then inactive from the start); callers that want to refuse it do
	// so themselves.
	PeriodEnd *time.Time
	// Source defaults to SourceManual.
	Source PlanSource
	// GrantedBy is who set it, for the audit trail ("cfctl:alice"). It is stored as given, so it must not be a secret.
	GrantedBy string
	// Force replaces a record whose source is not manual. Without it that is ErrPlanSourceConflict.
	Force bool
}

// GrantPlan sets the plan of the workspace with status active, replacing whatever it had. It touches the row of this
// workspace only. It returns the entitlements as they stand afterwards.
func (s *Store) GrantPlan(ctx context.Context, in GrantInput) (Entitlements, error) {
	if !in.Plan.Grantable() {
		return Entitlements{}, fmt.Errorf("%w: plan %q cannot be granted", ErrInvalid, in.Plan)
	}
	if in.Source == "" {
		in.Source = SourceManual
	}
	if in.Source != SourceManual && in.Source != SourceMerchant {
		return Entitlements{}, fmt.Errorf("%w: plan source %q", ErrInvalid, in.Source)
	}
	if in.WorkspaceID.IsZero() {
		return Entitlements{}, fmt.Errorf("%w: workspace id is required", ErrInvalid)
	}
	var end *time.Time
	if in.PeriodEnd != nil {
		t := in.PeriodEnd.UTC()
		end = &t
	}
	return s.writePlan(ctx, db.UpsertWorkspacePlanParams{
		WorkspaceID: in.WorkspaceID, Plan: string(in.Plan), Status: string(StatusActive), Source: string(in.Source),
		CurrentPeriodEnd: end, GrantedBy: strings.TrimSpace(in.GrantedBy), Force: in.Force,
	})
}

// RevokeInput is a plan to take away from a workspace.
type RevokeInput struct {
	WorkspaceID ids.WorkspaceID
	// Source defaults to SourceManual.
	Source    PlanSource
	GrantedBy string
	// Force replaces a record whose source is not manual. Without it that is ErrPlanSourceConflict.
	Force bool
}

// RevokePlan takes the plan of the workspace away: plan none, status canceled. Revoking a workspace that has no plan
// is fine. It returns the entitlements as they stand afterwards.
func (s *Store) RevokePlan(ctx context.Context, in RevokeInput) (Entitlements, error) {
	if in.Source == "" {
		in.Source = SourceManual
	}
	if in.WorkspaceID.IsZero() {
		return Entitlements{}, fmt.Errorf("%w: workspace id is required", ErrInvalid)
	}
	return s.writePlan(ctx, db.UpsertWorkspacePlanParams{
		WorkspaceID: in.WorkspaceID, Plan: string(plans.None), Status: string(StatusCanceled), Source: string(in.Source),
		GrantedBy: strings.TrimSpace(in.GrantedBy), Force: in.Force,
	})
}

func (s *Store) writePlan(ctx context.Context, p db.UpsertWorkspacePlanParams) (Entitlements, error) {
	n, err := s.q.UpsertWorkspacePlan(ctx, p)
	if err != nil {
		return Entitlements{}, mapErr(err)
	}
	if n == 0 {
		return Entitlements{}, ErrPlanSourceConflict
	}
	return s.GetEntitlements(ctx, p.WorkspaceID)
}

// Person is one user record that a name, e-mail address or Clerk user id matched.
type Person struct {
	ID          ids.UserID
	ClerkUserID string
}

// FindPeople returns the users a person could be: the one user with that Clerk user id, or else every user with that
// e-mail address (any case). Several users can share an e-mail address, so callers that act on the result must handle
// more than one. Deleted users are left out; none matching is an empty result.
func (s *Store) FindPeople(ctx context.Context, person string) ([]Person, error) {
	person = strings.TrimSpace(person)
	if person == "" {
		return nil, fmt.Errorf("%w: a person (e-mail or Clerk user id) is required", ErrInvalid)
	}
	rows, err := s.q.FindPeople(ctx, person)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]Person, 0, len(rows))
	for _, r := range rows {
		out = append(out, Person{ID: r.ID, ClerkUserID: r.ClerkUserID})
	}
	return out, nil
}

// WorkspaceRef describes a workspace for an operator who has to choose between several. The name is user-chosen text.
type WorkspaceRef struct {
	ID         ids.WorkspaceID
	Name       string
	ClerkOrgID string
	CreatedAt  time.Time
	// Role is the user's role in it; Members is how many members it has.
	Role    Role
	Members int
}

// WorkspacesOfUser returns the (not deleted) workspaces a user belongs to, oldest first.
func (s *Store) WorkspacesOfUser(ctx context.Context, user ids.UserID) ([]WorkspaceRef, error) {
	rows, err := s.q.ListWorkspacesOfUser(ctx, user)
	if err != nil {
		return nil, mapErr(err)
	}
	refs := make([]WorkspaceRef, 0, len(rows))
	for _, r := range rows {
		refs = append(refs, WorkspaceRef{ID: r.ID, Name: r.Name, ClerkOrgID: r.ClerkOrgID, CreatedAt: r.CreatedAt, Role: Role(r.Role), Members: int(r.MemberCount)})
	}
	return refs, nil
}
