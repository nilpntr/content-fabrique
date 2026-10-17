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

// InsertOpts makes the job unique by asset, so a repeated completion enqueues one ingest.
func (AssetIngestArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// AssetIngestWorker works AssetIngestArgs. Until P1-04 implements the media pipeline it only logs and records that the
// asset is waiting: the asset stays `uploaded`, and P1-04 replaces this worker (same kind) to move it on.
type AssetIngestWorker struct {
	river.WorkerDefaults[AssetIngestArgs]
	deps Deps
}

// Work logs the job and records it as pending.
func (w *AssetIngestWorker) Work(ctx context.Context, job *river.Job[AssetIngestArgs]) error {
	w.deps.logger().InfoContext(ctx, "asset ingest is pending (the media pipeline arrives with P1-04)",
		"workspace_id", job.Args.WorkspaceID.String(), "asset_id", job.Args.AssetID.String(), "job_id", job.ID)
	return river.RecordOutput(ctx, map[string]string{"state": "pending", "reason": "ingest is implemented by P1-04"})
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
