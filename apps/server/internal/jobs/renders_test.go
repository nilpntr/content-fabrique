package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// fakeRenderer is the render pipeline of a worker, with the answer of the test.
type fakeRenderer struct {
	out    jobs.RenderOutcome
	err    error
	failed chan error
}

func (f fakeRenderer) Run(context.Context, ids.WorkspaceID, ids.RenderID) (jobs.RenderOutcome, error) {
	return f.out, f.err
}

func (f fakeRenderer) Fail(_ context.Context, _ ids.WorkspaceID, _ ids.RenderID, cause error) error {
	f.failed <- cause
	return nil
}

func (e *env) enqueueRender(t *testing.T, w store.Workspace, opts *river.InsertOpts) ids.RenderID {
	t.Helper()
	ctx := context.Background()
	id := ids.RenderID{UUID: uuid.Must(uuid.NewV7())}
	err := e.api.InTx(ctx, func(tx *store.Store) error {
		_, err := e.enq.InsertInStore(ctx, tx, jobs.RenderArgs{WorkspaceID: w.ID, RenderID: id}, opts)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) renderJob(t *testing.T, id ids.RenderID) (state string, attempt int, output string) {
	t.Helper()
	err := e.super.QueryRow(context.Background(),
		`SELECT state::text, attempt, COALESCE((metadata->'output')::text, '') FROM river_job WHERE kind = 'render' AND args->>'render_id' = $1`, id.String()).Scan(&state, &attempt, &output)
	if err != nil {
		t.Fatalf("render job %s: %v", id, err)
	}
	return state, attempt, output
}

func (e *env) renderJobIs(t *testing.T, id ids.RenderID, state string) func() bool {
	return func() bool {
		s, _, _ := e.renderJob(t, id)
		return s == state
	}
}

// A worker that has no render pipeline leaves the job waiting, snoozed, without using an attempt: nothing is lost for a missing
// setting, and a worker that has the pipeline picks it up.
func TestRenderJobWaitsForAWorkerWithAPipeline(t *testing.T) {
	e := newEnv(t)
	w := e.newWorkspace(t, "org_render_wait")
	e.startWorker(t, jobs.WorkerOptions{})
	id := e.enqueueRender(t, w, nil)
	eventually(t, "the render job to be snoozed", e.renderJobIs(t, id, "scheduled"))
	if _, attempt, _ := e.renderJob(t, id); attempt > 1 {
		t.Errorf("a snoozed job used %d attempts", attempt)
	}
}

// A job is unique by render: enqueuing it twice (a request that is repeated) makes one job.
func TestRenderJobIsUniqueByRender(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w := e.newWorkspace(t, "org_render_unique")
	id := e.enqueueRender(t, w, nil)
	err := e.api.InTx(ctx, func(tx *store.Store) error {
		_, err := e.enq.InsertInStore(ctx, tx, jobs.RenderArgs{WorkspaceID: w.ID, RenderID: id}, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'render' AND args->>'render_id' = $1`, id.String()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d jobs for one render (%v)", n, err)
	}
	var queue string
	if err := e.super.QueryRow(ctx, `SELECT queue FROM river_job WHERE kind = 'render' AND args->>'render_id' = $1`, id.String()).Scan(&queue); err != nil || queue != jobs.QueueMedia {
		t.Errorf("queue %q (%v), want the media queue", queue, err)
	}
}

func TestRenderJobOutcomes(t *testing.T) {
	t.Run("a finished render records its outcome", func(t *testing.T) {
		e := newEnv(t)
		w := e.newWorkspace(t, "org_render_ok")
		e.startWorker(t, jobs.WorkerOptions{Render: fakeRenderer{out: jobs.RenderOutcome{Status: store.RenderReady, WallMs: 1234}}})
		id := e.enqueueRender(t, w, nil)
		eventually(t, "the render job to complete", e.renderJobIs(t, id, "completed"))
		if _, _, out := e.renderJob(t, id); !strings.Contains(out, `"ready"`) || !strings.Contains(out, "1234") {
			t.Errorf("output %s", out)
		}
	})
	t.Run("a render that cannot be rendered completes: it did its work and is not retried", func(t *testing.T) {
		e := newEnv(t)
		w := e.newWorkspace(t, "org_render_failed")
		e.startWorker(t, jobs.WorkerOptions{Render: fakeRenderer{out: jobs.RenderOutcome{Status: store.RenderFailed, Reason: "render_failed"}}})
		id := e.enqueueRender(t, w, nil)
		eventually(t, "the render job to complete", e.renderJobIs(t, id, "completed"))
		if _, attempt, out := e.renderJob(t, id); attempt != 1 || !strings.Contains(out, "render_failed") {
			t.Errorf("attempts %d, output %s", attempt, out)
		}
	})
	t.Run("a render that is gone ends the job: cancelled, not retried", func(t *testing.T) {
		e := newEnv(t)
		w := e.newWorkspace(t, "org_render_gone")
		e.startWorker(t, jobs.WorkerOptions{Render: fakeRenderer{err: jobs.ErrRenderGone}})
		id := e.enqueueRender(t, w, nil)
		eventually(t, "the render job to be cancelled", e.renderJobIs(t, id, "cancelled"))
		if _, attempt, _ := e.renderJob(t, id); attempt != 1 {
			t.Errorf("attempts %d", attempt)
		}
	})
}

// An error is retried, and the last attempt, whatever ended it, hands the cause to Fail so that the render does not stay
// `rendering` for ever.
func TestRenderJobGivesUpOnTheLastAttempt(t *testing.T) {
	for _, c := range []struct {
		name  string
		cause error
	}{
		{"an error", errors.New("the sandbox is down")},
		{"the job timed out", fmt.Errorf("render: %w", context.DeadlineExceeded)},
		{"a tool timed out", fmt.Errorf("ffmpeg: %w", sandbox.ErrTimeout)},
		{"the worker was stopped", fmt.Errorf("upload: %w", context.Canceled)},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			w := e.newWorkspace(t, "org_render_giveup")
			r := fakeRenderer{err: c.cause, failed: make(chan error, 4)}
			e.startWorker(t, jobs.WorkerOptions{Render: r})
			e.enqueueRender(t, w, &river.InsertOpts{MaxAttempts: 1})
			select {
			case got := <-r.failed:
				if !errors.Is(got, c.cause) {
					t.Fatalf("Fail got %v, want %v", got, c.cause)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("the render was not marked failed")
			}
		})
	}
	t.Run("an earlier attempt does not fail it", func(t *testing.T) {
		e := newEnv(t)
		w := e.newWorkspace(t, "org_render_retry")
		r := fakeRenderer{err: errors.New("storage is down"), failed: make(chan error, 4)}
		e.startWorker(t, jobs.WorkerOptions{Render: r})
		id := e.enqueueRender(t, w, &river.InsertOpts{MaxAttempts: 3})
		eventually(t, "the first attempt to fail", func() bool {
			s, attempt, _ := e.renderJob(t, id)
			return attempt >= 1 && s != "available" && s != "running"
		})
		select {
		case got := <-r.failed:
			t.Fatalf("Fail was called on an attempt that is not the last: %v", got)
		case <-time.After(300 * time.Millisecond):
		}
	})
}

// River must not hand a running render to a second worker: the job's own timeout ends it first, and the rescue comes later.
func TestRenderIsNeverRescuedWhileItRuns(t *testing.T) {
	if jobs.JobRescueAfter <= jobs.RenderTimeout {
		t.Fatalf("River rescues a job after %v, a render may run %v", jobs.JobRescueAfter, jobs.RenderTimeout)
	}
	w := &jobs.RenderWorker{}
	if got := w.Timeout(&river.Job[jobs.RenderArgs]{}); got != jobs.RenderTimeout {
		t.Fatalf("Timeout = %v, want %v", got, jobs.RenderTimeout)
	}
}

// Without object storage the clean-up of deleted projects' renders snoozes, like the other jobs that need it.
func TestRendersPurgeWithoutObjectStorageSnoozes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.startWorker(t, jobs.WorkerOptions{})
	client, err := jobs.NewInsertClient(e.api.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, jobs.RendersPurgeArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the purge job to be snoozed", func() bool {
		var n int
		err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'renders_purge' AND state = 'scheduled' AND scheduled_at > now() + interval '4 minutes' AND attempt = 0`).Scan(&n)
		return err == nil && n == 1
	})
}
