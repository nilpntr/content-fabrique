package doctor

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// sentinelVars gives every variable in .env.example a distinctive value that is well formed enough to get past the
// format checks, so doctor goes on to call the (fake or dead) services with it. Enum variables keep valid values.
func sentinelVars(t *testing.T, api *apiFakes, dead string) map[string]string {
	t.Helper()
	vars := map[string]string{}
	for _, v := range loadExample(t) {
		vars[v.Name] = "SENTINEL_" + v.Name + "_k3y"
	}
	withUser := func(raw, name string) string {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword("SENTINEL_"+name+"_user", "SENTINEL_"+name+"_pw")
		u.Path = "/SENTINEL_" + name + "_path"
		return u.String()
	}
	set := map[string]string{
		"ANTHROPIC_API_KEY":          "sk-ant-SENTINEL_ANTHROPIC_API_KEY",
		"VITE_CLERK_PUBLISHABLE_KEY": "pk_test_SENTINEL_VITE_CLERK_PUBLISHABLE_KEY",
		"CLERK_SECRET_KEY":           "sk_test_SENTINEL_CLERK_SECRET_KEY",
		"CLERK_WEBHOOK_SECRET":       "whsec_SENTINEL_CLERK_WEBHOOK_SECRET",
		"STRIPE_SECRET_KEY":          "rk_test_SENTINEL_STRIPE_SECRET_KEY",
		"STRIPE_PUBLISHABLE_KEY":     "pk_test_SENTINEL_STRIPE_PUBLISHABLE_KEY",
		"STRIPE_WEBHOOK_SECRET":      "whsec_SENTINEL_STRIPE_WEBHOOK_SECRET",
		"SECRETS_MASTER_KEY":         base64.StdEncoding.EncodeToString([]byte("SENTINEL-master-key-0123456789ab")),
		"SECRETS_MASTER_KEY_VERSION": "1",
		"CLOUDFLARE_ACCOUNT_ID":      strings.Repeat("ab", 16),
		"CLOUDFLARE_ZONE_ID":         strings.Repeat("cd", 16),
		"CF_AIG_GATEWAY_ID":          "sentinel-gateway",
		"OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer SENTINEL_otlp_token,X-Axiom-Dataset=SENTINEL_dataset",

		"CF_ENV": "staging", "ROUTING_PROFILE": "v1-start", "LLM_MODE": "live", "BILLING_PROVIDER": "paddle",
		"PADDLE_ENVIRONMENT": "sandbox", "R2_JURISDICTION": "",

		"APP_BASE_URL":                withUser(api.Clerk.URL, "APP_BASE_URL"),
		"API_BASE_URL":                withUser(api.Clerk.URL, "API_BASE_URL"),
		"VITE_API_BASE_URL":           withUser(api.Clerk.URL, "VITE_API_BASE_URL"),
		"ANTHROPIC_BASE_URL":          withUser(api.Gateway.URL, "ANTHROPIC_BASE_URL"),
		"SANDBOX_GATEWAY_URL":         withUser(api.Sandbox.URL, "SANDBOX_GATEWAY_URL"),
		"OTEL_EXPORTER_OTLP_ENDPOINT": withUser(api.OTLP.URL, "OTEL_EXPORTER_OTLP_ENDPOINT"),
		"TIKTOK_REDIRECT_URI":         withUser(api.Clerk.URL, "TIKTOK_REDIRECT_URI"),
		"SENTRY_DSN":                  withUser(api.Clerk.URL, "SENTRY_DSN"),
		"VITE_SENTRY_DSN":             withUser(api.Clerk.URL, "VITE_SENTRY_DSN"),
		// A DSN with & (and a sentinel in a parameter) is the case JSON escaping would hide from the redactor.
		"DATABASE_URL":         withUser("postgres://"+dead, "DATABASE_URL") + "?sslmode=disable&application_name=SENTINEL_app",
		"DATABASE_DIRECT_URL":  withUser("postgres://"+dead, "DATABASE_DIRECT_URL") + "?sslmode=disable&application_name=SENTINEL_app",
		"DATABASE_MIGRATE_URL": withUser("postgres://"+dead, "DATABASE_MIGRATE_URL"),
		"R2_ENDPOINT":          withUser("http://"+dead, "R2_ENDPOINT"),
	}
	for k, v := range set {
		vars[k] = v
	}
	return vars
}

// secretValues lists the loaded values doctor must never print: every value of six or more characters except valid
// enum values, which are public by definition.
func secretValues(vars map[string]string) []string {
	var out []string
	for name, val := range vars {
		if allowed, ok := enums[name]; ok && slices.Contains(allowed, val) {
			continue
		}
		if len(val) >= minRedact {
			out = append(out, val)
		}
	}
	return out
}

func TestNoSentinelSecretIsEverPrinted(t *testing.T) {
	api := newAPIFakes(t, "never-matches", "never-matches")
	dead := unreachableAddr(t)
	vars := sentinelVars(t, api, dead)

	// Run once with every HTTP service answering 401 with a body that echoes a sentinel, and once with the same
	// services refusing connections.
	api.setStatus(401)
	live := Run(context.Background(), Options{
		Env: "staging", Phase: 7, Example: loadExample(t), Vars: vars, RoutingFS: routingFS,
		Endpoints: api.endpoints(), Timeout: 3 * time.Second,
	})
	deadEP := "http://" + dead
	refused := Run(context.Background(), Options{
		Env: "staging", Phase: 7, Example: loadExample(t), Vars: vars, RoutingFS: routingFS,
		Endpoints: Endpoints{Anthropic: deadEP, Cloudflare: deadEP, Clerk: deadEP}, Timeout: 3 * time.Second,
	})

	// The services must actually have been called, or the test proves nothing.
	if r := find(t, live, "anthropic claude-opus-5-5"); r.Reason != "auth rejected (HTTP 401)" {
		t.Errorf("anthropic reason = %q", r.Reason)
	}
	if r := find(t, live, "clerk"); r.Reason != "auth rejected (HTTP 401)" {
		t.Errorf("clerk reason = %q", r.Reason)
	}
	if r := find(t, refused, "clerk"); r.Reason != "unreachable" {
		t.Errorf("clerk reason = %q", r.Reason)
	}
	if r := find(t, refused, "postgres DATABASE_URL"); r.Reason != "unreachable" {
		t.Errorf("postgres reason = %q", r.Reason)
	}

	secrets := secretValues(vars)
	if len(secrets) < 40 {
		t.Fatalf("only %d sentinel values", len(secrets))
	}
	for name, rep := range map[string]Report{"live": live, "refused": refused} {
		var text, js bytes.Buffer
		WriteText(&text, rep, ".env")
		if err := WriteJSON(&js, rep, ".env"); err != nil {
			t.Fatal(err)
		}
		// First guard: nothing in the report is a value, even before redaction.
		for _, out := range []string{text.String(), js.String()} {
			for _, s := range secrets {
				if strings.Contains(out, s) {
					t.Errorf("%s: the report contains a loaded value (%d bytes)", name, len(s))
				}
			}
			if strings.Contains(out, "SENTINEL") {
				t.Errorf("%s: the report contains a sentinel fragment", name)
			}
		}
	}
}

func TestRedactorBlanksLeakedValues(t *testing.T) {
	vars := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-leaky-value",
		"DATABASE_URL":      "postgres://appuser:hunter2hunter2@db.internal:5432/cf",
		"SHORT":             "abc",
		"CF_ENV":            "staging",
		"ROUTING_PROFILE":   "dev-cheap",
		"LLM_MODE":          "not-a-mode",             // invalid enum value: not exempt
		"APP_BASE_URL":      "http://public-url.test", // [public]: kept
	}
	red := NewRedactor(loadExample(t), vars)
	in := "key sk-ant-leaky-value dsn postgres://appuser:hunter2hunter2@db.internal:5432/cf pw hunter2hunter2 user appuser " +
		"short abc env staging profile dev-cheap mode not-a-mode app http://public-url.test"
	got := red.Redact(in)
	for _, leaked := range []string{"sk-ant-leaky-value", "hunter2hunter2", "not-a-mode", "db.internal"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%q survived: %s", leaked, got)
		}
	}
	// A URL's user name is not blanked on its own, and neither are short values, enum values or [public] ones.
	for _, kept := range []string{"user appuser", "abc", "staging", "dev-cheap", "http://public-url.test"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q should be kept: %s", kept, got)
		}
	}

	var buf bytes.Buffer
	n, err := red.Writer(&buf).Write([]byte("x sk-ant-leaky-value y"))
	if err != nil || n != len("x sk-ant-leaky-value y") || buf.String() != "x [redacted] y" {
		t.Errorf("Writer: n=%d err=%v out=%q", n, err, buf.String())
	}
}
