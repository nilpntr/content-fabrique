package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The quick look job (P1-04b). The worker itself lives in internal/quicklook (it needs the model client, which this package
// does not import) and is added with Deps.Register; its arguments, queue and options are here with the others.

// QueueQuickLook holds the quick looks: seconds of work each, mostly a model call, on a queue of their own so a long ingest
// never delays the label of a clip that is ready, and a burst of uploads never floods the model API.
const QueueQuickLook = "quicklook"

// DefaultQuickLookConcurrency is how many quick looks one worker runs at once.
const DefaultQuickLookConcurrency = 2

// QuickLookTimeout is how long one attempt may take: downloading the proxy, cutting the frames and one model call. It is
// minutes, not hours; a clip that needs longer is retried.
const QuickLookTimeout = 5 * time.Minute

// QuickLookArgs is enqueued, in the transaction that marks a video ready, for that asset.
type QuickLookArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
	AssetID     ids.AssetID     `json:"asset_id"`
}

// Kind implements river.JobArgs.
func (QuickLookArgs) Kind() string { return "quick_look" }

// InsertOpts puts the job on its own queue and makes it unique by asset, whatever its state (River's default states include
// completed jobs, until River removes them), so an asset that is ingested again does not run its quick look again: a person
// may have edited what it wrote. `cfctl assets quicklook` runs one on demand.
func (QuickLookArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueQuickLook, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// EnqueueQuickLook is the store's quick-look hook (Store.SetQuickLookHook): it inserts the job in the transaction that marks
// the video ready, so the job exists exactly when the ready asset does.
func (e *Enqueuer) EnqueueQuickLook(ctx context.Context, tx *store.Store, a store.Asset) error {
	if _, err := e.InsertInStore(ctx, tx, QuickLookArgs{WorkspaceID: a.WorkspaceID, AssetID: a.ID}, nil); err != nil {
		return fmt.Errorf("enqueue quick_look: %w", err)
	}
	return nil
}

// Errors a QuickLooker returns for the worker to act on.
var (
	// ErrModelUnavailable: the worker has no model access (no key, a bad setting). The job waits, snoozed, instead of failing.
	ErrModelUnavailable = errors.New("jobs: model access is not configured")
	// ErrQuickLookNotReady: the asset is being ingested again. The job looks again in a minute.
	ErrQuickLookNotReady = errors.New("jobs: the asset is not ready")
)

// The codes a quick look is marked failed with when the job gives up without the quick look having failed itself
// (assets.quick_look, "error"). The quick look's own codes are in package quicklook.
const (
	// QuickLookFailInternal: the last attempt failed with an error.
	QuickLookFailInternal = "internal"
	// QuickLookFailModelUnavailable: for QuickLookMaxAge no worker had model access, object storage or a sandbox for it.
	QuickLookFailModelUnavailable = "model_unavailable"
	// QuickLookFailAssetNotReady: for QuickLookMaxAge the asset was not ready (it kept being ingested again).
	QuickLookFailAssetNotReady = "asset_not_ready"
)

// QuickLookMaxAge is how long a quick look job may wait, snoozed, for a worker that can run it or an asset that is ready:
// after this long since the job was enqueued it gives up and the quick look is marked failed with a clear code, so an asset
// does not stay `pending` for ever. `cfctl assets quicklook` runs it again once the cause is fixed.
const QuickLookMaxAge = 24 * time.Hour

// QuickLooker is the quick look (package quicklook) as the worker sees it.
type QuickLooker interface {
	// RunJob does the quick look. gone says the asset is not a ready video of the workspace any more (the job is cancelled);
	// output is what the job records. ErrModelUnavailable and ErrQuickLookNotReady snooze the job; other errors are retried.
	RunJob(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (output any, gone bool, err error)
}

// noQuickLookSnooze is how long a quick look waits when the worker cannot run it (no object storage, sandbox or model access).
const noQuickLookSnooze = 5 * time.Minute

// notReadySnooze is how long a quick look waits for an asset that is being ingested again.
const notReadySnooze = time.Minute

// QuickLookWorker works QuickLookArgs. It is always registered, so the ingest can enqueue the job on a worker that cannot
// run it (River refuses to insert a kind its bundle does not know): such a worker snoozes the job, and one that can picks it up.
type QuickLookWorker struct {
	river.WorkerDefaults[QuickLookArgs]
	deps Deps
}

// Timeout implements river.Worker.
func (w *QuickLookWorker) Timeout(*river.Job[QuickLookArgs]) time.Duration { return QuickLookTimeout }

// Work runs one attempt. An asset that is gone ends the job; a worker without model access or an asset that is being ingested
// again snoozes it (no attempt is used) for at most QuickLookMaxAge, then the quick look is marked failed; any other error is
// retried by River, and the last attempt marks the quick look failed too, so the asset does not stay `pending` for ever.
func (w *QuickLookWorker) Work(ctx context.Context, job *river.Job[QuickLookArgs]) error {
	ws, id := job.Args.WorkspaceID, job.Args.AssetID
	if w.deps.QuickLook == nil {
		return w.snooze(ctx, job, QuickLookFailModelUnavailable, noQuickLookSnooze, "this worker cannot run quick looks (no object storage, sandbox or model access)")
	}
	out, gone, err := w.deps.QuickLook.RunJob(ctx, ws, id)
	switch {
	case errors.Is(err, ErrModelUnavailable):
		return w.snooze(ctx, job, QuickLookFailModelUnavailable, noQuickLookSnooze, err.Error())
	case errors.Is(err, ErrQuickLookNotReady):
		return w.snooze(ctx, job, QuickLookFailAssetNotReady, notReadySnooze, err.Error())
	case err != nil:
		w.deps.logger().WarnContext(ctx, "quick look attempt failed", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "attempt", job.Attempt, "error", err)
		if job.Attempt >= job.MaxAttempts {
			w.giveUp(ctx, ws, id, QuickLookFailInternal)
		}
		return err
	case gone:
		return river.JobCancel(fmt.Errorf("asset %s is not a ready video of workspace %s", id, ws))
	}
	return river.RecordOutput(ctx, out)
}

// snooze puts the job back for later and says so in the log, unless it has waited QuickLookMaxAge: then the quick look is
// marked failed with reason and the job is cancelled.
func (w *QuickLookWorker) snooze(ctx context.Context, job *river.Job[QuickLookArgs], reason string, d time.Duration, why string) error {
	ws, id := job.Args.WorkspaceID, job.Args.AssetID
	age := w.deps.now().Sub(job.CreatedAt)
	if age >= QuickLookMaxAge {
		w.deps.logger().ErrorContext(ctx, "quick look gives up after waiting", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "reason", reason, "age", age.Round(time.Minute), "detail", why)
		w.giveUp(ctx, ws, id, reason)
		return river.JobCancel(fmt.Errorf("quick look gave up after %s: %s", age.Round(time.Minute), reason))
	}
	w.deps.logger().WarnContext(ctx, "quick look waits", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "reason", reason, "retry_in", d, "age", age.Round(time.Minute), "detail", why)
	return river.JobSnooze(d)
}

// giveUp marks the quick look failed with a code, with a fresh context for the write: it may be the job's own that ended.
func (w *QuickLookWorker) giveUp(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	detail, _ := json.Marshal(map[string]string{"error": reason})
	if _, err := w.deps.Store.FailQuickLook(fctx, ws, id, detail); err != nil && !errors.Is(err, store.ErrNotFound) {
		w.deps.logger().ErrorContext(ctx, "could not mark a quick look that gave up as failed", "asset_id", id.String(), "reason", reason, "error", err)
	}
}
