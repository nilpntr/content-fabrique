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

// The jobs of the renders (P3-06): the render job a request enqueues, and the periodic sweep that fails renders whose job is gone and
// removes the objects nobody can use.

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

// InsertOpts makes the job unique by render and puts it on the render queue: a render is minutes of CPU in a sandbox, so few at a
// time, and an ingest that runs for hours must not hold up the preview someone waits for. The priority is set per insert
// (RenderPriority).
func (RenderArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueRender, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// RenderPriority is the River priority of a render of the profile (1 is first): a preview, which an editor or the agent waits for,
// goes before a final render, which is a batch job.
func RenderPriority(profile string) int {
	if profile == "preview" {
		return 1
	}
	return 3
}

// RenderJobLost is the failure reason of a render whose job is gone: discarded after its last attempt, cancelled or removed while
// the render was still queued or rendering (the sweeper fails it).
const RenderJobLost = "job_lost"

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

// RendersSweepInterval is how often the sweep of the renders runs.
const RendersSweepInterval = 5 * time.Minute

const (
	rendersSweepID = "renders_sweep"
	// sweepPage is how many renders one query returns.
	sweepPage = 100
	// RenderSweepGrace is how old a queued or rendering render must be before the sweeper asks whether its job is alive. The job is
	// enqueued in the transaction that creates the render, so it exists from the first moment; the grace is for clock and commit
	// slack only.
	RenderSweepGrace = 2 * time.Minute
)

// RendersSweepArgs is the periodic job that keeps the renders table honest: it fails renders whose job is gone, and removes the
// objects of renders nobody can use.
type RendersSweepArgs struct{}

// Kind implements river.JobArgs.
func (RendersSweepArgs) Kind() string { return "renders_sweep" }

// InsertOpts runs it on the maintenance queue, once per period however it was enqueued.
func (RendersSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaintenance, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: RendersSweepInterval / 2}}
}

// RendersSweepWorker works RendersSweepArgs.
type RendersSweepWorker struct {
	river.WorkerDefaults[RendersSweepArgs]
	deps Deps
}

// RendersSweepOutput is what a finished run records.
type RendersSweepOutput struct {
	Orphans     int `json:"orphans"`      // queued or rendering renders whose job was gone, now failed
	Purged      int `json:"purged"`       // renders whose objects were removed
	PurgeFailed int `json:"purge_failed"` // storage refused; the next run tries again
}

// Work does two things, in this order, so that the second sees the result of the first.
//
// First it fails the renders that can never finish: queued or rendering, older than RenderSweepGrace, with no River job that can
// still run them (the job was discarded after its last attempt, cancelled, deleted, or its worker died on the last attempt). Without
// this a ghost would hold its place in the workspace's in-flight count, and in the dedupe index, for ever. A render whose job is
// merely waiting or running is left alone however old it is: River's own rescue and the job's timeout deal with those.
//
// Then it removes the objects of the renders that are over and that nobody can use: failed renders (an attempt may have left
// files behind) and ready renders of deleted projects. The prefix is built from the ids of the row, and the row's workspace and
// project are the ones the query joined, so the job cannot remove the objects of a live render. Safe to run twice and from several
// workers: deleting a prefix that is already empty is a no-op.
func (w *RendersSweepWorker) Work(ctx context.Context, _ *river.Job[RendersSweepArgs]) error {
	if w.deps.Objects == nil {
		return noObjectStore()
	}
	var out RendersSweepOutput
	before := w.deps.now().Add(-RenderSweepGrace)
	stale, err := w.deps.Store.ListStaleRenders(ctx, before, sweepPage)
	if err != nil {
		return fmt.Errorf("list stale renders: %w", err)
	}
	for _, r := range stale {
		alive, err := w.deps.Store.RenderJobAlive(ctx, r.ID)
		if err != nil {
			return fmt.Errorf("render job: %w", err)
		}
		if alive {
			continue
		}
		if _, err := w.deps.Store.FailRender(ctx, r.WorkspaceID, r.ID, RenderJobLost, -1, -1); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // it finished meanwhile
			}
			return fmt.Errorf("fail orphaned render: %w", err)
		}
		out.Orphans++
		w.deps.logger().WarnContext(ctx, "failed a render whose job is gone", "workspace_id", r.WorkspaceID.String(), "render_id", r.ID.String(), "status", r.Status)
	}

	seen := map[ids.RenderID]bool{}
	for {
		rows, err := w.deps.Store.ListRendersToPurge(ctx, sweepPage)
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
				out.PurgeFailed++
				w.deps.logger().WarnContext(ctx, "could not remove the objects of a render", "workspace_id", r.WorkspaceID.String(), "render_id", r.ID.String(), "error", err)
				continue
			}
			if err := w.deps.Store.MarkRenderObjectsPurged(ctx, r.WorkspaceID, r.ID); err != nil {
				out.PurgeFailed++
				w.deps.logger().WarnContext(ctx, "could not mark a render purged", "render_id", r.ID.String(), "error", err)
				continue
			}
			out.Purged++
		}
		if len(rows) < sweepPage || !progressed {
			break
		}
	}
	w.deps.logger().InfoContext(ctx, "renders swept", "orphans", out.Orphans, "purged", out.Purged, "purge_failed", out.PurgeFailed)
	if err := river.RecordOutput(ctx, out); err != nil {
		return err
	}
	if out.PurgeFailed > 0 {
		return fmt.Errorf("%d renders could not be cleaned up", out.PurgeFailed)
	}
	return nil
}
