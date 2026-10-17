package setup

import (
	"context"
	"errors"
	"testing"

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
		parts, err := Build(buildConfig(t, env, config.TranscribeWorkersAI))
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
		parts, err = Build(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if hosted, ok := parts.Provider.(*workersai.Provider); !ok || !hosted.Configured() {
			t.Errorf("%s with a price: %T is not configured", env, parts.Provider)
		}
	}
	// Development: local whisper.cpp is the default, and an unpriced hosted provider is allowed (it makes no call without an account).
	parts, err := Build(buildConfig(t, config.EnvLocal, config.TranscribeWhisperCPP))
	if err != nil || parts.Provider != transcribe.Provider(parts.Local) {
		t.Fatalf("local: %T %v", parts.Provider, err)
	}
	parts, err = Build(buildConfig(t, config.EnvLocal, config.TranscribeWorkersAI))
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
	if _, err := Build(cfg); err == nil {
		t.Error("unknown model")
	}
	cfg = buildConfig(t, config.EnvLocal, config.TranscribeWhisperCPP)
	cfg.MediaSandboxProvider = "vm"
	if _, err := Build(cfg); err == nil {
		t.Error("unknown sandbox")
	}
	cfg = buildConfig(t, config.EnvStaging, config.TranscribeWhisperCPP)
	cfg.MediaSandboxProvider = config.SandboxLocal
	if _, err := Build(cfg); err == nil {
		t.Error("the local sandbox in staging")
	}
}
