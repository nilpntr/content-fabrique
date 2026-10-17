package renderjobs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// A render whose job is gone (discarded after its last attempt, cancelled, finished without finishing the render) can never end by
// itself: the sweeper fails it with job_lost, takes back what an attempt left in storage, and so frees its place in the cap and in
// the dedupe index. Renders whose job is alive, and renders younger than the grace, are left alone.
func TestSweepFailsRendersWhoseJobIsGone(t *testing.T) {
	f := newFixture(t, "org_sweep_orphans")
	e, ctx := f.e, context.Background()
	for range 2 {
		e.addVersion(t, f.w, f.project, shortSpec(f.a, f.b))
	}
	alive, _ := e.request(t, f.w, f.project, 1, "preview")
	discarded, _ := e.request(t, f.w, f.project, 1, "final")
	ghost, _ := e.request(t, f.w, f.project, 2, "preview")
	waiting, _ := e.request(t, f.w, f.project, 2, "final")
	young, _ := e.request(t, f.w, f.project, 3, "preview")

	// The ghost was running when its worker died on the last attempt and River gave up on the job; the discarded one never started.
	if _, err := e.st.ClaimRender(ctx, f.w.ID, ghost.ID); err != nil {
		t.Fatal(err)
	}
	leftover := f.renderKeys(t, discarded)[0]
	e.put(t, leftover, []byte("half a video"))
	for _, r := range []Render{discarded, ghost, young} {
		if _, err := e.super.Exec(ctx, `UPDATE river_job SET state = 'discarded', finalized_at = now() WHERE kind = 'render' AND args->>'render_id' = $1`, r.ID.String()); err != nil {
			t.Fatal(err)
		}
	}
	// All but the young one are old enough to be looked at.
	if _, err := e.super.Exec(ctx, `UPDATE renders SET created_at = now() - interval '1 hour' WHERE id <> $1`, young.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.st.CountRendersInFlight(ctx, f.w.ID); n != 5 {
		t.Fatalf("%d renders in flight before the sweep, want 5", n)
	}
	// At the cap, a request for a render that is in flight is still answered, and a new one is refused.
	if _, _, err := e.svc.Request(ctx, e.caller(f.w), f.project.ID, 1, "final"); err != nil {
		t.Fatalf("an existing render at the cap: %v", err)
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
	if _, err := client.Insert(ctx, jobs.RendersSweepArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the sweep to complete", 30*time.Second, func() bool {
		var n int
		err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'renders_sweep' AND state = 'completed'`).Scan(&n)
		return err == nil && n == 1
	})

	for name, c := range map[string]struct {
		r      Render
		status string
	}{
		"discarded job": {discarded, store.RenderFailed}, "ghost": {ghost, store.RenderFailed},
		"live job": {alive, store.RenderQueued}, "waiting job": {waiting, store.RenderQueued}, "younger than the grace": {young, store.RenderQueued},
	} {
		status, reason, _ := e.row(t, c.r.ID)
		if status != c.status {
			t.Errorf("%s: status %s, want %s", name, status, c.status)
		}
		if c.status == store.RenderFailed && (reason == nil || *reason != jobs.RenderJobLost) {
			t.Errorf("%s: reason %v, want %s", name, reason, jobs.RenderJobLost)
		}
	}
	if e.exists(t, leftover) {
		t.Error("what the lost attempt uploaded is still in storage")
	}
	var marked *time.Time
	if err := e.super.QueryRow(ctx, `SELECT objects_purged_at FROM renders WHERE id = $1`, discarded.ID).Scan(&marked); err != nil || marked == nil {
		t.Errorf("the failed render is not marked purged (%v, %v)", marked, err)
	}
	if n, _ := e.st.CountRendersInFlight(ctx, f.w.ID); n != 3 {
		t.Errorf("%d renders in flight after the sweep, want 3", n)
	}
	// The ghosts no longer hold the dedupe index: asking again makes new renders.
	for _, c := range []struct {
		v    int
		prof string
	}{{1, "final"}, {2, "preview"}} {
		if r, created, err := e.svc.Request(ctx, e.caller(f.w), f.project.ID, c.v, c.prof); err != nil || !created || r.Status != store.RenderQueued {
			t.Errorf("a new request for version %d %s: %+v %v %v", c.v, c.prof, r.Status, created, err)
		}
	}
}

// The cap on renders in flight holds however many requests race: the count and the insert run under the workspace's lock, whatever
// the projects they are for.
func TestRenderCapHoldsUnderConcurrency(t *testing.T) {
	f := newFixture(t, "org_cap_race")
	e := f.e
	type target struct {
		project store.Project
		version int
		profile string
	}
	var targets []target
	for range 5 {
		p := e.newProject(t, f.w, f.a, f.b)
		v := e.addVersion(t, f.w, p, shortSpec(f.a, f.b))
		targets = append(targets, target{p, v, "preview"}, target{p, v, "final"})
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int
		refused int
	)
	for _, tg := range targets {
		wg.Go(func() {
			_, created, err := e.svc.Request(context.Background(), e.caller(f.w), tg.project.ID, tg.version, tg.profile)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && created:
				granted++
			case err != nil && err.Status == 429:
				refused++
			default:
				t.Errorf("Request: created=%v err=%v", created, err)
			}
		})
	}
	wg.Wait()
	if n, _ := e.st.CountRendersInFlight(context.Background(), f.w.ID); granted != MaxInFlight || refused != len(targets)-MaxInFlight || n != MaxInFlight {
		t.Fatalf("%d granted, %d refused, %d in flight; want %d, %d, %d", granted, refused, n, MaxInFlight, len(targets)-MaxInFlight, MaxInFlight)
	}
}

// Renders have a queue of their own, and a preview goes before a final.
func TestRenderJobsAreOnTheRenderQueueWithPriority(t *testing.T) {
	f := newFixture(t, "org_priority")
	e := f.e
	pre, _ := e.request(t, f.w, f.project, f.spec, "preview")
	fin, _ := e.request(t, f.w, f.project, f.spec, "final")
	for r, want := range map[ids.RenderID]int{pre.ID: 1, fin.ID: 3} {
		var queue string
		var priority int
		err := e.super.QueryRow(context.Background(), `SELECT queue, priority FROM river_job WHERE kind = 'render' AND args->>'render_id' = $1`, r.String()).Scan(&queue, &priority)
		if err != nil || queue != jobs.QueueRender || priority != want {
			t.Errorf("render %s: queue %q priority %d (%v), want %q and %d", r, queue, priority, err, jobs.QueueRender, want)
		}
	}
	if jobs.RenderPriority("preview") >= jobs.RenderPriority("final") {
		t.Error("a preview must have the higher priority (the lower number)")
	}
}

// A list does not presign: only the render itself carries URLs.
func TestListedRendersCarryNoURLs(t *testing.T) {
	f := newFixture(t, "org_list_urls")
	e, ctx := f.e, context.Background()
	r, _ := e.request(t, f.w, f.project, f.spec, "preview")
	if _, out := e.runRender(t, f.w, r); out.Status != store.RenderReady {
		t.Fatalf("outcome %+v", out)
	}
	h := NewAPI(e.svc, callerOf(f.w.ID, e.user, store.RoleViewer), quiet)
	resp, err := h.ListRenders(ctx, api.ListRendersRequestObject{Id: f.project.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	list, ok := resp.(api.ListRenders200JSONResponse)
	if !ok || len(list.Items) != 1 {
		t.Fatalf("list: %T %+v", resp, resp)
	}
	if it := list.Items[0]; it.Status != api.RenderStatusReady || it.Files != nil || it.UrlsExpiresAt != nil || it.ContactSheet == nil || it.DurationMs == nil {
		t.Errorf("a listed render: %+v", it)
	}
	got, err := h.GetRender(ctx, api.GetRenderRequestObject{Id: r.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if one, ok := got.(api.GetRender200JSONResponse); !ok || one.Files == nil || one.UrlsExpiresAt == nil {
		t.Errorf("the render itself: %+v", got)
	}
}

// A job directory counts as alive while anything in it, two levels deep, changed recently, so a live job whose directory itself
// is old (its files are in the subdirectories) is not swept.
func TestLastActivityLooksInsideTheJobDirectory(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-10 * time.Hour)
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o750); err != nil {
		t.Fatal(err)
	}
	progress := filepath.Join(dir, "work", "progress-render.txt")
	if err := os.WriteFile(progress, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{progress, filepath.Join(dir, "work"), dir} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := lastActivity(dir); err != nil || time.Since(got) < 9*time.Hour {
		t.Fatalf("an old directory: %v %v", got, err)
	}
	// The sandbox writes its progress file: the job is alive although the directories are old.
	now := time.Now()
	if err := os.Chtimes(progress, now, now); err != nil {
		t.Fatal(err)
	}
	if got, err := lastActivity(dir); err != nil || time.Since(got) > time.Minute {
		t.Fatalf("a file written in a subdirectory: %v %v", got, err)
	}
	// And so does the heartbeat, which touches the directory itself.
	if err := os.Chtimes(progress, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, now, now); err != nil {
		t.Fatal(err)
	}
	if got, err := lastActivity(dir); err != nil || time.Since(got) > time.Minute {
		t.Fatalf("a touched directory: %v %v", got, err)
	}
}

func TestNewKeepsALiveJobDirectoryWithOldDirectories(t *testing.T) {
	f := newFixture(t, "org_sweep_alive")
	e := f.e
	old := time.Now().Add(-(jobs.RenderTimeout + time.Hour))
	live := filepath.Join(e.tempDir, "render-live")
	if err := os.MkdirAll(filepath.Join(live, "work"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "work", "progress-render.txt"), []byte("frame=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{live, filepath.Join(live, "work")} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	e.newPipeline(t, e.objs, e.runner)
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the directory of a job that is still writing was removed: %v", err)
	}
}

// RenderAndWait sees a render ready within half a second of it being ready.
func TestRenderAndWaitPollsOftenEnough(t *testing.T) {
	if pollLongest > 500*time.Millisecond {
		t.Errorf("RenderAndWait waits up to %v between looks", pollLongest)
	}
}
