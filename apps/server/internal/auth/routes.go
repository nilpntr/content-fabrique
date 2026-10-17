package auth

import (
	"log/slog"
	"net/http"

	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/nilpntr/content-fabrique/internal/config"
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
}

// Mount registers the auth routes on mux:
//
//	GET  /v1/me              the signed-in user and workspace (behind the middleware)
//	POST /v1/webhooks/clerk  Clerk's Svix-signed webhook (signed, not behind the middleware)
//
// and returns the authentication middleware, which every other /v1 route (except webhooks) goes behind. Without
// CLERK_SECRET_KEY the middleware answers 503 to everything it guards, and the webhook without CLERK_WEBHOOK_SECRET
// answers 503, so a missing secret is never an open door.
func Mount(mux *http.ServeMux, d Deps) (func(http.Handler) http.Handler, error) {
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
		logger.Warn("CLERK_SECRET_KEY is not set: authenticated endpoints answer 503 " + CodeNotConfigured)
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

	mux.Handle("GET /v1/me", mw(MeHandler()))
	mux.Handle("POST /v1/webhooks/clerk", wh)
	return mw, nil
}
