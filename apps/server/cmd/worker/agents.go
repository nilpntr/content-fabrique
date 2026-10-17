package main

import (
	"cmp"
	"log/slog"
	"os"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// modelAccess is what the worker's model-using jobs share: the settings (LLM_MODE, ROUTING_PROFILE ...), the factory that
// hands out clients and writes the usage ledger, and the router. One factory serves them all, so the per-key concurrency
// limit and the in-flight spend reservations are shared.
type modelAccess struct {
	settings llm.Settings
	factory  *llm.Factory
	router   *llm.Router
}

// newModelAccess builds model access from the environment and the usage ledger of the store. It returns nil, with a warning,
// when that cannot be done (a bad setting): the jobs that need a model then wait, snoozed, until a worker that has model
// access picks them up, and the worker's other jobs are not affected.
func newModelAccess(st *store.Store, logger *slog.Logger) *modelAccess {
	settings, err := llm.LoadSettings(os.Getenv)
	if err != nil {
		logger.Warn("model access is off: the settings are invalid", "error", err)
		return nil
	}
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		logger.Warn("model access is off: the price table does not load", "error", err)
		return nil
	}
	factory, err := llm.NewFactory(llm.FactoryOptions{Settings: settings, Getenv: os.Getenv, Prices: prices, Ledger: st, Logger: logger})
	if err != nil {
		logger.Warn("model access is off", "error", err)
		return nil
	}
	router, err := llm.NewRouter(configfiles.Routing(), cmp.Or(settings.RoutingProfile, "dev-cheap"))
	if err != nil {
		// Agent runs resolve their routes themselves; the quick look needs the router and waits without it.
		logger.Warn("quick looks are off: the routing profile does not load", "error", err)
		router = nil
	}
	return &modelAccess{settings: settings, factory: factory, router: router}
}

// newAgentRunner builds the agent loop of the worker on the model access. It returns nil, with a warning, when that is off or
// cannot be built: agent_run jobs then wait (snoozed).
func newAgentRunner(st *store.Store, logger *slog.Logger, ma *modelAccess) *runtime.Runner {
	if ma == nil {
		return nil
	}
	agents, err := runtime.NewAgents(runtime.DefaultAgents()...)
	if err != nil {
		logger.Warn("agent runs are off: an agent definition is invalid", "error", err)
		return nil
	}
	runner, err := runtime.NewRunner(runtime.Deps{Store: st, Factory: ma.factory, Agents: agents, Logger: logger})
	if err != nil {
		logger.Warn("agent runs are off", "error", err)
		return nil
	}
	logger.Info("agent runtime ready", "llm_mode", ma.settings.Mode, "agents", agents.Names())
	return runner
}
