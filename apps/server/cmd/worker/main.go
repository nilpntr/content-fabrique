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

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/app"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
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
			// Object storage for the jobs that remove objects. Without the R2_* settings those jobs fail and are retried.
			var (
				objects  jobs.ObjectStore
				storageC *storage.Client    // the same client, for the agents
				ingester jobs.AssetIngester // nil without storage: asset_ingest jobs then wait (snoozed)
				renderer jobs.Renderer      // nil without storage: render jobs then wait (snoozed)
				transcr  jobs.Transcriber   // nil without storage: transcribe jobs then wait (snoozed)
			)
			if cfg.ObjectStorageConfigured() {
				client, err := storage.New(cfg.Storage())
				if err != nil {
					return nil, err
				}
				objects, storageC = client, client
				health.AddCheck(httpapi.Check{Name: "object-storage", Fn: client.Ready})
				pipeline, err := newIngestPipeline(cfg, st, client, logger)
				if err != nil {
					return nil, err
				}
				ingester = pipeline
				rp, err := newRenderPipeline(cfg, st, client, logger)
				if err != nil {
					return nil, err
				}
				renderer = rp
				tr, err := newTranscriber(cfg, st, client, logger)
				if err != nil {
					return nil, err
				}
				transcr = tr
				logger.Info("media pipeline ready", "sandbox", cfg.MediaSandboxProvider, "image", cfg.MediaToolsImage, "temp_dir", cfg.MediaTempDir, "concurrency", cfg.MediaConcurrency, "render_concurrency", cfg.RenderConcurrency,
					"transcribe_provider", cfg.TranscribeProvider, "transcribe_model", cfg.TranscribeModel, "models_dir", cfg.WhisperModelsDir)
				warnIfImageMissing(ctx, cfg, logger)
			} else {
				logger.Warn("R2_* is not set: the jobs that delete objects, clean up uploads ingest assets or render timelines wait (snoozed, no attempt used) until a worker has it")
			}
			// The agent runtime and the quick look share their model access and frame cache; both wait when it is off.
			var (
				runner      *runtime.Runner
				quickLooker jobs.QuickLooker
			)
			if rt := newAgentRuntime(cfg, st, storageC, logger); rt != nil {
				runner, quickLooker = rt.Runner, newQuickLook(rt, st, storageC, logger)
			}
			queues := jobs.DefaultConfig()
			for i := range queues.Queues {
				switch queues.Queues[i].Name {
				case jobs.QueueMedia:
					queues.Queues[i].MaxWorkers = cfg.MediaConcurrency
				case jobs.QueueRender:
					queues.Queues[i].MaxWorkers = cfg.RenderConcurrency
				case jobs.QueueTranscribe:
					queues.Queues[i].MaxWorkers = cfg.TranscribeConcurrency
				}
			}
			w, err := jobs.StartWorker(ctx, jobs.WorkerOptions{
				Config:     queues,
				DirectURL:  cfg.DatabaseDirectURL,
				Store:      st,
				Objects:    objects,
				Ingest:     ingester,
				Render:     renderer,
				Transcribe: transcr,
				Register:   runtime.Register(runner),
				QuickLook:  quickLooker,
				Logger:     logger,
				UIAddr:     *riverUIAddr,
				UIPassword: cfg.RiverUIPassword,
				// The direct URL is the same app role: on staging and production a privileged login is refused here too.
				CheckLogin: func(ctx context.Context, direct *store.Store) error {
					return app.StartupLoginCheck(ctx, direct, cfg.Env, logger)
				},
			})
			if err != nil {
				return nil, err
			}
			health.AddCheck(httpapi.Check{Name: "postgres-direct", Fn: app.PostgresCheck(w.Direct(), cfg.Env, logger)})
			health.AddCheck(httpapi.Check{Name: "river", Fn: w.Ready})
			return w.Stop, nil
		},
	})
}
