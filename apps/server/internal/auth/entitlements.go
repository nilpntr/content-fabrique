package auth

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The paywall (P0-16). Every /v1 route is PAID-ONLY BY DEFAULT. Mount puts a catch-all on "/v1/" that runs the
// authentication middleware and then RequireActivePlan in front of the router returned as *V1, so a route registered
// with V1.Handle (the only way to add one) is refused with 401 without a valid token and with 402 plan_required when
// the workspace has no active plan, and so is a path that no route has. Nothing else can register under /v1/ in
// this process but Mount itself, and it opens exactly this allowlist, with authentication alone or none at all:
//
//	GET  /v1/me                the web app needs it to learn that the workspace has no plan (it carries the entitlements)
//	GET  /v1/workspace/plan    the plan, its status and what it includes
//	POST /v1/webhooks/clerk    Svix-signed, not behind a session at all
//
// plus the unauthenticated health endpoints (/healthz, /readyz), which are not under /v1. Opening another route to
// everyone is therefore a visible edit of Mount and of this list.
//
// A route registered on the shared mux directly, with a pattern more specific than "/v1/", would bypass the catch-all:
// do not do that. The test TestV1CatchAll covers the shape; code review covers the rest.

// CodePlanRequired is the stable error code of the 402 answer. The web client branches on it.
const CodePlanRequired = "plan_required"

// EntitlementReader reads the entitlements of a workspace. *store.Store is the real one.
type EntitlementReader interface {
	GetEntitlements(ctx context.Context, ws ids.WorkspaceID) (store.Entitlements, error)
}

// Entitlements is the plan of the workspace as the API shows it. The /v1/me body carries it as `entitlements`.
type Entitlements struct {
	Plan   plans.Plan       `json:"plan"`               // none, trial, studio, creator or pro
	Status store.PlanStatus `json:"status"`             // active, past_due, canceled, none (never had a plan) or unknown (could not be read)
	Active bool             `json:"active"`             // the paywall looks at this and nothing else
	End    *time.Time       `json:"current_period_end"` // null: the plan does not end (or there is none)
}

func entitlementsOf(e store.Entitlements) Entitlements {
	return Entitlements{Plan: e.Plan, Status: e.Status, Active: e.Active, End: e.CurrentPeriodEnd}
}

type entitlementsKey struct{}

// ContextWithEntitlements returns a context that carries e, the entitlements read for this request.
func ContextWithEntitlements(ctx context.Context, e store.Entitlements) context.Context {
	return context.WithValue(ctx, entitlementsKey{}, e)
}

// EntitlementsFromContext returns the entitlements the guard read for this request. ok is false outside the guard.
func EntitlementsFromContext(ctx context.Context) (store.Entitlements, bool) {
	e, ok := ctx.Value(entitlementsKey{}).(store.Entitlements)
	return e, ok
}

// readEntitlements returns the entitlements of the principal's workspace: the ones the guard already read for this
// request when they are for this workspace, else one read from Postgres (with the database clock, on every request, so a
// revoked or expired plan stops working at once). A request reads the plan once.
func readEntitlements(ctx context.Context, ents EntitlementReader, p Principal) (store.Entitlements, error) {
	if e, ok := EntitlementsFromContext(ctx); ok && e.WorkspaceID == p.WorkspaceID {
		return e, nil
	}
	return ents.GetEntitlements(ctx, p.WorkspaceID)
}

// loadEntitlements is the one way a handler or the guard gets the plan, or the error answer: 401 without a Principal, 500
// (logged, nothing leaked) when the plan cannot be read. ok is false when the answer was written.
func loadEntitlements(w http.ResponseWriter, r *http.Request, ents EntitlementReader, logger *slog.Logger) (store.Entitlements, bool) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, CodeUnauthenticated, "sign in to use the API")
		return store.Entitlements{}, false
	}
	e, err := readEntitlements(r.Context(), ents, p)
	if err != nil {
		logger.ErrorContext(r.Context(), "reading the workspace plan failed", "error", err)
		httpapi.WriteError(w, http.StatusInternalServerError, CodeInternal, "something went wrong")
		return store.Entitlements{}, false
	}
	return e, true
}

// RequireActivePlan returns the middleware that refuses a request whose workspace has no active plan. It must run
// after the authentication middleware (it reads the Principal). It fails closed: a request that is not authenticated
// is 401, and a failure to read the plan is 500, never a pass. It leaves the entitlements it read on the request
// context, so nothing behind it reads them again.
func RequireActivePlan(ents EntitlementReader, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e, ok := loadEntitlements(w, r, ents, logger)
			if !ok {
				return
			}
			if !e.Active {
				httpapi.WriteError(w, http.StatusPaymentRequired, CodePlanRequired, "this workspace has no active plan")
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithEntitlements(r.Context(), e)))
		})
	}
}

// WorkspacePlanResponse is the body of GET /v1/workspace/plan.
type WorkspacePlanResponse struct {
	Entitlements
	Source store.PlanSource `json:"source"` // manual, merchant, or none when the workspace never had a plan
	Limits plans.Limits     `json:"limits"` // what the plan includes; all zero without an active plan
}

// WorkspacePlanHandler is GET /v1/workspace/plan: the plan of the caller's workspace and its limits. It is on the
// allowlist (it answers for a workspace without a plan too). Behind the authentication middleware.
func WorkspacePlanHandler(ents EntitlementReader, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, ok := loadEntitlements(w, r, ents, logger)
		if !ok {
			return
		}
		limits := plans.Limits{}
		if e.Active {
			limits = plans.LimitsOf(e.Plan)
		}
		httpapi.WriteJSON(w, http.StatusOK, WorkspacePlanResponse{Entitlements: entitlementsOf(e), Source: e.Source, Limits: limits})
	})
}

// LibrarySummaryResponse is the body of GET /v1/library/summary.
type LibrarySummaryResponse struct {
	Assets   int `json:"assets"`
	Projects int `json:"projects"`
}

// LibrarySummaryHandler is GET /v1/library/summary, the first paid-only endpoint. There is no library yet (P1), so the
// counts are zero; it exists to prove the paywall on the API. Register it with V1.Handle, which puts the
// authentication middleware and RequireActivePlan in front of it.
func LibrarySummaryHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpapi.WriteJSON(w, http.StatusOK, LibrarySummaryResponse{})
	})
}
