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
	if c.TranscribeProvider != TranscribeWhisperCPP || c.TranscribeModel != "large-v3-turbo-q5_0" || c.TranscribeLanguage != "" || c.TranscribeConcurrency != 1 ||
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
		"TRANSCRIBE_PROVIDER": " WorkersAI ", "TRANSCRIBE_MODEL": "tiny.en", "TRANSCRIBE_LANGUAGE": "NL", "TRANSCRIBE_CONCURRENCY": "3",
		"WHISPER_MODELS_DIR": "/models", "WORKERS_AI_ACCOUNT_ID": " acct ", "WORKERS_AI_API_TOKEN": "tok", "WORKERS_AI_PRICE_MICROS_PER_MINUTE": "1500",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.TranscribeProvider != "workersai" || c.TranscribeModel != "tiny.en" || c.TranscribeLanguage != "nl" || c.TranscribeConcurrency != 3 ||
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
