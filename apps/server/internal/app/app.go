// Package app is the shared process bootstrap for the api and worker binaries: configuration, signal handling,
// telemetry, the health/mux/server wiring and graceful shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
	"github.com/nilpntr/content-fabrique/internal/version"
)

// Options describes one HTTP-serving process.
type Options struct {
	// Service is the telemetry service name, e.g. telemetry.ServiceAPI.
	Service string
	// Addr picks the listen address from the loaded config.
	Addr func(config.Config) string
	// Routes optionally registers extra routes and readiness checks before the server starts. st is the process's
	// Postgres store (the app role, over DATABASE_URL), already registered as the "postgres" readiness check. An error
	// stops the process before it listens.
	Routes func(cfg config.Config, mux *http.ServeMux, health *httpapi.Health, logger *slog.Logger, st *store.Store) error
	// Background optionally starts background work (the worker's River client) after Routes and before the server
	// listens; it may register readiness checks on health. The stop function it returns is called when shutdown
	// begins, at the same time as the HTTP drain, with a context that is never cancelled: the work bounds itself
	// (jobs.StopTimeout, inside the budget of package httpapi). An error from stop is returned by Run. An error from
	// Background itself stops the process before it listens.
	Background func(ctx context.Context, cfg config.Config, logger *slog.Logger, st *store.Store, health *httpapi.Health) (stop func(ctx context.Context) error, err error)
	// Middleware optionally wraps the whole mux (CORS, for the api), inside tracing and the access log and outside
	// the routes, so it also covers the 404s and 405s of the mux.
	Middleware func(cfg config.Config) func(http.Handler) http.Handler
}

// Run loads config, sets up telemetry, serves HTTP until SIGINT or SIGTERM, then shuts down gracefully
// within the budget documented in package httpapi. It returns nil after a clean shutdown.
func Run(opts Options) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger, shutdownTelemetry, err := telemetry.Setup(ctx, telemetry.Options{
		Service:      opts.Service,
		Version:      version.Get(),
		Env:          cfg.Env,
		OTLPEndpoint: cfg.OTLPEndpoint,
		LogOutput:    os.Stdout,
		LogLevel:     slog.LevelInfo,
		LogText:      cfg.IsLocal(),
	})
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), httpapi.TelemetryFlushTimeout)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			logger.Warn("telemetry shutdown", "error", err)
		}
	}()

	addr := opts.Addr(cfg)
	logger.Info("starting", "env", cfg.Env, "version", version.Get(), "addr", addr)

	// The pool connects lazily, so a Postgres that is down at start makes /readyz fail instead of killing the
	// process, and the pool recovers on its own when it returns.
	st, err := store.Open(ctx, cfg.DatabaseURL, store.Options{AppName: opts.Service})
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer st.Close()

	if err := startupLoginCheck(ctx, st, cfg.Env, logger); err != nil {
		return err
	}

	health := httpapi.NewHealth(logger, httpapi.Check{Name: "postgres", Fn: postgresCheck(st, cfg.Env, logger)})
	mux := httpapi.NewMux(health)
	if opts.Routes != nil {
		if err := opts.Routes(cfg, mux, health, logger, st); err != nil {
			return fmt.Errorf("routes: %w", err)
		}
	}
	// Background work stops as soon as shutdown begins, alongside the HTTP drain, so the two share one budget instead of
	// adding up. It also stops when the server fails to listen.
	stopBackground := func() error { return nil }
	if opts.Background != nil {
		stop, err := opts.Background(ctx, cfg, logger, st, health)
		if err != nil {
			return fmt.Errorf("background: %w", err)
		}
		serverDone := make(chan struct{})
		stopped := make(chan error, 1)
		go func() {
			select {
			case <-ctx.Done():
			case <-serverDone:
			}
			stopped <- stop(context.WithoutCancel(ctx))
		}()
		stopBackground = func() error {
			close(serverDone)
			return <-stopped
		}
	}

	var handler http.Handler = mux
	if opts.Middleware != nil {
		handler = opts.Middleware(cfg)(handler)
	}
	srv := httpapi.NewServer(addr, httpapi.Handler(handler, logger))

	// After the first signal, restore default handling so a second one terminates immediately.
	go func() {
		<-ctx.Done()
		stop()
	}()

	serveErr := httpapi.Serve(ctx, srv, health, httpapi.ServeOptions{}, logger)
	if err := stopBackground(); err != nil {
		logger.Error("background work did not stop cleanly", "error", err)
		return errors.Join(serveErr, fmt.Errorf("background: %w", err))
	}
	return serveErr
}
