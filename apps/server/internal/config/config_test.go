package config

import (
	"strings"
	"testing"
)

const testDB = "postgres://cf:cf@localhost:5432/cf?sslmode=disable"

// env returns a lookup over m plus a valid DATABASE_URL, which Load requires, unless m sets its own.
func env(m map[string]string) Getenv {
	return func(k string) string {
		if v, ok := m[k]; ok {
			return v
		}
		if k == "DATABASE_URL" {
			return testDB
		}
		return ""
	}
}

func TestLoadDatabaseURL(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil || c.DatabaseURL != testDB {
		t.Fatalf("DatabaseURL = %q, %v", c.DatabaseURL, err)
	}

	const secret = "hunter2-secret"
	for _, v := range []string{"", "   ", "mysql://u:" + secret + "@h/db", "localhost:5432", "postgres://:" + secret + "@/db", "://" + secret} {
		_, err := Load(env(map[string]string{"DATABASE_URL": v}))
		if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Errorf("%q: err = %v, want a DATABASE_URL error", v, err)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%q: the error repeats the password: %v", v, err)
		}
	}
}

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
		"APP_BASE_URL":                "https://staging.contentfabrique.app",
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
