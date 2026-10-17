package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// fakeQuickLook stands in for package quicklook.
type fakeQuickLook struct {
	mu      sync.Mutex
	runs    int
	gaveUp  int
	outcome func(run int) (any, bool, error)
}

func (f *fakeQuickLook) RunJob(context.Context, ids.WorkspaceID, ids.AssetID) (any, bool, error) {
	f.mu.Lock()
	f.runs++
	n := f.runs
	f.mu.Unlock()
	return f.outcome(n)
}

func (f *fakeQuickLook) GiveUp(context.Context, ids.WorkspaceID, ids.AssetID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gaveUp++
	return nil
}

func (f *fakeQuickLook) counts() (runs, gaveUp int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs, f.gaveUp
}

// finishedVideo takes a video through the ingest's transaction the way the pipeline does, with the worker's hook in place
// (StartWorker sets it), and returns it ready with its quick look pending.
func (e *assetsEnv) finishedVideo(t *testing.T, w store.Workspace, name string) store.Asset {
	t.Helper()
	ctx := context.Background()
	a := e.newUploadedAsset(t, w, store.AssetKindVideo, name, []byte("data"))
	if _, err := e.api.ClaimAssetForIngest(ctx, w.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	done, err := e.api.FinishAssetIngest(ctx, w.ID, a.ID, store.IngestResult{DurationMs: 5000, Width: 1080, Height: 1920})
	if err != nil {
		t.Fatalf("finishing the ingest: %v", err)
	}
	return done
}

func (e *assetsEnv) quickLookJob(t *testing.T, a store.Asset) (state, queue string, attempt int, output string) {
	t.Helper()
	err := e.super.QueryRow(context.Background(),
		`SELECT state::text, queue, attempt, COALESCE((metadata->'output')::text, '') FROM river_job WHERE kind = 'quick_look' AND args->>'asset_id' = $1`,
		a.ID.String()).Scan(&state, &queue, &attempt, &output)
	if err != nil {
		t.Fatalf("quick_look job of %s: %v", a.ID, err)
	}
	return state, queue, attempt, output
}

func (e *assetsEnv) quickLookJobs(t *testing.T, a store.Asset) int {
	t.Helper()
	var n int
	if err := e.super.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'quick_look' AND args->>'asset_id' = $1`, a.ID.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The ingest finishing a video enqueues its quick look in the same transaction, once, on the quicklook queue. A worker with
// no quick look (no model access, no sandbox) leaves the job waiting without using an attempt.
func TestQuickLookIsEnqueuedByTheIngestAndWaitsForAWorkerThatCanRunIt(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_ql_wait")
	e.startWorker(t, jobs.WorkerOptions{}) // sets the hook on e.api, runs no quick look

	a := e.finishedVideo(t, w, "waiting.mp4")
	if a.Status != store.AssetReady || a.QuickLookStatus == nil || *a.QuickLookStatus != store.QuickLookPending {
		t.Fatalf("after the ingest: %s, quick look %v", a.Status, a.QuickLookStatus)
	}
	state, queue, _, _ := e.quickLookJob(t, a)
	if queue != jobs.QueueQuickLook || (state != "available" && state != "scheduled" && state != "running") {
		t.Fatalf("job state %s on queue %s", state, queue)
	}
	eventually(t, "the quick look job to be snoozed", func() bool { s, _, _, _ := e.quickLookJob(t, a); return s == "scheduled" })
	if _, _, attempt, _ := e.quickLookJob(t, a); attempt > 1 {
		t.Errorf("a snooze must not use attempts, got %d", attempt)
	}

	// An audio file or an image never gets one; neither does a failed ingest.
	audio := e.newUploadedAsset(t, w, store.AssetKindAudio, "voice.mp3", []byte("data"))
	if _, err := e.api.ClaimAssetForIngest(context.Background(), w.ID, audio.ID); err != nil {
		t.Fatal(err)
	}
	done, err := e.api.FinishAssetIngest(context.Background(), w.ID, audio.ID, store.IngestResult{DurationMs: 1000})
	if err != nil || done.QuickLookStatus != nil || e.quickLookJobs(t, done) != 0 {
		t.Errorf("audio: quick look %v, %d jobs, %v", done.QuickLookStatus, e.quickLookJobs(t, done), err)
	}
}

func TestQuickLookJobOutcomes(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_ql_outcomes")
	ql := &fakeQuickLook{}
	e.startWorker(t, jobs.WorkerOptions{QuickLook: ql})

	t.Run("a result is recorded as the job's output", func(t *testing.T) {
		ql.outcome = func(int) (any, bool, error) { return map[string]string{"outcome": "done"}, false, nil }
		a := e.finishedVideo(t, w, "ok.mp4")
		eventually(t, "the job to complete", func() bool { s, _, _, _ := e.quickLookJob(t, a); return s == "completed" })
		if _, _, _, out := e.quickLookJob(t, a); !strings.Contains(out, `"done"`) {
			t.Errorf("job output %s", out)
		}
	})

	t.Run("an asset that is gone cancels the job", func(t *testing.T) {
		ql.outcome = func(int) (any, bool, error) { return nil, true, nil }
		a := e.finishedVideo(t, w, "gone.mp4")
		eventually(t, "the job to be cancelled", func() bool { s, _, _, _ := e.quickLookJob(t, a); return s == "cancelled" })
	})

	t.Run("no model access waits without using an attempt", func(t *testing.T) {
		ql.outcome = func(int) (any, bool, error) { return nil, false, fmt.Errorf("no key: %w", jobs.ErrModelUnavailable) }
		a := e.finishedVideo(t, w, "nokey.mp4")
		eventually(t, "the job to be snoozed", func() bool { s, _, _, _ := e.quickLookJob(t, a); return s == "scheduled" })
		if _, _, attempt, _ := e.quickLookJob(t, a); attempt > 1 {
			t.Errorf("attempt = %d", attempt)
		}
	})

	t.Run("an error is retried, and only the last attempt gives up", func(t *testing.T) {
		ql.outcome = func(int) (any, bool, error) { return nil, false, errors.New("sandbox down") }
		_, gaveBefore := ql.counts()
		a := e.finishedVideo(t, w, "retry.mp4")
		eventually(t, "the first attempt to fail", func() bool { s, _, _, _ := e.quickLookJob(t, a); return s == "retryable" })
		if _, gave := ql.counts(); gave != gaveBefore {
			t.Errorf("GiveUp was called after the first attempt")
		}
	})

	t.Run("the last attempt marks the quick look failed", func(t *testing.T) {
		ql.outcome = func(int) (any, bool, error) { return nil, false, errors.New("sandbox down") }
		_, gaveBefore := ql.counts()
		a := e.newUploadedAsset(t, w, store.AssetKindVideo, "last.mp4", []byte("data"))
		// The same job with a single attempt, as the last attempt of a longer one would be.
		err := e.api.InTx(context.Background(), func(tx *store.Store) error {
			_, err := e.enq.InsertInStore(context.Background(), tx, jobs.QuickLookArgs{WorkspaceID: w.ID, AssetID: a.ID}, &river.InsertOpts{MaxAttempts: 1})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "the job to be discarded", func() bool { s, _, _, _ := e.quickLookJob(t, a); return s == "discarded" })
		if _, gave := ql.counts(); gave != gaveBefore+1 {
			t.Errorf("GiveUp calls = %d, want %d", gave, gaveBefore+1)
		}
	})
}

// One job per asset, however often the ingest finishes: a second ingest of an asset whose quick look is still pending does not
// enqueue another.
func TestQuickLookJobIsUniquePerAsset(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_ql_unique")
	e.startWorker(t, jobs.WorkerOptions{})
	a := e.finishedVideo(t, w, "twice.mp4")
	ctx := context.Background()
	if _, err := e.api.ClaimAssetForIngest(ctx, w.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.FinishAssetIngest(ctx, w.ID, a.ID, store.IngestResult{DurationMs: 5000, Width: 1080, Height: 1920}); err != nil {
		t.Fatal(err)
	}
	if n := e.quickLookJobs(t, a); n != 1 {
		t.Errorf("%d quick_look jobs for one asset, want 1", n)
	}
}
