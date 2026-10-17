package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscribeDefaults(t *testing.T) {
	c, err := Load(mediaEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.TranscribeProvider != TranscribeWhisperCPP || c.TranscribeModel != "large-v3-turbo-q5_0" || c.TranscribeLanguage != "" || c.TranscribeConcurrency != 1 || c.TranscribeDailyMinutes != 240 ||
		c.WhisperModelsDir != filepath.Join(os.TempDir(), "cf-media", "whisper-models") || c.WorkersAIAccountID != "" || c.WorkersAIPriceMicrosPerMinute != 0 {
		t.Fatalf("%+v", c)
	}
	// CI uses the tiny model; staging and production call Workers AI (D-27).
	if c, err := Load(mediaEnv(map[string]string{"CF_ENV": EnvCI})); err != nil || c.TranscribeModel != "tiny.en" || c.TranscribeProvider != TranscribeWhisperCPP {
		t.Fatalf("ci: %q %q %v", c.TranscribeModel, c.TranscribeProvider, err)
	}
	for _, env := range []string{EnvStaging, EnvProduction} {
		c, err := Load(mediaEnv(map[string]string{"CF_ENV": env}))
		if err != nil || c.TranscribeProvider != TranscribeWorkersAI {
			t.Fatalf("%s: %q %v", env, c.TranscribeProvider, err)
		}
	}
	// The models directory follows MEDIA_TEMP_DIR unless it is set.
	if c, _ := Load(mediaEnv(map[string]string{"MEDIA_TEMP_DIR": "/var/lib/cf"})); c.WhisperModelsDir != "/var/lib/cf/whisper-models" {
		t.Errorf("models dir %q", c.WhisperModelsDir)
	}
}

func TestTranscribeSettings(t *testing.T) {
	c, err := Load(mediaEnv(map[string]string{
		"TRANSCRIBE_PROVIDER": " WorkersAI ", "TRANSCRIBE_MODEL": "tiny.en", "TRANSCRIBE_LANGUAGE": "NL", "TRANSCRIBE_CONCURRENCY": "3", "TRANSCRIBE_DAILY_MINUTES": "90",
		"WHISPER_MODELS_DIR": "/models", "WORKERS_AI_ACCOUNT_ID": " acct ", "WORKERS_AI_API_TOKEN": "tok", "WORKERS_AI_PRICE_MICROS_PER_MINUTE": "1500",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.TranscribeProvider != "workersai" || c.TranscribeModel != "tiny.en" || c.TranscribeLanguage != "nl" || c.TranscribeConcurrency != 3 || c.TranscribeDailyMinutes != 90 ||
		c.WhisperModelsDir != "/models" || c.WorkersAIAccountID != "acct" || c.WorkersAIAPIToken != "tok" || c.WorkersAIPriceMicrosPerMinute != 1500 {
		t.Fatalf("%+v", c)
	}
	if c, _ := Load(mediaEnv(map[string]string{"TRANSCRIBE_LANGUAGE": "auto"})); c.TranscribeLanguage != "" {
		t.Errorf("auto means detect: %q", c.TranscribeLanguage)
	}
}

func TestTranscribeSettingsAreValidated(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"TRANSCRIBE_PROVIDER":                {"TRANSCRIBE_PROVIDER": "elevenlabs"},
		"TRANSCRIBE_MODEL":                   {"TRANSCRIBE_MODEL": "large-v3"},
		"TRANSCRIBE_LANGUAGE":                {"TRANSCRIBE_LANGUAGE": "english"},
		"TRANSCRIBE_CONCURRENCY":             {"TRANSCRIBE_CONCURRENCY": "0"},
		"TRANSCRIBE_CONCURRENCY high":        {"TRANSCRIBE_CONCURRENCY": "99"},
		"TRANSCRIBE_DAILY_MINUTES":           {"TRANSCRIBE_DAILY_MINUTES": "0"},
		"TRANSCRIBE_DAILY_MINUTES words":     {"TRANSCRIBE_DAILY_MINUTES": "lots"},
		"WHISPER_MODELS_DIR":                 {"WHISPER_MODELS_DIR": "models"},
		"WORKERS_AI_PRICE_MICROS_PER_MINUTE": {"WORKERS_AI_PRICE_MICROS_PER_MINUTE": "-1"},
		"WORKERS_AI_ACCOUNT_ID":              {"WORKERS_AI_ACCOUNT_ID": "acct"},
		"WORKERS_AI_API_TOKEN":               {"WORKERS_AI_API_TOKEN": "tok"},
	} {
		_, err := Load(mediaEnv(env))
		if err == nil || !strings.Contains(err.Error(), strings.Fields(name)[0]) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// The API token is never in an error.
	_, err := Load(mediaEnv(map[string]string{"WORKERS_AI_API_TOKEN": "super-secret-token"}))
	if err == nil || strings.Contains(err.Error(), "super-secret-token") {
		t.Errorf("%v", err)
	}
}

// D-27, D-31: the price of hosted transcription lives in config/pricing, so an account in staging or production needs no price
// variable; the override, when set, is still checked. (That an unpriced provider never runs is tested where it is built, in
// transcribe/setup.)
func TestWorkersAIPriceIsOptionalBecauseThePriceTableHasIt(t *testing.T) {
	for _, env := range []string{EnvStaging, EnvProduction} {
		base := map[string]string{"CF_ENV": env, "WORKERS_AI_ACCOUNT_ID": "acct", "WORKERS_AI_API_TOKEN": "tok"}
		if _, err := Load(mediaEnv(base)); err != nil {
			t.Errorf("%s without an override: %v", env, err)
		}
		base["WORKERS_AI_PRICE_MICROS_PER_MINUTE"] = "6000"
		if _, err := Load(mediaEnv(base)); err != nil {
			t.Errorf("%s with an override: %v", env, err)
		}
		// Not switched on yet (staging today has no account): the worker starts and the jobs wait.
		if _, err := Load(mediaEnv(map[string]string{"CF_ENV": env})); err != nil {
			t.Errorf("%s without an account: %v", env, err)
		}
	}
}

// D-31: LLM_ALLOW_UNPRICED is honoured only in development and ci. Every other environment refuses to load the configuration.
func TestAllowUnpricedIsRefusedOutsideDevelopmentAndCI(t *testing.T) {
	for env, ok := range map[string]bool{EnvLocal: true, EnvCI: true, EnvBench: false, EnvStaging: false, EnvProduction: false} {
		_, err := Load(mediaEnv(map[string]string{"CF_ENV": env, "LLM_ALLOW_UNPRICED": "1"}))
		switch {
		case ok && err != nil:
			t.Errorf("%s: %v", env, err)
		case !ok && (err == nil || !strings.Contains(err.Error(), "LLM_ALLOW_UNPRICED")):
			t.Errorf("%s: err = %v, want a refusal naming LLM_ALLOW_UNPRICED", env, err)
		}
	}
	if _, err := Load(mediaEnv(map[string]string{"CF_ENV": EnvProduction, "LLM_ALLOW_UNPRICED": "0"})); err != nil {
		t.Errorf("0 is off: %v", err)
	}
	for name, val := range map[string]string{"LLM_ALLOW_UNPRICED": "yes", "PLATFORM_DAILY_SPEND_CAP_MICROS": "0", "DAILY_SPEND_CAP_MICROS": "-5", "LLM_MAX_REQUEST_MICROS": "x"} {
		if _, err := Load(mediaEnv(map[string]string{name: val})); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%s: %v", name, val, err)
		}
	}
}
