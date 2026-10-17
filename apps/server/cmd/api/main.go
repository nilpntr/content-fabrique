// Command api is the Content Fabrique HTTP API.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/nilpntr/content-fabrique/internal/app"
	"github.com/nilpntr/content-fabrique/internal/assets"
	"github.com/nilpntr/content-fabrique/internal/auth"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

func main() {
	err := app.Run(app.Options{
		Service: telemetry.ServiceAPI,
		Addr:    func(c config.Config) string { return c.APIListenAddr },
		// app.Run registers the Postgres readiness check. The health endpoints stay unauthenticated; everything under
		// /v1 sits behind the Clerk middleware and the paywall, except the Svix-signed webhook and the allowlist in
		// internal/auth/routes.go. A new endpoint goes in the OpenAPI spec first (packages/schemas/openapi.yaml, `task gen:api`) and is paid-only until
		// it is added to that allowlist.
		Routes: func(cfg config.Config, mux *http.ServeMux, health *httpapi.Health, logger *slog.Logger, st *store.Store) error {
			// The insert-only River client over the pooled pool: the api never works jobs and holds no listener. A job is
			// enqueued in the transaction of the write it belongs to; a new workspace (just-in-time sync or Clerk
			// webhook) enqueues its welcome job that way.
			river, err := jobs.NewInsertClient(st.Pool(), logger)
			if err != nil {
				return err
			}
			st.SetWorkspaceCreatedHook(jobs.NewEnqueuer(river, logger).WorkspaceCreated)
			// Creating a workspace needs River's tables, so an api without them is not ready (the check is cached).
			health.AddCheck(httpapi.Check{Name: "river-tables", Fn: jobs.MigratedCheck(st.Pool())})
			// The assets API needs object storage (R2, or MinIO locally). Without the R2_* settings the process still
			// starts and the assets operations answer 503 storage_not_configured.
			var assetsSvc *assets.Service
			if cfg.ObjectStorageConfigured() {
				objects, err := storage.New(storage.FromConfig(cfg))
				if err != nil {
					return err
				}
				health.AddCheck(httpapi.Check{Name: "object-storage", Fn: objects.Ready})
				if assetsSvc, err = assets.NewService(st, objects, jobs.NewEnqueuer(river, logger), logger); err != nil {
					return err
				}
			} else {
				logger.Warn("R2_* is not set: the assets API answers 503 storage_not_configured")
			}
			// Mount registers every route of the OpenAPI contract (GET /v1/library/summary included); the V1 router it
			// returns is for routes that are not in the contract yet.
			_, err = auth.Mount(mux, auth.Deps{Config: cfg, Store: st, Logger: logger, Assets: assetsSvc})
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
