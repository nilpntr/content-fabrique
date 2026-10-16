// Package app is the shared process bootstrap for the api and worker binaries: configuration, signal handling,
// telemetry, the health/mux/server wiring and graceful shutdown.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
	"github.com/nilpntr/content-fabrique/internal/version"
)

// Options describes one HTTP-serving process.
type Options struct {
	// Service is the telemetry service name, e.g. telemetry.ServiceAPI.
	Service string
	// Addr picks the listen address from the loaded config.
	Addr func(config.Config) string
	// Routes optionally registers extra routes and readiness checks before the server starts.
	Routes func(mux *http.ServeMux, health *httpapi.Health, logger *slog.Logger)
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

	health := httpapi.NewHealth(logger)
	mux := httpapi.NewMux(health)
	if opts.Routes != nil {
		opts.Routes(mux, health, logger)
	}
	srv := httpapi.NewServer(addr, httpapi.Handler(mux, logger))

	// After the first signal, restore default handling so a second one terminates immediately.
	go func() {
		<-ctx.Done()
		stop()
	}()

	return httpapi.Serve(ctx, srv, health, httpapi.ServeOptions{}, logger)
}
