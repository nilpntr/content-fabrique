package doctor

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCFEnvMustMatchEnv(t *testing.T) {
	for _, tc := range []struct {
		env, cfEnv string
		want       Status
	}{
		{"local", "local", OK},
		{"ci", "ci", OK},
		{"staging", "staging", OK},
		{"production", "staging", Invalid}, // the staging file loaded for production
		{"staging", "local", Invalid},
		{"ci", "local", Invalid},
		{"bench", "bench", OK},
		{"bench", "local", OK}, // bench runs with the local .env
		{"local", "bench", Invalid},
	} {
		vars := goodVars(t)
		vars["CF_ENV"] = tc.cfEnv
		// Run directly: runDoctor would rewrite CF_ENV=local to follow --env.
		rep := Run(context.Background(), Options{
			Env: tc.env, Example: loadExample(t), Vars: vars, RoutingFS: routingFS,
			Endpoints: Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"}, Timeout: time.Second,
		})
		r := find(t, rep, "CF_ENV")
		if r.Status != tc.want {
			t.Errorf("--env %s with CF_ENV=%s: %s (%s), want %s", tc.env, tc.cfEnv, r.Status, r.Reason, tc.want)
		}
		if tc.want == Invalid && !strings.Contains(r.Reason, "--env") {
			t.Errorf("reason = %q", r.Reason)
		}
	}
}

// POSTGRES_USER, POSTGRES_PASSWORD and POSTGRES_DB configure the local Docker Postgres only.
func TestPostgresContainerVarsOnlyForLocalCIBench(t *testing.T) {
	names := []string{"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB"}
	for _, env := range []string{"local", "ci", "bench", "staging", "production"} {
		vars := goodVars(t)
		for _, n := range names {
			vars[n] = ""
		}
		rep := runDoctor(t, env, 0, vars, Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"})
		for _, n := range names {
			r := find(t, rep, n)
			if env == "staging" || env == "production" {
				if r.Status != Skipped || r.Reason != "only for the local Docker Postgres" {
					t.Errorf("%s in %s: %s (%s), want skipped", n, env, r.Status, r.Reason)
				}
			} else if r.Status != Missing {
				t.Errorf("%s in %s: %s, want missing", n, env, r.Status)
			}
		}
	}
}

// With ANTHROPIC_BASE_URL set the gateway is in use, so its token is required: the variable line and the service
// line say the same thing.
func TestGatewayTokenRequiredWhenBaseURLSet(t *testing.T) {
	vars := goodVars(t)
	localInfra(t, vars)
	vars["ANTHROPIC_API_KEY"] = "sk-ant-x1234"
	vars["VITE_CLERK_PUBLISHABLE_KEY"] = "pk_test_abc"
	vars["CLERK_SECRET_KEY"] = "sk_test_abc"

	rep := runDoctor(t, "local", 0, vars, Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"})
	wantStatus(t, rep, "CF_AIG_TOKEN", Skipped)
	wantStatus(t, rep, "ai-gateway", Skipped)

	vars["ANTHROPIC_BASE_URL"] = "http://127.0.0.1:1/anthropic"
	rep = runDoctor(t, "local", 0, vars, Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"})
	v := wantStatus(t, rep, "CF_AIG_TOKEN", Missing)
	if !strings.Contains(v.Reason, "ANTHROPIC_BASE_URL") {
		t.Errorf("variable reason = %q, want it to say why", v.Reason)
	}
	s := wantStatus(t, rep, "ai-gateway", Missing)
	if s.Reason != "CF_AIG_TOKEN not set" {
		t.Errorf("service reason = %q", s.Reason)
	}

	// ci without an Anthropic key is in replay mode: nothing calls the gateway, so nothing needs its token.
	vars["ANTHROPIC_API_KEY"] = ""
	rep = runDoctor(t, "ci", 0, vars, Endpoints{})
	wantStatus(t, rep, "CF_AIG_TOKEN", Skipped)
	wantStatus(t, rep, "ai-gateway", Skipped)
}

// An unauthenticated local collector is valid: the headers are optional when the endpoint is set.
func TestObservabilityNeedsNoHeaders(t *testing.T) {
	api := newAPIFakes(t, "k", "")
	vars := goodVars(t)
	localInfra(t, vars)
	vars["OTEL_EXPORTER_OTLP_ENDPOINT"] = api.OTLP.URL
	rep := runDoctor(t, "local", 0, vars, Endpoints{})
	wantStatus(t, rep, "observability", OK)
	if api.Got["otlp-auth"] != "" {
		t.Error("no Authorization header should be sent without headers configured")
	}
}

// countFS counts how often each file is opened.
type countFS struct {
	fs.FS
	mu    sync.Mutex
	opens map[string]int
}

func (c *countFS) Open(name string) (fs.File, error) {
	c.mu.Lock()
	c.opens[name]++
	c.mu.Unlock()
	return c.FS.Open(name)
}

func TestRoutingLoadedOncePerRun(t *testing.T) {
	const key = "sk-ant-direct-key"
	api := newAPIFakes(t, key, "aig")
	vars := goodVars(t)
	localInfra(t, vars)
	vars["ANTHROPIC_API_KEY"] = key
	vars["ANTHROPIC_BASE_URL"] = api.Gateway.URL + "/anthropic"
	vars["CF_AIG_TOKEN"] = "aig"
	vars["CLOUDFLARE_ACCOUNT_ID"] = strings.Repeat("a1", 16)
	vars["CF_AI_API_TOKEN"] = "cf-rest-token-value"
	fsys := &countFS{FS: os.DirFS("../../config/routing"), opens: map[string]int{}}
	rep := Run(context.Background(), Options{
		Env: "local", Example: loadExample(t), Vars: vars, RoutingFS: fsys,
		Endpoints: api.endpoints(), Timeout: 5 * time.Second,
	})
	wantStatus(t, rep, "anthropic claude-haiku-5-5", OK)
	wantStatus(t, rep, "ai-gateway claude-haiku-5-5", OK)
	wantStatus(t, rep, "cloudflare-rest anthropic/claude-haiku-5.5", OK)
	if fsys.opens["dev-cheap.yaml"] != 1 || fsys.opens["models.yaml"] != 1 {
		t.Errorf("opens = %v, want each file once", fsys.opens)
	}
}

// The Cloudflare REST check uses the Anthropic-compatible /ai/v1/messages route.
func TestCloudflareRESTRoute(t *testing.T) {
	api := newAPIFakes(t, "k", "")
	vars := goodVars(t)
	localInfra(t, vars)
	vars["CLOUDFLARE_ACCOUNT_ID"] = strings.Repeat("a1", 16)
	vars["CF_AI_API_TOKEN"] = "cf-rest-token-value"
	rep := runDoctor(t, "local", 0, vars, api.endpoints())
	wantStatus(t, rep, "cloudflare-rest anthropic/claude-haiku-5.5", OK)
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.CFModels) != 1 || api.CFModels[0] != "anthropic/claude-haiku-5.5" {
		t.Errorf("REST models = %v", api.CFModels)
	}
}
