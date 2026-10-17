package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/ingest"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The jobs of the assets API (P1-01): the ingest job a completed upload enqueues, the deletion of a deleted asset's
// objects, and the periodic clean-up of uploads that were never finished.

// ObjectStore is what the workers need from object storage. *storage.Client is the real one. nil in Deps means storage is
// not configured: the jobs that need it then snooze (see noObjectStore) instead of failing or pretending.
type ObjectStore interface {
	// Abort discards a multipart upload; one that does not exist any more is not an error.
	Abort(ctx context.Context, key, uploadID string) error
	// Head reports an object, or storage.ErrNotFound.
	Head(ctx context.Context, key string) (storage.ObjectInfo, error)
	// DeletePrefix deletes every object of one asset (ws/{workspace}/assets/{asset}/) and returns how many.
	DeletePrefix(ctx context.Context, prefix string) (int, error)
	// DeleteRenderObjects deletes every object of one render (ws/{workspace}/projects/{project}/renders/{render}/).
	DeleteRenderObjects(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, render ids.RenderID) (int, error)
}

// noObjectStoreSnooze is how long a job waits when the worker has no object storage settings.
const noObjectStoreSnooze = 5 * time.Minute

// noObjectStore is what the jobs that need object storage return when the worker was started without the R2_* settings.
// They snooze: River puts the job back for later without counting an attempt, so a job that removes objects is never
// discarded for the lack of a setting, and it runs as soon as a worker that has the settings picks it up. The worker
// logs a warning at start (cmd/worker).
func noObjectStore() error { return river.JobSnooze(noObjectStoreSnooze) }

// AssetIngestArgs is enqueued, in the transaction that marks an upload completed, for the asset that is now `uploaded`.
type AssetIngestArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
	AssetID     ids.AssetID     `json:"asset_id"`
}

// Kind implements river.JobArgs.
func (AssetIngestArgs) Kind() string { return "asset_ingest" }

// InsertOpts makes the job unique by asset, so a repeated completion enqueues one ingest, and puts it on the media queue,
// which has its own (small) concurrency: an ingest is minutes of CPU, and must not delay the short jobs.
func (AssetIngestArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMedia, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// AssetIngester is the media pipeline (package ingest) as the worker sees it.
type AssetIngester interface {
	// Run ingests the asset. ingest.ErrAssetGone means there is nothing to do; other errors are retried.
	Run(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (ingest.Result, error)
	// Fail marks the asset failed with a reason code, for a job that has used its last attempt.
	Fail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string) error
}

// IngestTimeout is how long one attempt of an ingest may run: River's per-job timeout for this worker (its default is a
// minute). Package ingest derives it from the longest clip it accepts and checks that the tool timeouts fit.
const IngestTimeout = ingest.JobTimeout

// JobRescueAfter is how long River lets a job run before it assumes the worker died and hands the job to another one
// (Config.RescueStuckJobsAfter). It is longer than IngestTimeout by a margin, so a running ingest is never rescued: its
// own timeout ends it first. (River's default of an hour is shorter than an ingest may run.)
const JobRescueAfter = IngestTimeout + 30*time.Minute

// noIngestSnooze is how long an ingest waits when the worker has no media pipeline (no object storage or no sandbox set up).
const noIngestSnooze = 5 * time.Minute

// AssetIngestWorker works AssetIngestArgs: it runs the pipeline of package ingest, which downloads the original, probes and
// validates it in the sandbox, makes the derivatives (proxy, poster, sprite, waveform, 16 kHz audio, hover preview),
// uploads them and moves the asset to `ready`, or to `failed` with a reason when the file cannot be processed.
type AssetIngestWorker struct {
	river.WorkerDefaults[AssetIngestArgs]
	deps Deps
}

// Timeout implements river.Worker.
func (w *AssetIngestWorker) Timeout(*river.Job[AssetIngestArgs]) time.Duration { return IngestTimeout }

// Work runs one attempt. A worker without the pipeline snoozes the job (no attempt is used), like the jobs that need
// object storage do, so nothing is lost for a missing setting. An asset that was deleted, failed or never existed in the
// workspace of the job ends it (cancelled, not retried). Any other error is retried by River; the last attempt marks the
// asset failed, so it does not stay `processing` for ever.
func (w *AssetIngestWorker) Work(ctx context.Context, job *river.Job[AssetIngestArgs]) error {
	if w.deps.Ingest == nil {
		return river.JobSnooze(noIngestSnooze)
	}
	ws, id := job.Args.WorkspaceID, job.Args.AssetID
	began := w.deps.now()
	res, err := w.deps.Ingest.Run(ctx, ws, id)
	w.deps.meterMedia(ctx, ws, id, "ingest", job.ID, job.Attempt, began)
	switch {
	case errors.Is(err, ingest.ErrAssetGone):
		w.deps.logger().InfoContext(ctx, "asset ingest skipped", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "reason", err.Error())
		return river.JobCancel(err)
	case err != nil:
		w.deps.logger().WarnContext(ctx, "asset ingest attempt failed", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "attempt", job.Attempt, "error", err)
		if job.Attempt >= job.MaxAttempts {
			// The last attempt, whatever ended it (even a cancelled or timed-out context: River will not run the job again),
			// fails the asset, with a fresh context for the write.
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if ferr := w.deps.Ingest.Fail(fctx, ws, id, giveUpReason(ctx, err)); ferr != nil {
				w.deps.logger().ErrorContext(ctx, "could not mark an asset whose ingest gave up as failed", "asset_id", id.String(), "error", ferr)
			}
		}
		return err
	}
	return river.RecordOutput(ctx, res)
}

// giveUpReason is the failure reason of an asset whose last ingest attempt failed with err: a timeout (the job's or a
// tool's), an interruption (the worker was stopped), or processing_failed for anything else.
func giveUpReason(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded), errors.Is(err, sandbox.ErrTimeout):
		return store.FailureTimeout
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return store.FailureInterrupted
	}
	return store.FailureProcessing
}

// AssetObjectDeleteArgs is enqueued, in the transaction that deletes an asset (or fails its upload while an object may
// exist), to remove the asset's objects: the original and, later, every derivative under the asset's prefix.
type AssetObjectDeleteArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
	AssetID     ids.AssetID     `json:"asset_id"`
}

// Kind implements river.JobArgs.
func (AssetObjectDeleteArgs) Kind() string { return "asset_object_delete" }

// InsertOpts makes the job unique by asset while one is waiting or running. A finished job does not block a new one:
// an object can appear after the first deletion (a completion that raced with an abort), and the next job removes it.
func (AssetObjectDeleteArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueMaintenance,
		MaxAttempts: 8,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			},
		},
	}
}

// AssetObjectDeleteWorker works AssetObjectDeleteArgs.
type AssetObjectDeleteWorker struct {
	river.WorkerDefaults[AssetObjectDeleteArgs]
	deps Deps
}

// Work deletes the objects under the asset's prefix. The prefix is built from the ids of the row, and the row must be
// soft-deleted or failed already: a job (however it came to exist) cannot remove the objects of a live asset, nor of
// another workspace, because the row is looked up with the workspace of the job and the prefix comes from the row.
func (w *AssetObjectDeleteWorker) Work(ctx context.Context, job *river.Job[AssetObjectDeleteArgs]) error {
	if w.deps.Objects == nil {
		return noObjectStore()
	}
	a, err := w.deps.Store.GetAssetForObjectDeletion(ctx, job.Args.WorkspaceID, job.Args.AssetID)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(fmt.Errorf("asset %s is neither deleted nor failed in workspace %s", job.Args.AssetID, job.Args.WorkspaceID))
	}
	if err != nil {
		return fmt.Errorf("get deleted asset: %w", err)
	}
	n, err := w.deps.Objects.DeletePrefix(ctx, storage.AssetPrefix(a.WorkspaceID, a.ID))
	if err != nil {
		return fmt.Errorf("delete objects: %w", err)
	}
	w.deps.logger().InfoContext(ctx, "deleted asset objects", "workspace_id", a.WorkspaceID.String(), "asset_id", a.ID.String(), "objects", n)
	return river.RecordOutput(ctx, map[string]int{"deleted": n})
}

// UploadsExpireInterval is how often the clean-up of expired uploads runs.
const UploadsExpireInterval = 15 * time.Minute

const (
	uploadsExpireID = "uploads_expire"
	// expirePage is how many expired uploads one query returns.
	expirePage = 100
)

// UploadsExpireArgs is the periodic job that aborts uploads past their expires_at (24 hours after they started) and marks
// their assets failed.
type UploadsExpireArgs struct{}

// Kind implements river.JobArgs.
func (UploadsExpireArgs) Kind() string { return "uploads_expire" }

// InsertOpts runs it on the maintenance queue, once per period however it was enqueued.
func (UploadsExpireArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaintenance, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: UploadsExpireInterval / 2}}
}

// UploadsExpireWorker works UploadsExpireArgs.
type UploadsExpireWorker struct {
	river.WorkerDefaults[UploadsExpireArgs]
	deps Deps
}

// UploadsExpireOutput is what a finished run records.
type UploadsExpireOutput struct {
	Aborted int `json:"aborted"`
	Skipped int `json:"skipped"` // completed or aborted by someone else in the meantime
	Failed  int `json:"failed"`  // object storage refused; the next run tries again
	Orphans int `json:"orphans"` // an object was there although the upload never completed in the database; its deletion is queued
}

// Work aborts every active upload past its expiry: first the multipart upload in object storage (idempotent), then the
// rows, each guarded so an upload that was completed or aborted in the meantime is left alone. Safe to run twice and
// from several workers.
//
// A multipart upload that no longer exists in storage may have been completed there while the database never heard of it
// (a request died between the two). So before the rows change, the job looks for the final object; when there is one and
// the guarded update then wins, the object is an orphan and its deletion is queued in the same transaction (the guard
// is what makes that safe: a completion that is still in flight loses the guard, and then removes the object itself).
func (w *UploadsExpireWorker) Work(ctx context.Context, _ *river.Job[UploadsExpireArgs]) error {
	if w.deps.Objects == nil {
		return noObjectStore()
	}
	var (
		out   UploadsExpireOutput
		after ids.UploadID
	)
	now := w.deps.now()
	for {
		rows, err := w.deps.Store.ListExpiredUploads(ctx, now, after, expirePage)
		if err != nil {
			return fmt.Errorf("list expired uploads: %w", err)
		}
		for _, r := range rows {
			after = r.UploadID
			if err := w.deps.Objects.Abort(ctx, r.R2Key, r.R2UploadID); err != nil {
				out.Failed++
				w.deps.logger().WarnContext(ctx, "could not abort an expired upload", "workspace_id", r.WorkspaceID.String(), "asset_id", r.AssetID.String(), "error", err)
				continue
			}
			orphan := false
			if _, err := w.deps.Objects.Head(ctx, r.R2Key); err == nil {
				orphan = true
			} else if !errors.Is(err, storage.ErrNotFound) {
				out.Failed++
				w.deps.logger().WarnContext(ctx, "could not look for the object of an expired upload", "workspace_id", r.WorkspaceID.String(), "asset_id", r.AssetID.String(), "error", err)
				continue
			}
			err := w.deps.Store.InTx(ctx, func(tx *store.Store) error {
				if _, err := tx.AbortUpload(ctx, r.WorkspaceID, r.AssetID, store.FailureUploadExpired, false); err != nil {
					return err
				}
				if !orphan {
					return nil
				}
				pgxTx, err := tx.Tx()
				if err != nil {
					return err
				}
				_, err = river.ClientFromContext[pgx.Tx](ctx).InsertTx(ctx, pgxTx, AssetObjectDeleteArgs{WorkspaceID: r.WorkspaceID, AssetID: r.AssetID}, nil)
				return err
			})
			switch {
			case errors.Is(err, store.ErrNotActive):
				out.Skipped++
			case err != nil:
				out.Failed++
				w.deps.logger().WarnContext(ctx, "could not mark an expired upload", "workspace_id", r.WorkspaceID.String(), "asset_id", r.AssetID.String(), "error", err)
			default:
				out.Aborted++
				if orphan {
					out.Orphans++
				}
			}
		}
		if len(rows) < expirePage {
			break
		}
	}
	w.deps.logger().InfoContext(ctx, "expired uploads cleaned up", "aborted", out.Aborted, "skipped", out.Skipped, "failed", out.Failed)
	if err := river.RecordOutput(ctx, out); err != nil {
		return err
	}
	if out.Failed > 0 {
		return fmt.Errorf("%d expired uploads could not be cleaned up", out.Failed)
	}
	return nil
}
