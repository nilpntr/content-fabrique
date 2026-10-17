package setup

import (
	"context"
	"errors"
	"testing"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
	"github.com/nilpntr/content-fabrique/internal/transcribe/workersai"
)

func buildConfig(t *testing.T, env, provider string) config.Config {
	t.Helper()
	return config.Config{
		Env: env, MediaSandboxProvider: config.SandboxDocker, MediaToolsImage: "media-tools:dev", MediaTempDir: t.TempDir(),
		WhisperModelsDir: t.TempDir(), TranscribeProvider: provider, TranscribeModel: "tiny.en", TranscribeDailyMinutes: 240,
		WorkersAIAccountID: "acct", WorkersAIAPIToken: "tok",
	}
}

// Staging and production never call a hosted provider whose price is unknown (D-27): the provider built there refuses to send.
func TestHostedProviderRequiresAPriceInStagingAndProduction(t *testing.T) {
	for _, env := range []string{config.EnvStaging, config.EnvProduction} {
		parts, err := Build(buildConfig(t, env, config.TranscribeWorkersAI), nil)
		if err != nil {
			t.Fatal(err)
		}
		p, ok := parts.Provider.(*workersai.Provider)
		if !ok {
			t.Fatalf("%s: provider %T", env, parts.Provider)
		}
		if p.Configured() {
			t.Errorf("%s: configured without a price", env)
		}
		if _, err := p.Transcribe(context.Background(), transcribe.Audio{Path: "/nonexistent.wav"}, transcribe.Options{}); !errors.Is(err, transcribe.ErrNotConfigured) {
			t.Errorf("%s: %v", env, err)
		}

		cfg := buildConfig(t, env, config.TranscribeWorkersAI)
		cfg.WorkersAIPriceMicrosPerMinute = 6000
		parts, err = Build(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if hosted, ok := parts.Provider.(*workersai.Provider); !ok || !hosted.Configured() {
			t.Errorf("%s with a price: %T is not configured", env, parts.Provider)
		}
	}
	// Development: local whisper.cpp is the default, and an unpriced hosted provider is allowed (it makes no call without an account).
	parts, err := Build(buildConfig(t, config.EnvLocal, config.TranscribeWhisperCPP), nil)
	if err != nil || parts.Provider != transcribe.Provider(parts.Local) {
		t.Fatalf("local: %T %v", parts.Provider, err)
	}
	parts, err = Build(buildConfig(t, config.EnvLocal, config.TranscribeWorkersAI), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hosted, ok := parts.Provider.(*workersai.Provider); !ok || !hosted.Configured() {
		t.Errorf("local workers ai: %T is not configured", parts.Provider)
	}
}

func TestBilledToFollowsTheEnvironment(t *testing.T) {
	for env, want := range map[string]string{"local": store.BilledToTest, "ci": store.BilledToTest, "bench": store.BilledToTest, "staging": store.BilledToPlatform, "production": store.BilledToPlatform} {
		if got := BilledTo(env); got != want {
			t.Errorf("%s: %s, want %s", env, got, want)
		}
	}
}

func TestBuildRefusesAnUnknownModelOrSandbox(t *testing.T) {
	cfg := buildConfig(t, config.EnvLocal, config.TranscribeWhisperCPP)
	cfg.TranscribeModel = "huge"
	if _, err := Build(cfg, nil); err == nil {
		t.Error("unknown model")
	}
	cfg = buildConfig(t, config.EnvLocal, config.TranscribeWhisperCPP)
	cfg.MediaSandboxProvider = "vm"
	if _, err := Build(cfg, nil); err == nil {
		t.Error("unknown sandbox")
	}
	cfg = buildConfig(t, config.EnvStaging, config.TranscribeWhisperCPP)
	cfg.MediaSandboxProvider = config.SandboxLocal
	if _, err := Build(cfg, nil); err == nil {
		t.Error("the local sandbox in staging")
	}
}

// The price table is where the paid prices live (D-31): with 2026-10b, Workers AI is priced in staging and production without an
// override, the ledger names the table, an operator's override wins and says so, and local whisper.cpp is priced as compute.
func TestPricesComeFromThePriceTable(t *testing.T) {
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{config.EnvStaging, config.EnvProduction} {
		parts, err := Build(buildConfig(t, env, config.TranscribeWorkersAI), prices)
		if err != nil {
			t.Fatal(err)
		}
		hosted, ok := parts.Provider.(*workersai.Provider)
		if !ok || !hosted.Configured() {
			t.Fatalf("%s: priced by the table and still not configured", env)
		}
		if micros, version := hosted.CostMicros(60_000); micros != 500 || version != prices.Version {
			t.Errorf("%s: a minute costs %d (%s), want 500 (%s)", env, micros, version, prices.Version)
		}
		cfg := buildConfig(t, env, config.TranscribeWorkersAI)
		cfg.WorkersAIPriceMicrosPerMinute = 700
		parts, err = Build(cfg, prices)
		if err != nil {
			t.Fatal(err)
		}
		if micros, version := parts.Provider.CostMicros(60_000); micros != 700 || version != "env:WORKERS_AI_PRICE_MICROS_PER_MINUTE" {
			t.Errorf("%s: override: %d (%s)", env, micros, version)
		}
	}
	// Local whisper.cpp: compute, estimated from the audio (4 vCPUs x 1 s per audio second x 20 micro-dollars per vCPU-second).
	parts, err := Build(buildConfig(t, config.EnvLocal, config.TranscribeWhisperCPP), prices)
	if err != nil {
		t.Fatal(err)
	}
	if micros, version := parts.Provider.CostMicros(10_000); micros != 800 || version != prices.Version {
		t.Errorf("local whisper: 10 s of audio costs %d (%s), want 800", micros, version)
	}
	if unit, q := parts.Local.Quantity(10_000); unit != "vcpu_ms" || q != 40_000 {
		t.Errorf("local whisper quantity %d %s", q, unit)
	}
}
