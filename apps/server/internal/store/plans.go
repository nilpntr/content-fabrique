package store

import (
	"context"
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

// The sources the workspace_plans check constraint allows.
const (
	// SourceManual is `cfctl plan grant`, until billing exists.
	SourceManual PlanSource = "manual"
	// SourceMerchant is the merchant of record's webhooks (P7-01, D-13).
	SourceMerchant PlanSource = "merchant"
)

// PlanStatus is the state of a plan record.
type PlanStatus string

// The statuses the check constraint allows, plus StatusNone, which is reported (never stored) for a workspace that has
// no record at all.
const (
	StatusActive   PlanStatus = "active"
	StatusPastDue  PlanStatus = "past_due"
	StatusCanceled PlanStatus = "canceled"
	StatusNone     PlanStatus = "none"
)

// Entitlements is what a workspace may use right now.
type Entitlements struct {
	WorkspaceID ids.WorkspaceID
	Plan        plans.Plan
	Status      PlanStatus
	Source      PlanSource
	// CurrentPeriodEnd is when the paid period ends; nil means it does not end.
	CurrentPeriodEnd *time.Time
	GrantedBy        string
	// Active is true when the plan is not none, its status is active and its period has not ended, judged by the
	// database clock at the moment of the read. It is the only field the paywall looks at.
	Active bool
}

// GetEntitlements returns the entitlements of the workspace: no plan (inactive) when it never had one. ErrNotFound
// means the workspace itself does not exist.
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
	// PeriodEnd is when the plan ends; nil means it does not. A time in the past is allowed (the plan is then
	// inactive from the start); callers that want to refuse it do so themselves.
	PeriodEnd *time.Time
	// Source defaults to SourceManual.
	Source PlanSource
	// GrantedBy is who set it, for the audit trail ("cfctl:alice"). It is stored as given, so it must not be a secret.
	GrantedBy string
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
	if err := s.q.UpsertWorkspacePlan(ctx, db.UpsertWorkspacePlanParams{
		WorkspaceID: in.WorkspaceID, Plan: string(in.Plan), Status: string(StatusActive), Source: string(in.Source),
		CurrentPeriodEnd: end, GrantedBy: strings.TrimSpace(in.GrantedBy),
	}); err != nil {
		return Entitlements{}, mapErr(err)
	}
	return s.GetEntitlements(ctx, in.WorkspaceID)
}

// RevokePlan takes the plan of the workspace away: plan none, status canceled. Revoking a workspace that has no plan
// is fine. It returns the entitlements as they stand afterwards.
func (s *Store) RevokePlan(ctx context.Context, ws ids.WorkspaceID, source PlanSource, grantedBy string) (Entitlements, error) {
	if source == "" {
		source = SourceManual
	}
	if ws.IsZero() {
		return Entitlements{}, fmt.Errorf("%w: workspace id is required", ErrInvalid)
	}
	if err := s.q.UpsertWorkspacePlan(ctx, db.UpsertWorkspacePlanParams{
		WorkspaceID: ws, Plan: string(plans.None), Status: string(StatusCanceled), Source: string(source),
		GrantedBy: strings.TrimSpace(grantedBy),
	}); err != nil {
		return Entitlements{}, mapErr(err)
	}
	return s.GetEntitlements(ctx, ws)
}

// WorkspaceRef names a workspace for an operator: enough to choose between several, nothing private.
type WorkspaceRef struct {
	ID         ids.WorkspaceID
	Name       string
	ClerkOrgID string
}

// WorkspacesOfPerson returns the workspaces that a person belongs to, found by e-mail address (any case) or Clerk
// user id. known is false when no user matches at all, which tells "this person has never signed in" apart from "in no
// workspace". Deleted users and workspaces are left out.
func (s *Store) WorkspacesOfPerson(ctx context.Context, person string) (refs []WorkspaceRef, known bool, err error) {
	person = strings.TrimSpace(person)
	if person == "" {
		return nil, false, fmt.Errorf("%w: a person (e-mail or Clerk user id) is required", ErrInvalid)
	}
	known, err = s.q.PersonExists(ctx, person)
	if err != nil {
		return nil, false, mapErr(err)
	}
	rows, err := s.q.ListWorkspacesOfPerson(ctx, person)
	if err != nil {
		return nil, false, mapErr(err)
	}
	for _, r := range rows {
		refs = append(refs, WorkspaceRef{ID: r.ID, Name: r.Name, ClerkOrgID: r.ClerkOrgID})
	}
	return refs, known, nil
}
