-- Agent runs and their events (P4-01). EVERY query filters by workspace_id: a run of another workspace is never returned
-- or changed (the repository turns "no rows" into ErrNotFound).

-- name: CreateAgentRun :one
INSERT INTO agent_runs (
    workspace_id, agent, stage, parent_run_id, project_id, input, routing_profile, model, effort, max_tokens,
    budget_micros, budget_tokens, budget_tool_calls, budget_wall_clock_ms, billed_to, started_by
) VALUES (
    @workspace_id, @agent, @stage, @parent_run_id, NULLIF(@project_id::uuid, '00000000-0000-0000-0000-000000000000'::uuid), @input, @routing_profile, @model, @effort, @max_tokens,
    @budget_micros, @budget_tokens, @budget_tool_calls, @budget_wall_clock_ms, @billed_to, @started_by
)
RETURNING *;

-- name: GetAgentRun :one
SELECT * FROM agent_runs WHERE workspace_id = @workspace_id AND id = @id;

-- A worker takes the run: a queued one, or a running one whose worker died (its heartbeat is older than stale_seconds),
-- becomes running under a new epoch. A running run with a fresh heartbeat belongs to a live worker and is left alone, so a
-- duplicate job cannot take a healthy run. No row: the run is waiting for input, finished, held by a live worker, or not
-- this workspace's.
-- name: ClaimAgentRun :one
UPDATE agent_runs
SET status = 'running', epoch = epoch + 1, started_at = COALESCE(started_at, now()), heartbeat_at = now(),
    resume_enqueued_at = NULL,
    resumes = resumes + CASE WHEN status = 'running' THEN 1 ELSE 0 END
WHERE workspace_id = @workspace_id AND id = @id
  AND (status = 'queued'
       OR (status = 'running' AND COALESCE(heartbeat_at, '-infinity'::timestamptz) < now() - make_interval(secs => @stale_seconds::int)))
RETURNING *;

-- A worker that stops without finishing the run (it returns an error to be retried) puts the run back, so the retry can
-- take it at once instead of waiting for the heartbeat to go stale. Fenced by the epoch. count_resume says the retry is
-- because something went wrong (it counts toward the cap on resumes); a worker that was only told to stop does not count.
-- name: ReleaseAgentRun :execrows
UPDATE agent_runs SET status = 'queued', resumes = resumes + CASE WHEN @count_resume::boolean THEN 1 ELSE 0 END
WHERE workspace_id = @workspace_id AND id = @id AND epoch = @epoch AND status = 'running';

-- Appends the next event of a run and adds the deltas to the run's totals, in one statement (so the bump of last_seq,
-- the totals and the event commit together or not at all). A positive epoch fences the write: it only counts while the
-- run is running under that epoch. Epoch 0 is for the writers that hold their own guard (reply and cancel, in a
-- transaction that has just changed the status). No row: the write was fenced off.
-- name: AppendAgentEvent :one
WITH bump AS (
    UPDATE agent_runs
    SET last_seq = last_seq + 1,
        input_tokens = input_tokens + @d_input_tokens::bigint,
        cache_write_tokens = cache_write_tokens + @d_cache_write_tokens::bigint,
        cache_read_tokens = cache_read_tokens + @d_cache_read_tokens::bigint,
        output_tokens = output_tokens + @d_output_tokens::bigint,
        cost_micros = cost_micros + @d_cost_micros::bigint,
        tool_calls = tool_calls + @d_tool_calls::integer,
        active_ms = active_ms + @d_active_ms::bigint
    WHERE workspace_id = @workspace_id AND id = @run_id
      AND (@epoch::bigint = 0 OR (epoch = @epoch::bigint AND status = 'running'))
    RETURNING last_seq
)
INSERT INTO agent_events (workspace_id, run_id, seq, type, payload)
SELECT @workspace_id, @run_id, bump.last_seq, @type, @payload FROM bump
RETURNING *;

-- The running worker ends the run (succeeded, failed) or parks it (waiting_input). Fenced by the epoch. A run that is
-- parked after a cancel was requested is cancelled instead: the cancel wins over the question.
-- name: SetAgentRunState :one
UPDATE agent_runs
SET status = CASE WHEN @status::text = 'waiting_input' AND cancel_requested_at IS NOT NULL THEN 'cancelled' ELSE @status::text END,
    output = CASE WHEN @status::text = 'waiting_input' AND cancel_requested_at IS NOT NULL
                  THEN '{"stopped": "cancelled", "summary": "Cancelled before finishing."}'::jsonb
                  ELSE COALESCE(sqlc.narg('output')::jsonb, output) END,
    failure_reason = sqlc.narg('failure_reason')::text,
    finished_at = CASE WHEN @status::text IN ('succeeded', 'failed', 'cancelled')
                         OR (@status::text = 'waiting_input' AND cancel_requested_at IS NOT NULL) THEN now() ELSE finished_at END
WHERE workspace_id = @workspace_id AND id = @id AND epoch = @epoch AND status = 'running'
RETURNING *;

-- The input of a waiting run is answered: waiting_input becomes queued. No row: the run is not waiting for input.
-- name: QueueAgentRunAfterReply :one
UPDATE agent_runs SET status = 'queued'
WHERE workspace_id = @workspace_id AND id = @id AND status = 'waiting_input'
RETURNING *;

-- A cancel request. A run that is not running (queued, or waiting for input) is cancelled right away; a running run only
-- gets the flag, and its loop ends it between steps. No row: the run had finished already.
-- name: RequestAgentRunCancel :one
UPDATE agent_runs
SET cancel_requested_at = COALESCE(cancel_requested_at, now()),
    status = CASE WHEN status IN ('queued', 'waiting_input') THEN 'cancelled' ELSE status END,
    finished_at = CASE WHEN status IN ('queued', 'waiting_input') THEN now() ELSE finished_at END,
    output = CASE WHEN status IN ('queued', 'waiting_input') THEN @output::jsonb ELSE output END
WHERE workspace_id = @workspace_id AND id = @id AND status IN ('queued', 'running', 'waiting_input')
RETURNING *;

-- name: ListAgentEvents :many
SELECT * FROM agent_events
WHERE workspace_id = @workspace_id AND run_id = @run_id AND seq > @after_seq
ORDER BY seq
LIMIT @row_limit;

-- name: GetLastAgentEvent :one
SELECT * FROM agent_events
WHERE workspace_id = @workspace_id AND run_id = @run_id
ORDER BY seq DESC
LIMIT 1;

-- What the ledger holds for a run: the loop takes the larger of this and the run's own total when it resumes, so a model
-- call that was paid for and then lost to a crash before its event was written still counts against the budget.
-- name: SumAgentRunLedgerCost :one
SELECT COALESCE(sum(list_cost_micros), 0)::bigint AS cost_micros
FROM usage_ledger
WHERE workspace_id = @workspace_id AND run_id = @run_id;

-- The running worker shows it is alive, and adds the time it has run since its last write (so a long model call counts
-- against the wall-clock budget even if the worker dies before it writes an event). No row: it lost the run.
-- name: TouchAgentRun :execrows
UPDATE agent_runs SET heartbeat_at = now(), active_ms = active_ms + @d_active_ms::bigint
WHERE workspace_id = @workspace_id AND id = @id AND epoch = @epoch AND status = 'running';

-- SYSTEM (the resume sweeper, across workspaces, then acts per row with the row's own workspace_id): running runs whose
-- worker has not touched them for stale_seconds and that no job was enqueued for in the last five minutes.
-- name: ListStaleAgentRuns :many
SELECT workspace_id, id FROM agent_runs
WHERE status = 'running' AND heartbeat_at < now() - make_interval(secs => @stale_seconds::int)
  AND (resume_enqueued_at IS NULL OR resume_enqueued_at < now() - interval '5 minutes')
ORDER BY heartbeat_at
LIMIT @row_limit;

-- The sweeper marks a stale run before it enqueues the job, in the same transaction, so concurrent sweepers enqueue one.
-- No row: the run is not stale any more, or a job was enqueued lately.
-- name: MarkAgentRunForResume :execrows
UPDATE agent_runs SET resume_enqueued_at = now()
WHERE workspace_id = @workspace_id AND id = @id AND status = 'running'
  AND heartbeat_at < now() - make_interval(secs => @stale_seconds::int)
  AND (resume_enqueued_at IS NULL OR resume_enqueued_at < now() - interval '5 minutes');

-- The run of an agent that is not finished yet on a project, if there is one (the edit endpoint allows one at a time).
-- name: GetActiveAgentRunForProject :one
SELECT * FROM agent_runs
WHERE workspace_id = @workspace_id AND project_id = @project_id AND agent = @agent
  AND status IN ('queued', 'running', 'waiting_input')
ORDER BY created_at
LIMIT 1;
