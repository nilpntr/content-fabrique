package renderjobs

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// startWorker runs the worker side the way cmd/worker does (the queue, the render pipeline and storage) and stops it with the test.
func (e *env) startWorker(t *testing.T) {
	t.Helper()
	w, err := jobs.StartWorker(context.Background(), jobs.WorkerOptions{
		DirectURL: e.dsn, Store: e.st, Logger: quiet, Objects: e.objs, Render: e.pipe,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Stop(context.Background()); err != nil {
			t.Errorf("worker stop: %v", err)
		}
	})
}

func (e *env) jobState(t *testing.T, r Render) (state string, attempt int) {
	t.Helper()
	err := e.super.QueryRow(context.Background(), `SELECT state::text, attempt FROM river_job WHERE kind = 'render' AND args->>'render_id' = $1`, r.ID.String()).Scan(&state, &attempt)
	if err != nil {
		t.Fatalf("job of %s: %v", r.ID, err)
	}
	return state, attempt
}

// A render requested through the API path is rendered by the worker from the job the request enqueued, and RenderAndWait, the
// entry point of the agent's render_preview tool, returns it ready. A file that cannot be rendered ends the job without a retry.
func TestRenderThroughTheQueue(t *testing.T) {
	f := newFixture(t, "org_queue")
	e, ctx := f.e, context.Background()
	e.startWorker(t)

	r, err := RenderAndWait(ctx, e.svc, AgentCaller(f.w.ID, ids.Encode(ids.PrefixRun, newV7(t))), f.project.ID, f.spec, "preview", 90*time.Second)
	if err != nil {
		t.Fatalf("RenderAndWait: %v", err)
	}
	if r.Status != store.RenderReady || r.Progress != 100 || r.DurationMs == nil || *r.DurationMs < 2900 || r.RequestedByKind != store.RenderRequestedByAgent || !strings.HasPrefix(r.RequestedByID, "run_") {
		t.Fatalf("render %+v", r)
	}
	// The row is ready a moment before River records the job as done.
	eventually(t, "the job to complete", 10*time.Second, func() bool { s, _ := e.jobState(t, r); return s == "completed" })
	if _, attempt := e.jobState(t, r); attempt != 1 {
		t.Errorf("the job took %d attempts", attempt)
	}
	var out string
	if err := e.super.QueryRow(ctx, `SELECT metadata->'output'->>'status' FROM river_job WHERE kind = 'render' AND args->>'render_id' = $1`, r.ID.String()).Scan(&out); err != nil || out != "ready" {
		t.Errorf("job output %q (%v)", out, err)
	}
	// The picture the agent gets is a JPEG, and asking again returns the same render at once, with no second job.
	sheet, err := e.svc.ContactSheetJPEG(ctx, r)
	if err != nil || !bytes.HasPrefix(sheet, []byte{0xff, 0xd8}) {
		t.Errorf("contact sheet: %v", err)
	}
	again, err := RenderAndWait(ctx, e.svc, e.caller(f.w), f.project.ID, f.spec, "preview", time.Second)
	if err != nil || again.ID != r.ID || e.jobsFor(t, r) != 1 {
		t.Errorf("a second RenderAndWait: %v %v, %d jobs", again.ID, err, e.jobsFor(t, r))
	}

	// A proxy that is not a video fails the render. The job completes (it did its work) on its first attempt.
	v2 := e.addVersion(t, f.w, f.project, shortSpec(f.a, f.b))
	key, _ := storage.DerivativeKey(f.w.ID, f.b.ID, "proxy_720.mp4")
	e.put(t, key, bytes.Repeat([]byte("x"), len(e.get(t, key))))
	failed, err := RenderAndWait(ctx, e.svc, e.caller(f.w), f.project.ID, v2, "preview", 90*time.Second)
	var fe *FailedError
	if !errors.As(err, &fe) || !errors.Is(err, ErrRenderFailed) || fe.Reason != ReasonRenderFailed || failed.Status != store.RenderFailed {
		t.Fatalf("RenderAndWait of a bad proxy = %+v, %v", failed, err)
	}
	eventually(t, "the job to complete", 10*time.Second, func() bool { s, _ := e.jobState(t, failed); return s == "completed" })
	if _, attempt := e.jobState(t, failed); attempt != 1 {
		t.Errorf("the job of a bad file was attempted %d times", attempt)
	}
	// A failed render does not block asking again.
	if _, created, err := e.svc.Request(ctx, e.caller(f.w), f.project.ID, v2, "preview"); err != nil || !created {
		t.Errorf("asking again after a failure: %v %v", created, err)
	}
}

// RenderAndWait against a service of its own: the answers it gives for each way a render can end.
type fakeRequester struct {
	req    *Error
	states []Render
	calls  atomic.Int32
}

func (f *fakeRequester) Request(context.Context, Caller, ids.ProjectID, int, string) (Render, bool, *Error) {
	if f.req != nil {
		return Render{}, false, f.req
	}
	return f.states[0], true, nil
}

func (f *fakeRequester) Get(context.Context, ids.WorkspaceID, ids.RenderID) (Render, *Error) {
	n := int(f.calls.Add(1))
	return f.states[min(n, len(f.states)-1)], nil
}

func TestRenderAndWait(t *testing.T) {
	ctx := context.Background()
	reason := ReasonRenderFailed
	at := func(status string, progress int16) Render { return Render{Status: status, Progress: progress} }
	call := func(f *fakeRequester, timeout time.Duration) (Render, error) {
		return RenderAndWait(ctx, f, Caller{}, ids.ProjectID{}, 1, "preview", timeout)
	}

	if r, err := call(&fakeRequester{states: []Render{at(store.RenderReady, 100)}}, time.Second); err != nil || r.Status != store.RenderReady {
		t.Errorf("ready at once: %v %v", r.Status, err)
	}
	if r, err := call(&fakeRequester{states: []Render{at(store.RenderQueued, 0), at(store.RenderRendering, 40), at(store.RenderReady, 100)}}, 10*time.Second); err != nil || r.Status != store.RenderReady {
		t.Errorf("queued, rendering, ready: %v %v", r.Status, err)
	}
	failed := Render{Status: store.RenderFailed, FailureReason: &reason}
	var fe *FailedError
	if r, err := call(&fakeRequester{states: []Render{at(store.RenderRendering, 10), failed}}, 10*time.Second); !errors.As(err, &fe) || fe.Reason != reason || !errors.Is(err, ErrRenderFailed) || r.Status != store.RenderFailed {
		t.Errorf("failed: %v %v", r.Status, err)
	}
	if r, err := call(&fakeRequester{states: []Render{at(store.RenderRendering, 55)}}, 600*time.Millisecond); !errors.Is(err, ErrWaitTimeout) || r.Status != store.RenderRendering || r.Progress != 55 {
		t.Errorf("timeout: %v %v", r.Status, err)
	}
	refused := &Error{Status: 422, Code: api.ErrorCodeTimelineInvalid, Message: "no"}
	var re *Error
	if _, err := call(&fakeRequester{req: refused}, time.Second); !errors.As(err, &re) || re.Status != 422 {
		t.Errorf("a refused request: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := RenderAndWait(cctx, &fakeRequester{states: []Render{at(store.RenderQueued, 0)}}, Caller{}, ids.ProjectID{}, 1, "preview", time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}
	if _, err := call(&fakeRequester{states: []Render{at(store.RenderReady, 100)}}, 0); err == nil {
		t.Error("a zero timeout was accepted")
	}
}

// The clean-up job removes the objects of renders whose project was deleted, and only those: not another project's, not another
// workspace's. It marks them, so a second run does nothing.
func TestRendersPurgeJob(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	doomed := e.fixture(t, "org_purge_a")
	live := e.fixture(t, "org_purge_b")

	render := func(f *fixture) (Render, []string) {
		r, _ := e.request(t, f.w, f.project, f.spec, "preview")
		got, out := e.runRender(t, f.w, r)
		if out.Status != store.RenderReady {
			t.Fatalf("outcome %+v", out)
		}
		return got, f.renderKeys(t, got)
	}
	gone, goneKeys := render(doomed)
	kept, keptKeys := render(live)
	// A failed render of the doomed project has no objects; it is only marked.
	vFailed := e.addVersion(t, doomed.w, doomed.project, shortSpec(doomed.a, doomed.b))
	failedRender, _ := e.request(t, doomed.w, doomed.project, vFailed, "final")
	if _, err := e.st.FailRender(ctx, doomed.w.ID, failedRender.ID, ReasonRenderFailed, -1, -1); err != nil {
		t.Fatal(err)
	}
	// A render still queued is left to its own job.
	vQueued := e.addVersion(t, doomed.w, doomed.project, shortSpec(doomed.b, doomed.a))
	queued, _ := e.request(t, doomed.w, doomed.project, vQueued, "preview")

	if err := e.st.SoftDeleteProject(ctx, doomed.w.ID, doomed.project.ID); err != nil {
		t.Fatal(err)
	}
	for _, k := range goneKeys {
		if !e.exists(t, k) {
			t.Fatalf("%s is missing before the clean-up", k)
		}
	}

	w, err := jobs.StartWorker(ctx, jobs.WorkerOptions{DirectURL: e.dsn, Store: e.st, Logger: quiet, Objects: e.objs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(context.Background()) })
	client, err := jobs.NewInsertClient(e.st.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, jobs.RendersPurgeArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the purge job to complete", 30*time.Second, func() bool {
		var n int
		err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'renders_purge' AND state = 'completed'`).Scan(&n)
		return err == nil && n == 1
	})
	for _, k := range goneKeys {
		if e.exists(t, k) {
			t.Errorf("%s of a deleted project is still there", k)
		}
	}
	for _, k := range keptKeys {
		if !e.exists(t, k) {
			t.Errorf("%s of a live project was removed", k)
		}
	}
	purged := func(id ids.RenderID) bool {
		var at *time.Time
		if err := e.super.QueryRow(ctx, `SELECT objects_purged_at FROM renders WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at != nil
	}
	if !purged(gone.ID) || !purged(failedRender.ID) || purged(kept.ID) || purged(queued.ID) {
		t.Errorf("purged: ready %v, failed %v; live %v, queued %v", purged(gone.ID), purged(failedRender.ID), purged(kept.ID), purged(queued.ID))
	}
	var out string
	if err := e.super.QueryRow(ctx, `SELECT metadata->'output'::text FROM river_job WHERE kind = 'renders_purge'`).Scan(&out); err != nil || !strings.Contains(out, `"purged": 2`) {
		t.Errorf("output %s (%v)", out, err)
	}
}
