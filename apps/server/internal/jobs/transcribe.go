package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The transcribe job (P2-05, D-27): the words of an asset's speech, with timings and structure. The pipeline lives in package
// transcribe (it needs the sandbox runner and the provider, which this package does not import); the job, its queue and its
// options are here with the others.

// QueueTranscribe holds the transcriptions: minutes of CPU each with the local provider, so one at a time by default, and a queue
// of their own so they never delay an ingest or a render.
const QueueTranscribe = "transcribe"

// DefaultTranscribeConcurrency is how many transcriptions one worker runs at once unless TRANSCRIBE_CONCURRENCY says otherwise.
const DefaultTranscribeConcurrency = 1

// TranscribeTimeout is how long one attempt may run. The tools' own limits scale with the clip (package whispercpp: four times real
// time plus three minutes, so 2 h 3 min for the longest clip of 30 minutes) and the speech check and the transfers come on top.
const TranscribeTimeout = 3 * time.Hour

// TranscribeMaxAge is how long the job may wait, snoozed, for a worker that can run it (the provider is switched off, or the worker
// has no storage or sandbox): after this long the transcript is marked failed.
const TranscribeMaxAge = 24 * time.Hour

const noTranscribeSnooze = 5 * time.Minute

// TranscribeArgs is enqueued, in the transaction that marks an asset with audio ready, for that asset.
type TranscribeArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
	AssetID     ids.AssetID     `json:"asset_id"`
	// Generation is the ingest the job was enqueued for (assets.ingested_at in microseconds). It is part of the job's unique key, so
	// an asset that is ingested again gets a new transcription instead of being deduplicated against the old job (River keeps
	// finished jobs unique for a day), and a job of an old ingest that runs late does nothing (transcribe.Pipeline.Run).
	Generation int64 `json:"generation"`
	// Force is a nonce `cfctl transcribe run --force` sets to enqueue a job that is not deduplicated against the finished one of
	// the same ingest. Zero for every job the ingest enqueues.
	Force int64 `json:"force,omitempty"`
}

// Kind implements river.JobArgs.
func (TranscribeArgs) Kind() string { return "transcribe" }

// InsertOpts puts the job on its own queue and makes it unique by its arguments, which include the ingest generation: a double
// enqueue for one ingest runs once (and meters once), and a re-ingest is never deduplicated away. `cfctl transcribe run`
// transcribes on demand.
func (TranscribeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueTranscribe, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// EnqueueTranscribe is the store's transcribe hook (Store.SetTranscribeHook): it inserts the job in the transaction that marks the
// asset ready, and says whether it inserted one (false: a job for this asset exists already).
func (e *Enqueuer) EnqueueTranscribe(ctx context.Context, tx *store.Store, a store.Asset) (bool, error) {
	res, err := e.InsertInStore(ctx, tx, TranscribeArgs{WorkspaceID: a.WorkspaceID, AssetID: a.ID, Generation: generationOf(a)}, nil)
	if err != nil {
		return false, fmt.Errorf("enqueue transcribe: %w", err)
	}
	return !res.UniqueSkippedAsDuplicate, nil
}

// Errors a Transcriber returns for the worker to act on.
var (
	// ErrTranscribeGone: the asset is deleted or not in the workspace. The job is cancelled.
	ErrTranscribeGone = errors.New("jobs: the asset to transcribe is gone")
	// ErrTranscribeNotConfigured: the provider is switched off (Workers AI without an account, say). The job waits.
	ErrTranscribeNotConfigured = errors.New("jobs: the transcription provider is not configured")
)

// Transcriber is the transcription pipeline (package transcribe) as the worker sees it.
type Transcriber interface {
	// Run transcribes the asset for the ingest generation and returns what the job records. A run for an ingest that is no longer the
	// asset's does nothing and says so in its output. ErrTranscribeGone and ErrTranscribeNotConfigured are the errors the worker
	// acts on; any other error is retried.
	Run(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, generation int64) (output any, err error)
	// Fail marks the transcript failed, for a job that has used its last attempt (only if the asset is still at that generation).
	Fail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, generation int64) error
}

// TranscribeWorker works TranscribeArgs. It is always registered, so the ingest can enqueue the job on a worker that cannot run it:
// such a worker snoozes the job, and one that can picks it up.
type TranscribeWorker struct {
	river.WorkerDefaults[TranscribeArgs]
	deps Deps
}

// Timeout implements river.Worker.
func (w *TranscribeWorker) Timeout(*river.Job[TranscribeArgs]) time.Duration {
	return TranscribeTimeout
}

// Work runs one attempt. A gone asset ends the job; a worker that cannot transcribe snoozes it (no attempt is used) for at most
// TranscribeMaxAge; any other error is retried, and the last attempt marks the transcript failed.
func (w *TranscribeWorker) Work(ctx context.Context, job *river.Job[TranscribeArgs]) error {
	ws, id := job.Args.WorkspaceID, job.Args.AssetID
	if w.deps.Transcribe == nil {
		return w.snooze(ctx, job, "this worker cannot transcribe (no object storage or sandbox)")
	}
	out, err := w.deps.Transcribe.Run(ctx, ws, id, job.Args.Generation)
	switch {
	case errors.Is(err, ErrTranscribeGone):
		return river.JobCancel(fmt.Errorf("asset %s is gone from workspace %s", id, ws))
	case errors.Is(err, ErrTranscribeNotConfigured):
		return w.snooze(ctx, job, err.Error())
	case err != nil:
		w.deps.logger().WarnContext(ctx, "transcribe attempt failed", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "attempt", job.Attempt, "error", err)
		if job.Attempt >= job.MaxAttempts {
			w.giveUp(ctx, ws, id, job.Args.Generation)
		}
		return err
	}
	return river.RecordOutput(ctx, out)
}

func (w *TranscribeWorker) snooze(ctx context.Context, job *river.Job[TranscribeArgs], why string) error {
	ws, id := job.Args.WorkspaceID, job.Args.AssetID
	age := w.deps.now().Sub(job.CreatedAt)
	if age >= TranscribeMaxAge {
		w.deps.logger().ErrorContext(ctx, "transcribe gives up after waiting", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "age", age.Round(time.Minute), "detail", why)
		w.giveUp(ctx, ws, id, job.Args.Generation)
		return river.JobCancel(fmt.Errorf("transcribe gave up after %s: %s", age.Round(time.Minute), why))
	}
	w.deps.logger().WarnContext(ctx, "transcribe waits", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "retry_in", noTranscribeSnooze, "age", age.Round(time.Minute), "detail", why)
	return river.JobSnooze(noTranscribeSnooze)
}

// giveUp marks the transcript failed with a fresh context (the job's may be what ended).
func (w *TranscribeWorker) giveUp(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, generation int64) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if w.deps.Transcribe != nil {
		if err := w.deps.Transcribe.Fail(fctx, ws, id, generation); err != nil {
			w.deps.logger().ErrorContext(ctx, "could not mark a transcript whose job gave up as failed", "asset_id", id.String(), "error", err)
		}
		return
	}
	if err := w.deps.Store.FailTranscript(fctx, ws, id, store.TranscriptGaveUp, store.GenerationTime(generation)); err != nil && !errors.Is(err, store.ErrNotFound) {
		w.deps.logger().ErrorContext(ctx, "could not mark a transcript whose job gave up as failed", "asset_id", id.String(), "error", err)
	}
}
