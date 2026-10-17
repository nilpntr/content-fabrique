package agentset

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func openStore(t *testing.T) *store.Store {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "agentset-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func getenv(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

var replayVars = map[string]string{"CF_ENV": "ci", "LLM_MODE": "replay", "ROUTING_PROFILE": "dev-cheap"}

func TestOpenDefinitionOnly(t *testing.T) {
	st := openStore(t)
	rt, err := Open(Options{Store: st, Logger: quiet, Getenv: getenv(replayVars), DefinitionOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Runner != nil || rt.Service == nil {
		t.Fatalf("a definition-only runtime: runner %v, service %v", rt.Runner, rt.Service)
	}
	if got := strings.Join(rt.Agents.Names(), ","); got != "echo,editor" {
		t.Fatalf("agents: %s", got)
	}
	// The editor resolves to a model per profile, the way the API starts a run.
	for profile, want := range map[string]string{"dev-cheap": "haiku", "v1-start": "opus"} {
		route, err := rt.Router.Resolve(profile, "editor")
		if err != nil || !strings.Contains(route.Model, want) {
			t.Errorf("%s: %+v %v", profile, route, err)
		}
	}
}

func TestOpenWithoutStorage(t *testing.T) {
	st := openStore(t)
	// The dev CLI and the tests get a runner; the editor's tools cannot look at footage there.
	rt, err := Open(Options{Store: st, Logger: quiet, Getenv: getenv(replayVars), Inline: true})
	if err != nil || rt.Runner == nil {
		t.Fatalf("Open: %v", err)
	}
	// The worker refuses to run agents without object storage and the sandbox: its jobs wait for a worker that has them.
	if _, err := Open(Options{Store: st, Logger: quiet, Getenv: getenv(replayVars), RequireStorage: true}); !errors.Is(err, ErrNoStorage) {
		t.Fatalf("RequireStorage without storage: %v", err)
	}
	// With everything it works.
	runner, dir, err := NewFrameRunner("local", "media-tools:dev", t.TempDir(), "ci")
	if err != nil {
		t.Fatal(err)
	}
	objects, err := storage.New(storage.Config{Endpoint: "http://localhost:9", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err = Open(Options{Store: st, Logger: quiet, Getenv: getenv(replayVars), RequireStorage: true, Objects: objects, Frames: runner, TempDir: dir})
	if err != nil || rt.Runner == nil {
		t.Fatalf("Open with storage: %v", err)
	}
}

func TestOpenRefusesBadSettings(t *testing.T) {
	st := openStore(t)
	for name, vars := range map[string]map[string]string{
		"a mode that does not exist": {"CF_ENV": "ci", "LLM_MODE": "yolo"},
		"an unknown profile":         {"CF_ENV": "ci", "LLM_MODE": "replay", "ROUTING_PROFILE": "no-such-profile"},
	} {
		if _, err := Open(Options{Store: st, Logger: quiet, Getenv: getenv(vars), DefinitionOnly: true}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Open(Options{Getenv: getenv(replayVars)}); err == nil {
		t.Error("no store: accepted")
	}
}

func TestNewFrameRunner(t *testing.T) {
	root := t.TempDir()
	for _, provider := range []string{"docker", "hardened", "local"} {
		r, dir, err := NewFrameRunner(provider, "media-tools:dev", root, "local")
		if err != nil || r == nil {
			t.Fatalf("%s: %v", provider, err)
		}
		resolved, _ := filepath.EvalSymlinks(root)
		if dir != filepath.Join(resolved, "frames") {
			t.Errorf("%s: the working directory is %s", provider, dir)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Errorf("%s: %v", provider, err)
		}
	}
	// The local provider isolates nothing: not in staging or production.
	for _, env := range []string{"staging", "production"} {
		if _, _, err := NewFrameRunner("local", "media-tools:dev", root, env); err == nil {
			t.Errorf("the local provider was accepted in %s", env)
		}
	}
	if _, _, err := NewFrameRunner("podman", "media-tools:dev", root, "local"); err == nil {
		t.Error("an unknown provider was accepted")
	}
}
