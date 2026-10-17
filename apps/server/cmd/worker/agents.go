package main

import (
	"log/slog"
	"os"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// newAgentRunner builds the agent loop of the worker from the environment: LLM_MODE, ROUTING_PROFILE and the rest of the
// model-access settings, the price table, and the usage ledger of the store. It returns nil, with a warning, when that
// cannot be done (a bad setting): agent_run jobs then wait, snoozed, until a worker that has model access picks them up,
// and the worker's other jobs are not affected.
func newAgentRunner(st *store.Store, logger *slog.Logger) *runtime.Runner {
	settings, err := llm.LoadSettings(os.Getenv)
	if err != nil {
		logger.Warn("agent runs are off: the model-access settings are invalid", "error", err)
		return nil
	}
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		logger.Warn("agent runs are off: the price table does not load", "error", err)
		return nil
	}
	factory, err := llm.NewFactory(llm.FactoryOptions{Settings: settings, Getenv: os.Getenv, Prices: prices, Ledger: st, Logger: logger})
	if err != nil {
		logger.Warn("agent runs are off", "error", err)
		return nil
	}
	agents, err := runtime.NewAgents(runtime.DefaultAgents()...)
	if err != nil {
		logger.Warn("agent runs are off: an agent definition is invalid", "error", err)
		return nil
	}
	runner, err := runtime.NewRunner(runtime.Deps{Store: st, Factory: factory, Agents: agents, Logger: logger})
	if err != nil {
		logger.Warn("agent runs are off", "error", err)
		return nil
	}
	logger.Info("agent runtime ready", "llm_mode", settings.Mode, "agents", agents.Names())
	return runner
}
