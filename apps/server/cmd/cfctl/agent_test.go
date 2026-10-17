package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// mustRunOf reads the run id the command printed.
func mustRunOf(t *testing.T, out string) uuid.UUID {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "run      "); ok {
			id, err := parseRunID(strings.TrimSpace(rest))
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
	}
	t.Fatalf("no run id in the output:\n%s", out)
	return uuid.Nil
}

func runAgent(t *testing.T, deps agentDeps, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = cmdAgent(args, &o, &e, fakeProcessEnv(nil), deps)
	return code, o.String(), e.String()
}

// cfctl agent run and show over the echo agent's cassettes in replay mode: no API, no key, no cost.
func TestAgentRunAndShowInReplayMode(t *testing.T) {
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
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "agent-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	w, err := st.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: "org_agent", Name: "Agent"})
	if err != nil {
		t.Fatal(err)
	}
	cassettes, err := filepath.Abs("../../internal/agents/runtime/testdata/cassettes")
	if err != nil {
		t.Fatal(err)
	}
	deps := agentDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "agent-test", MaxConns: 2})
	}}
	env := pingEnv(t, "CF_ENV=local\nROUTING_PROFILE=dev-cheap\nLLM_MODE=replay\nLLM_CASSETTE_DIR="+cassettes+"\nDATABASE_URL="+dsn+"\n")

	code, out, errOut := runAgent(t, deps, "run", "--agent", "echo", "--workspace", w.ID.String(), "--input", "hello cassette", "--yes", "--events", "--env-file", env)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"LLM_MODE=replay", "profile  dev-cheap  model claude-haiku-5-5  effort medium", "succeeded", "usage_ledger: 0 calls", "events (", "note", "finish", "Noted: hello cassette", "status"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	runs, err := st.AgentEvents(context.Background(), w.ID, mustRunOf(t, out), 0, 0)
	if err != nil || len(runs) == 0 {
		t.Fatalf("events = %d, %v", len(runs), err)
	}

	// show prints the same run.
	id := mustRunOf(t, out)
	code, out2, errOut2 := runAgent(t, deps, "show", id.String(), "--workspace", w.ID.String(), "--env-file", env)
	if code != 0 || !strings.Contains(out2, "succeeded") || !strings.Contains(out2, "events (") {
		t.Fatalf("show: exit %d\n%s%s", code, out2, errOut2)
	}

	// Guards.
	prod := pingEnv(t, "CF_ENV=production\nLLM_MODE=live\nDATABASE_URL="+dsn+"\n")
	if code, _, errOut := runAgent(t, deps, "run", "--agent", "echo", "--workspace", w.ID.String(), "--input", "x", "--yes", "--env-file", prod); code != 2 || !strings.Contains(errOut, "development only") {
		t.Errorf("production = %d %s", code, errOut)
	}
	if code, _, errOut := runAgent(t, deps, "run", "--agent", "nope", "--workspace", w.ID.String(), "--input", "x", "--yes", "--env-file", env); code != 1 || !strings.Contains(errOut, "unknown agent") {
		t.Errorf("unknown agent = %d %s", code, errOut)
	}
	if code, _, errOut := runAgent(t, deps, "run", "--agent", "echo", "--input", "x", "--env-file", env); code != 2 || !strings.Contains(errOut, "--workspace") {
		t.Errorf("no workspace = %d %s", code, errOut)
	}
	// A message no cassette holds fails the run with the miss, and the exit code says so.
	if code, out, _ := runAgent(t, deps, "run", "--agent", "echo", "--workspace", w.ID.String(), "--input", "not recorded", "--yes", "--env-file", env); code != 1 || !strings.Contains(out, "failed (replay_miss)") {
		t.Errorf("replay miss = %d\n%s", code, out)
	}
}
