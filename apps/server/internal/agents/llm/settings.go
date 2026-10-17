package llm

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Mode is LLM_MODE.
type Mode string

// The three modes (docs/design/agent-runtime-v1.md section 2).
const (
	// ModeLive calls the API.
	ModeLive Mode = "live"
	// ModeRecord calls the API and writes each exchange to a cassette.
	ModeRecord Mode = "record"
	// ModeReplay serves cassettes and never calls the API. CI runs it.
	ModeReplay Mode = "replay"
)

// Defaults of the settings, in micro-dollars where they are money.
const (
	// DefaultMaxRequestMicros is $2.
	DefaultMaxRequestMicros int64 = 2_000_000
	// DefaultDailySpendCapMicros is $20.
	DefaultDailySpendCapMicros int64 = 20_000_000
	// DefaultPlatformDailySpendCapMicros is $100: the breaker over all platform-billed cost per UTC day. Staging and production should
	// set their own on purpose (cfctl doctor requires it); the default only keeps an unset one from meaning "no breaker".
	DefaultPlatformDailySpendCapMicros int64 = 100_000_000
	// DefaultConcurrency is the per-key limit on requests in flight.
	DefaultConcurrency = 4
	// DefaultCassetteDir is relative to the working directory, which is the package directory under `go test`.
	DefaultCassetteDir = "testdata/cassettes"
	// DefaultMaxTokens is the router's max_tokens when a profile does not say.
	DefaultMaxTokens = 16000
)

// Settings is the non-secret part of the model-access configuration. The API key and the gateway token are not in
// here: only the factory reads them, at the moment it builds a client.
type Settings struct {
	// Env is CF_ENV: local, ci, bench, staging or production.
	Env string
	// Mode is LLM_MODE.
	Mode Mode
	// BaseURL is ANTHROPIC_BASE_URL; empty calls api.anthropic.com directly (the gateway bypass).
	BaseURL string
	// RoutingProfile is ROUTING_PROFILE, the default profile.
	RoutingProfile string
	// MaxRequestMicros is LLM_MAX_REQUEST_MICROS: no single call may have a worst-case cost above it.
	MaxRequestMicros int64
	// DailySpendCapMicros is DAILY_SPEND_CAP_MICROS: per workspace, per UTC day.
	DailySpendCapMicros int64
	// PlatformDailySpendCapMicros is PLATFORM_DAILY_SPEND_CAP_MICROS: the breaker over every billed_to=platform cost (all workspaces, all
	// kinds) per UTC day. When it trips, managed paid calls are refused until the next UTC day.
	PlatformDailySpendCapMicros int64
	// Concurrency is LLM_KEY_CONCURRENCY: requests in flight per API key.
	Concurrency int
	// AllowUnpriced is LLM_ALLOW_UNPRICED=1: run a model that has no price (cost recorded as 0).
	AllowUnpriced bool
	// CassetteDir is LLM_CASSETTE_DIR.
	CassetteDir string
}

// SpendCaps are the money caps the settings say, for the spend gate: the workspace's daily cap and the platform breaker. The audio cap
// (TRANSCRIBE_DAILY_MINUTES) is the transcription pipeline's to add.
func (s Settings) SpendCaps() store.SpendCaps {
	return store.SpendCaps{WorkspaceDailyMicros: s.DailySpendCapMicros, PlatformDailyMicros: s.PlatformDailySpendCapMicros}
}

// LoadSettings parses the settings from environment variables, returning every problem found. Messages name the
// variable and never repeat a value.
func LoadSettings(getenv func(string) string) (Settings, error) {
	var errs []error
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	s := Settings{
		Env:            get("CF_ENV"),
		Mode:           Mode(strings.ToLower(get("LLM_MODE"))),
		BaseURL:        get("ANTHROPIC_BASE_URL"),
		RoutingProfile: get("ROUTING_PROFILE"),
		CassetteDir:    get("LLM_CASSETTE_DIR"),
	}
	if s.Env == "" {
		s.Env = "local"
	}
	if _, err := BillingFor(s.Env); err != nil {
		errs = append(errs, errors.New("CF_ENV: must be local, ci, bench, staging or production"))
	}
	if s.Mode == "" {
		s.Mode = ModeLive
	}
	switch s.Mode {
	case ModeLive, ModeRecord, ModeReplay:
	default:
		errs = append(errs, errors.New("LLM_MODE: must be live, record or replay"))
	}
	if s.Mode != ModeLive && (s.Env == "staging" || s.Env == "production") {
		errs = append(errs, errors.New("LLM_MODE: record and replay are for development, ci and bench; staging and production run live"))
	}
	if s.BaseURL != "" {
		u, err := url.Parse(s.BaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" {
			errs = append(errs, errors.New("ANTHROPIC_BASE_URL: must be an http(s) URL without credentials or a query"))
		} else if u.Scheme == "http" && s.Env != "local" && s.Env != "ci" && s.Env != "bench" {
			errs = append(errs, errors.New("ANTHROPIC_BASE_URL: must be https outside development"))
		}
	}
	var err error
	if s.MaxRequestMicros, err = positiveInt(get("LLM_MAX_REQUEST_MICROS"), DefaultMaxRequestMicros); err != nil {
		errs = append(errs, fmt.Errorf("LLM_MAX_REQUEST_MICROS: %w", err))
	}
	if s.DailySpendCapMicros, err = positiveInt(get("DAILY_SPEND_CAP_MICROS"), DefaultDailySpendCapMicros); err != nil {
		errs = append(errs, fmt.Errorf("DAILY_SPEND_CAP_MICROS: %w", err))
	}
	if s.PlatformDailySpendCapMicros, err = positiveInt(get("PLATFORM_DAILY_SPEND_CAP_MICROS"), DefaultPlatformDailySpendCapMicros); err != nil {
		errs = append(errs, fmt.Errorf("PLATFORM_DAILY_SPEND_CAP_MICROS: %w", err))
	}
	conc, err := positiveInt(get("LLM_KEY_CONCURRENCY"), DefaultConcurrency)
	if err != nil || conc > 64 {
		errs = append(errs, errors.New("LLM_KEY_CONCURRENCY: must be an integer from 1 to 64"))
	}
	s.Concurrency = int(conc)
	switch v := get("LLM_ALLOW_UNPRICED"); v {
	case "", "0":
	case "1":
		// An unpriced paid call cannot be capped or metered: only development and ci may run one (D-31). config.Load refuses it too.
		if !config.UnpricedAllowedIn(s.Env) {
			errs = append(errs, errors.New("LLM_ALLOW_UNPRICED: honoured only in local and ci; staging, production and bench refuse an unpriced paid provider"))
		}
		s.AllowUnpriced = true
	default:
		errs = append(errs, errors.New("LLM_ALLOW_UNPRICED: must be 1 or empty"))
	}
	if s.CassetteDir == "" {
		s.CassetteDir = DefaultCassetteDir
	}
	return s, errors.Join(errs...)
}

// positiveInt parses a positive integer; empty gives def.
func positiveInt(v string, def int64) (int64, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return def, errors.New("must be a positive integer")
	}
	return n, nil
}

// BillingFor is the billed_to of a managed workspace's calls: test in local, ci and bench (so test spend never
// mixes into customer margin), platform in staging and production.
func BillingFor(env string) (Billing, error) {
	switch env {
	case "staging", "production":
		return BilledToPlatform, nil
	case "local", "ci", "bench":
		return BilledToTest, nil
	default:
		return "", fmt.Errorf("llm: CF_ENV %q is not one of local, ci, bench, staging, production", env)
	}
}

// retry tuning, kept together. Tests override them through the limiter.
const (
	defaultBackoffBase  = time.Second
	defaultBackoffCap   = 30 * time.Second
	defaultMaxTotalWait = 5 * time.Minute
	defaultMaxAttempts  = 8
)
