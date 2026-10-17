package renderjobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// flakyObjects is the pipeline's object storage with faults: a Put or Get whose key ends with a given suffix fails.
type flakyObjects struct {
	PipelineObjects
	mu      sync.Mutex
	failPut string
	failGet string
	puts    []string
	deleted []string
}

func (f *flakyObjects) Get(ctx context.Context, key string) (storage.Object, error) {
	if f.failGet != "" && strings.HasSuffix(key, f.failGet) {
		return storage.Object{}, errors.New("storage is down")
	}
	return f.PipelineObjects.Get(ctx, key)
}

func (f *flakyObjects) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	if f.failPut != "" && strings.HasSuffix(key, f.failPut) {
		return errors.New("storage is down")
	}
	if err := f.PipelineObjects.Put(ctx, key, body, size, contentType); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, key)
	return nil
}

func (f *flakyObjects) DeleteRenderObjects(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, render ids.RenderID) (int, error) {
	f.mu.Lock()
	f.deleted = append(f.deleted, render.String())
	f.mu.Unlock()
	return f.PipelineObjects.DeleteRenderObjects(ctx, ws, project, render)
}

// hookRunner runs fn before the first tool run of a job (the render), then goes on with the real runner.
type hookRunner struct {
	media.Runner
	once sync.Once
	fn   func()
}

func (h *hookRunner) Run(ctx context.Context, inv media.Invocation) (sandbox.Result, error) {
	h.once.Do(h.fn)
	return h.Runner.Run(ctx, inv)
}

// exitRunner says every tool was killed by a signal.
type exitRunner struct{ code int }

func (r exitRunner) Run(context.Context, media.Invocation) (sandbox.Result, error) {
	return sandbox.Result{ExitCode: r.code, Stderr: []byte("Killed")}, nil
}

func (e *env) row(t *testing.T, id ids.RenderID) (status string, reason *string, progress int) {
	t.Helper()
	err := e.super.QueryRow(context.Background(), `SELECT status, failure_reason, progress FROM renders WHERE id = $1`, id).Scan(&status, &reason, &progress)
	if err != nil {
		t.Fatal(err)
	}
	return status, reason, progress
}

func (f *fixture) noObjects(t *testing.T, r Render) {
	t.Helper()
	for _, k := range f.renderKeys(t, r) {
		if f.e.exists(t, k) {
			t.Errorf("object %s was left behind", k)
		}
	}
}

// A timeline or footage that cannot be rendered fails the render with a reason that is safe to show: the job ends normally
// (it is not retried), nothing stays in storage and the job directory is gone.
func TestRenderFailsForWhatNoRetryCanFix(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		spoil  func(t *testing.T, f *fixture)
	}{
		{"an asset deleted since the request", ReasonInvalidTimeline, func(t *testing.T, f *fixture) {
			if _, err := f.e.st.SoftDeleteAsset(context.Background(), f.w.ID, f.b.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"a proxy that is missing from storage", ReasonSourceUnavailable, func(t *testing.T, f *fixture) {
			key, _ := storage.DerivativeKey(f.w.ID, f.a.ID, "proxy_720.mp4")
			if err := f.e.objs.Delete(context.Background(), key); err != nil {
				t.Fatal(err)
			}
		}},
		{"a proxy that has no row", ReasonSourceUnavailable, func(t *testing.T, f *fixture) {
			if _, err := f.e.super.Exec(context.Background(), `DELETE FROM asset_derivatives WHERE asset_id = $1 AND kind = 'proxy_720'`, f.a.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"a proxy that is not a video", ReasonRenderFailed, func(t *testing.T, f *fixture) {
			key, _ := storage.DerivativeKey(f.w.ID, f.a.ID, "proxy_720.mp4")
			size := len(f.e.get(t, key))
			f.e.put(t, key, bytes.Repeat([]byte("not a video "), size/12+1)[:size])
		}},
		{"more footage than a render stages", ReasonInputsTooLarge, func(t *testing.T, f *fixture) {
			if _, err := f.e.super.Exec(context.Background(), `UPDATE asset_derivatives SET size_bytes = 9000000000 WHERE asset_id = $1`, f.a.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"a key that is not the key of its asset", ReasonProcessing, func(t *testing.T, f *fixture) {
			if _, err := f.e.super.Exec(context.Background(), `UPDATE asset_derivatives SET r2_key = 'ws/x/assets/y/proxy_720.mp4' WHERE asset_id = $1`, f.a.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, "org_fail")
			e := f.e
			r, _ := e.request(t, f.w, f.project, f.spec, "preview")
			c.spoil(t, f)
			got, out := e.runRender(t, f.w, r)
			if out.Status != store.RenderFailed || out.Reason != c.reason {
				t.Fatalf("outcome %+v, want failed with %s", out, c.reason)
			}
			if got.Status != store.RenderFailed || got.FailureReason == nil || *got.FailureReason != c.reason || got.FinishedAt == nil || got.WallMs == nil || got.Stage != nil {
				t.Fatalf("row %+v", got)
			}
			if got.Mp4Key != nil || got.ContactSheetKey != nil || got.TimeMapKey != nil {
				t.Errorf("a failed render has keys: %+v", got)
			}
			f.noObjects(t, r)
			e.tempDirEmpty(t)
			// It is over: a second job for it has nothing to do, and asking again makes a new render.
			if _, err := e.pipe.Run(context.Background(), f.w.ID, r.ID); !errors.Is(err, errGone) {
				t.Errorf("a second job: %v", err)
			}
		})
	}
}

// A problem of ours (storage, the sandbox, the worker stopping) is an error: the job is retried, the render stays running, and
// only the last attempt fails it, with what ended it as the reason, removing what the attempts left in storage.
func TestRenderRetriesWhatIsNotTheTimelinesFault(t *testing.T) {
	ctx := context.Background()

	t.Run("storage fails during the upload", func(t *testing.T) {
		f := newFixture(t, "org_infra")
		e := f.e
		flaky := &flakyObjects{PipelineObjects: e.objs, failPut: storage.RenderTimeMapName}
		e.pipe = e.newPipeline(t, flaky, e.runner)
		r, _ := e.request(t, f.w, f.project, f.spec, "preview")
		_, err := e.pipe.Run(ctx, f.w.ID, r.ID)
		var fe *fileError
		if err == nil || errors.As(err, &fe) || errors.Is(err, errGone) {
			t.Fatalf("Run = %v, want an error that is retried", err)
		}
		if status, _, _ := e.row(t, r.ID); status != store.RenderRendering {
			t.Errorf("status %s, want rendering until the last attempt", status)
		}
		// The attempt took back what it had uploaded; the row is not marked purged, as the retry will write them again.
		f.noObjects(t, r)
		var marked *time.Time
		if err := e.super.QueryRow(ctx, `SELECT objects_purged_at FROM renders WHERE id = $1`, r.ID).Scan(&marked); err != nil || marked != nil {
			t.Errorf("objects_purged_at = %v after a retried attempt (%v)", marked, err)
		}
		e.tempDirEmpty(t)
		// The retry (storage is back) renders it.
		flaky.failPut = ""
		if _, err := e.pipe.Run(ctx, f.w.ID, r.ID); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if got, _ := e.svc.Get(ctx, f.w.ID, r.ID); got.Status != store.RenderReady {
			t.Errorf("after the retry: %+v", got)
		}
	})

	t.Run("the last attempt fails the render and cleans storage", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			cause  error
			reason string
		}{
			{"an error", errors.New("the sandbox is down"), ReasonProcessing},
			{"the job timed out", fmt.Errorf("render: %w", context.DeadlineExceeded), ReasonTimeout},
			{"a tool timed out", fmt.Errorf("ffmpeg: %w", sandbox.ErrTimeout), ReasonTimeout},
			{"the worker was stopped", fmt.Errorf("upload: %w", context.Canceled), ReasonInterrupted},
		} {
			f := newFixture(t, "org_giveup")
			e := f.e
			flaky := &flakyObjects{PipelineObjects: e.objs, failPut: storage.RenderTimeMapName}
			e.pipe = e.newPipeline(t, flaky, e.runner)
			r, _ := e.request(t, f.w, f.project, f.spec, "preview")
			if _, err := e.pipe.Run(ctx, f.w.ID, r.ID); err == nil {
				t.Fatalf("%s: Run succeeded", c.name)
			}
			if len(flaky.puts) != 2 {
				t.Fatalf("%s: the attempt uploaded %v, want the video and the sheet before the failure", c.name, flaky.puts)
			}
			f.noObjects(t, r) // already taken back by the failed attempt
			if err := e.pipe.Fail(ctx, f.w.ID, r.ID, c.cause); err != nil {
				t.Fatalf("%s: Fail: %v", c.name, err)
			}
			if status, reason, _ := e.row(t, r.ID); status != store.RenderFailed || reason == nil || *reason != c.reason {
				t.Errorf("%s: %s %v, want failed with %s", c.name, status, reason, c.reason)
			}
			f.noObjects(t, r)
			// Failing twice, or failing a render that is ready, is not an error and changes nothing.
			if err := e.pipe.Fail(ctx, f.w.ID, r.ID, c.cause); err != nil {
				t.Errorf("%s: a second Fail: %v", c.name, err)
			}
		}
	})

	t.Run("a tool killed by a signal", func(t *testing.T) {
		f := newFixture(t, "org_killed")
		e := f.e
		e.pipe = e.newPipeline(t, e.objs, exitRunner{code: 137})
		r, _ := e.request(t, f.w, f.project, f.spec, "preview")
		_, err := e.pipe.Run(ctx, f.w.ID, r.ID)
		var fe *fileError
		if err == nil || errors.As(err, &fe) || !errors.Is(err, errKilled) {
			t.Fatalf("Run = %v, want a retried kill", err)
		}
		if status, _, _ := e.row(t, r.ID); status != store.RenderRendering {
			t.Errorf("status %s", status)
		}
		// An ordinary non-zero exit is ffmpeg refusing the input, which a retry would repeat.
		e.pipe = e.newPipeline(t, e.objs, exitRunner{code: 1})
		out, err := e.pipe.Run(ctx, f.w.ID, r.ID)
		if err != nil || out.Status != store.RenderFailed || out.Reason != ReasonRenderFailed {
			t.Errorf("exit 1: %+v %v", out, err)
		}
		e.tempDirEmpty(t)
	})

	t.Run("storage does not answer for the footage", func(t *testing.T) {
		f := newFixture(t, "org_nostorage")
		e := f.e
		e.pipe = e.newPipeline(t, &flakyObjects{PipelineObjects: e.objs, failGet: "proxy_720.mp4"}, e.runner)
		r, _ := e.request(t, f.w, f.project, f.spec, "preview")
		if _, err := e.pipe.Run(ctx, f.w.ID, r.ID); err == nil || errors.Is(err, errGone) {
			t.Fatalf("Run = %v, want an error that is retried", err)
		}
		if status, _, _ := e.row(t, r.ID); status != store.RenderRendering {
			t.Errorf("status %s", status)
		}
		e.tempDirEmpty(t)
	})

	t.Run("the worker is stopped in the middle of the render", func(t *testing.T) {
		f := newFixture(t, "org_stopped")
		e := f.e
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		e.pipe = e.newPipeline(t, e.objs, &hookRunner{Runner: e.runner, fn: cancel})
		r, _ := e.request(t, f.w, f.project, f.spec, "preview")
		_, err := e.pipe.Run(cctx, f.w.ID, r.ID)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want the cancellation", err)
		}
		e.tempDirEmpty(t)
		f.noObjects(t, r)
		// The next worker runs it again from the start.
		e.pipe = e.newPipeline(t, e.objs, e.runner)
		if out, err := e.pipe.Run(ctx, f.w.ID, r.ID); err != nil || out.Status != store.RenderReady {
			t.Errorf("rerun: %+v %v", out, err)
		}
	})
}

// A deleted project ends its renders: a queued one never starts, a running one stops and leaves nothing in storage, and neither
// stays in flight.
func TestRenderOfADeletedProject(t *testing.T) {
	ctx := context.Background()

	t.Run("deleted while queued", func(t *testing.T) {
		f := newFixture(t, "org_del_queued")
		e := f.e
		r, _ := e.request(t, f.w, f.project, f.spec, "preview")
		if err := e.st.SoftDeleteProject(ctx, f.w.ID, f.project.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pipe.Run(ctx, f.w.ID, r.ID); !errors.Is(err, errGone) {
			t.Fatalf("Run = %v, want ErrRenderGone", err)
		}
		if status, reason, _ := e.row(t, r.ID); status != store.RenderFailed || reason == nil || *reason != ReasonProjectDeleted {
			t.Errorf("row %s %v", status, reason)
		}
		if _, err := e.svc.Get(ctx, f.w.ID, r.ID); err == nil || err.Status != 404 {
			t.Errorf("Get of a deleted project's render: %v", err)
		}
		if n, _ := e.st.CountRendersInFlight(ctx, f.w.ID); n != 0 {
			t.Errorf("%d renders in flight", n)
		}
		e.tempDirEmpty(t)
	})

	t.Run("deleted while rendering", func(t *testing.T) {
		f := newFixture(t, "org_del_running")
		e := f.e
		hook := func() {
			if err := e.st.SoftDeleteProject(ctx, f.w.ID, f.project.ID); err != nil {
				t.Error(err)
			}
		}
		e.pipe = e.newPipeline(t, e.objs, &hookRunner{Runner: e.runner, fn: hook})
		r, _ := e.request(t, f.w, f.project, f.spec, "preview")
		if _, err := e.pipe.Run(ctx, f.w.ID, r.ID); !errors.Is(err, errGone) {
			t.Fatalf("Run = %v, want ErrRenderGone", err)
		}
		if status, reason, _ := e.row(t, r.ID); status != store.RenderFailed || reason == nil || *reason != ReasonProjectDeleted {
			t.Errorf("row %s %v", status, reason)
		}
		f.noObjects(t, r)
		e.tempDirEmpty(t)
	})
}

// A job directory that a crashed worker left behind is removed when the next pipeline starts, once it is older than any run
// lives; a recent one (another worker's) and files that are not ours stay.
func TestNewSweepsStaleJobDirectories(t *testing.T) {
	f := newFixture(t, "org_sweep")
	e := f.e
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(e.tempDir, name)
		if err := os.MkdirAll(filepath.Join(p, "in"), 0o750); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		for _, d := range []string{filepath.Join(p, "in"), p} { // the child first: it is what makes a directory look alive
			if err := os.Chtimes(d, when, when); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	stale := mk("render-stale", jobs.RenderTimeout+time.Hour)
	recent := mk("render-recent", jobs.RenderTimeout-time.Hour)
	foreign := mk("somebody-elses", jobs.RenderTimeout+time.Hour)
	e.newPipeline(t, e.objs, e.runner)
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the stale directory is still there (%v)", err)
	}
	for _, p := range []string{recent, foreign} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}

func TestNewValidatesItsConfig(t *testing.T) {
	f := newFixture(t, "org_config")
	e := f.e
	for name, cfg := range map[string]Config{
		"no store":          {Objects: e.objs, Runner: e.runner, TempDir: e.tempDir},
		"no storage":        {Store: e.st, Runner: e.runner, TempDir: e.tempDir},
		"no runner":         {Store: e.st, Objects: e.objs, TempDir: e.tempDir},
		"a relative dir":    {Store: e.st, Objects: e.objs, Runner: e.runner, TempDir: "tmp/render"},
		"an unclean dir":    {Store: e.st, Objects: e.objs, Runner: e.runner, TempDir: e.tempDir + "/../x"},
		"no dir":            {Store: e.st, Objects: e.objs, Runner: e.runner},
		"a trailing slash ": {Store: e.st, Objects: e.objs, Runner: e.runner, TempDir: e.tempDir + "/"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
