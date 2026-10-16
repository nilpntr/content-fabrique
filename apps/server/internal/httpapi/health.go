// Package httpapi holds HTTP handlers, middleware and server plumbing shared by the api and worker binaries.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// CheckTimeout bounds how long a single readiness check may run.
const CheckTimeout = 2 * time.Second

// Check is one readiness probe, such as "postgres". It returns nil when the dependency is usable.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Health serves liveness (/healthz) and readiness (/readyz).
//
// Liveness answers 200 for as long as the process can serve a request. Readiness answers 200 when every
// registered check passes, and 503 when one fails or once the process has started shutting down, so a
// load balancer or tunnel stops sending traffic before in-flight requests drain.
type Health struct {
	logger       *slog.Logger
	mu           sync.RWMutex
	checks       []Check
	shuttingDown atomic.Bool
}

// NewHealth returns a Health with the given readiness checks. Later tasks register Postgres and the like.
func NewHealth(logger *slog.Logger, checks ...Check) *Health {
	if logger == nil {
		logger = slog.Default()
	}
	return &Health{logger: logger, checks: checks}
}

// AddCheck registers a readiness check.
func (h *Health) AddCheck(c Check) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks = append(h.checks, c)
}

// SetShuttingDown flips readiness to 503. Call it first when a shutdown signal arrives.
func (h *Health) SetShuttingDown() { h.shuttingDown.Store(true) }

// ShuttingDown reports whether SetShuttingDown was called.
func (h *Health) ShuttingDown() bool { return h.shuttingDown.Load() }

// Register adds GET /healthz and GET /readyz to mux.
func (h *Health) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.Liveness)
	mux.HandleFunc("GET /readyz", h.Readiness)
}

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Liveness always answers 200.
func (h *Health) Liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// Readiness runs the registered checks and answers 200, or 503 when shutting down or a check fails.
// Failure details go to the log, not the response.
func (h *Health) Readiness(w http.ResponseWriter, r *http.Request) {
	if h.ShuttingDown() {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "shutting_down"})
		return
	}

	h.mu.RLock()
	checks := append([]Check(nil), h.checks...)
	h.mu.RUnlock()

	results := make(map[string]string, len(checks))
	status, code := "ok", http.StatusOK
	for _, c := range checks {
		ctx, cancel := context.WithTimeout(r.Context(), CheckTimeout)
		err := c.Fn(ctx)
		cancel()
		if err != nil {
			h.logger.WarnContext(r.Context(), "readiness check failed", "check", c.Name, "error", err)
			results[c.Name] = "fail"
			status, code = "unavailable", http.StatusServiceUnavailable
			continue
		}
		results[c.Name] = "ok"
	}
	if len(results) == 0 {
		results = nil
	}
	writeJSON(w, code, healthResponse{Status: status, Checks: results})
}

// CheckNames returns the registered check names, sorted. Handy for startup logs.
func (h *Health) CheckNames() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	names := make([]string, 0, len(h.checks))
	for _, c := range h.checks {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
