package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/ingest"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/store"
)

type downIngester struct {
	err    error
	failed []string
}

func (d *downIngester) Run(context.Context, ids.WorkspaceID, ids.AssetID) (ingest.Result, error) {
	return ingest.Result{}, d.err
}

func (d *downIngester) Fail(_ context.Context, _ ids.WorkspaceID, _ ids.AssetID, reason string) error {
	d.failed = append(d.failed, reason)
	return nil
}

// An unavailable sandbox makes the ingest wait, SandboxWaitsMax times at most; then the asset fails as processing_failed, and
// the job records the plain reason only, never docker's or the tool's output.
func TestIngestWaitsForTheSandboxAtMostTenTimes(t *testing.T) {
	raw := "docker: Error response from daemon: unknown or invalid runtime name: runsc /home/deploy/secret-path"
	down := &sandbox.UnavailableError{Reason: "the gVisor runtime (runsc) is not registered with Docker", Err: errors.New(raw)}
	ing := &downIngester{err: fmt.Errorf("ffprobe: %w", down)}
	created := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	w := &AssetIngestWorker{deps: Deps{Ingest: ing, Now: func() time.Time { return created.Add(time.Hour) }}}
	run := func(snoozes int) error {
		job := &river.Job[AssetIngestArgs]{JobRow: &rivertype.JobRow{ID: 1, Attempt: 5, MaxAttempts: 5, CreatedAt: created,
			Metadata: fmt.Appendf(nil, `{"snoozes": %d}`, snoozes)}}
		return w.Work(context.Background(), job)
	}
	for n := range SandboxWaitsMax {
		var snooze *river.JobSnoozeError
		if err := run(n); !errors.As(err, &snooze) {
			t.Fatalf("wait %d: want a snooze, got %v", n, err)
		}
	}
	if len(ing.failed) != 0 {
		t.Fatalf("the asset failed while waiting (even on the last attempt): %v", ing.failed)
	}
	err := run(SandboxWaitsMax)
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) || len(ing.failed) != 1 || ing.failed[0] != store.FailureProcessing {
		t.Fatalf("after %d waits: %v, failed %v", SandboxWaitsMax, err, ing.failed)
	}
	if msg := err.Error(); strings.Contains(msg, "docker:") || strings.Contains(msg, "secret-path") || !strings.Contains(msg, "the sandbox is unavailable: the gVisor runtime") {
		t.Fatalf("the stored error must be the plain reason only: %q", msg)
	}
}

func TestSandboxWaitGrows(t *testing.T) {
	for _, c := range []struct{ age, want time.Duration }{
		{0, 2 * time.Minute}, {20 * time.Minute, 5 * time.Minute}, {10 * time.Hour, 30 * time.Minute},
	} {
		if got := sandboxWait(c.age); got != c.want {
			t.Errorf("sandboxWait(%v) = %v, want %v", c.age, got, c.want)
		}
	}
	if snoozes([]byte(`{"snoozes":3}`)) != 3 || snoozes(nil) != 0 {
		t.Error("snoozes")
	}
}
