package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

const pingKey = "sk-ant-api03-PING-TEST-KEY-do-not-print-0123456789"

// pingAPI is a fake Anthropic API that records the paths it was asked.
type pingAPI struct {
	mu    sync.Mutex
	paths []string
	keys  []string
}

func (p *pingAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, r.Body)
	p.mu.Lock()
	p.paths = append(p.paths, r.URL.Path)
	p.keys = append(p.keys, r.Header.Get("x-api-key"))
	p.mu.Unlock()
	body := `{"input_tokens": 12}`
	if strings.HasSuffix(r.URL.Path, "/v1/messages") {
		body = `{"id":"msg_p","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","stop_sequence":null,` +
			`"usage":{"input_tokens":14,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":3}}`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Request-Id": {"req_ping_1"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func pingEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ping.env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runPing(t *testing.T, deps llmDeps, proc map[string]string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = cmdLLM(append([]string{"ping"}, args...), &o, &e, fakeProcessEnv(proc), deps)
	return code, o.String(), e.String()
}

func TestLLMPingCountsTokensByDefault(t *testing.T) {
	api := &pingAPI{}
	deps := llmDeps{httpClient: &http.Client{Transport: api}}
	env := pingEnv(t, "CF_ENV=local\nROUTING_PROFILE=dev-cheap\nANTHROPIC_API_KEY="+pingKey+"\n")

	code, out, errOut := runPing(t, deps, nil, "--stage", "editor", "--env-file", env)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"stage      editor", "profile    dev-cheap", "model      claude-haiku-5-5", "effort     medium", "billed_to  test", "direct (api.anthropic.com)", "model priced", "count_tokens ok: 12", "ok (count only"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(api.paths) != 1 || !strings.HasSuffix(api.paths[0], "/v1/messages/count_tokens") {
		t.Fatalf("the API saw %v; a default ping may only count tokens", api.paths)
	}
	if api.keys[0] != pingKey {
		t.Error("the configured key was not used")
	}
	if strings.Contains(out+errOut, pingKey) || strings.Contains(out+errOut, "PING-TEST-KEY") {
		t.Errorf("the key was printed:\n%s%s", out, errOut)
	}

	// A profile for this run overrides ROUTING_PROFILE: Opus 5.5 on the editor stage.
	api.paths = nil
	code, out, errOut = runPing(t, deps, nil, "--stage", "editor", "--profile", "v1-start", "--env-file", env)
	if code != 0 || !strings.Contains(out, "model      claude-opus-5-5") || !strings.Contains(out, "profile    v1-start") {
		t.Fatalf("v1-start = %d\n%s%s", code, out, errOut)
	}
}

func TestLLMPingFailuresAreClear(t *testing.T) {
	api := &pingAPI{}
	deps := llmDeps{httpClient: &http.Client{Transport: api}}
	good := pingEnv(t, "CF_ENV=local\nROUTING_PROFILE=dev-cheap\nANTHROPIC_API_KEY="+pingKey+"\n")
	noKey := pingEnv(t, "CF_ENV=local\nROUTING_PROFILE=dev-cheap\n")

	if code, _, errOut := runPing(t, deps, nil, "--stage", "editor", "--env-file", noKey); code != 1 || !strings.Contains(errOut, "ANTHROPIC_API_KEY") {
		t.Errorf("no key = %d %s", code, errOut)
	}
	if code, _, errOut := runPing(t, deps, nil, "--env-file", good); code != 2 || !strings.Contains(errOut, "--stage is required") {
		t.Errorf("no stage = %d %s", code, errOut)
	}
	if code, _, errOut := runPing(t, deps, nil, "--stage", "nope", "--env-file", good); code != 2 || !strings.Contains(errOut, "nope") {
		t.Errorf("unknown stage = %d %s", code, errOut)
	}
	if code, _, errOut := runPing(t, deps, nil, "--stage", "editor", "--profile", "nope", "--env-file", good); code != 2 || !strings.Contains(errOut, "nope") {
		t.Errorf("unknown profile = %d %s", code, errOut)
	}
	if code, _, errOut := runPing(t, deps, nil, "--stage", "editor", "--live", "--env-file", good); code != 2 || !strings.Contains(errOut, "--workspace") {
		t.Errorf("--live without a workspace = %d %s", code, errOut)
	}
	if code, _, errOut := runPing(t, deps, nil, "--stage", "editor", "--workspace", "wks_x", "--env-file", good); code != 2 || !strings.Contains(errOut, "--live") {
		t.Errorf("--workspace without --live = %d %s", code, errOut)
	}
	if code, _, _ := runPing(t, deps, nil, "--stage", "editor", "extra", "--env-file", good); code != 2 {
		t.Errorf("extra argument = %d", code)
	}
	if len(api.paths) != 0 {
		t.Errorf("a failing ping reached the API: %v", api.paths)
	}
	// LLM_MODE=replay is ignored with a note: ping always talks to the API.
	replay := pingEnv(t, "CF_ENV=local\nLLM_MODE=replay\nROUTING_PROFILE=dev-cheap\nANTHROPIC_API_KEY="+pingKey+"\n")
	if code, out, _ := runPing(t, deps, nil, "--stage", "editor", "--env-file", replay); code != 0 || !strings.Contains(out, "LLM_MODE=replay is ignored") || len(api.paths) != 1 {
		t.Errorf("replay = %d %s %v", code, out, api.paths)
	}
}

func TestLLMUsage(t *testing.T) {
	var o, e bytes.Buffer
	if code := cmdLLM(nil, &o, &e, fakeProcessEnv(nil), llmDeps{}); code != 2 || !strings.Contains(e.String(), "cfctl llm ping") {
		t.Errorf("no args = %d %s", code, e.String())
	}
	o.Reset()
	if code := cmdLLM([]string{"help"}, &o, &e, fakeProcessEnv(nil), llmDeps{}); code != 0 || !strings.Contains(o.String(), "--live") {
		t.Errorf("help = %d", code)
	}
	if code := cmdLLM([]string{"nope"}, &o, &e, fakeProcessEnv(nil), llmDeps{}); code != 2 {
		t.Errorf("unknown = %d", code)
	}
}

func TestLLMPingLiveWritesTheLedgerRow(t *testing.T) {
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "llm-ping-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	w, err := st.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: "org_ping", Name: "Ping"})
	if err != nil {
		t.Fatal(err)
	}

	api := &pingAPI{}
	deps := llmDeps{httpClient: &http.Client{Transport: api}, openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "llm-ping-test", MaxConns: 2})
	}}
	env := pingEnv(t, "CF_ENV=local\nROUTING_PROFILE=dev-cheap\nANTHROPIC_API_KEY="+pingKey+"\nDATABASE_URL="+dsn+"\n")

	// Without --yes the guard may refuse (the test container's host is not guaranteed to be loopback), so pass it; the
	// guard itself is the shared mutationRefusal, tested with the plan command.
	code, out, errOut := runPing(t, deps, nil, "--stage", "editor", "--live", "--workspace", w.ID.String(), "--yes", "--env-file", env)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{`reply      "pong"`, "request_id req_ping_1", "ledger     ", "stage editor", "model claude-haiku-5-5", "billed_to test", "price_table 2026-10", "cost $0.000", "\nok\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+errOut, "PING-TEST-KEY") {
		t.Error("the key was printed")
	}
	rows, err := st.RecentUsage(context.Background(), w.ID, 5)
	if err != nil || len(rows) != 1 || rows[0].InputTokens != 14 || rows[0].OutputTokens != 3 || rows[0].RequestID != "req_ping_1" || rows[0].BilledTo != "test" || rows[0].RunID.String() == "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("ledger = %+v, %v", rows, err)
	}
	// A workspace that does not exist is refused before anything is sent.
	api.paths = nil
	if code, _, errOut := runPing(t, deps, nil, "--stage", "editor", "--live", "--workspace", "wks_01h455vb4pex5vsknk084sn02q", "--yes", "--env-file", env); code != 1 || len(api.paths) != 0 {
		t.Errorf("missing workspace = %d %s %v", code, errOut, api.paths)
	}
	// Production needs --allow-production.
	prod := pingEnv(t, "CF_ENV=production\nROUTING_PROFILE=v1-start\nANTHROPIC_API_KEY="+pingKey+"\nDATABASE_URL="+dsn+"\n")
	if code, _, errOut := runPing(t, deps, nil, "--stage", "editor", "--live", "--workspace", w.ID.String(), "--yes", "--env-file", prod); code != 2 || !strings.Contains(errOut, "--allow-production") {
		t.Errorf("production = %d %s", code, errOut)
	}
}
