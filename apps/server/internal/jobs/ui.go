package jobs

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"riverqueue.com/riverui"
)

// UIUser is the HTTP Basic user name of River UI. The password is RIVER_UI_PASSWORD.
const UIUser = "river"

// ErrNoUIPassword is returned when River UI is asked for without a password: it can retry and delete jobs, so it
// is never served open.
var ErrNoUIPassword = errors.New("RIVER_UI_PASSWORD is empty: refusing to serve River UI without a password")

// UIHandler is River UI behind HTTP Basic auth.
type UIHandler struct {
	ui   *riverui.Handler
	auth http.Handler
}

// NewUIHandler builds River UI over the worker's client, behind HTTP Basic auth with UIUser and password. An empty
// password is an error (ErrNoUIPassword). Call Start before serving.
func NewUIHandler(client *river.Client[pgx.Tx], password string, logger *slog.Logger) (*UIHandler, error) {
	if password == "" {
		return nil, ErrNoUIPassword
	}
	if logger == nil {
		logger = slog.Default()
	}
	ui, err := riverui.NewHandler(&riverui.HandlerOpts{
		Endpoints: riverui.NewEndpoints(client, nil),
		Logger:    logger,
	})
	if err != nil {
		return nil, fmt.Errorf("river ui: %w", err)
	}
	return &UIHandler{ui: ui, auth: basicAuth(ui, UIUser, password)}, nil
}

// Start starts the background services of River UI (its caches). They stop when ctx is cancelled.
func (h *UIHandler) Start(ctx context.Context) error { return h.ui.Start(ctx) }

// ServeHTTP implements http.Handler.
func (h *UIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.auth.ServeHTTP(w, r) }

// basicAuth answers 401 unless the request carries the user and password. Both are compared in constant time, over
// SHA-256 digests so the comparison does not depend on their lengths, and both comparisons always run so a wrong user
// name takes as long as a wrong password.
func basicAuth(next http.Handler, user, password string) http.Handler {
	wantUser, wantPass := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		gotUser, gotPass := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
		userOK := subtle.ConstantTimeCompare(gotUser[:], wantUser[:]) == 1
		passOK := subtle.ConstantTimeCompare(gotPass[:], wantPass[:]) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="River UI", charset="UTF-8"`)
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// NewUIServer returns the http.Server of River UI on addr, with timeouts.
func NewUIServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
