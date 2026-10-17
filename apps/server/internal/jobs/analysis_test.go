package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// fakeAnalysis stands in for package analysis.
type fakeAnalysis struct {
	mu      sync.Mutex
	runs    int
	outcome func(run int) (any, bool, error)
}

func (f *fakeAnalysis) RunJob(context.Context, ids.WorkspaceID, ids.AssetID) (any, bool, error) {
	f.mu.Lock()
	f.runs++
	n := f.runs
	f.mu.Unlock()
	return f.outcome(n)
}

func (e *assetsEnv) analyzeJob(t *testing.T, a store.Asset) (state, queue string, attempt int, output string) {
	t.Helper()
	err := e.super.QueryRow(context.Background(),
		`SELECT state::text, queue, attempt, COALESCE((metadata->'output')::text, '') FROM river_job WHERE kind = 'analyze' AND args->>'asset_id' = $1`,
		a.ID.String()).Scan(&state, &queue, &attempt, &output)
	if err != nil {
		t.Fatalf("analyze job of %s: %v", a.ID, err)
	}
	return state, queue, attempt, output
}

// The ingest finishing a video enqueues its analysis in the same transaction, once, on the analysis queue. A worker with no
// analysis (no sandbox) leaves the job waiting without using an attempt.
func TestAnalyzeIsEnqueuedByTheIngestAndWaitsForAWorkerThatCanRunIt(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_an_wait")
	e.startWorker(t, jobs.WorkerOptions{})

	a := e.finishedVideo(t, w, "waiting.mp4")
	state, queue, _, _ := e.analyzeJob(t, a)
	if queue != jobs.QueueAnalysis || (state != "available" && state != "scheduled" && state != "running") {
		t.Fatalf("job state %s on queue %s", state, queue)
	}
	eventually(t, "the analyze job to be snoozed", func() bool { s, _, _, _ := e.analyzeJob(t, a); return s == "scheduled" })
	if _, _, attempt, _ := e.analyzeJob(t, a); attempt > 1 {
		t.Errorf("a snooze must not use attempts, got %d", attempt)
	}

	// An audio file never gets one.
	audio := e.newUploadedAsset(t, w, store.AssetKindAudio, "voice.mp3", []byte("data"))
	if _, err := e.api.ClaimAssetForIngest(context.Background(), w.ID, audio.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.FinishAssetIngest(context.Background(), w.ID, audio.ID, store.IngestResult{DurationMs: 1000}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.super.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'analyze' AND args->>'asset_id' = $1`, audio.ID.String()).Scan(&n); err != nil || n != 0 {
		t.Errorf("audio got %d analyze jobs (%v)", n, err)
	}
}

func TestAnalyzeJobOutcomes(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_an_outcomes")
	an := &fakeAnalysis{}
	e.startWorker(t, jobs.WorkerOptions{Analysis: an})

	t.Run("a result is recorded as the job's output", func(t *testing.T) {
		an.outcome = func(int) (any, bool, error) { return map[string]string{"outcome": "done"}, false, nil }
		a := e.finishedVideo(t, w, "ok.mp4")
		eventually(t, "the job to complete", func() bool { s, _, _, _ := e.analyzeJob(t, a); return s == "completed" })
		if _, _, _, out := e.analyzeJob(t, a); !strings.Contains(out, `"done"`) {
			t.Errorf("job output %s", out)
		}
	})

	t.Run("an asset that is gone cancels the job", func(t *testing.T) {
		an.outcome = func(int) (any, bool, error) { return nil, true, nil }
		a := e.finishedVideo(t, w, "gone.mp4")
		eventually(t, "the job to be cancelled", func() bool { s, _, _, _ := e.analyzeJob(t, a); return s == "cancelled" })
	})

	t.Run("an asset being ingested again waits", func(t *testing.T) {
		an.outcome = func(int) (any, bool, error) { return nil, false, fmt.Errorf("busy: %w", jobs.ErrAnalysisNotReady) }
		a := e.finishedVideo(t, w, "busy.mp4")
		eventually(t, "the job to be snoozed", func() bool { s, _, _, _ := e.analyzeJob(t, a); return s == "scheduled" })
		if _, _, attempt, _ := e.analyzeJob(t, a); attempt > 1 {
			t.Errorf("attempt = %d", attempt)
		}
	})

	t.Run("an error is retried", func(t *testing.T) {
		an.outcome = func(int) (any, bool, error) { return nil, false, errors.New("sandbox down") }
		a := e.finishedVideo(t, w, "retry.mp4")
		eventually(t, "the first attempt to fail", func() bool { s, _, _, _ := e.analyzeJob(t, a); return s == "retryable" })
	})
}
