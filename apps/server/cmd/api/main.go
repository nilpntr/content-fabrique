// Command api is the Content Fabrique HTTP API.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/nilpntr/content-fabrique/internal/app"
	"github.com/nilpntr/content-fabrique/internal/auth"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

func main() {
	err := app.Run(app.Options{
		Service: telemetry.ServiceAPI,
		Addr:    func(c config.Config) string { return c.APIListenAddr },
		// app.Run registers the Postgres readiness check. The health endpoints stay unauthenticated; everything under
		// /v1 sits behind the Clerk middleware, except the Svix-signed webhook.
		Routes: func(cfg config.Config, mux *http.ServeMux, _ *httpapi.Health, logger *slog.Logger, st *store.Store) error {
			// The insert-only River client over the pooled pool: the api never works jobs and holds no listener. A job is
			// enqueued in the transaction of the write it belongs to; a new workspace (just-in-time sync or Clerk
			// webhook) enqueues its welcome job that way.
			river, err := jobs.NewInsertClient(st.Pool(), logger)
			if err != nil {
				return err
			}
			st.SetWorkspaceCreatedHook(jobs.NewEnqueuer(river).WorkspaceCreated)
			_, err = auth.Mount(mux, auth.Deps{Config: cfg, Store: st, Logger: logger})
			return err
		},
		// The CORS allowlist is the web app's origin, APP_BASE_URL, and nothing else.
		Middleware: func(cfg config.Config) func(http.Handler) http.Handler { return httpapi.CORS(cfg.AppOrigin, nil) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}
