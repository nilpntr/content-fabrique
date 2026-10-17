package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
)

type fakeVerifier struct {
	pingErr   error
	verifyErr error
	verifies  int
}

func (f *fakeVerifier) Ping(context.Context) error { return f.pingErr }
func (f *fakeVerifier) VerifyAppLogin(context.Context) error {
	f.verifies++
	return f.verifyErr
}

var errPrivileged = fmt.Errorf("%w: the DATABASE_URL login is a superuser", store.ErrPrivilegedLogin)

func TestStartupRefusesAPrivilegedLoginOnStagingAndProduction(t *testing.T) {
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	for _, env := range []string{"staging", "production"} {
		err := startupLoginCheck(context.Background(), &fakeVerifier{verifyErr: errPrivileged}, env, logger)
		if !errors.Is(err, store.ErrPrivilegedLogin) || !strings.Contains(err.Error(), "refusing to start in "+env) {
			t.Errorf("%s: err = %v", env, err)
		}
	}
	// One login does both jobs locally, in ci and in bench.
	for _, env := range []string{"local", "ci", "bench"} {
		v := &fakeVerifier{verifyErr: errPrivileged}
		if err := startupLoginCheck(context.Background(), v, env, logger); err != nil || v.verifies != 0 {
			t.Errorf("%s: err = %v, verifies = %d; want no check", env, err, v.verifies)
		}
	}
	// An unprivileged login starts.
	if err := startupLoginCheck(context.Background(), &fakeVerifier{}, "production", logger); err != nil {
		t.Errorf("unprivileged login refused: %v", err)
	}
	// A database that is down is not a crash loop: start, and let readiness fail.
	down := &fakeVerifier{verifyErr: errors.New("connection refused")}
	if err := startupLoginCheck(context.Background(), down, "staging", logger); err != nil {
		t.Errorf("unreachable database refused the start: %v", err)
	}
	if !strings.Contains(log.String(), "cannot verify the app login yet") {
		t.Errorf("the unreachable case was not logged: %s", log.String())
	}
}

func TestReadinessFailsWithAPrivilegedLogin(t *testing.T) {
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	v := &fakeVerifier{verifyErr: errPrivileged}
	check := postgresCheck(v, "staging", logger)
	for range 2 {
		if err := check(context.Background()); !errors.Is(err, store.ErrPrivilegedLogin) {
			t.Fatalf("readiness err = %v, want ErrPrivilegedLogin", err)
		}
	}
	if v.verifies != 2 {
		t.Errorf("a privileged login must be re-checked every time, verified %d times", v.verifies)
	}
	if !strings.Contains(log.String(), "login is privileged") || !strings.Contains(log.String(), "level=ERROR") {
		t.Errorf("no clear log line: %s", log.String())
	}

	// Once the login is proven unprivileged, the proof is not repeated on every probe.
	v.verifyErr = nil
	for range 3 {
		if err := check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if v.verifies != 3 {
		t.Errorf("verifies = %d, want 3 (2 failures + 1 success)", v.verifies)
	}

	// A ping failure wins, and nothing is verified.
	v2 := &fakeVerifier{pingErr: errors.New("down")}
	if err := postgresCheck(v2, "production", logger)(context.Background()); err == nil || v2.verifies != 0 {
		t.Errorf("ping failure: err = %v, verifies = %d", err, v2.verifies)
	}
	// Locally only the ping runs.
	v3 := &fakeVerifier{verifyErr: errPrivileged}
	if err := postgresCheck(v3, "local", logger)(context.Background()); err != nil || v3.verifies != 0 {
		t.Errorf("local: err = %v, verifies = %d", err, v3.verifies)
	}
}
