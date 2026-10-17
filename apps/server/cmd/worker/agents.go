package main

import (
	"errors"
	"log/slog"
	"os"

	"github.com/nilpntr/content-fabrique/internal/agents/agentset"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/quicklook"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// newAgentRuntime builds what the worker's model-using jobs run on from the environment: LLM_MODE, ROUTING_PROFILE and the rest
// of the model-access settings, the price table, the usage ledger of the store, and for the editor agent the object storage and
// the sandbox its frames are cut in. It returns nil, with a warning, when that cannot be done (a bad setting, no object
// storage): agent_run and quick_look jobs then wait, snoozed, until a worker that has everything picks them up, and the
// worker's other jobs are not affected.
func newAgentRuntime(cfg config.Config, st *store.Store, objects *storage.Client, logger *slog.Logger) *agentset.Runtime {
	opts := agentset.Options{Store: st, Logger: logger, Getenv: os.Getenv, Objects: objects, RequireStorage: true}
	if objects != nil {
		var err error
		if opts.Frames, opts.TempDir, err = agentset.NewFrameRunner(cfg.MediaSandboxProvider, cfg.MediaToolsImage, cfg.MediaTempDir, cfg.Env); err != nil {
			logger.Warn("agent runs and quick looks are off: the sandbox for frames cannot be set up", "error", err)
			return nil
		}
	}
	// TODO(director): set opts.Preview to the render jobs' RenderAndWait (P3-06, renderjobs) once it has merged.
	rt, err := agentset.Open(opts)
	if err != nil {
		if errors.Is(err, agentset.ErrNoStorage) {
			logger.Warn("agent runs and quick looks are off: R2_* is not set, and they need object storage for the footage")
			return nil
		}
		logger.Warn("agent runs and quick looks are off", "error", err)
		return nil
	}
	logger.Info("agent runtime ready", "llm_mode", rt.Settings.Mode, "agents", rt.Agents.Names())
	return rt
}

// newQuickLook builds the quick look (P1-04b) on the same model access and frame cache as the agents, so it shares their
// client factory (one rate limit per key, one set of in-flight spend reservations) and the editor's frames/ cache in object
// storage. It returns nil when the runtime is off.
func newQuickLook(rt *agentset.Runtime, st *store.Store, objects *storage.Client, logger *slog.Logger) jobs.QuickLooker {
	if rt == nil || rt.Frames == nil || objects == nil {
		return nil
	}
	svc, err := quicklook.New(quicklook.Config{Store: st, Objects: objects, Frames: rt.Frames, Models: rt.Factory, Router: rt.Router, Logger: logger})
	if err != nil {
		logger.Warn("quick looks are off", "error", err)
		return nil
	}
	logger.Info("quick look ready", "llm_mode", rt.Settings.Mode, "profile", rt.Router.DefaultProfile(), "concurrency", jobs.DefaultQuickLookConcurrency)
	return svc
}
