package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != EnvLocal || c.APIListenAddr != ":8080" || c.APIBaseURL != "http://localhost:8080" || c.OTLPEndpoint != "" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if !c.IsLocal() {
		t.Fatal("expected local")
	}
}

func TestLoadListenAddr(t *testing.T) {
	tests := []struct{ base, want string }{
		{"http://localhost:8080", ":8080"},
		{"http://localhost:9090", ":9090"},
		{"https://api.contentfabrique.app", ":8080"},
		{"https://api-staging.contentfabrique.app:8443/", ":8443"},
	}
	for _, tc := range tests {
		c, err := Load(env(map[string]string{"API_BASE_URL": tc.base}))
		if err != nil {
			t.Fatalf("%s: %v", tc.base, err)
		}
		if c.APIListenAddr != tc.want {
			t.Errorf("%s: got %s, want %s", tc.base, c.APIListenAddr, tc.want)
		}
	}
}

func TestLoadValues(t *testing.T) {
	c, err := Load(env(map[string]string{
		"CF_ENV":                      " staging ",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://api.axiom.co",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != EnvStaging || c.OTLPEndpoint != "https://api.axiom.co" || c.IsLocal() {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	_, err := Load(env(map[string]string{
		"CF_ENV":                      "prod",
		"API_BASE_URL":                "localhost:8080",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "nope",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"CF_ENV", "API_BASE_URL", "OTEL_EXPORTER_OTLP_ENDPOINT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoadRejectsOTLPScheme(t *testing.T) {
	for _, ep := range []string{"ftp://collector:4318", "grpc://collector:4317", "collector:4318"} {
		if _, err := Load(env(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ep})); err == nil {
			t.Errorf("%q: expected error", ep)
		}
	}
}
