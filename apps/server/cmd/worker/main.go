// Command worker runs Content Fabrique background work. For now it only serves health endpoints:
// the River job queue arrives with P0-06.
package main

import (
	"context"
	"flag"
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
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	healthAddr := fs.String("health-addr", ":8081", "listen address for /healthz and /readyz")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger, shutdownTelemetry, err := telemetry.Setup(ctx, telemetry.Options{
		Service:      telemetry.ServiceWorker,
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

	logger.Info("worker starting", "env", cfg.Env, "version", version.Get(), "health_addr", *healthAddr)

	health := httpapi.NewHealth(logger)
	mux := httpapi.NewMux(health)
	srv := httpapi.NewServer(*healthAddr, httpapi.Handler(mux, logger))

	// After the first signal, restore default handling so a second one terminates immediately.
	go func() {
		<-ctx.Done()
		stop()
	}()

	return httpapi.Serve(ctx, srv, health, httpapi.ServeOptions{}, logger)
}
