// Package setup builds the transcription pipeline from the configuration, for the worker and for cfctl: the sandbox provider with
// its two roots (the job directory and the model cache), the model cache, the provider TRANSCRIBE_PROVIDER names and the speech
// detector. It is the one place that knows how the pieces fit, which is why it sits next to, and not in, package transcribe (it
// imports config, the sandbox and the providers; they import transcribe).
package setup

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
	"github.com/nilpntr/content-fabrique/internal/transcribe/whispercpp"
	"github.com/nilpntr/content-fabrique/internal/transcribe/workersai"
)

// Limits bound the sandboxed whisper runs: CPU is what speeds a transcription up, so it gets the most, memory is the model plus
// the audio (large-v3-turbo quantised needs about 1.5 GB).
func Limits() sandbox.Limits {
	return sandbox.Limits{CPU: 4, MemoryMB: 4096, Timeout: 10 * time.Minute, PIDs: 512}
}

// Parts are the pieces of a transcription setup.
type Parts struct {
	Provider transcribe.Provider
	Detector transcribe.Detector
	// Local is the whisper.cpp provider: the speech detector, and the provider when TRANSCRIBE_PROVIDER says whispercpp.
	Local *whispercpp.Provider
	// TempDir is where the jobs work.
	TempDir string
}

// Build makes the provider and the detector of the configuration. It creates the directories it needs and never touches the network
// (the models are downloaded on first use). prices is the price table (transcription and compute sections): it prices Workers AI per
// minute and local whisper.cpp as compute.
func Build(cfg config.Config, prices *llm.PriceTable) (Parts, error) {
	if err := os.MkdirAll(cfg.MediaTempDir, 0o750); err != nil {
		return Parts{}, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	root, err := filepath.EvalSymlinks(cfg.MediaTempDir)
	if err != nil {
		return Parts{}, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	if err := os.MkdirAll(cfg.WhisperModelsDir, 0o755); err != nil { // #nosec G301 -- the sandbox's user must be able to read the models
		return Parts{}, fmt.Errorf("WHISPER_MODELS_DIR: %w", err)
	}
	models, err := filepath.EvalSymlinks(cfg.WhisperModelsDir)
	if err != nil {
		return Parts{}, fmt.Errorf("WHISPER_MODELS_DIR: %w", err)
	}
	pol := sandbox.Policy{AllowedRoots: []string{root, models}}
	var provider sandbox.Provider
	switch cfg.MediaSandboxProvider {
	case config.SandboxDocker:
		provider = sandbox.NewDocker(pol)
	case config.SandboxHardened:
		provider = sandbox.NewHardened(pol)
	case config.SandboxLocal:
		if err := sandbox.CheckLocalAllowed(cfg.Env); err != nil {
			return Parts{}, err
		}
		provider = sandbox.NewLocal(pol)
	default:
		return Parts{}, fmt.Errorf("MEDIA_SANDBOX_PROVIDER: unknown provider %q", cfg.MediaSandboxProvider)
	}
	spec, ok := transcribe.SpeechModel(cfg.TranscribeModel)
	if !ok {
		return Parts{}, fmt.Errorf("TRANSCRIBE_MODEL: unknown model %q", cfg.TranscribeModel)
	}
	tempDir := filepath.Join(root, "transcribe")
	if err := os.MkdirAll(tempDir, 0o750); err != nil {
		return Parts{}, fmt.Errorf("transcribe temp dir: %w", err)
	}
	local := &whispercpp.Provider{
		Runner:   &media.SandboxRunner{Provider: provider, Image: cfg.MediaToolsImage, Limits: Limits()},
		Cache:    &transcribe.ModelCache{Dir: models},
		Spec:     spec,
		VADModel: transcribe.ModelSileroVAD,
		TempDir:  tempDir,
		Threads:  int(Limits().CPU),
	}
	if prices != nil {
		if w := prices.WhisperLocal(); w.VCPUs > 0 && prices.VCPUSecondMicros() > 0 {
			local.WallSecondsPerAudioSecond = w.WallSecondsPerAudioSecond
			local.Threads = w.VCPUs
			local.ComputeCost = func(vcpuMs int64) (int64, string) {
				micros, _ := prices.ComputeCostMicros(vcpuMs)
				return micros, prices.Version
			}
		}
	}
	parts := Parts{Local: local, Detector: local, Provider: local, TempDir: tempDir}
	if cfg.TranscribeProvider == config.TranscribeWorkersAI {
		price, version := cfg.WorkersAIPriceMicrosPerMinute, ""
		switch {
		case price > 0:
			version = "env:WORKERS_AI_PRICE_MICROS_PER_MINUTE" // the operator's override of the table
		case prices != nil:
			price, version = prices.WorkersAIMicrosPerMinute(), prices.Version
		}
		parts.Provider = workersai.New(workersai.Config{
			AccountID: cfg.WorkersAIAccountID, APIToken: cfg.WorkersAIAPIToken, PriceMicrosPerMinute: price, PriceVersion: version,
			// Staging and production never call a paid provider without a price: its spend could not be metered or capped (D-27, D-31).
			RequirePrice: cfg.Env == config.EnvStaging || cfg.Env == config.EnvProduction,
		})
	}
	return parts, nil
}

// BilledTo is the ledger's billed_to of the environment's transcriptions: test in local, ci and bench (so test spend never mixes
// into customer margin, as for model calls), platform in staging and production.
func BilledTo(env string) string {
	switch env {
	case config.EnvStaging, config.EnvProduction:
		return store.BilledToPlatform
	}
	return store.BilledToTest
}

// Pipeline builds the job's pipeline over the parts. gate is the spend gate (caps from the settings and TRANSCRIBE_DAILY_MINUTES): the
// same holds, counters and caps the model client uses, in the database, so a day of transcription and a day of editing draw on one
// budget and every worker sees the same reservations.
func Pipeline(cfg config.Config, parts Parts, st *store.Store, objects transcribe.Objects, gate *spend.Gate, logger *slog.Logger) (*transcribe.Pipeline, error) {
	return transcribe.New(transcribe.Config{
		Store: st, Objects: objects, Provider: parts.Provider, Detector: parts.Detector, Language: cfg.TranscribeLanguage,
		Gate: gate, BilledTo: BilledTo(cfg.Env), TempDir: parts.TempDir, Logger: logger,
	})
}

// Gate builds the spend gate of the transcription pipeline: the model settings' daily and platform caps, and the transcribed minutes
// as the audio cap.
func Gate(cfg config.Config, settings llm.Settings, st *store.Store, logger *slog.Logger) *spend.Gate {
	return spend.NewGate(st, spend.Options{
		Caps: store.SpendCaps{
			WorkspaceDailyMicros: settings.DailySpendCapMicros, PlatformDailyMicros: settings.PlatformDailySpendCapMicros,
			WorkspaceDailyAudioMs: int64(cfg.TranscribeDailyMinutes) * 60_000,
		},
		Logger: logger,
	})
}
