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

// The paywall (P0-16). An authenticated /v1 route is PAID-ONLY BY DEFAULT: the guard Mount returns checks the plan of
// the caller's workspace after it has checked the token, and refuses with 402 plan_required when the workspace has no
// active plan. A route is open to a signed-in user without a plan only if it is on this allowlist, which is the whole
// list and is mounted explicitly in Mount:
//
//	GET  /v1/me                the web app needs it to learn that the workspace has no plan (it carries the entitlements)
//	GET  /v1/workspace/plan    the plan, its status and what it includes
//	POST /v1/webhooks/clerk    Svix-signed, not behind a session at all
//
// plus the unauthenticated health endpoints (/healthz, /readyz), which are not under /v1. A new route added by a later
// task therefore starts as paid-only, and opening it to everyone is a visible decision in this list.

// CodePlanRequired is the stable error code of the 402 answer. The web client branches on it.
const CodePlanRequired = "plan_required"

// EntitlementReader reads the entitlements of a workspace. *store.Store is the real one.
type EntitlementReader interface {
	GetEntitlements(ctx context.Context, ws ids.WorkspaceID) (store.Entitlements, error)
}

// Entitlements is the plan of the workspace as the API shows it. The /v1/me body carries it as `entitlements`.
type Entitlements struct {
	Plan   plans.Plan       `json:"plan"`               // none, trial, studio, creator or pro
	Status store.PlanStatus `json:"status"`             // active, past_due, canceled, or none when the workspace never had a plan
	Active bool             `json:"active"`             // the paywall looks at this and nothing else
	End    *time.Time       `json:"current_period_end"` // null: the plan does not end (or there is none)
}

func entitlementsOf(e store.Entitlements) Entitlements {
	return Entitlements{Plan: e.Plan, Status: e.Status, Active: e.Active, End: e.CurrentPeriodEnd}
}

// RequireActivePlan returns the middleware that refuses a request whose workspace has no active plan. It must run
// after the authentication middleware (it reads the Principal). It fails closed: a request that is not authenticated
// is 401, and a failure to read the plan is 500, never a pass.
func RequireActivePlan(ents EntitlementReader, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFromContext(r.Context())
			if !ok {
				httpapi.WriteError(w, http.StatusUnauthorized, CodeUnauthenticated, "sign in to use the API")
				return
			}
			// Read on every request, from Postgres, with the database clock: a revoked or expired plan stops working at once.
			e, err := ents.GetEntitlements(r.Context(), p.WorkspaceID)
			if err != nil {
				logger.ErrorContext(r.Context(), "reading the workspace plan failed", "error", err)
				httpapi.WriteError(w, http.StatusInternalServerError, CodeInternal, "something went wrong")
				return
			}
			if !e.Active {
				httpapi.WriteError(w, http.StatusPaymentRequired, CodePlanRequired, "this workspace has no active plan")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WorkspacePlanResponse is the body of GET /v1/workspace/plan.
type WorkspacePlanResponse struct {
	Entitlements
	Source store.PlanSource `json:"source"`
	Limits plans.Limits     `json:"limits"` // what the plan includes; all zero without a plan
}

// WorkspacePlanHandler is GET /v1/workspace/plan: the plan of the caller's workspace and its limits. It is on the
// allowlist (it answers for a workspace without a plan too). Behind the authentication middleware.
func WorkspacePlanHandler(ents EntitlementReader, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, http.StatusUnauthorized, CodeUnauthenticated, "sign in to use the API")
			return
		}
		e, err := ents.GetEntitlements(r.Context(), p.WorkspaceID)
		if err != nil {
			logger.ErrorContext(r.Context(), "reading the workspace plan failed", "error", err)
			httpapi.WriteError(w, http.StatusInternalServerError, CodeInternal, "something went wrong")
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
// counts are zero; it exists to prove the paywall on the API. Behind the authentication middleware and
// RequireActivePlan, both applied by Mount.
func LibrarySummaryHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := PrincipalFromContext(r.Context()); !ok {
			httpapi.WriteError(w, http.StatusUnauthorized, CodeUnauthenticated, "sign in to use the API")
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, LibrarySummaryResponse{})
	})
}
