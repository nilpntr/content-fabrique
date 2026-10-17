package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Deps are what the workers need. Nothing here is a secret.
type Deps struct {
	// Store is the app role's store (DATABASE_URL). Workers read and write rows through it like the api does.
	Store  *store.Store
	Logger *slog.Logger
	// Objects is object storage (R2 or MinIO), for the jobs that remove objects. Nil when storage is not configured: those
	// jobs then fail and are retried, so a worker started later (with the settings) finishes them.
	Objects ObjectStore
	// Ingest is the media pipeline for the asset_ingest job. Nil when the worker has no object storage or sandbox set up:
	// ingest jobs then wait (snoozed) until a worker that has them picks them up.
	Ingest AssetIngester
	// Render is the render pipeline for the render job. Nil when the worker has no object storage or sandbox set up: render jobs
	// then wait (snoozed) until a worker that has them picks them up.
	Render Renderer
	// Now replaces time.Now (tests).
	Now func() time.Time
	// Register optionally adds more workers to the bundle (tests, and packages with jobs of their own later).
	Register func(*river.Workers) error
}

func (d Deps) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Workers builds the River worker bundle: one worker per args type of this package.
func Workers(deps Deps) (*river.Workers, error) {
	if deps.Store == nil {
		return nil, errors.New("jobs: a store is required")
	}
	w := river.NewWorkers()
	if err := river.AddWorkerSafely(w, &WorkspaceWelcomeWorker{deps: deps}); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(w, &WebhookEventsPruneWorker{deps: deps}); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(w, &AssetIngestWorker{deps: deps}); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(w, &AssetObjectDeleteWorker{deps: deps}); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(w, &UploadsExpireWorker{deps: deps}); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(w, &AgentRunsResumeWorker{deps: deps}); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(w, &RenderWorker{deps: deps}); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(w, &RendersPurgeWorker{deps: deps}); err != nil {
		return nil, err
	}
	if deps.Register != nil {
		if err := deps.Register(w); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// WorkspaceWelcomeArgs is enqueued, inside the transaction that creates a workspace, by the Clerk sync (the
// just-in-time sync and the webhook). It is the demo job: it stands for the first-workspace setup later tasks fill in.
type WorkspaceWelcomeArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
}

// Kind implements river.JobArgs.
func (WorkspaceWelcomeArgs) Kind() string { return "workspace_welcome" }

// InsertOpts makes the job unique by its arguments, whatever its state (River's default states include completed
// jobs, until River removes them after its retention period): a workspace is welcomed once, however many paths or
// retries try to enqueue it.
func (WorkspaceWelcomeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueDefault,
		MaxAttempts: 5,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// WelcomeOutput is what a finished welcome job records as its River output (shown by River UI, stored in
// river_job.metadata under "output"). It is the observable result of the demo job.
type WelcomeOutput struct {
	WelcomedAt time.Time `json:"welcomed_at"`
}

// WorkspaceWelcomeWorker works WorkspaceWelcomeArgs.
type WorkspaceWelcomeWorker struct {
	river.WorkerDefaults[WorkspaceWelcomeArgs]
	deps Deps
}

// Work checks that the workspace is there, logs, and records when it welcomed it. Real onboarding steps (a default
// profile, sample content) are added here by later tasks.
func (w *WorkspaceWelcomeWorker) Work(ctx context.Context, job *river.Job[WorkspaceWelcomeArgs]) error {
	ws, err := w.deps.Store.GetWorkspace(ctx, job.Args.WorkspaceID)
	if errors.Is(err, store.ErrNotFound) {
		// The creating transaction committed before this job could run, so the row is gone since. A retry cannot
		// change that.
		return river.JobCancel(fmt.Errorf("workspace %s does not exist", job.Args.WorkspaceID))
	}
	if err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}
	if ws.DeletedAt != nil {
		return river.JobCancel(fmt.Errorf("workspace %s was deleted", job.Args.WorkspaceID))
	}
	w.deps.logger().InfoContext(ctx, "welcome workspace", "workspace_id", ws.ID.String(), "job_id", job.ID, "attempt", job.Attempt)
	return river.RecordOutput(ctx, WelcomeOutput{WelcomedAt: w.deps.now().UTC()})
}
