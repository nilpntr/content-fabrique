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

func TestLoadObjectStorage(t *testing.T) {
	full := map[string]string{
		"R2_ENDPOINT": "http://localhost:9000", "R2_BUCKET": "cf-media-dev", "R2_ACCESS_KEY_ID": "minioadmin", "R2_SECRET_ACCESS_KEY": "s3cr3t-storage-key",
	}
	c, err := Load(env(full))
	if err != nil || !c.ObjectStorageConfigured() || c.R2Bucket != "cf-media-dev" || c.R2Jurisdiction != "" {
		t.Fatalf("full settings: %+v, %v", c, err)
	}
	if s := c.String() + " " + c.GoString(); strings.Contains(s, "s3cr3t-storage-key") || !strings.Contains(s, "storage=true") {
		t.Errorf("String() = %s", s)
	}
	// None at all is fine: the assets API then answers 503.
	if c, err := Load(env(nil)); err != nil || c.ObjectStorageConfigured() {
		t.Errorf("no settings: %+v, %v", c, err)
	}
	// R2 in the cloud names its jurisdiction.
	eu := map[string]string{}
	for k, v := range full {
		eu[k] = v
	}
	eu["R2_ENDPOINT"], eu["R2_JURISDICTION"] = "https://acct.eu.r2.cloudflarestorage.com", " EU "
	if c, err := Load(env(eu)); err != nil || c.R2Jurisdiction != "eu" {
		t.Errorf("eu: %+v, %v", c, err)
	}

	for name, mutate := range map[string]func(map[string]string){
		"a missing bucket":   func(m map[string]string) { delete(m, "R2_BUCKET") },
		"a missing secret":   func(m map[string]string) { delete(m, "R2_SECRET_ACCESS_KEY") },
		"a bad endpoint":     func(m map[string]string) { m["R2_ENDPOINT"] = "localhost:9000" },
		"a bad jurisdiction": func(m map[string]string) { m["R2_JURISDICTION"] = "us" },
	} {
		m := map[string]string{}
		for k, v := range full {
			m[k] = v
		}
		mutate(m)
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), "R2_") {
			t.Errorf("%s: err = %v, want an R2_ error", name, err)
		} else if strings.Contains(err.Error(), "s3cr3t-storage-key") {
			t.Errorf("%s: the error repeats the secret: %v", name, err)
		}
	}
}
