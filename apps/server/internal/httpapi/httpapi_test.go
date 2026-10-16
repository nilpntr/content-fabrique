package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	h := NewHealth(discardLogger(), Check{Name: "broken", Fn: func(context.Context) error { return errors.New("down") }})
	mux := NewMux(h)

	rec := get(t, mux, "/healthz")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("healthz = %d %s", rec.Code, rec.Body)
	}

	// Liveness ignores readiness checks and shutdown state.
	h.SetShuttingDown()
	if rec := get(t, mux, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz while shutting down = %d", rec.Code)
	}
}

func TestReadyzNoChecks(t *testing.T) {
	rec := get(t, NewMux(NewHealth(discardLogger())), "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body)
	}
}

func TestReadyzChecks(t *testing.T) {
	h := NewHealth(discardLogger(), Check{Name: "ok", Fn: func(context.Context) error { return nil }})
	mux := NewMux(h)
	if rec := get(t, mux, "/readyz"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":"ok"`) {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body)
	}

	h.AddCheck(Check{Name: "postgres", Fn: func(context.Context) error { return errors.New("secret detail") }})
	rec := get(t, mux, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz with failing check = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret detail") || !strings.Contains(rec.Body.String(), `"postgres":"fail"`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestReadyzShuttingDown(t *testing.T) {
	h := NewHealth(discardLogger())
	mux := NewMux(h)
	if rec := get(t, mux, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("before shutdown = %d", rec.Code)
	}
	h.SetShuttingDown()
	rec := get(t, mux, "/readyz")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "shutting_down") {
		t.Fatalf("during shutdown = %d %s", rec.Code, rec.Body)
	}
}

func TestHandlerRecoversPanics(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	rec := get(t, Handler(mux, discardLogger()), "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}
}

// TestServeGracefulShutdown checks the full sequence: readyz flips to 503 while the drain delay runs, an
// in-flight request still finishes, and Serve returns nil.
func TestServeGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	health := NewHealth(discardLogger())
	mux := NewMux(health)
	started := make(chan struct{})
	release := make(chan struct{})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	srv := NewServer(addr, Handler(mux, discardLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- Serve(ctx, srv, health, ServeOptions{DrainDelay: 300 * time.Millisecond, ShutdownTimeout: 5 * time.Second}, discardLogger())
	}()

	base := "http://" + addr
	waitFor(t, func() bool {
		resp, err := http.Get(base + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	slowBody := make(chan string, 1)
	go func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			slowBody <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		slowBody <- string(b)
	}()
	<-started

	cancel()
	waitFor(t, func() bool { return health.ShuttingDown() })
	resp, err := http.Get(base + "/readyz")
	if err != nil {
		t.Fatalf("readyz during drain: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz during drain = %d", resp.StatusCode)
	}

	// Let the drain delay pass and Shutdown begin, then release the in-flight request.
	time.Sleep(500 * time.Millisecond)
	close(release)

	if got := <-slowBody; got != "done" {
		t.Fatalf("in-flight request = %q", got)
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
