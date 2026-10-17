package doctor

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func runDoctor(t *testing.T, env string, phase int, vars map[string]string, ep Endpoints) Report {
	t.Helper()
	// CF_ENV must match --env. goodVars carries the .env.example default (local), so follow the environment under test.
	if vars["CF_ENV"] == "local" && env != "bench" {
		vars["CF_ENV"] = env
	}
	return Run(context.Background(), Options{
		Env: env, Phase: phase, Example: loadExample(t), Vars: vars, RoutingFS: routingFS,
		Endpoints: ep, Timeout: 5 * time.Second,
	})
}

// localInfra points Postgres and object storage at fakes that work.
func localInfra(t *testing.T, vars map[string]string) {
	t.Helper()
	pg := fakePG(t)
	vars["DATABASE_URL"] = "postgres://cf:cf@" + pg + "/cf?sslmode=disable"
	vars["DATABASE_DIRECT_URL"] = "postgres://cf:cf@" + pg + "/cf?sslmode=disable"
	vars["DATABASE_MIGRATE_URL"] = "postgres://cf:cf@" + pg + "/cf?sslmode=disable"
	s3 := newFakeS3(t, vars["R2_BUCKET"])
	vars["R2_ENDPOINT"] = s3.URL
}

func TestCIWithoutAnthropicAndClerkPasses(t *testing.T) {
	vars := goodVars(t)
	localInfra(t, vars)
	rep := runDoctor(t, "ci", 0, vars, Endpoints{})
	if rep.Failed() {
		for _, r := range rep.Results {
			if r.Status == Missing || r.Status == Invalid {
				t.Errorf("%s %s: %s", r.Status, r.Name, r.Reason)
			}
		}
		t.Fatal("doctor --env ci failed")
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "VITE_CLERK_PUBLISHABLE_KEY", "CLERK_SECRET_KEY", "anthropic", "ai-gateway", "clerk"} {
		r := wantStatus(t, rep, name, Skipped)
		if r.Reason != "not used in ci (replay mode, D-22)" {
			t.Errorf("%s reason = %q", name, r.Reason)
		}
	}
	wantStatus(t, rep, "postgres DATABASE_URL", OK)
	wantStatus(t, rep, "postgres DATABASE_DIRECT_URL", OK)
	wantStatus(t, rep, "postgres DATABASE_MIGRATE_URL", OK)
	wantStatus(t, rep, "object-storage", OK)
}

func TestSameVarsFailOutsideCI(t *testing.T) {
	vars := goodVars(t)
	localInfra(t, vars)
	rep := runDoctor(t, "local", 0, vars, Endpoints{})
	if !rep.Failed() {
		t.Fatal("local without the three start values should fail")
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "VITE_CLERK_PUBLISHABLE_KEY", "CLERK_SECRET_KEY", "anthropic", "clerk"} {
		wantStatus(t, rep, name, Missing)
	}
}

func TestCIChecksKeysWhenSet(t *testing.T) {
	api := newAPIFakes(t, "sk-ant-ci-key", "")
	vars := goodVars(t)
	localInfra(t, vars)
	vars["ANTHROPIC_API_KEY"] = "sk-ant-ci-key"
	vars["CLERK_SECRET_KEY"] = "sk_test_abcdef"
	vars["VITE_CLERK_PUBLISHABLE_KEY"] = "pk_test_abcdef"
	rep := runDoctor(t, "ci", 0, vars, api.endpoints())
	if rep.Failed() {
		t.Fatalf("failed: %+v", rep.Results)
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "CLERK_SECRET_KEY", "anthropic claude-haiku-5-5", "clerk"} {
		wantStatus(t, rep, name, OK)
	}
}

func TestObjectStorageCleansUp(t *testing.T) {
	vars := goodVars(t)
	pg := fakePG(t)
	vars["DATABASE_URL"] = "postgres://cf:cf@" + pg + "/cf?sslmode=disable"
	vars["DATABASE_DIRECT_URL"] = vars["DATABASE_URL"]
	s3 := newFakeS3(t, vars["R2_BUCKET"])
	vars["R2_ENDPOINT"] = s3.URL
	rep := runDoctor(t, "ci", 0, vars, Endpoints{})
	wantStatus(t, rep, "object-storage", OK)
	if s3.puts != 1 || s3.deletes != 1 || s3.left() != 0 {
		t.Errorf("puts = %d, deletes = %d, objects left = %d; want one put, one delete, none left", s3.puts, s3.deletes, s3.left())
	}

	// Wrong credentials are rejected by the fake the same way R2 would.
	vars["R2_BUCKET"] = "some-other-bucket"
	rep = runDoctor(t, "ci", 0, vars, Endpoints{})
	if r := wantStatus(t, rep, "object-storage", Invalid); r.Reason != "put: auth rejected (HTTP 403)" {
		t.Errorf("reason = %q", r.Reason)
	}
}

func TestUnreachableServices(t *testing.T) {
	vars := goodVars(t)
	dead := unreachableAddr(t)
	vars["DATABASE_URL"] = "postgres://cf:cf@" + dead + "/cf?sslmode=disable"
	vars["DATABASE_DIRECT_URL"] = vars["DATABASE_URL"]
	vars["R2_ENDPOINT"] = "http://" + dead
	rep := runDoctor(t, "ci", 0, vars, Endpoints{})
	if r := wantStatus(t, rep, "postgres DATABASE_URL", Invalid); r.Reason != "unreachable" {
		t.Errorf("postgres reason = %q", r.Reason)
	}
	if r := wantStatus(t, rep, "object-storage", Invalid); r.Reason != "put: unreachable" {
		t.Errorf("storage reason = %q", r.Reason)
	}
	if !rep.Failed() {
		t.Error("unreachable services must fail the run")
	}
}

func TestEveryServiceOK(t *testing.T) {
	const key, aig = "sk-ant-direct-key", "aig-token-value"
	api := newAPIFakes(t, key, aig)
	vars := goodVars(t)
	localInfra(t, vars)
	vars["ANTHROPIC_API_KEY"] = key
	vars["VITE_CLERK_PUBLISHABLE_KEY"] = "pk_test_abcdef"
	vars["CLERK_SECRET_KEY"] = "sk_test_abcdef"
	vars["ROUTING_PROFILE"] = "v1-start"
	vars["ANTHROPIC_BASE_URL"] = api.Gateway.URL + "/anthropic"
	vars["CF_AIG_TOKEN"] = aig
	vars["CLOUDFLARE_ACCOUNT_ID"] = strings.Repeat("a1", 16)
	vars["CLOUDFLARE_API_TOKEN"] = "cf-api-token-value"
	vars["CF_AI_API_TOKEN"] = "cf-rest-token-value"
	vars["SANDBOX_GATEWAY_URL"] = api.Sandbox.URL
	vars["OTEL_EXPORTER_OTLP_ENDPOINT"] = api.OTLP.URL
	vars["OTEL_EXPORTER_OTLP_HEADERS"] = "Authorization=Bearer%20ingest,X-Axiom-Dataset=cf-local"

	rep := runDoctor(t, "local", 4, vars, api.endpoints())
	if rep.Failed() {
		for _, r := range rep.Results {
			if r.Status == Missing || r.Status == Invalid {
				t.Errorf("%s %s: %s", r.Status, r.Name, r.Reason)
			}
		}
		t.FailNow()
	}

	models := []string{"claude-haiku-5-5", "claude-opus-5-5", "claude-sonnet-5-5"}
	for _, m := range models {
		wantStatus(t, rep, "anthropic "+m, OK)
		wantStatus(t, rep, "ai-gateway "+m, OK)
	}
	for _, name := range []string{"cloudflare-rest anthropic/claude-opus-5.5", "cloudflare-rest anthropic/claude-sonnet-5.5", "cloudflare-rest anthropic/claude-haiku-5.5"} {
		wantStatus(t, rep, name, OK)
	}
	for _, name := range []string{"cloudflare token", "cloudflare r2 buckets", "cloudflare ai gateways", "clerk", "sandbox-gateway", "observability"} {
		wantStatus(t, rep, name, OK)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	slices.Sort(api.Direct)
	slices.Sort(api.ViaGW)
	if !slices.Equal(api.Direct, models) || !slices.Equal(api.ViaGW, models) {
		t.Errorf("direct = %v, via gateway = %v, want %v", api.Direct, api.ViaGW, models)
	}
	if len(api.CFModels) != 3 {
		t.Errorf("cloudflare REST models = %v", api.CFModels)
	}
	if api.Got["r2-jurisdiction"] != "eu" {
		t.Error("R2 bucket listing did not send the EU jurisdiction header")
	}
	if api.Got["clerk-path"] != "/v1/users?limit=1" {
		t.Errorf("clerk path = %q", api.Got["clerk-path"])
	}
	if api.Got["sandbox-path"] != "/healthz" || api.Got["sandbox-auth"] != "Bearer "+vars["SANDBOX_GATEWAY_TOKEN"] {
		t.Error("sandbox gateway call was not /healthz with the bearer token")
	}
	if api.Got["otlp-path"] != "/v1/traces" || api.Got["otlp-type"] != "application/x-protobuf" ||
		api.Got["otlp-auth"] != "Bearer ingest" || api.Got["otlp-dataset"] != "cf-local" {
		t.Errorf("otlp request = %v", api.Got)
	}
}

func TestDirectCallIgnoresBaseURLAndSDKEnvironment(t *testing.T) {
	const key = "sk-ant-direct-key"
	api := newAPIFakes(t, key, "unused")
	// The SDK would pick these up if doctor let it.
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-process-env")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "token-from-process-env")
	vars := goodVars(t)
	localInfra(t, vars)
	vars["ANTHROPIC_API_KEY"] = key
	rep := runDoctor(t, "ci", 0, vars, Endpoints{Anthropic: api.Anthropic.URL})
	wantStatus(t, rep, "anthropic claude-haiku-5-5", OK)
	wantStatus(t, rep, "ai-gateway", Skipped)
}

func TestSkippedModelServicesNeedTheirVariables(t *testing.T) {
	vars := goodVars(t)
	localInfra(t, vars)
	vars["ANTHROPIC_API_KEY"] = "sk-ant-x1234"
	vars["ANTHROPIC_BASE_URL"] = "http://127.0.0.1:1/anthropic"
	rep := runDoctor(t, "local", 0, vars, Endpoints{Anthropic: "http://127.0.0.1:1"})
	// A gateway URL without CF_AIG_TOKEN cannot be tested.
	if r := wantStatus(t, rep, "ai-gateway", Missing); r.Reason != "CF_AIG_TOKEN not set" {
		t.Errorf("reason = %q", r.Reason)
	}
	wantStatus(t, rep, "cloudflare-rest", Skipped)
}

func TestHTTPFailureCategories(t *testing.T) {
	const key = "sk-ant-direct-key"
	api := newAPIFakes(t, key, "unused")
	vars := goodVars(t)
	localInfra(t, vars)
	vars["ANTHROPIC_API_KEY"] = "sk-ant-wrong-key"
	vars["CLERK_SECRET_KEY"] = "sk_test_abcdef"
	vars["VITE_CLERK_PUBLISHABLE_KEY"] = "pk_test_abcdef"
	rep := runDoctor(t, "local", 0, vars, api.endpoints())
	if r := wantStatus(t, rep, "anthropic claude-haiku-5-5", Invalid); r.Reason != "auth rejected (HTTP 401)" {
		t.Errorf("reason = %q", r.Reason)
	}
	wantStatus(t, rep, "clerk", OK)

	api.setStatus(503)
	rep = runDoctor(t, "local", 0, vars, api.endpoints())
	if r := wantStatus(t, rep, "clerk", Invalid); r.Reason != "HTTP 503" {
		t.Errorf("reason = %q", r.Reason)
	}

	// A model the API does not know.
	api.setStatus(0)
	vars["ANTHROPIC_API_KEY"] = key
	rep = runDoctor(t, "local", 0, vars, Endpoints{Anthropic: api.Anthropic.URL + "/nope", Clerk: api.Clerk.URL})
	if r := wantStatus(t, rep, "anthropic claude-haiku-5-5", Invalid); r.Reason != "not found (HTTP 404)" {
		t.Errorf("reason = %q", r.Reason)
	}
}

func TestSlowServerTimesOut(t *testing.T) {
	api := newAPIFakes(t, "k", "")
	slow := newSlowServer(t)
	vars := goodVars(t)
	localInfra(t, vars)
	vars["CLERK_SECRET_KEY"] = "sk_test_abcdef"
	vars["VITE_CLERK_PUBLISHABLE_KEY"] = "pk_test_abcdef"
	rep := Run(context.Background(), Options{
		Env: "local", Example: loadExample(t), Vars: vars, RoutingFS: routingFS,
		Endpoints: Endpoints{Anthropic: api.Anthropic.URL, Clerk: slow.URL}, Timeout: 200 * time.Millisecond,
	})
	if r := wantStatus(t, rep, "clerk", Invalid); r.Reason != "timeout" {
		t.Errorf("reason = %q", r.Reason)
	}
}

func TestRequiredFollowsTags(t *testing.T) {
	missing := func(rep Report) []string {
		var out []string
		for _, r := range rep.Results {
			if r.Kind == "variable" && r.Status == Missing {
				out = append(out, r.Name)
			}
		}
		return out
	}
	base := func() map[string]string {
		v := goodVars(t)
		v["ANTHROPIC_API_KEY"] = "sk-ant-x1234"
		v["VITE_CLERK_PUBLISHABLE_KEY"] = "pk_test_abc"
		v["CLERK_SECRET_KEY"] = "sk_test_abc"
		return v
	}
	if got := missing(runDoctor(t, "local", 0, base(), Endpoints{})); len(got) != 0 {
		t.Errorf("local P0 missing %v", got)
	}

	staging := missing(runDoctor(t, "staging", 0, base(), Endpoints{}))
	for _, n := range []string{"CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_API_TOKEN", "CF_AIG_TOKEN", "DEPLOY_HOST", "OTEL_EXPORTER_OTLP_ENDPOINT", "SENTRY_DSN", "R2_JURISDICTION"} {
		if !slices.Contains(staging, n) {
			t.Errorf("staging should require %s (missing = %v)", n, staging)
		}
	}
	if slices.Contains(staging, "TIKTOK_CLIENT_KEY") {
		t.Error("TikTok is not required at P0")
	}

	p6 := missing(runDoctor(t, "local", 6, base(), Endpoints{}))
	if !slices.Equal(p6, []string{"TIKTOK_CLIENT_KEY", "TIKTOK_CLIENT_SECRET"}) {
		t.Errorf("local P6 missing = %v", p6)
	}

	p7 := missing(runDoctor(t, "local", 7, base(), Endpoints{})) // BILLING_PROVIDER=paddle
	if !slices.Contains(p7, "PADDLE_API_KEY") || slices.Contains(p7, "STRIPE_SECRET_KEY") {
		t.Errorf("P7 with paddle missing = %v", p7)
	}
	v := base()
	v["BILLING_PROVIDER"] = "stripe"
	p7 = missing(runDoctor(t, "local", 7, v, Endpoints{}))
	if !slices.Contains(p7, "STRIPE_SECRET_KEY") || slices.Contains(p7, "PADDLE_API_KEY") {
		t.Errorf("P7 with stripe missing = %v", p7)
	}
}

func TestFormatChecks(t *testing.T) {
	tests := []struct {
		name, value string
		extra       map[string]string
		reason      string // substring; empty means the value must be ok
	}{
		{"ANTHROPIC_API_KEY", "sk-oops-123456", nil, "sk-ant-"},
		{"ANTHROPIC_API_KEY", "sk-ant-123456", nil, ""},
		{"VITE_CLERK_PUBLISHABLE_KEY", "sk_test_123456", nil, "pk_test_"},
		{"CLERK_SECRET_KEY", "pk_test_123456", nil, "sk_test_"},
		{"CLERK_SECRET_KEY", "sk_live_123456", map[string]string{"VITE_CLERK_PUBLISHABLE_KEY": "pk_test_123456"}, "mismatch"},
		{"CLERK_SECRET_KEY", "sk_test_123456", map[string]string{"VITE_CLERK_PUBLISHABLE_KEY": "pk_test_123456"}, ""},
		{"SECRETS_MASTER_KEY", "dG9vc2hvcnQ=", nil, "32 bytes"},
		{"SECRETS_MASTER_KEY", "not base64!!", nil, "32 bytes"},
		{"CF_ENV", "prod", nil, "documented"},
		{"ROUTING_PROFILE", "v2", nil, "documented"},
		{"LLM_MODE", "fake", nil, "documented"},
		{"API_BASE_URL", "localhost:8080", nil, "URL"},
		{"APP_BASE_URL", "ftp://example.com", nil, "URL"},
		{"DATABASE_URL", "mysql://x@y/z", nil, "URL"},
		{"R2_ENDPOINT", "http://localhost:9000", map[string]string{"R2_JURISDICTION": "eu"}, "EU"},
		{"R2_JURISDICTION", "us", nil, "documented"},
		{"SECRETS_MASTER_KEY_VERSION", "0", nil, "positive"},
		{"CLOUDFLARE_ACCOUNT_ID", "xyz", nil, "32 hex"},
	}
	for _, tc := range tests {
		vars := goodVars(t)
		vars[tc.name] = tc.value
		for k, v := range tc.extra {
			vars[k] = v
		}
		rep := runDoctor(t, "ci", 0, vars, Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"})
		r := find(t, rep, tc.name)
		if tc.reason == "" {
			if r.Status != OK {
				t.Errorf("%s=%q: %s (%s), want ok", tc.name, tc.value, r.Status, r.Reason)
			}
			continue
		}
		if r.Status != Invalid || !strings.Contains(r.Reason, tc.reason) {
			t.Errorf("%s=%q: %s (%s), want invalid containing %q", tc.name, tc.value, r.Status, r.Reason, tc.reason)
		}
	}
}

func TestMergeEnvFileWins(t *testing.T) {
	example := loadExample(t)
	process := map[string]string{"CF_ENV": "ci", "LLM_MODE": "replay", "ROUTING_PROFILE": "dev-mid", "NOT_LISTED": "x", "APP_BASE_URL": ""}
	file := map[string]string{"CF_ENV": "local", "ROUTING_PROFILE": "", "APP_BASE_URL": "", "API_BASE_URL": ""}
	got := MergeEnv(example, file, func(k string) (string, bool) {
		v, ok := process[k]
		return v, ok
	})
	if got["CF_ENV"] != "local" {
		t.Error("a non-empty value in the file must win over the process environment")
	}
	if got["LLM_MODE"] != "replay" {
		t.Error("the process environment fills names the file lacks")
	}
	if got["ROUTING_PROFILE"] != "dev-mid" {
		t.Error("the process environment fills a name that is empty in the file")
	}
	if v, ok := got["APP_BASE_URL"]; !ok || v != "" {
		t.Error("empty in both stays empty")
	}
	if v, ok := got["API_BASE_URL"]; !ok || v != "" {
		t.Error("empty in the file and absent from the process stays empty")
	}
	if _, ok := got["NOT_LISTED"]; ok {
		t.Error("names outside .env.example are ignored")
	}
}

func TestParsePhase(t *testing.T) {
	for in, want := range map[string]int{"P0": 0, "p4": 4, "P7": 7} {
		if got, err := ParsePhase(in); err != nil || got != want {
			t.Errorf("ParsePhase(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "P8", "4", "P10"} {
		if _, err := ParsePhase(in); err == nil {
			t.Errorf("ParsePhase(%q) accepted", in)
		}
	}
}

func TestWriteTextAndJSON(t *testing.T) {
	rep := Report{Env: "local", Phase: "P0", Results: []Result{
		{Kind: "variable", Name: "A", Status: OK, Reason: "set"},
		{Kind: "service", Name: "svc", Status: Invalid, Reason: "unreachable"},
	}}
	var text, js bytes.Buffer
	WriteText(&text, rep, ".env")
	if err := WriteJSON(&js, rep, ".env"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Variables", "Services", "invalid  svc", "FAILED", "1 ok, 0 missing, 1 invalid, 0 skipped"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text report lacks %q:\n%s", want, text.String())
		}
	}
	if !strings.Contains(js.String(), `"failed": true`) || !strings.Contains(js.String(), `"status": "invalid"`) {
		t.Errorf("json report = %s", js.String())
	}
}
