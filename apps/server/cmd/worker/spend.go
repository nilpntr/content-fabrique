package main

import (
	"log/slog"
	"os"
	"time"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/agentset"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/transcribe/setup"
)

// analysisLimits bound the sandboxed tool runs of the clip analysis.
func analysisLimits() sandbox.Limits {
	return sandbox.Limits{CPU: 2, MemoryMB: 2048, Timeout: 5 * time.Minute, PIDs: 256}
}

// newComputeMeter builds the meter of our own compute (P7-09a): the CPU time of renders and of ingest, quick look and analysis,
// priced from the price table's compute section. A process whose settings or price table cannot be read meters nothing and says so
// loudly: the work still runs, but its rows would be missing from the ledger.
func newComputeMeter(cfg config.Config, st *store.Store, logger *slog.Logger) *spend.ComputeMeter {
	settings, err := llm.LoadSettings(os.Getenv)
	if err != nil {
		logger.Error("compute metering is off: the model settings (the spend caps and the rest) cannot be read", "error", err)
		return nil
	}
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		logger.Error("compute metering is off: the price table cannot be read", "error", err)
		return nil
	}
	return &spend.ComputeMeter{
		Gate:         setup.Gate(cfg, settings, st, logger),
		Cost:         prices.ComputeCostMicros,
		PriceVersion: prices.Version,
		BilledTo:     setup.BilledTo(cfg.Env),
		// The vCPUs of each wall-time-metered stage are the CPU limits of the sandboxes they run in.
		VCPUs: map[string]float64{
			"ingest":     ingestLimits().CPU,
			"analysis":   analysisLimits().CPU,
			"quick_look": agentset.FrameLimits().CPU,
		},
		Log: logger,
	}
}
