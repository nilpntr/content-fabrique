// Package config loads service configuration from environment variables.
//
// Every variable read here must be listed in .env.example at the repository root.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Environment names, the values CF_ENV can take.
const (
	EnvLocal      = "local"
	EnvCI         = "ci"
	EnvBench      = "bench"
	EnvStaging    = "staging"
	EnvProduction = "production"
)

// DefaultAPIPort is the listen port when API_BASE_URL carries none. In staging and production
// API_BASE_URL is the public https URL behind Cloudflare Tunnel, which has no port.
const DefaultAPIPort = "8080"

// Config is the parsed service configuration.
type Config struct {
	// Env is the environment name (CF_ENV).
	Env string
	// APIBaseURL is the public base URL of the API (API_BASE_URL).
	APIBaseURL string
	// APIListenAddr is the address the API binds, derived from API_BASE_URL.
	APIListenAddr string
	// OTLPEndpoint is OTEL_EXPORTER_OTLP_ENDPOINT. Empty disables trace export. The OpenTelemetry SDK
	// reads this and OTEL_EXPORTER_OTLP_HEADERS itself, so the value is only used to decide whether to export.
	OTLPEndpoint string
	// DatabaseURL is DATABASE_URL: the pooled Postgres URL of the app role. It carries a password, so it is never
	// logged. The migrate role's URL is not here: only `cfctl migrate` reads it.
	DatabaseURL string
}

// Getenv looks up an environment variable, like os.Getenv.
type Getenv func(key string) string

// Load parses configuration from getenv (pass os.Getenv). It returns every problem found, joined.
func Load(getenv Getenv) (Config, error) {
	var errs []error
	c := Config{
		Env:          strings.TrimSpace(getenv("CF_ENV")),
		APIBaseURL:   strings.TrimSpace(getenv("API_BASE_URL")),
		OTLPEndpoint: strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
		DatabaseURL:  strings.TrimSpace(getenv("DATABASE_URL")),
	}

	if c.Env == "" {
		c.Env = EnvLocal
	}
	switch c.Env {
	case EnvLocal, EnvCI, EnvBench, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("CF_ENV: %q is not one of local, ci, bench, staging, production", c.Env))
	}

	if c.APIBaseURL == "" {
		c.APIBaseURL = "http://localhost:" + DefaultAPIPort
	}
	addr, err := listenAddr(c.APIBaseURL)
	if err != nil {
		errs = append(errs, fmt.Errorf("API_BASE_URL: %w", err))
	}
	c.APIListenAddr = addr

	if c.OTLPEndpoint != "" {
		if u, err := url.Parse(c.OTLPEndpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT: must be an absolute http(s) URL"))
		}
	}

	// The messages never repeat the value: it holds a password.
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL: required (see .env.example)"))
	} else if u, err := url.Parse(c.DatabaseURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		errs = append(errs, errors.New("DATABASE_URL: must be a postgres:// or postgresql:// URL"))
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

// listenAddr derives the bind address from the API base URL: its explicit port, else DefaultAPIPort.
func listenAddr(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "", errors.New("must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme %q is not http or https", u.Scheme)
	}
	port := u.Port()
	if port == "" {
		port = DefaultAPIPort
	}
	return ":" + port, nil
}

// IsLocal reports whether the config describes a developer machine, where logs are human-readable.
func (c Config) IsLocal() bool { return c.Env == EnvLocal }
