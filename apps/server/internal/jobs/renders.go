package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/ids"
)

// The jobs of the renders (P3-06): the render job a request enqueues, and the periodic clean-up of the objects of renders
// whose project was deleted.

// ErrRenderGone is returned by a Renderer when there is nothing left to render: the render does not exist in the workspace,
// finished or failed already, or its project was deleted. Retrying cannot help.
var ErrRenderGone = errors.New("render: the render is finished, failed, deleted or not in this workspace")

// RenderTimeout is how long one attempt of a render job may run: River's per-job timeout for this worker. Package renderjobs
// checks at start-up that its worst case (the transfers, the two sandbox runs of a final render and the contact sheet, each at
// its own limit) fits in it. It is shorter than JobRescueAfter by a margin, so River never hands a running render to a second
// worker.
const RenderTimeout = 3 * time.Hour

// RenderArgs is enqueued, in the transaction that creates a render, for the render that is now queued.
type RenderArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
	RenderID    ids.RenderID    `json:"render_id"`
}

// Kind implements river.JobArgs.
func (RenderArgs) Kind() string { return "render" }

// InsertOpts makes the job unique by render and puts it on the media queue, with the ingests: a render is minutes of CPU in a
// sandbox, so few at a time, and it must not delay the short jobs.
func (RenderArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMedia, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// RenderOutcome is what a render job that ended normally records as its River output.
type RenderOutcome struct {
	// Status is "ready", or "failed" when the timeline could not be rendered (see Reason).
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	WallMs int64  `json:"wall_ms,omitempty"`
}

// Renderer is the render pipeline (package renderjobs) as the worker sees it.
type Renderer interface {
	// Run renders. ErrRenderGone means there is nothing to do; other errors are retried.
	Run(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID) (RenderOutcome, error)
	// Fail marks the render failed, for a job that has used its last attempt; cause is the error that ended it.
	Fail(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID, cause error) error
}

// noRenderSnooze is how long a render waits when the worker has no render pipeline.
const noRenderSnooze = 5 * time.Minute

// RenderWorker works RenderArgs.
type RenderWorker struct {
	river.WorkerDefaults[RenderArgs]
	deps Deps
}

// Timeout implements river.Worker.
func (w *RenderWorker) Timeout(*river.Job[RenderArgs]) time.Duration { return RenderTimeout }

// Work runs one attempt. A worker without the pipeline snoozes the job (no attempt is used). A render that is gone ends the
// job (cancelled, not retried). Any other error is retried by River; the last attempt marks the render failed, so it does not
// stay `rendering` for ever.
func (w *RenderWorker) Work(ctx context.Context, job *river.Job[RenderArgs]) error {
	if w.deps.Render == nil {
		return river.JobSnooze(noRenderSnooze)
	}
	ws, id := job.Args.WorkspaceID, job.Args.RenderID
	res, err := w.deps.Render.Run(ctx, ws, id)
	switch {
	case errors.Is(err, ErrRenderGone):
		w.deps.logger().InfoContext(ctx, "render skipped", "workspace_id", ws.String(), "render_id", id.String(), "job_id", job.ID, "reason", err.Error())
		return river.JobCancel(err)
	case err != nil:
		w.deps.logger().WarnContext(ctx, "render attempt failed", "workspace_id", ws.String(), "render_id", id.String(), "job_id", job.ID, "attempt", job.Attempt, "error", err)
		if job.Attempt >= job.MaxAttempts {
			// The last attempt, whatever ended it (even a cancelled or timed-out context: River will not run the job again),
			// fails the render, with a fresh context for the write.
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if ferr := w.deps.Render.Fail(fctx, ws, id, err); ferr != nil {
				w.deps.logger().ErrorContext(ctx, "could not mark a render whose job gave up as failed", "render_id", id.String(), "error", ferr)
			}
		}
		return err
	}
	return river.RecordOutput(ctx, res)
}

// RendersPurgeInterval is how often the clean-up of the objects of deleted projects' renders runs.
const RendersPurgeInterval = 30 * time.Minute

const (
	rendersPurgeID = "renders_purge"
	// purgePage is how many renders one query returns.
	purgePage = 100
)

// RendersPurgeArgs is the periodic job that removes the objects of finished renders whose project was deleted.
type RendersPurgeArgs struct{}

// Kind implements river.JobArgs.
func (RendersPurgeArgs) Kind() string { return "renders_purge" }

// InsertOpts runs it on the maintenance queue, once per period however it was enqueued.
func (RendersPurgeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaintenance, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: RendersPurgeInterval / 2}}
}

// RendersPurgeWorker works RendersPurgeArgs.
type RendersPurgeWorker struct {
	river.WorkerDefaults[RendersPurgeArgs]
	deps Deps
}

// RendersPurgeOutput is what a finished run records.
type RendersPurgeOutput struct {
	Purged int `json:"purged"`
	Failed int `json:"failed"` // storage refused; the next run tries again
}

// Work deletes the objects of every finished render of a deleted project and marks the row. The prefix of each render is built
// from the ids of its row, which the query joined with a deleted project of the same workspace, so the job cannot remove the
// objects of a live project. Safe to run twice and from several workers: deleting a prefix that is already empty is a no-op.
func (w *RendersPurgeWorker) Work(ctx context.Context, _ *river.Job[RendersPurgeArgs]) error {
	if w.deps.Objects == nil {
		return noObjectStore()
	}
	var out RendersPurgeOutput
	seen := map[ids.RenderID]bool{}
	for {
		rows, err := w.deps.Store.ListRendersToPurge(ctx, purgePage)
		if err != nil {
			return fmt.Errorf("list renders to purge: %w", err)
		}
		progressed := false
		for _, r := range rows {
			if seen[r.ID] {
				continue // failed above in this run: do not loop on it
			}
			seen[r.ID] = true
			progressed = true
			if _, err := w.deps.Objects.DeleteRenderObjects(ctx, r.WorkspaceID, r.ProjectID, r.ID); err != nil {
				out.Failed++
				w.deps.logger().WarnContext(ctx, "could not remove the objects of a render of a deleted project", "workspace_id", r.WorkspaceID.String(), "render_id", r.ID.String(), "error", err)
				continue
			}
			if err := w.deps.Store.MarkRenderObjectsPurged(ctx, r.WorkspaceID, r.ID); err != nil {
				out.Failed++
				w.deps.logger().WarnContext(ctx, "could not mark a render purged", "render_id", r.ID.String(), "error", err)
				continue
			}
			out.Purged++
		}
		if len(rows) < purgePage || !progressed {
			break
		}
	}
	w.deps.logger().InfoContext(ctx, "renders of deleted projects cleaned up", "purged", out.Purged, "failed", out.Failed)
	if err := river.RecordOutput(ctx, out); err != nil {
		return err
	}
	if out.Failed > 0 {
		return fmt.Errorf("%d renders could not be cleaned up", out.Failed)
	}
	return nil
}
