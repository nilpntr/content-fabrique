// Command api is the Content Fabrique HTTP API.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
	"github.com/nilpntr/content-fabrique/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger, shutdownTelemetry, err := telemetry.Setup(ctx, telemetry.Options{
		Service:      telemetry.ServiceAPI,
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
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			logger.Warn("telemetry shutdown", "error", err)
		}
	}()

	logger.Info("api starting", "env", cfg.Env, "version", version.Get(), "addr", cfg.APIListenAddr)

	// Later tasks register readiness checks (Postgres, ...) on health.
	health := httpapi.NewHealth(logger)
	mux := httpapi.NewMux(health)
	srv := httpapi.NewServer(cfg.APIListenAddr, httpapi.Handler(mux, logger))

	// After the first signal, restore default handling so a second one terminates immediately.
	go func() {
		<-ctx.Done()
		stop()
	}()

	return httpapi.Serve(ctx, srv, health, httpapi.ServeOptions{}, logger)
}
