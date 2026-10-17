package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/jobs"
)

// Worker is the River worker of the agent_run job. A Worker without a runner (the worker process has no model access
// configured) snoozes the jobs, so nothing is lost: they run when a worker that has it picks them up.
type Worker struct {
	river.WorkerDefaults[jobs.AgentRunArgs]
	runner *Runner
}

// noRunnerSnooze is how long a job waits when the worker cannot run agents.
const noRunnerSnooze = 5 * time.Minute

// NewWorker returns the worker for r; r may be nil.
func NewWorker(r *Runner) *Worker { return &Worker{runner: r} }

// Register adds the agent_run worker to a River bundle: pass it as jobs.Deps.Register (or WorkerOptions.Register).
func Register(r *Runner) func(*river.Workers) error {
	return func(w *river.Workers) error { return river.AddWorkerSafely(w, NewWorker(r)) }
}

// Timeout implements river.Worker: longer than the longest wall-clock budget, which is what ends a run.
func (w *Worker) Timeout(*river.Job[jobs.AgentRunArgs]) time.Duration { return jobs.AgentRunTimeout }

// Work runs the agent loop for the run. An error is retried by River (the run resumes from its events); the last attempt
// fails the run, so it does not stay queued for ever.
func (w *Worker) Work(ctx context.Context, job *river.Job[jobs.AgentRunArgs]) error {
	if w.runner == nil {
		return river.JobSnooze(noRunnerSnooze)
	}
	args := job.Args
	out, err := w.runner.Execute(ctx, args.WorkspaceID, args.RunID)
	if err != nil {
		w.runner.deps.Logger.WarnContext(ctx, "agent run attempt failed", "run_id", args.RunID.String(), "workspace_id", args.WorkspaceID.String(), "job_id", job.ID, "attempt", job.Attempt, "error", err)
		if job.Attempt >= job.MaxAttempts {
			w.runner.GiveUp(ctx, args.WorkspaceID, args.RunID, err)
		}
		return err
	}
	if out == OutcomeGone {
		return river.JobCancel(fmt.Errorf("agent run %s does not exist in workspace %s", args.RunID, args.WorkspaceID))
	}
	return river.RecordOutput(ctx, map[string]string{"outcome": string(out)})
}
