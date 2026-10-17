package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The jobs of the agent runtime (P4-01, docs/design/agent-runtime-v1.md section 3). The agent_run worker itself lives in
// internal/agents/runtime (it needs the model client, which this package does not import) and is added with Deps.Register;
// its arguments, queue and options are here with the others.

// QueueAgents holds the agent runs: long (minutes), mostly waiting on the model, and each one costs money, so a bounded
// number run at once.
const QueueAgents = "agents"

// DefaultAgentConcurrency is how many agent runs one worker runs at once.
const DefaultAgentConcurrency = 4

const (
	// AgentRunStaleAfter is how long a running run may go without a heartbeat before it is taken for dead and another
	// worker may take it over. The worker touches it every AgentRunHeartbeat.
	AgentRunStaleAfter = 90 * time.Second
	// AgentRunHeartbeat is how often the worker that holds a run touches it.
	AgentRunHeartbeat = 15 * time.Second
	// AgentRunsResumeInterval is how often the sweeper looks for runs whose worker died.
	AgentRunsResumeInterval = 30 * time.Second
	// AgentRunTimeout is how long one attempt of an agent_run job may take: longer than the longest wall-clock budget a run
	// can have (runtime.MaxWallClock), so the budget ends a run before River's timeout does.
	AgentRunTimeout = 40 * time.Minute

	agentRunsResumeID = "agent_runs_resume"
	resumePage        = 100
)

// AgentRunArgs works one agent run. The run's workspace goes along, because every query is scoped by it. The same job is
// enqueued again when an ask_user is answered and when the sweeper finds a run whose worker died; the run's own state
// (claim, epoch, events), not the job, says what is left to do.
type AgentRunArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
	RunID       uuid.UUID       `json:"run_id"`
}

// Kind implements river.JobArgs.
func (AgentRunArgs) Kind() string { return "agent_run" }

// InsertOpts puts the job on the agents queue. It is not unique: a job for a run that is held by a live worker, or has
// finished, does nothing, and uniqueness over the states River allows would also swallow the job that answers an
// ask_user while the job that parked the run is still finishing.
func (AgentRunArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueAgents, MaxAttempts: 5}
}

// AgentRunsResumeArgs is the periodic job that enqueues the runs whose worker died.
type AgentRunsResumeArgs struct{}

// Kind implements river.JobArgs.
func (AgentRunsResumeArgs) Kind() string { return "agent_runs_resume" }

// InsertOpts runs it on the maintenance queue, once per period however it was enqueued.
func (AgentRunsResumeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaintenance, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: AgentRunsResumeInterval / 2}}
}

// AgentRunsResumeWorker works AgentRunsResumeArgs.
type AgentRunsResumeWorker struct {
	river.WorkerDefaults[AgentRunsResumeArgs]
	deps Deps
}

// Work enqueues an agent_run job for every running run whose heartbeat is stale: its worker was killed or lost the
// database. The new job claims the run (which fences the old worker out if it is somehow still alive) and resumes it from
// its events. Running twice is harmless: a job for a run that is no longer stale does nothing.
func (w *AgentRunsResumeWorker) Work(ctx context.Context, _ *river.Job[AgentRunsResumeArgs]) error {
	stale, err := w.deps.Store.StaleAgentRuns(ctx, AgentRunStaleAfter, resumePage)
	if err != nil {
		return fmt.Errorf("list stale agent runs: %w", err)
	}
	client := river.ClientFromContext[pgx.Tx](ctx)
	resumed := 0
	for _, r := range stale {
		// Mark the run and enqueue the job in one transaction: a run gets one job however many sweepers look, and no job
		// is enqueued again for it until the first had five minutes.
		var marked bool
		err := w.deps.Store.InTx(ctx, func(tx *store.Store) error {
			var err error
			if marked, err = tx.MarkAgentRunForResume(ctx, r.WorkspaceID, r.ID, AgentRunStaleAfter); err != nil || !marked {
				return err
			}
			pgxTx, err := tx.Tx()
			if err != nil {
				return err
			}
			_, err = client.InsertTx(ctx, pgxTx, AgentRunArgs{WorkspaceID: r.WorkspaceID, RunID: r.ID}, nil)
			return err
		})
		if err != nil {
			return fmt.Errorf("enqueue agent run %s: %w", r.ID, err)
		}
		if marked {
			resumed++
			w.deps.logger().WarnContext(ctx, "resuming an agent run whose worker stopped", "workspace_id", r.WorkspaceID.String(), "run_id", r.ID.String())
		}
	}
	return river.RecordOutput(ctx, map[string]int{"resumed": resumed})
}
