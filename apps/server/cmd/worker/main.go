// Command worker runs Content Fabrique background work: the River job queue (workers and periodic jobs), with
// /healthz and /readyz on its health address and, when asked for, River UI on its own address.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/nilpntr/content-fabrique/internal/app"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
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
	riverUIAddr := fs.String("river-ui-addr", "", "listen address for River UI, such as 127.0.0.1:8082 (empty: off). Needs RIVER_UI_PASSWORD; HTTP Basic auth, user "+jobs.UIUser)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage was already printed
		}
		return err
	}

	return app.Run(app.Options{
		Service: telemetry.ServiceWorker,
		Addr:    func(config.Config) string { return *healthAddr },
		Background: func(ctx context.Context, cfg config.Config, logger *slog.Logger, st *store.Store, health *httpapi.Health) (func(context.Context) error, error) {
			w, err := jobs.StartWorker(ctx, jobs.WorkerOptions{
				DirectURL:  cfg.DatabaseDirectURL,
				Store:      st,
				Logger:     logger,
				UIAddr:     *riverUIAddr,
				UIPassword: cfg.RiverUIPassword,
			})
			if err != nil {
				return nil, err
			}
			health.AddCheck(httpapi.Check{Name: "river", Fn: w.Ready})
			return w.Stop, nil
		},
	})
}
