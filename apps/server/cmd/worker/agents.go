package main

import (
	"errors"
	"log/slog"
	"os"

	"github.com/nilpntr/content-fabrique/internal/agents/agentset"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// newAgentRunner builds the agent loop of the worker from the environment: LLM_MODE, ROUTING_PROFILE and the rest of the
// model-access settings, the price table, the usage ledger of the store, and for the editor agent the object storage and the
// sandbox its frames are cut in. It returns nil, with a warning, when that cannot be done (a bad setting, no object storage):
// agent_run jobs then wait, snoozed, until a worker that has everything picks them up, and the worker's other jobs are not
// affected.
func newAgentRunner(cfg config.Config, st *store.Store, objects *storage.Client, logger *slog.Logger) *runtime.Runner {
	opts := agentset.Options{Store: st, Logger: logger, Getenv: os.Getenv, Objects: objects, RequireStorage: true}
	if objects != nil {
		var err error
		if opts.Frames, opts.TempDir, err = agentset.NewFrameRunner(cfg.MediaSandboxProvider, cfg.MediaToolsImage, cfg.MediaTempDir, cfg.Env); err != nil {
			logger.Warn("agent runs are off: the sandbox for the editor's frames cannot be set up", "error", err)
			return nil
		}
	}
	// TODO(director): set opts.Preview to the render jobs' RenderAndWait (P3-06, renderjobs) once it has merged.
	rt, err := agentset.Open(opts)
	if err != nil {
		if errors.Is(err, agentset.ErrNoStorage) {
			logger.Warn("agent runs are off: R2_* is not set, and the editor agent needs object storage for the footage")
			return nil
		}
		logger.Warn("agent runs are off", "error", err)
		return nil
	}
	logger.Info("agent runtime ready", "llm_mode", rt.Settings.Mode, "agents", rt.Agents.Names())
	return rt.Runner
}
