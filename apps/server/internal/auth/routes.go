package auth

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/nilpntr/content-fabrique/internal/assets"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Deps are what Mount needs.
type Deps struct {
	Config config.Config
	Store  *store.Store
	Logger *slog.Logger
	// Directory replaces Clerk's Backend API (tests). Nil means the real one, when CLERK_SECRET_KEY is set.
	Directory Directory
	// JWKSClient replaces the JWKS client of the middleware (tests). Nil means the real one.
	JWKSClient *jwks.Client
	// Assets is the assets service (object storage and the job enqueuer behind it). Nil means object storage is not
	// configured: the assets operations answer 503 storage_not_configured.
	Assets *assets.Service
}

// V1 is the router of the paid-only API. Every route added with Handle sits behind the authentication middleware and
// RequireActivePlan, because Mount put them in front of this router with a catch-all on "/v1/": a request gets 401 without
// a valid token and 402 plan_required without an active plan, whether or not a route matches it, and only then reaches
// the route (or 404, or 405). Add routes here, never on the shared mux.
type V1 struct{ mux *http.ServeMux }

// Handle registers a paid-only route. The pattern is ServeMux's (an optional method, then a path) and the path must start
// with /v1/; anything else is a programming error and panics at start-up.
func (v *V1) Handle(pattern string, h http.Handler) {
	path := pattern
	if _, p, ok := strings.Cut(pattern, " "); ok {
		path = strings.TrimSpace(p)
	}
	if !strings.HasPrefix(path, "/v1/") {
		panic("auth: V1.Handle pattern " + strconv.Quote(pattern) + " is not under /v1/")
	}
	v.mux.Handle(pattern, h)
}

// Mount registers the routes of the API contract (packages/schemas/openapi.yaml) and the webhook on mux:
//
//	GET  /v1/me                the signed-in user, workspace and entitlements (authenticated; open without a plan)
//	GET  /v1/workspace/plan    the plan and what it includes (authenticated; open without a plan)
//	GET  /v1/library/summary   the first paid-only endpoint (authentication, then RequireActivePlan, through the catch-all)
//	POST /v1/webhooks/clerk    Clerk's Svix-signed webhook (signed, not behind the middleware)
//	ANY  /v1/                  everything else: the authentication middleware, RequireActivePlan, then the returned V1
//
// and returns the V1 router that every other /v1 route is added to (see entitlements.go for the allowlist). Without
// CLERK_SECRET_KEY the catch-all answers 503 to everything it guards, and the webhook without CLERK_WEBHOOK_SECRET
// answers 503, so a missing secret is never an open door.
func Mount(mux *http.ServeMux, d Deps) (*V1, error) {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cfg := d.Config

	dir := d.Directory
	if dir == nil && cfg.ClerkSecretKey != "" {
		dir = NewClerkDirectory(cfg.ClerkSecretKey)
	}

	var mw func(http.Handler) http.Handler
	if cfg.ClerkSecretKey == "" && d.JWKSClient == nil {
		logger.Warn("CLERK_SECRET_KEY is not set: authenticated endpoints answer 503 " + string(CodeNotConfigured))
		mw = NotConfigured()
	} else {
		var err error
		mw, err = NewMiddleware(MiddlewareConfig{
			AppOrigin:  cfg.AppOrigin,
			SecretKey:  cfg.ClerkSecretKey,
			JWKSClient: d.JWKSClient,
			Logger:     logger,
		}, NewSyncer(d.Store, dir))
		if err != nil {
			return nil, err
		}
	}

	wh, err := NewWebhookHandler(d.Store, dir, cfg.ClerkWebhookSecret, logger)
	if err != nil {
		return nil, err
	}

	// The routes of the OpenAPI contract, built by the generated code (see api.go). Which middleware each sits behind is
	// decided here and nowhere else: the operations the spec marks x-open-without-plan are open to a workspace without a plan
	// (authentication alone), and every other operation of the spec, including one added later, goes to the paid-only router.
	open, err := openOperations()
	if err != nil {
		return nil, err
	}
	v1 := &V1{mux: http.NewServeMux()}
	srv := newAPIServer(d.Store, logger)
	srv.Handler = assets.NewHandler(d.Assets, callerOf, logger)
	projectsSvc, err := projects.NewService(d.Store, logger)
	if err != nil {
		return nil, err
	}
	srv.API = projects.NewAPI(projectsSvc, projectsCallerOf, logger)
	for pattern, h := range apiRoutes(srv, logger) {
		if open[pattern] {
			mux.Handle(pattern, mw(h))
		} else {
			v1.Handle(pattern, h)
		}
	}
	// Clerk's webhook is Svix-signed and not behind a session, so it is no part of the generated interface.
	mux.Handle("POST /v1/webhooks/clerk", wh)

	// Everything else, also a path no route has: authenticated, then an active plan, then the V1 router.
	mux.Handle("/v1/", mw(RequireActivePlan(d.Store, logger)(v1.mux)))
	return v1, nil
}
