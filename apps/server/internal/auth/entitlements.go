package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The paywall (P0-16). Every /v1 route is PAID-ONLY BY DEFAULT. Mount puts a catch-all on "/v1/" that runs the
// authentication middleware and then RequireActivePlan in front of the router returned as *V1, so a route registered
// with V1.Handle (Mount does this for every operation of the OpenAPI spec that is not on the allowlist) is refused with 401 without a valid token and with 402 plan_required when
// the workspace has no active plan, and so is a path that no route has. Nothing else can register under /v1/ in
// this process but Mount itself, and it opens exactly this allowlist (the operations marked x-open-without-plan in packages/schemas/openapi.yaml), with authentication alone or none at all:
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
const CodePlanRequired = api.ErrorCodePlanRequired

// EntitlementReader reads the entitlements of a workspace. *store.Store is the real one.
type EntitlementReader interface {
	GetEntitlements(ctx context.Context, ws ids.WorkspaceID) (store.Entitlements, error)
}

// entitlementsOf is the plan of the workspace as the API shows it (api.Entitlements, which /v1/me carries as `entitlements`).
func entitlementsOf(e store.Entitlements) api.Entitlements {
	return api.Entitlements{Plan: api.PlanName(e.Plan), Status: api.PlanStatus(e.Status), Active: e.Active, CurrentPeriodEnd: e.CurrentPeriodEnd}
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

// failure is an error answer in the making: the status, the stable code and the message of WriteError.
type failure struct {
	status  int
	code    api.ErrorCode
	message string
}

// loadEntitlementsFor is the one way a handler or the guard gets the plan, or the failure to answer with: 401 without a
// Principal, 500 (logged, nothing leaked) when the plan cannot be read.
func loadEntitlementsFor(ctx context.Context, ents EntitlementReader, logger *slog.Logger) (store.Entitlements, *failure) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return store.Entitlements{}, &failure{http.StatusUnauthorized, CodeUnauthenticated, "sign in to use the API"}
	}
	e, err := readEntitlements(ctx, ents, p)
	if err != nil {
		logger.ErrorContext(ctx, "reading the workspace plan failed", "error", err)
		return store.Entitlements{}, &failure{http.StatusInternalServerError, CodeInternal, "something went wrong"}
	}
	return e, nil
}

// loadEntitlements is loadEntitlementsFor for middleware: it writes the failure. ok is false when the answer was written.
func loadEntitlements(w http.ResponseWriter, r *http.Request, ents EntitlementReader, logger *slog.Logger) (store.Entitlements, bool) {
	e, f := loadEntitlementsFor(r.Context(), ents, logger)
	if f != nil {
		httpapi.WriteError(w, f.status, f.code, f.message)
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

// GetWorkspacePlan is GET /v1/workspace/plan: the plan of the caller's workspace and its limits. It is on the
// allowlist (it answers for a workspace without a plan too). Behind the authentication middleware.
func (s *apiServer) GetWorkspacePlan(ctx context.Context, _ api.GetWorkspacePlanRequestObject) (api.GetWorkspacePlanResponseObject, error) {
	e, f := loadEntitlementsFor(ctx, s.ents, s.logger)
	if f != nil {
		switch f.status {
		case http.StatusUnauthorized:
			return api.GetWorkspacePlan401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(httpapi.ErrorBody(f.code, f.message))}, nil
		case http.StatusInternalServerError:
			return api.GetWorkspacePlan500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(httpapi.ErrorBody(f.code, f.message))}, nil
		default:
			// A status this operation does not document (the spec lists 401 and 500 here): the generated error handler
			// answers 500 internal_error and logs it, so a new kind of failure is never reported as another one.
			return nil, fmt.Errorf("loading the plan failed with undocumented status %d", f.status)
		}
	}
	limits := plans.Limits{}
	if e.Active {
		limits = plans.LimitsOf(e.Plan)
	}
	en := entitlementsOf(e)
	return api.GetWorkspacePlan200JSONResponse{
		Plan:             en.Plan,
		Status:           en.Status,
		Active:           en.Active,
		CurrentPeriodEnd: en.CurrentPeriodEnd,
		Source:           api.PlanSource(e.Source),
		Limits:           api.PlanLimits{Exports: limits.Exports, StorageGb: limits.StorageGB, AiBilling: api.AiBilling(limits.AIBilling)},
	}, nil
}

// GetLibrarySummary is GET /v1/library/summary, the first paid-only endpoint. There is no library yet (P1), so the
// counts are zero; it exists to prove the paywall on the API. It is not on the allowlist, so Mount puts the authentication
// middleware and RequireActivePlan in front of it.
func (s *apiServer) GetLibrarySummary(context.Context, api.GetLibrarySummaryRequestObject) (api.GetLibrarySummaryResponseObject, error) {
	return api.GetLibrarySummary200JSONResponse{}, nil
}
