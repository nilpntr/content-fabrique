package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Shutdown budget. Container runtimes send SIGTERM and SIGKILL about ten seconds later, so the worst case
// must stay under that: DefaultDrainDelay (readyz is 503, still serving) + DefaultShutdownTimeout (in-flight
// requests finish) + TelemetryFlushTimeout (pending spans exported) = 1s + 6s + 2s = 9s.
const (
	DefaultDrainDelay      = 1 * time.Second
	DefaultShutdownTimeout = 6 * time.Second
	TelemetryFlushTimeout  = 2 * time.Second
)

// NewMux returns a mux with the health endpoints registered. Later tasks add API routes to it.
func NewMux(h *Health) *http.ServeMux {
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

// Handler wraps next with HTTP server tracing (otelhttp), access logging and panic recovery, outermost first.
// Tracing wraps the access log so log lines carry trace_id and span_id, and the access log wraps recovery so a
// panicked request is logged with status 500. Probe requests (/healthz, /readyz) are not traced.
func Handler(next http.Handler, logger *slog.Logger) http.Handler {
	return otelhttp.NewHandler(accessLog(recoverer(next, logger), logger), "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool { return !isProbe(r) }))
}

// NewServer returns an http.Server with conservative timeouts.
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// ServeOptions tunes Serve. Zero values mean the defaults; a negative DrainDelay means none.
type ServeOptions struct {
	// DrainDelay is how long to keep serving, with /readyz at 503, before closing listeners.
	DrainDelay time.Duration
	// ShutdownTimeout bounds how long in-flight requests get to finish.
	ShutdownTimeout time.Duration
}

// Serve runs srv until it fails or ctx is cancelled. On cancel it flips health to shutting down, waits
// DrainDelay, then stops accepting connections and lets in-flight requests finish within ShutdownTimeout.
// It returns nil after a clean shutdown.
func Serve(ctx context.Context, srv *http.Server, health *Health, opts ServeOptions, logger *slog.Logger) error {
	if opts.DrainDelay == 0 {
		opts.DrainDelay = DefaultDrainDelay
	}
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = DefaultShutdownTimeout
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutdown started", "drain_delay", opts.DrainDelay.String(), "timeout", opts.ShutdownTimeout.String())
	health.SetShuttingDown()
	if opts.DrainDelay > 0 {
		select {
		case <-time.After(opts.DrainDelay):
		case err := <-errCh:
			return err
		}
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		_ = srv.Close()
		if errors.Is(err, context.DeadlineExceeded) {
			logger.Error("forced shutdown after timeout", "timeout", opts.ShutdownTimeout.String())
			return fmt.Errorf("forced shutdown after %s: in-flight requests did not finish: %w", opts.ShutdownTimeout, err)
		}
		return fmt.Errorf("server shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

func isProbe(r *http.Request) bool {
	return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
}

func recoverer(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				logger.ErrorContext(r.Context(), "panic in handler", "panic", rec, "stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// accessLog logs one line per request. Probe endpoints log at debug so they do not drown real traffic.
func accessLog(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if isProbe(r) {
			level = slog.LevelDebug
		}
		logger.Log(r.Context(), level, "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
