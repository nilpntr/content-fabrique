package doctor

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/envfile"
)

// allowedEmpty are [AUTO] variables whose documented default is empty, so empty is a valid value: an empty
// ANTHROPIC_BASE_URL means a direct call, an empty R2_JURISDICTION means MinIO, and the media settings fall back to
// their defaults (docker or hardened, the system temp directory). The three generated secrets are empty in .env.example
// too, but must be filled in, so they are not listed.
var allowedEmpty = map[string]bool{
	"ANTHROPIC_BASE_URL":     true,
	"R2_JURISDICTION":        true,
	"MEDIA_SANDBOX_PROVIDER": true,
	"MEDIA_TEMP_DIR":         true,
}

const (
	ciNotUsedReason     = "not used in ci (replay mode, D-22)"
	dockerOnlyReason    = "only for the local Docker Postgres"
	gatewayRequiredNote = "needed because ANTHROPIC_BASE_URL is set (the gateway is authenticated, D-19)"
)

// notUsedIn is the explicit per-environment exception list to the tag rules. A variable listed for an environment
// is not required there and reports skipped with the reason; if someone sets it anyway it is checked like any
// other. This is code, not a change to the tags in .env.example. Every other variable follows the tags.
//
//   - ci holds no Anthropic or Clerk secrets: CI runs in replay mode and makes no live calls (decision D-22).
//   - staging and production use PlanetScale, so the POSTGRES_* variables, which only configure the local Docker
//     Postgres container, do not apply.
var notUsedIn = map[string]map[string]string{
	"ci": {
		"ANTHROPIC_API_KEY":          ciNotUsedReason,
		"CLERK_SECRET_KEY":           ciNotUsedReason,
		"VITE_CLERK_PUBLISHABLE_KEY": ciNotUsedReason,
	},
	"staging":    appServer,
	"production": appServer,
}

// appServer is what staging and production leave out. DATABASE_MIGRATE_URL, the schema owner's connection, never
// reaches an app server, so doctor checks it there only when someone sets it (for example from a laptop).
var appServer = map[string]string{
	"POSTGRES_USER":        dockerOnlyReason,
	"POSTGRES_PASSWORD":    dockerOnlyReason,
	"POSTGRES_DB":          dockerOnlyReason,
	"DATABASE_MIGRATE_URL": "not on app servers",
}

// enums are the variables with a fixed set of documented values. A value inside the set is not secret, so the
// Redactor leaves it alone and the report can say "profile dev-cheap".
var enums = map[string][]string{
	"CF_ENV":                 {"local", "ci", "bench", "staging", "production"},
	"MEDIA_SANDBOX_PROVIDER": {"docker", "hardened", "local"},
	"ROUTING_PROFILE":        {"dev-cheap", "dev-mid", "v1-start"},
	"LLM_MODE":               {"live", "record", "replay"},
	"BILLING_PROVIDER":       {"paddle", "stripe"},
	"PADDLE_ENVIRONMENT":     {"sandbox", "production"},
	"R2_JURISDICTION":        {"", "eu"},
}

// A validator returns "" when the value is well formed, else a short reason. Reasons never contain the value.
type validator func(r *run, val string) string

var (
	hex32RE = regexp.MustCompile(`^[0-9a-f]{32}$`)
	slugRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// matches checks the value against a pattern; what describes it in the reason.
func matches(re *regexp.Regexp, what string) validator {
	return func(_ *run, val string) string {
		if !re.MatchString(val) {
			return "bad format (expected " + what + ")"
		}
		return ""
	}
}

func urlOf(schemes ...string) validator {
	return func(_ *run, val string) string {
		u, err := url.Parse(val)
		if err != nil || u.Host == "" || !slices.Contains(schemes, u.Scheme) {
			return "bad format (expected " + strings.Join(schemes, " or ") + " URL)"
		}
		return ""
	}
}

func prefixed(prefixes ...string) validator {
	return func(_ *run, val string) string {
		for _, p := range prefixes {
			if strings.HasPrefix(val, p) {
				return ""
			}
		}
		return "bad format (expected prefix " + strings.Join(prefixes, " or ") + ")"
	}
}

func oneOf(name string) validator {
	return func(_ *run, val string) string {
		if slices.Contains(enums[name], val) {
			return ""
		}
		return "bad format (not a documented value)"
	}
}

func minLen(n int) validator {
	return func(_ *run, val string) string {
		if len(val) < n {
			return "bad format (too short)"
		}
		return ""
	}
}

var validators = map[string]validator{
	"ANTHROPIC_API_KEY":          prefixed("sk-ant-"),
	"VITE_CLERK_PUBLISHABLE_KEY": prefixed("pk_test_", "pk_live_"),
	"CLERK_SECRET_KEY": func(r *run, val string) string {
		if why := prefixed("sk_test_", "sk_live_")(r, val); why != "" {
			return why
		}
		// The publishable and secret key must come from the same Clerk instance kind.
		pk := r.getenv("VITE_CLERK_PUBLISHABLE_KEY")
		pkTest, pkLive := strings.HasPrefix(pk, "pk_test_"), strings.HasPrefix(pk, "pk_live_")
		skTest := strings.HasPrefix(val, "sk_test_")
		if (pkTest && !skTest) || (pkLive && skTest) {
			return "test/live mismatch with VITE_CLERK_PUBLISHABLE_KEY"
		}
		return ""
	},
	"CLERK_WEBHOOK_SECRET": prefixed("whsec_"),

	// CF_ENV must name the environment being checked: a mismatch means the wrong file was loaded. bench is the
	// exception: it runs on a developer machine with the local .env, so CF_ENV=local is accepted there too.
	"CF_ENV": func(r *run, val string) string {
		if why := oneOf("CF_ENV")(r, val); why != "" {
			return why
		}
		benchOnLocal := r.opts.Env == "bench" && val == "local"
		if val != r.opts.Env && !benchOnLocal {
			return "does not match --env (wrong env file?)"
		}
		return ""
	},
	"ROUTING_PROFILE":    oneOf("ROUTING_PROFILE"),
	"LLM_MODE":           oneOf("LLM_MODE"),
	"BILLING_PROVIDER":   oneOf("BILLING_PROVIDER"),
	"PADDLE_ENVIRONMENT": oneOf("PADDLE_ENVIRONMENT"),
	"R2_JURISDICTION":    oneOf("R2_JURISDICTION"),

	// The local sandbox provider isolates nothing: staging and production refuse it (the worker and config.Load do too).
	"MEDIA_SANDBOX_PROVIDER": func(r *run, val string) string {
		if why := oneOf("MEDIA_SANDBOX_PROVIDER")(r, val); why != "" {
			return why
		}
		if val == "local" && (r.opts.Env == "staging" || r.opts.Env == "production") {
			return "local isolates nothing and is not allowed in " + r.opts.Env + " (use hardened)"
		}
		return ""
	},
	"MEDIA_TEMP_DIR": func(_ *run, val string) string {
		if !strings.HasPrefix(val, "/") {
			return "bad format (expected an absolute path)"
		}
		return ""
	},
	"MEDIA_CONCURRENCY": func(_ *run, val string) string {
		if n, err := strconv.Atoi(val); err != nil || n < 1 || n > 32 {
			return "bad format (expected a whole number from 1 to 32)"
		}
		return ""
	},

	"APP_BASE_URL":                urlOf("http", "https"),
	"API_BASE_URL":                urlOf("http", "https"),
	"VITE_API_BASE_URL":           urlOf("http", "https"),
	"ANTHROPIC_BASE_URL":          urlOf("http", "https"),
	"SANDBOX_GATEWAY_URL":         urlOf("http", "https"),
	"OTEL_EXPORTER_OTLP_ENDPOINT": urlOf("http", "https"),
	"TIKTOK_REDIRECT_URI":         urlOf("http", "https"),
	"SENTRY_DSN":                  urlOf("http", "https"),
	"VITE_SENTRY_DSN":             urlOf("http", "https"),
	"DATABASE_URL":                urlOf("postgres", "postgresql"),
	"DATABASE_DIRECT_URL":         urlOf("postgres", "postgresql"),
	"DATABASE_MIGRATE_URL":        urlOf("postgres", "postgresql"),
	"R2_ENDPOINT": func(r *run, val string) string {
		if why := urlOf("http", "https")(r, val); why != "" {
			return why
		}
		// An EU bucket is reachable only through the EU endpoint (docs/06-setup-and-credentials.md §3.3).
		if r.getenv("R2_JURISDICTION") == "eu" {
			if u, err := url.Parse(val); err != nil || !strings.HasSuffix(u.Hostname(), ".eu.r2.cloudflarestorage.com") {
				return "bad format (EU jurisdiction needs the .eu.r2.cloudflarestorage.com endpoint)"
			}
		}
		return ""
	},

	"SECRETS_MASTER_KEY": func(_ *run, val string) string {
		if b, err := base64.StdEncoding.DecodeString(val); err != nil || len(b) != 32 {
			return "bad format (expected base64 of 32 bytes)"
		}
		return ""
	},
	"SECRETS_MASTER_KEY_VERSION": func(_ *run, val string) string {
		if n, err := strconv.Atoi(val); err != nil || n < 1 {
			return "bad format (expected a positive integer)"
		}
		return ""
	},
	"SANDBOX_GATEWAY_TOKEN": minLen(16),
	"RIVER_UI_PASSWORD":     minLen(16),

	"CLOUDFLARE_ACCOUNT_ID": matches(hex32RE, "32 hex characters"),
	"CLOUDFLARE_ZONE_ID":    matches(hex32RE, "32 hex characters"),
	"CF_AIG_GATEWAY_ID":     matches(slugRE, "a gateway slug"),
	"OTEL_EXPORTER_OTLP_HEADERS": func(_ *run, val string) string {
		for _, kv := range strings.Split(val, ",") {
			if k, _, ok := strings.Cut(kv, "="); !ok || strings.TrimSpace(k) == "" {
				return "bad format (expected key=value pairs)"
			}
		}
		return ""
	},

	// Stripe: a restricted key, not the full secret key (.env.example).
	"STRIPE_SECRET_KEY":      prefixed("rk_test_", "rk_live_"),
	"STRIPE_PUBLISHABLE_KEY": prefixed("pk_test_", "pk_live_"),
	"STRIPE_WEBHOOK_SECRET":  prefixed("whsec_"),
}

// required reports whether the variable must be set for this environment and phase, from its tags.
func (r *run) required(v envfile.Var) bool {
	if _, exempt := notUsedIn[r.opts.Env][v.Name]; exempt {
		return false
	}
	// The gateway is authenticated (D-19), so an ANTHROPIC_BASE_URL needs its token in every environment.
	if v.Name == "CF_AIG_TOKEN" && r.gatewayInUse() {
		return true
	}
	has := func(t envfile.Tag) bool { return slices.Contains(v.Tags, t) }
	// Billing: only the chosen provider's set is required.
	if has(envfile.TagP7) {
		if r.opts.Phase < 7 {
			return false
		}
		return r.billingMine(v.Name)
	}
	switch {
	case has(envfile.TagNow), has(envfile.TagAuto):
		return true
	case has(envfile.TagStaging):
		return r.opts.Env == "staging" || r.opts.Env == "production"
	case has(envfile.TagP6):
		return r.opts.Phase >= 6
	}
	return false // [optional]
}

// gatewayInUse reports whether the AI Gateway is called: ANTHROPIC_BASE_URL is set, and the environment uses
// Anthropic at all (ci without a key is in replay mode and calls nothing).
func (r *run) gatewayInUse() bool {
	if r.getenv("ANTHROPIC_BASE_URL") == "" {
		return false
	}
	replayOnly := r.opts.Env == "ci" && r.getenv("ANTHROPIC_API_KEY") == ""
	return !replayOnly
}

// billingMine reports whether a [YOU-P7] variable belongs to the chosen BILLING_PROVIDER (or to neither).
func (r *run) billingMine(name string) bool {
	provider := r.getenv("BILLING_PROVIDER")
	switch {
	case strings.HasPrefix(name, "PADDLE_"):
		return provider == "paddle"
	case strings.HasPrefix(name, "STRIPE_"):
		return provider == "stripe"
	}
	return true
}

// notRequiredReason says why an empty, not-required variable is skipped.
func (r *run) notRequiredReason(v envfile.Var) string {
	if why, exempt := notUsedIn[r.opts.Env][v.Name]; exempt {
		return why
	}
	has := func(t envfile.Tag) bool { return slices.Contains(v.Tags, t) }
	switch {
	case has(envfile.TagOptional):
		return "optional, not set"
	case has(envfile.TagP7):
		if r.opts.Phase < 7 {
			return "not needed before P7"
		}
		return "other billing provider"
	case has(envfile.TagP6):
		return "not needed before P6"
	case has(envfile.TagStaging):
		return "not needed for " + r.opts.Env
	}
	return "not required"
}

// checkVariables reports every variable in .env.example, in file order.
func (r *run) checkVariables() []Result {
	var out []Result
	for _, v := range r.opts.Example {
		val := r.vars[v.Name]
		res := Result{Kind: "variable", Name: v.Name}
		switch {
		case val == "" && v.Name == "R2_JURISDICTION" && (r.opts.Env == "staging" || r.opts.Env == "production"):
			res.Status, res.Reason = Missing, "must be eu for real R2"
		case val == "" && allowedEmpty[v.Name]:
			res.Status, res.Reason = OK, "empty is valid"
		case val == "" && r.required(v):
			res.Status, res.Reason = Missing, "not set"
			if v.Name == "CF_AIG_TOKEN" && r.gatewayInUse() {
				res.Reason = "not set, " + gatewayRequiredNote
			}
		case val == "":
			res.Status, res.Reason = Skipped, r.notRequiredReason(v)
		default:
			res.Status, res.Reason = OK, "set"
			if check := validators[v.Name]; check != nil {
				if why := check(r, val); why != "" {
					res.Status, res.Reason = Invalid, why
				}
			}
		}
		r.varStatus[v.Name] = res.Status
		out = append(out, res)
	}
	return out
}

// problem says why a service cannot be checked because of its variables, as a ready-made result, or ok=false when
// every named variable is set and well formed. A missing variable gives Missing, a malformed one Invalid.
func (r *run) problem(kind, name string, vars ...string) (Result, bool) {
	var missing, invalid []string
	for _, v := range vars {
		switch {
		case r.vars[v] == "":
			missing = append(missing, v)
		case r.varStatus[v] == Invalid:
			invalid = append(invalid, v)
		}
	}
	switch {
	case len(missing) > 0:
		return Result{Kind: kind, Name: name, Status: Missing, Reason: strings.Join(missing, ", ") + " not set"}, true
	case len(invalid) > 0:
		return Result{Kind: kind, Name: name, Status: Invalid, Reason: strings.Join(invalid, ", ") + " has a bad format"}, true
	}
	return Result{}, false
}

func skipped(name, reason string) Result {
	return Result{Kind: "service", Name: name, Status: Skipped, Reason: reason}
}

func okResult(name, reason string) Result {
	return Result{Kind: "service", Name: name, Status: OK, Reason: reason}
}

func failed(name, reason string) Result {
	return Result{Kind: "service", Name: name, Status: Invalid, Reason: reason}
}

// groupResults adds the TikTok and billing lines. They have format checks only until their phase, so each is the
// roll-up of its variables' results.
func (r *run) groupResults() []Result {
	group := func(name, notBefore string, in func(string) bool) Result {
		var bad, missing []string
		set := 0
		for _, v := range r.opts.Example {
			if !in(v.Name) {
				continue
			}
			switch r.varStatus[v.Name] {
			case Invalid:
				bad = append(bad, v.Name)
			case Missing:
				missing = append(missing, v.Name)
			case OK:
				// A value that is just the .env.example default does not count as the person having set it up.
				if val := r.vars[v.Name]; val != "" && val != v.Default {
					set++
				}
			}
		}
		switch {
		case len(bad) > 0:
			return Result{Kind: "service", Name: name, Status: Invalid, Reason: "bad format: " + strings.Join(bad, ", ")}
		case len(missing) > 0:
			return Result{Kind: "service", Name: name, Status: Missing, Reason: strings.Join(missing, ", ") + " not set"}
		case set == 0:
			return skipped(name, "not needed before "+notBefore)
		}
		return okResult(name, "format checks only")
	}
	return []Result{
		group("tiktok", "P6", func(n string) bool { return strings.HasPrefix(n, "TIKTOK_") }),
		group("billing", "P7", func(n string) bool {
			return n == "BILLING_PROVIDER" || strings.HasPrefix(n, "PADDLE_") || strings.HasPrefix(n, "STRIPE_")
		}),
	}
}
