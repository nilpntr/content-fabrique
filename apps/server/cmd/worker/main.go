// Command worker runs Content Fabrique background work. For now it only serves health endpoints:
// the River job queue arrives with P0-06.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/nilpntr/content-fabrique/internal/app"
	"github.com/nilpntr/content-fabrique/internal/config"
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
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage was already printed
		}
		return err
	}

	return app.Run(app.Options{
		Service: telemetry.ServiceWorker,
		Addr:    func(config.Config) string { return *healthAddr },
	})
}
