// Command api is the Content Fabrique HTTP API.
package main

import (
	"fmt"
	"os"

	"github.com/nilpntr/content-fabrique/internal/app"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

func main() {
	err := app.Run(app.Options{
		Service: telemetry.ServiceAPI,
		Addr:    func(c config.Config) string { return c.APIListenAddr },
		// app.Run registers the Postgres readiness check. Later tasks add API routes through Options.Routes.
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}
