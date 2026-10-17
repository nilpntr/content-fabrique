package llm

import (
	"strings"
	"testing"
)

func TestLoadSettingsDefaults(t *testing.T) {
	s, err := LoadSettings(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.Env != "local" || s.Mode != ModeLive || s.BaseURL != "" || s.MaxRequestMicros != 2_000_000 || s.DailySpendCapMicros != 20_000_000 ||
		s.Concurrency != 4 || s.AllowUnpriced || s.CassetteDir != "testdata/cassettes" {
		t.Fatalf("defaults = %+v", s)
	}
}

func TestLoadSettingsValues(t *testing.T) {
	s, err := LoadSettings(env(map[string]string{
		"CF_ENV": "ci", "LLM_MODE": "Replay", "ANTHROPIC_BASE_URL": "https://gateway.ai.cloudflare.com/v1/a/b/anthropic",
		"LLM_MAX_REQUEST_MICROS": "500000", "DAILY_SPEND_CAP_MICROS": "1000000", "LLM_KEY_CONCURRENCY": "2",
		"LLM_ALLOW_UNPRICED": "1", "LLM_CASSETTE_DIR": "/tmp/x", "ROUTING_PROFILE": "v1-start",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != ModeReplay || s.MaxRequestMicros != 500_000 || s.DailySpendCapMicros != 1_000_000 || s.Concurrency != 2 || !s.AllowUnpriced ||
		s.CassetteDir != "/tmp/x" || s.RoutingProfile != "v1-start" {
		t.Fatalf("settings = %+v", s)
	}
}

func TestLoadSettingsErrorsNameTheVariableNotTheValue(t *testing.T) {
	bad := map[string]map[string]string{
		"LLM_MODE":               {"LLM_MODE": "sneaky-secret-value"},
		"CF_ENV":                 {"CF_ENV": "prod-ish-secret"},
		"ANTHROPIC_BASE_URL":     {"ANTHROPIC_BASE_URL": "https://user:pw-secret@gateway.example/anthropic"},
		"LLM_MAX_REQUEST_MICROS": {"LLM_MAX_REQUEST_MICROS": "-5"},
		"DAILY_SPEND_CAP_MICROS": {"DAILY_SPEND_CAP_MICROS": "0"},
		"LLM_KEY_CONCURRENCY":    {"LLM_KEY_CONCURRENCY": "1000"},
		"LLM_ALLOW_UNPRICED":     {"LLM_ALLOW_UNPRICED": "yes-secret"},
	}
	for name, vars := range bad {
		_, err := LoadSettings(env(vars))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s: error does not name the variable: %v", name, err)
		}
		for _, v := range vars {
			if strings.Contains(err.Error(), v) {
				t.Errorf("%s: error repeats the value: %v", name, err)
			}
		}
	}
	// Record and replay are for development; staging and production run live.
	if _, err := LoadSettings(env(map[string]string{"CF_ENV": "production", "LLM_MODE": "replay"})); err == nil {
		t.Error("replay accepted in production")
	}
	if _, err := LoadSettings(env(map[string]string{"CF_ENV": "staging", "ANTHROPIC_BASE_URL": "http://gateway.example"})); err == nil {
		t.Error("plain http base URL accepted in staging")
	}
}

func TestBillingFor(t *testing.T) {
	for env, want := range map[string]Billing{"local": BilledToTest, "ci": BilledToTest, "bench": BilledToTest, "staging": BilledToPlatform, "production": BilledToPlatform} {
		if got, err := BillingFor(env); err != nil || got != want {
			t.Errorf("BillingFor(%s) = %q, %v; want %q", env, got, err, want)
		}
	}
	if _, err := BillingFor("prod"); err == nil {
		t.Error("unknown env accepted")
	}
}

func TestRouter(t *testing.T) {
	r, err := NewRouter(routingFS, "dev-cheap")
	if err != nil {
		t.Fatal(err)
	}
	// The environment default is dev-cheap: Haiku for the editor.
	got, err := r.Resolve("", "editor")
	if err != nil || got.Model != "claude-haiku-5-5" || got.Effort != "medium" || got.MaxTokens != 24000 || got.Profile != "dev-cheap" {
		t.Fatalf("dev-cheap editor = %+v, %v", got, err)
	}
	// A run overrides the profile: v1-start puts Opus 5.5 on the editor while the default stays dev-cheap.
	got, err = r.Resolve("v1-start", "editor")
	if err != nil || got.Model != "claude-opus-5-5" || got.Profile != "v1-start" || got.Stage != "editor" {
		t.Fatalf("v1-start editor = %+v, %v", got, err)
	}
	if got, _ = r.Resolve("dev-mid", "ideation"); got.Model != "claude-sonnet-5-5" {
		t.Fatalf("dev-mid ideation = %+v", got)
	}
	if got, _ = r.Resolve("v1-start", "research.workers"); got.Model != "claude-haiku-5-5" || got.Effort != "" {
		t.Fatalf("v1-start workers = %+v", got)
	}
	for _, c := range []struct{ profile, stage string }{{"", "nope"}, {"nope", "editor"}, {"../x", "editor"}} {
		if _, err := r.Resolve(c.profile, c.stage); err == nil {
			t.Errorf("Resolve(%q, %q) succeeded", c.profile, c.stage)
		}
	}
	// Apply fills a request.
	req := got.Apply(Request{})
	if req.Model != got.Model || req.MaxTokens != got.MaxTokens {
		t.Fatalf("Apply = %+v", req)
	}
	if _, err := NewRouter(routingFS, ""); err == nil {
		t.Error("empty default profile accepted")
	}
	if _, err := NewRouter(routingFS, "missing"); err == nil {
		t.Error("missing default profile accepted")
	}
}
