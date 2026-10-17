// Package config loads service configuration from environment variables.
//
// Every variable read here must be listed in .env.example at the repository root.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
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

// DefaultAppBaseURL is where the web dev server listens, the APP_BASE_URL of a developer machine.
const DefaultAppBaseURL = "http://localhost:3000"

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
	// AppBaseURL is the public base URL of the web app (APP_BASE_URL), as given.
	AppBaseURL string
	// AppOrigin is AppBaseURL reduced to its origin (scheme://host[:port], no path or trailing slash). It is the one
	// origin the CORS allowlist admits and the only "authorized party" of a session token.
	AppOrigin string
	// ClerkSecretKey is CLERK_SECRET_KEY. The API verifies session tokens and calls Clerk's Backend API with it. Empty
	// means Clerk is not configured: the authenticated endpoints answer 503 instead of the process refusing to start
	// (the ci environment holds no Clerk secrets, D-22). It is a secret and is never logged.
	ClerkSecretKey string
	// ClerkWebhookSecret is CLERK_WEBHOOK_SECRET, the Svix signing secret of the webhook endpoint. Empty means the
	// endpoint answers 503. A secret: never logged.
	ClerkWebhookSecret string
	// DatabaseURL is DATABASE_URL: the pooled Postgres URL of the app role. It carries a password, so it is never
	// logged. The migrate role's URL is not here: only `cfctl migrate` reads it.
	DatabaseURL string
	// DatabaseDirectURL is DATABASE_DIRECT_URL: the direct (not pooled) Postgres URL of the same app role. The worker's
	// River client listens on it, because LISTEN/NOTIFY does not work through a transaction pooler. Optional here (the
	// api does not use it); the worker refuses to start without it. It carries a password: never logged.
	DatabaseDirectURL string
	// RiverUIPassword is RIVER_UI_PASSWORD, the HTTP Basic password of River UI (worker flag -river-ui-addr). A secret:
	// never logged. The worker refuses to serve River UI when it is empty.
	RiverUIPassword string

	// The object storage settings (R2_*): MinIO locally, Cloudflare R2 in staging and production. All four of the
	// endpoint, bucket and key pair are set or none is: without them the assets API answers 503 storage_not_configured
	// instead of the process refusing to start (the ci environment holds no storage). The secret is never logged.
	R2Endpoint        string
	R2Bucket          string
	R2AccessKeyID     string
	R2SecretAccessKey string
	// R2Jurisdiction is "eu" for real R2 and empty for MinIO.
	R2Jurisdiction string

	// The media pipeline of the worker (P1-04): which sandbox runs ffmpeg and ffprobe on uploaded files, with which image,
	// where the job directories live and how many ingests run at once.
	//
	// MediaSandboxProvider is MEDIA_SANDBOX_PROVIDER: "docker" (the default on a developer machine and in CI), "hardened"
	// (docker under gVisor; the default in staging and production) or "local" (no isolation at all; refused in staging and
	// production).
	MediaSandboxProvider string
	// MediaToolsImage is MEDIA_TOOLS_IMAGE, the media-tools image (packages/media-tools). It is never pulled at job time.
	MediaToolsImage string
	// MediaTempDir is MEDIA_TEMP_DIR: an absolute directory the worker may write to and the sandbox may mount from.
	MediaTempDir string
	// MediaConcurrency is MEDIA_CONCURRENCY: ingests that run at once on one worker.
	MediaConcurrency int
}

// The sandbox providers MEDIA_SANDBOX_PROVIDER accepts.
const (
	SandboxDocker   = "docker"
	SandboxHardened = "hardened"
	SandboxLocal    = "local"
)

// Defaults of the media settings.
const (
	DefaultMediaToolsImage  = "media-tools:dev"
	DefaultMediaConcurrency = 2
	maxMediaConcurrency     = 32
)

// Storage is the connection to the bucket, from the R2_* settings.
func (c Config) Storage() storage.Config {
	return storage.Config{Endpoint: c.R2Endpoint, Bucket: c.R2Bucket, AccessKeyID: c.R2AccessKeyID, SecretAccessKey: c.R2SecretAccessKey, Jurisdiction: c.R2Jurisdiction}
}

// ObjectStorageConfigured reports whether the R2_* settings are present (storage.Config.Configured is the one definition).
func (c Config) ObjectStorageConfigured() bool { return c.Storage().Configured() }

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

		DatabaseDirectURL: strings.TrimSpace(getenv("DATABASE_DIRECT_URL")),
		// Not trimmed: Basic auth compares the password as typed.
		RiverUIPassword: getenv("RIVER_UI_PASSWORD"),

		AppBaseURL:         strings.TrimSpace(getenv("APP_BASE_URL")),
		ClerkSecretKey:     strings.TrimSpace(getenv("CLERK_SECRET_KEY")),
		ClerkWebhookSecret: strings.TrimSpace(getenv("CLERK_WEBHOOK_SECRET")),

		R2Endpoint:        strings.TrimSpace(getenv("R2_ENDPOINT")),
		R2Bucket:          strings.TrimSpace(getenv("R2_BUCKET")),
		R2AccessKeyID:     strings.TrimSpace(getenv("R2_ACCESS_KEY_ID")),
		R2SecretAccessKey: strings.TrimSpace(getenv("R2_SECRET_ACCESS_KEY")),
		R2Jurisdiction:    strings.ToLower(strings.TrimSpace(getenv("R2_JURISDICTION"))),

		MediaSandboxProvider: strings.ToLower(strings.TrimSpace(getenv("MEDIA_SANDBOX_PROVIDER"))),
		MediaToolsImage:      strings.TrimSpace(getenv("MEDIA_TOOLS_IMAGE")),
		MediaTempDir:         strings.TrimSpace(getenv("MEDIA_TEMP_DIR")),
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

	// The CORS allowlist and the token's authorized party are built from this, so staging and production must say it:
	// a silent localhost default there would lock every real user out.
	if c.AppBaseURL == "" && c.Env != EnvStaging && c.Env != EnvProduction {
		c.AppBaseURL = DefaultAppBaseURL
	}
	if appOrigin, err := originOf(c.AppBaseURL, c.Env); err != nil {
		errs = append(errs, fmt.Errorf("APP_BASE_URL: %w", err))
	} else {
		c.AppOrigin = appOrigin
	}

	// The messages never repeat the values: they are secrets.
	if c.ClerkSecretKey != "" && !strings.HasPrefix(c.ClerkSecretKey, "sk_") {
		errs = append(errs, errors.New("CLERK_SECRET_KEY: must be a Clerk secret key (sk_test_... or sk_live_...)"))
	}
	if c.ClerkWebhookSecret != "" && !strings.HasPrefix(c.ClerkWebhookSecret, "whsec_") {
		errs = append(errs, errors.New("CLERK_WEBHOOK_SECRET: must be a Svix signing secret (whsec_...)"))
	}

	// All or none of the storage settings. The messages name the variables and never repeat the values.
	if set := countSet(c.R2Endpoint, c.R2Bucket, c.R2AccessKeyID, c.R2SecretAccessKey); set != 0 && set != 4 {
		errs = append(errs, errors.New("R2_ENDPOINT, R2_BUCKET, R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY must be set together"))
	}
	if c.R2Endpoint != "" {
		if u, err := url.Parse(c.R2Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, errors.New("R2_ENDPOINT: must be an absolute http(s) URL"))
		}
	}
	if c.R2Jurisdiction != "" && c.R2Jurisdiction != "eu" {
		errs = append(errs, errors.New(`R2_JURISDICTION: must be empty (MinIO) or "eu"`))
	}

	errs = append(errs, c.loadMedia(getenv)...)

	if c.OTLPEndpoint != "" {
		if u, err := url.Parse(c.OTLPEndpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT: must be an absolute http(s) URL"))
		}
	}

	// The messages never repeat the value: it holds a password.
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL: required (see .env.example)"))
	} else if err := ValidatePostgresURL(c.DatabaseURL); err != nil {
		errs = append(errs, fmt.Errorf("DATABASE_URL: %w", err))
	}

	if c.DatabaseDirectURL != "" {
		if err := ValidatePostgresURL(c.DatabaseDirectURL); err != nil {
			errs = append(errs, fmt.Errorf("DATABASE_DIRECT_URL: %w", err))
		}
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

// loadMedia fills in and checks the media settings (the three strings are already read).
func (c *Config) loadMedia(getenv Getenv) []error {
	var errs []error
	if c.MediaSandboxProvider == "" {
		c.MediaSandboxProvider = SandboxDocker
		if c.Env == EnvStaging || c.Env == EnvProduction {
			c.MediaSandboxProvider = SandboxHardened
		}
	}
	switch c.MediaSandboxProvider {
	case SandboxDocker, SandboxHardened:
	case SandboxLocal:
		if err := sandbox.CheckLocalAllowed(c.Env); err != nil {
			errs = append(errs, fmt.Errorf("MEDIA_SANDBOX_PROVIDER: %w", err))
		}
	default:
		errs = append(errs, fmt.Errorf("MEDIA_SANDBOX_PROVIDER: %q is not one of docker, hardened, local", c.MediaSandboxProvider))
	}
	if c.MediaToolsImage == "" {
		c.MediaToolsImage = DefaultMediaToolsImage
	}
	if c.MediaTempDir == "" {
		c.MediaTempDir = filepath.Join(os.TempDir(), "cf-media")
	} else if !filepath.IsAbs(c.MediaTempDir) {
		errs = append(errs, errors.New("MEDIA_TEMP_DIR: must be an absolute path"))
	}
	c.MediaConcurrency = DefaultMediaConcurrency
	if raw := strings.TrimSpace(getenv("MEDIA_CONCURRENCY")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxMediaConcurrency {
			errs = append(errs, fmt.Errorf("MEDIA_CONCURRENCY: must be a whole number from 1 to %d", maxMediaConcurrency))
		} else {
			c.MediaConcurrency = n
		}
	}
	return errs
}

func countSet(values ...string) int {
	n := 0
	for _, v := range values {
		if v != "" {
			n++
		}
	}
	return n
}

// ValidatePostgresURL checks that raw is a postgres:// or postgresql:// URL with a host. The error never contains
// raw, because a database URL carries a password. cfctl migrate uses it for DATABASE_MIGRATE_URL too.
func ValidatePostgresURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return errors.New("must be a postgres:// or postgresql:// URL")
	}
	return nil
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

// originOf reduces an app base URL to its origin. It must be an absolute http(s) URL with a host and no credentials,
// query or fragment; a path other than "/" is refused, because an origin has none. staging and production must be https.
func originOf(base, env string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "", errors.New("must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme %q is not http or https", u.Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("must be a bare origin: no credentials, path, query or fragment")
	}
	if u.Scheme != "https" && (env == EnvStaging || env == EnvProduction) {
		return "", errors.New("must be https in staging and production")
	}
	// The same form a browser sends in the Origin header: lowercase scheme and host, the port only when it is not
	// the default of the scheme.
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	result := u.Scheme + "://" + host
	if p := u.Port(); p != "" && (u.Scheme != "http" || p != "80") && (u.Scheme != "https" || p != "443") {
		result += ":" + p
	}
	return result, nil
}

// String describes the config without its secrets (database URL, Clerk keys), so a stray %v or %+v cannot leak them.
func (c Config) String() string {
	return fmt.Sprintf("config{env=%s api=%s app=%s clerk_secret=%t clerk_webhook_secret=%t river_ui_password=%t otlp=%t storage=%t}",
		c.Env, c.APIBaseURL, c.AppOrigin, c.ClerkSecretKey != "", c.ClerkWebhookSecret != "", c.RiverUIPassword != "", c.OTLPEndpoint != "", c.ObjectStorageConfigured())
}

// GoString makes %#v print the same redacted form.
func (c Config) GoString() string { return c.String() }

// IsLocal reports whether the config describes a developer machine, where logs are human-readable.
func (c Config) IsLocal() bool { return c.Env == EnvLocal }
