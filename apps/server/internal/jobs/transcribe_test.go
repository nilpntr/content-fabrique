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

// fakeTranscriber stands in for package transcribe.
type fakeTranscriber struct {
	mu     sync.Mutex
	runs   int
	failed []ids.AssetID
	// generations are the ingest generations the jobs ran and failed with.
	generations []int64
	failedGens  []int64
	outcome     func(run int) (any, error)
}

func (f *fakeTranscriber) Run(_ context.Context, _ ids.WorkspaceID, _ ids.AssetID, generation int64) (any, error) {
	f.mu.Lock()
	f.runs++
	n := f.runs
	f.generations = append(f.generations, generation)
	f.mu.Unlock()
	return f.outcome(n)
}

func (f *fakeTranscriber) Fail(_ context.Context, _ ids.WorkspaceID, id ids.AssetID, generation int64) error {
	f.mu.Lock()
	f.failed = append(f.failed, id)
	f.failedGens = append(f.failedGens, generation)
	f.mu.Unlock()
	return nil
}

// finishedWithAudio takes an asset through the ingest's transaction with the 16 kHz audio derivative, with the worker's hook in
// place, and returns it ready.
func (e *assetsEnv) finishedWithAudio(t *testing.T, w store.Workspace, name string, withAudio bool) store.Asset {
	t.Helper()
	ctx := context.Background()
	a := e.newUploadedAsset(t, w, store.AssetKindVideo, name, []byte("data"))
	if _, err := e.api.ClaimAssetForIngest(ctx, w.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	res := store.IngestResult{DurationMs: 5000, Width: 1080, Height: 1920, HasAudio: withAudio}
	if withAudio {
		res.Derivatives = []store.NewDerivative{{Kind: store.DerivativeAudio16k, Key: "ws/" + w.ID.String() + "/assets/" + a.ID.String() + "/audio_16k.wav", ContentType: "audio/wav", SizeBytes: 10}}
	}
	done, err := e.api.FinishAssetIngest(ctx, w.ID, a.ID, res)
	if err != nil {
		t.Fatalf("finishing the ingest: %v", err)
	}
	return done
}

func (e *assetsEnv) transcribeJob(t *testing.T, a store.Asset) (state, queue string, attempt int) {
	t.Helper()
	err := e.super.QueryRow(context.Background(),
		`SELECT state::text, queue, attempt FROM river_job WHERE kind = 'transcribe' AND args->>'asset_id' = $1`, a.ID.String()).Scan(&state, &queue, &attempt)
	if err != nil {
		t.Fatalf("transcribe job of %s: %v", a.ID, err)
	}
	return state, queue, attempt
}

func (e *assetsEnv) transcribeJobs(t *testing.T, a store.Asset) int {
	t.Helper()
	var n int
	if err := e.super.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'transcribe' AND args->>'asset_id' = $1`, a.ID.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *assetsEnv) transcriptStatus(t *testing.T, w store.Workspace, a store.Asset) string {
	t.Helper()
	got, err := e.api.GetAsset(context.Background(), w.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got.TranscriptStatus
}

// The ingest enqueues the transcription in its own transaction, once, on its own queue, and only for an asset with audio. A
// worker that cannot transcribe leaves the job waiting without using an attempt.
func TestTranscribeIsEnqueuedByTheIngestAndWaitsForAWorkerThatCanRunIt(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_tr_wait")
	e.startWorker(t, jobs.WorkerOptions{})

	silent := e.finishedWithAudio(t, w, "silent.mp4", false)
	if n := e.transcribeJobs(t, silent); n != 0 || silent.TranscriptStatus != store.TranscriptNone {
		t.Fatalf("a clip without audio: %d jobs, status %q", n, silent.TranscriptStatus)
	}

	a := e.finishedWithAudio(t, w, "voiced.mp4", true)
	if a.TranscriptStatus != store.TranscriptPending {
		t.Fatalf("status after the ingest: %q", a.TranscriptStatus)
	}
	state, queue, _ := e.transcribeJob(t, a)
	if queue != jobs.QueueTranscribe || (state != "available" && state != "scheduled" && state != "running") {
		t.Fatalf("job state %s on queue %s", state, queue)
	}
	eventually(t, "the job to be snoozed", func() bool { s, _, _ := e.transcribeJob(t, a); return s == "scheduled" })
	if _, _, attempt := e.transcribeJob(t, a); attempt > 1 {
		t.Errorf("a snooze must not use attempts, got %d", attempt)
	}
	if got := e.transcriptStatus(t, w, a); got != store.TranscriptPending {
		t.Errorf("a waiting job leaves the status %q, want pending", got)
	}

}

// A re-ingest is a new generation: it gets its own job (the first one's unique key does not swallow it), and a double enqueue for
// one ingest is still one job. Both carry the generation they were made for.
func TestTranscribeIsEnqueuedAgainForEveryIngest(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_tr_regen")
	e.startWorker(t, jobs.WorkerOptions{})
	ctx := context.Background()

	a := e.finishedWithAudio(t, w, "twice.mp4", true)
	if a.IngestedAt == nil {
		t.Fatal("no ingested_at")
	}
	audio := []store.NewDerivative{{Kind: store.DerivativeAudio16k, Key: "ws/" + w.ID.String() + "/assets/" + a.ID.String() + "/audio_16k.wav", ContentType: "audio/wav", SizeBytes: 10}}
	if _, err := e.api.ClaimAssetForIngest(ctx, w.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.api.SetTranscriptStatus(ctx, w.ID, a.ID, store.TranscriptReady); err != nil {
		t.Fatal(err)
	}
	again, err := e.api.FinishAssetIngest(ctx, w.ID, a.ID, store.IngestResult{DurationMs: 5000, Width: 1080, Height: 1920, HasAudio: true, Derivatives: audio})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.transcribeJobs(t, a); n != 2 || again.TranscriptStatus != store.TranscriptPending {
		t.Errorf("after a second ingest: %d jobs, status %q (want 2 jobs, pending)", n, again.TranscriptStatus)
	}
	rows, err := e.super.Query(ctx, `SELECT (args->>'generation')::bigint FROM river_job WHERE kind = 'transcribe' AND args->>'asset_id' = $1 ORDER BY id`, a.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gens []int64
	for rows.Next() {
		var g int64
		if err := rows.Scan(&g); err != nil {
			t.Fatal(err)
		}
		gens = append(gens, g)
	}
	if len(gens) != 2 || gens[0] != a.IngestedAt.UnixMicro() || gens[1] != again.IngestedAt.UnixMicro() || gens[0] == gens[1] {
		t.Errorf("generations %v, want %d then %d", gens, a.IngestedAt.UnixMicro(), again.IngestedAt.UnixMicro())
	}

	// The same ingest enqueued twice is one job.
	err = e.api.InTx(ctx, func(tx *store.Store) error {
		for range 2 {
			if _, err := e.enq.InsertInStore(ctx, tx, jobs.TranscribeArgs{WorkspaceID: w.ID, AssetID: a.ID, Generation: again.IngestedAt.UnixMicro()}, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.transcribeJobs(t, a); n != 2 {
		t.Errorf("a double enqueue made %d jobs in all, want 2", n)
	}
	// --force makes a different job of the same ingest.
	err = e.api.InTx(ctx, func(tx *store.Store) error {
		_, err := e.enq.InsertInStore(ctx, tx, jobs.TranscribeArgs{WorkspaceID: w.ID, AssetID: a.ID, Generation: again.IngestedAt.UnixMicro(), Force: 42}, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.transcribeJobs(t, a); n != 3 {
		t.Errorf("a forced job: %d jobs in all, want 3", n)
	}

	// An asset that lost its audio in the second ingest has no job for it and no transcript status.
	lost := e.finishedWithAudio(t, w, "lost.mp4", true)
	if _, err := e.api.ClaimAssetForIngest(ctx, w.ID, lost.ID); err != nil {
		t.Fatal(err)
	}
	cleared, err := e.api.FinishAssetIngest(ctx, w.ID, lost.ID, store.IngestResult{DurationMs: 5000, Width: 1080, Height: 1920})
	if err != nil || cleared.TranscriptStatus != store.TranscriptNone {
		t.Errorf("an ingest without audio: %q %v", cleared.TranscriptStatus, err)
	}
}

func TestTranscribeJobOutcomes(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_tr_outcomes")
	tr := &fakeTranscriber{}
	e.startWorker(t, jobs.WorkerOptions{Transcribe: tr})

	t.Run("a result is recorded as the job's output, and the job runs with its ingest generation", func(t *testing.T) {
		tr.outcome = func(int) (any, error) { return map[string]string{"status": "ready"}, nil }
		a := e.finishedWithAudio(t, w, "ok.mp4", true)
		eventually(t, "the job to complete", func() bool { s, _, _ := e.transcribeJob(t, a); return s == "completed" })
		tr.mu.Lock()
		if len(tr.generations) == 0 || tr.generations[len(tr.generations)-1] != a.IngestedAt.UnixMicro() {
			t.Errorf("the job ran with generations %v, want %d", tr.generations, a.IngestedAt.UnixMicro())
		}
		tr.mu.Unlock()
		var out string
		if err := e.super.QueryRow(context.Background(), `SELECT metadata->'output'::text FROM river_job WHERE kind = 'transcribe' AND args->>'asset_id' = $1`, a.ID.String()).Scan(&out); err != nil || !strings.Contains(out, "ready") {
			t.Errorf("output %q %v", out, err)
		}
	})

	t.Run("an asset that is gone cancels the job", func(t *testing.T) {
		tr.outcome = func(int) (any, error) { return nil, jobs.ErrTranscribeGone }
		a := e.finishedWithAudio(t, w, "gone.mp4", true)
		eventually(t, "the job to be cancelled", func() bool { s, _, _ := e.transcribeJob(t, a); return s == "cancelled" })
	})

	t.Run("a provider that is switched off waits without using an attempt", func(t *testing.T) {
		tr.outcome = func(int) (any, error) { return nil, fmt.Errorf("off: %w", jobs.ErrTranscribeNotConfigured) }
		a := e.finishedWithAudio(t, w, "off.mp4", true)
		eventually(t, "the job to be snoozed", func() bool { s, _, _ := e.transcribeJob(t, a); return s == "scheduled" })
		if _, _, attempt := e.transcribeJob(t, a); attempt > 1 {
			t.Errorf("attempt = %d", attempt)
		}
	})

	t.Run("an error is retried, and only the last attempt marks the transcript failed", func(t *testing.T) {
		tr.outcome = func(int) (any, error) { return nil, errors.New("sandbox down") }
		a := e.finishedWithAudio(t, w, "retry.mp4", true)
		eventually(t, "the first attempt to fail", func() bool { s, _, _ := e.transcribeJob(t, a); return s == "retryable" })
		if got := e.transcriptStatus(t, w, a); got != store.TranscriptPending {
			t.Errorf("after the first attempt the status is %q, want pending", got)
		}

		last := e.finishedWithAudio(t, w, "last.mp4", false) // no job of the hook: enqueue one with a single attempt
		err := e.api.InTx(context.Background(), func(tx *store.Store) error {
			_, err := e.enq.InsertInStore(context.Background(), tx, jobs.TranscribeArgs{WorkspaceID: w.ID, AssetID: last.ID, Generation: last.IngestedAt.UnixMicro()}, &river.InsertOpts{MaxAttempts: 1})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "the job to be discarded", func() bool { s, _, _ := e.transcribeJob(t, last); return s == "discarded" })
		tr.mu.Lock()
		defer tr.mu.Unlock()
		if len(tr.failed) != 1 || tr.failed[0] != last.ID || tr.failedGens[0] != last.IngestedAt.UnixMicro() {
			t.Errorf("failed assets %v with generations %v, want [%s] at %d", tr.failed, tr.failedGens, last.ID, last.IngestedAt.UnixMicro())
		}
	})
}

// A job that waits for a worker that can run it gives up after a day: the transcript is marked failed and the job cancelled.
func TestTranscribeGivesUpAfterWaitingADay(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_tr_age")
	e.startWorker(t, jobs.WorkerOptions{}) // no transcriber
	a := e.finishedWithAudio(t, w, "old.mp4", true)
	eventually(t, "the job to be snoozed", func() bool { s, _, _ := e.transcribeJob(t, a); return s == "scheduled" })
	_, err := e.super.Exec(context.Background(),
		`UPDATE river_job SET created_at = now() - interval '25 hours', scheduled_at = now() WHERE kind = 'transcribe' AND args->>'asset_id' = $1`, a.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the job to be cancelled", func() bool { s, _, _ := e.transcribeJob(t, a); return s == "cancelled" })
	if got := e.transcriptStatus(t, w, a); got != store.TranscriptFailed {
		t.Errorf("after a day: %q, want failed", got)
	}
}
