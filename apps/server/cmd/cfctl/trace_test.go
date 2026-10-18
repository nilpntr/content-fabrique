package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

func TestTraceCommandArguments(t *testing.T) {
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"trace"}, 2, "Usage"},
		{[]string{"trace", "help"}, 0, ""},
		{[]string{"trace", "nope"}, 2, "unknown command"},
		{[]string{"trace", "export"}, 2, "--run"},
		{[]string{"trace", "export", "--run", "run_nope"}, 2, "--run"},
		{[]string{"trace", "export", "--run", uuid.NewString()}, 2, "--workspace"},
	} {
		var out, errb bytes.Buffer
		if got := run(c.args, &out, &errb); got != c.code || !strings.Contains(out.String()+errb.String(), c.msg) {
			t.Errorf("%v: code %d, output %q", c.args, got, out.String()+errb.String())
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "api.axiom.co": false, "10.0.0.1": false} {
		if isLoopback(host) != want {
			t.Errorf("isLoopback(%q) != %v", host, want)
		}
	}
}

func TestPhoenixTraceURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"name":"default","id":"UHJvamVjdDox"},{"name":"content-fabrique","id":"UHJvamVjdDoy"}]}`))
	}))
	defer srv.Close()
	run := uuid.New()
	got := phoenixTraceURL(context.Background(), srv.URL, telemetry.RunTraceID(run))
	want := srv.URL + "/projects/UHJvamVjdDoy/traces/" + strings.ReplaceAll(run.String(), "-", "")
	if got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}
