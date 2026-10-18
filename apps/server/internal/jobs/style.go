package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The style jobs (P6-05b, docs/design/style-profile-v1.md section 11). A style reference that finishes its ingest gets a
// style_reference job: its picture and sound are measured in the sandbox and its title is read with one small model call. Every
// reference that finishes (or is removed) enqueues a style_profile job: the references' measures are aggregated into the workspace's
// profile and one cheap model call writes the summary and the suggested settings. The workers live in internal/creatorstyle (they need
// the sandbox and the model client); the arguments, queues and options are here with the others.

// StyleReferenceTimeout is how long one attempt of a reference may take: the download of the original (at most 1 GiB), two passes of
// ffmpeg over at most 3 minutes of video and one model call.
const StyleReferenceTimeout = 20 * time.Minute

// StyleProfileTimeout is how long the profile job may take: reading the measures, a few posters and one model call.
const StyleProfileTimeout = 5 * time.Minute

// StyleReferenceArgs is enqueued, in the transaction that marks a style reference ready, for that reference; and for every reference
// when the creator asks to learn the style again.
type StyleReferenceArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
	AssetID     ids.AssetID     `json:"asset_id"`
	// Pass tells the measuring after the ingest (0) from a later "learn again" (its time in microseconds), so a new pass is never
	// deduplicated against an old one.
	Pass int64 `json:"pass"`
}

// Kind implements river.JobArgs.
func (StyleReferenceArgs) Kind() string { return "style_reference" }

// InsertOpts puts the job on the analysis queue (a sandbox pass, like the clip analysis) and makes it unique by its arguments.
func (StyleReferenceArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueAnalysis, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// StyleProfileWindow is how long the profile job waits after the first reference that asks for it, so that a creator who uploads five
// videos at once gets one summary call, not five.
var StyleProfileWindow = 15 * time.Second // a variable so that tests can shorten it

// StyleProfileArgs is enqueued when a reference was measured or removed, or the style is learnt again.
type StyleProfileArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
	// Window is the time slot the job runs at (Unix seconds over StyleProfileWindow). Requests in one slot are one job; a request made
	// while a job runs falls in a later slot and is a job of its own, which finds nothing to do when the first one saw it already.
	Window int64 `json:"window"`
}

// Kind implements river.JobArgs.
func (StyleProfileArgs) Kind() string { return "style_profile" }

// InsertOpts puts the job on the quick look's queue (seconds of work, mostly a model call) and makes it unique by its arguments.
func (StyleProfileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueQuickLook, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// TxInserter inserts a job in the transaction behind tx. *Enqueuer is the real one.
type TxInserter interface {
	InsertInStore(ctx context.Context, tx *store.Store, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// ContextInserter inserts with the River client of the job that is running (river.ClientFromContext), for a worker that enqueues jobs
// of its own; outside a job it fails.
type ContextInserter struct{ Logger *slog.Logger }

// InsertInStore implements TxInserter.
func (c ContextInserter) InsertInStore(ctx context.Context, tx *store.Store, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return nil, fmt.Errorf("jobs: no River client in the context: %w", err)
	}
	return NewEnqueuer(client, c.Logger).InsertInStore(ctx, tx, args, opts)
}

// EnqueueStyleProfile inserts the profile job of the workspace in the transaction behind tx, for the next window after now.
func EnqueueStyleProfile(ctx context.Context, enq TxInserter, tx *store.Store, ws ids.WorkspaceID, now time.Time) error {
	step := max(int64(StyleProfileWindow/time.Second), 1)
	window := now.Unix()/step + 1 // the next slot: the job waits at most one window
	at := time.Unix(window*step, 0)
	opts := StyleProfileArgs{}.InsertOpts()
	opts.ScheduledAt = at
	if _, err := enq.InsertInStore(ctx, tx, StyleProfileArgs{WorkspaceID: ws, Window: window}, &opts); err != nil {
		return fmt.Errorf("enqueue style_profile: %w", err)
	}
	return nil
}

// EnqueueStyleReference is the store's style reference hook (Store.SetStyleReferenceHook): it inserts the measuring of a reference in
// the transaction that marks it ready.
func (e *Enqueuer) EnqueueStyleReference(ctx context.Context, tx *store.Store, a store.Asset) error {
	if _, err := e.InsertInStore(ctx, tx, StyleReferenceArgs{WorkspaceID: a.WorkspaceID, AssetID: a.ID}, nil); err != nil {
		return fmt.Errorf("enqueue style_reference: %w", err)
	}
	return nil
}

// ErrStyleNotReady: the reference is still being ingested. The job looks again in a minute.
var ErrStyleNotReady = errors.New("jobs: the style reference is not ready")

// StyleLearner is the style learning (package creatorstyle) as the worker sees it.
type StyleLearner interface {
	// AnalyzeReference measures one reference. lastAttempt says a failure now is final (the reference is marked failed). gone says the
	// reference is not a ready style reference of the workspace any more (the job is cancelled). ErrStyleNotReady and
	// ErrModelUnavailable snooze the job; other errors are retried.
	AnalyzeReference(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, lastAttempt bool) (output any, gone bool, err error)
	// LearnProfile aggregates the workspace's measured references into its profile and writes the summary. lastAttempt says a failure of
	// the summary call now is final (the profile is then stored without one).
	LearnProfile(ctx context.Context, ws ids.WorkspaceID, lastAttempt bool) (output any, err error)
}

// noStyleSnooze is how long a style job waits when the worker cannot run it (no object storage, sandbox or model access).
const noStyleSnooze = 5 * time.Minute

// StyleMaxAge is how long a style job may wait, snoozed, for a worker that can run it: after this long it is cancelled, and the creator
// sees the reference as still being learnt until they learn the style again.
const StyleMaxAge = 24 * time.Hour

// StyleReferenceWorker works StyleReferenceArgs. It is always registered, so the ingest can enqueue the job on a worker that cannot run
// it: such a worker snoozes the job, and one that can picks it up.
type StyleReferenceWorker struct {
	river.WorkerDefaults[StyleReferenceArgs]
	deps Deps
}

// Timeout implements river.Worker.
func (w *StyleReferenceWorker) Timeout(*river.Job[StyleReferenceArgs]) time.Duration {
	return StyleReferenceTimeout
}

// Work runs one attempt.
func (w *StyleReferenceWorker) Work(ctx context.Context, job *river.Job[StyleReferenceArgs]) error {
	ws, id := job.Args.WorkspaceID, job.Args.AssetID
	if w.deps.Style == nil {
		return styleSnooze(ctx, w.deps, job.CreatedAt, noStyleSnooze, "this worker cannot learn styles (no object storage, sandbox or model access)")
	}
	began := w.deps.now()
	out, gone, err := w.deps.Style.AnalyzeReference(ctx, ws, id, job.Attempt >= job.MaxAttempts)
	w.deps.meterMedia(ctx, ws, id, "style_reference", job.ID, job.Attempt, began)
	switch {
	case errors.Is(err, ErrStyleNotReady):
		return styleSnooze(ctx, w.deps, job.CreatedAt, notReadySnooze, err.Error())
	case errors.Is(err, ErrModelUnavailable):
		return styleSnooze(ctx, w.deps, job.CreatedAt, noStyleSnooze, err.Error())
	case err != nil:
		w.deps.logger().WarnContext(ctx, "style reference attempt failed", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "attempt", job.Attempt, "error", err)
		return err
	case gone:
		return river.JobCancel(fmt.Errorf("asset %s is not a style reference of workspace %s any more", id, ws))
	}
	return river.RecordOutput(ctx, out)
}

// StyleProfileWorker works StyleProfileArgs.
type StyleProfileWorker struct {
	river.WorkerDefaults[StyleProfileArgs]
	deps Deps
}

// Timeout implements river.Worker.
func (w *StyleProfileWorker) Timeout(*river.Job[StyleProfileArgs]) time.Duration {
	return StyleProfileTimeout
}

// Work runs one attempt.
func (w *StyleProfileWorker) Work(ctx context.Context, job *river.Job[StyleProfileArgs]) error {
	ws := job.Args.WorkspaceID
	if w.deps.Style == nil {
		return styleSnooze(ctx, w.deps, job.CreatedAt, noStyleSnooze, "this worker cannot learn styles (no object storage, sandbox or model access)")
	}
	out, err := w.deps.Style.LearnProfile(ctx, ws, job.Attempt >= job.MaxAttempts)
	switch {
	case errors.Is(err, ErrModelUnavailable):
		return styleSnooze(ctx, w.deps, job.CreatedAt, noStyleSnooze, err.Error())
	case err != nil:
		w.deps.logger().WarnContext(ctx, "style profile attempt failed", "workspace_id", ws.String(), "job_id", job.ID, "attempt", job.Attempt, "error", err)
		return err
	}
	return river.RecordOutput(ctx, out)
}

func styleSnooze(ctx context.Context, d Deps, created time.Time, wait time.Duration, why string) error {
	age := d.now().Sub(created)
	if age >= StyleMaxAge {
		d.logger().ErrorContext(ctx, "style job gives up after waiting", "age", age.Round(time.Minute), "detail", why)
		return river.JobCancel(fmt.Errorf("style job gave up after %s: %s", age.Round(time.Minute), why))
	}
	d.logger().WarnContext(ctx, "style job waits", "retry_in", wait, "age", age.Round(time.Minute), "detail", why)
	return river.JobSnooze(wait)
}
