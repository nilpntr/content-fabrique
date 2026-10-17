-- Agent runs and their events (P4-01, docs/design/agent-runtime-v1.md section 3, architecture sections 4 and 7).
--
-- agent_runs:   one row per run of an agent. The run is the unit the agent_run River job works on. Everything the loop
--               needs to resume after a crash is here or in agent_events: the loop rebuilds its conversation from the events.
--   status:     queued -> running -> waiting_input (ask_user) -> queued -> running ... -> succeeded | failed | cancelled.
--   stage, routing_profile, model, effort, max_tokens: the routing entry, resolved once when the run is created and
--               kept, so a resumed run keeps its model whatever config/routing says by then (and so its prompt cache and
--               record/replay keys stay valid). effort is never empty on a run the runtime made.
--   *_tokens, tool_calls, cost_micros, active_ms: totals, added to in the same statement that appends the event they
--               belong to, so a total never disagrees with the events. cost_micros is list price.
--   budget_*:   the limits of this run. A run stops (failed, reason budget) before the call that would pass one.
--               active_ms is the time the loop has been running (waiting_input does not count) against budget_wall_clock_ms.
--   billed_to:  who pays (platform, workspace_key or test), as the llm factory said when the run was created.
--   epoch:      incremented every time a worker claims the run. The loop's writes carry the epoch they claimed, so a
--               worker that lost the run (River rescued its job as stuck, and another worker took over) can no longer write.
--   last_seq:   the sequence number of the latest event; the next event takes last_seq + 1 under the row lock.
--   project_id: null for a run without a project; the foreign key keeps it inside the workspace.
--   resumes:    how many times a worker took the run over after another stopped without finishing it (a dead heartbeat, or an
--               error that sent the job to a retry). A run that is resumed too often is a poison run and is failed.
--   resume_enqueued_at: when the sweeper last enqueued a job for a stale run, so it does not enqueue another every pass.
--   cancel_requested_at: set by a cancel request; the loop looks at it between steps.
--   heartbeat_at: touched every few seconds while a worker holds the run. A running run whose heartbeat is stale lost its
--               worker; a periodic job enqueues it again and the new worker resumes it from the events.
--
-- agent_events: append-only log of a run. (run_id, seq) is the key; seq counts from 1 without gaps.
--   type:       status | assistant_message | tool_call | tool_result | error | artifact.
--   payload:    at most 64 KB of JSON, images by reference (R2 key), never inline. The Go layer caps the compact JSON at
--               65536 bytes; the check below is a looser backstop (jsonb prints with spaces) against a writer that skips it.
--               Payloads hold no secrets.
--
-- usage_ledger.run_id has no foreign key to agent_runs, although its migration said P4-01 would add one: the ledger is
-- billing history that must outlive any run, and `cfctl llm ping` and the llm tests write ledger rows with ad-hoc run ids.
--
-- Tenancy: both tables carry workspace_id and every query filters by it. agent_events and a run's parent reference
-- agent_runs through (workspace_id, id), so an event or a parent can never be in another workspace.

-- +goose Up

CREATE TABLE agent_runs (
    id                   uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id         uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent                text        NOT NULL,
    stage                text        NOT NULL,
    parent_run_id        uuid,
    project_id           uuid,
    status               text        NOT NULL DEFAULT 'queued',
    input                jsonb       NOT NULL DEFAULT '{}',
    output               jsonb,
    routing_profile      text        NOT NULL,
    model                text        NOT NULL,
    effort               text        NOT NULL DEFAULT '',
    max_tokens           integer     NOT NULL,
    escalated            boolean     NOT NULL DEFAULT false,
    input_tokens         bigint      NOT NULL DEFAULT 0,
    cache_write_tokens   bigint      NOT NULL DEFAULT 0,
    cache_read_tokens    bigint      NOT NULL DEFAULT 0,
    output_tokens        bigint      NOT NULL DEFAULT 0,
    tool_calls           integer     NOT NULL DEFAULT 0,
    cost_micros          bigint      NOT NULL DEFAULT 0,
    active_ms            bigint      NOT NULL DEFAULT 0,
    budget_micros        bigint      NOT NULL,
    budget_tokens        bigint      NOT NULL,
    budget_tool_calls    integer     NOT NULL,
    budget_wall_clock_ms bigint      NOT NULL,
    billed_to            text        NOT NULL,
    failure_reason       text,
    cancel_requested_at  timestamptz,
    heartbeat_at         timestamptz,
    epoch                bigint      NOT NULL DEFAULT 0,
    resumes              integer     NOT NULL DEFAULT 0,
    resume_enqueued_at   timestamptz,
    last_seq             integer     NOT NULL DEFAULT 0,
    started_at           timestamptz,
    finished_at          timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_runs_workspace_id_id_key UNIQUE (workspace_id, id),
    CONSTRAINT agent_runs_parent_fkey FOREIGN KEY (workspace_id, parent_run_id) REFERENCES agent_runs (workspace_id, id),
    CONSTRAINT agent_runs_project_fkey FOREIGN KEY (workspace_id, project_id) REFERENCES projects (workspace_id, id),
    CONSTRAINT agent_runs_status_check CHECK (status IN ('queued', 'running', 'waiting_input', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT agent_runs_agent_check CHECK (char_length(agent) BETWEEN 1 AND 100),
    CONSTRAINT agent_runs_stage_check CHECK (char_length(stage) BETWEEN 1 AND 100),
    CONSTRAINT agent_runs_billed_to_check CHECK (billed_to IN ('platform', 'workspace_key', 'test')),
    CONSTRAINT agent_runs_max_tokens_check CHECK (max_tokens > 0),
    CONSTRAINT agent_runs_totals_check CHECK (
        input_tokens >= 0 AND cache_write_tokens >= 0 AND cache_read_tokens >= 0 AND output_tokens >= 0
        AND tool_calls >= 0 AND cost_micros >= 0 AND active_ms >= 0 AND resumes >= 0),
    CONSTRAINT agent_runs_budget_check CHECK (
        budget_micros >= 0 AND budget_tokens > 0 AND budget_tool_calls > 0 AND budget_wall_clock_ms > 0),
    CONSTRAINT agent_runs_failure_reason_check CHECK (
        (status = 'failed') = (failure_reason IS NOT NULL) AND (failure_reason IS NULL OR char_length(failure_reason) BETWEEN 1 AND 100)),
    CONSTRAINT agent_runs_finished_check CHECK ((status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL))
);

-- A workspace's runs, newest first (ids are UUIDv7).
CREATE INDEX agent_runs_workspace_idx ON agent_runs (workspace_id, id DESC);
-- The runs of a project.
CREATE INDEX agent_runs_project_idx ON agent_runs (workspace_id, project_id, id DESC) WHERE project_id IS NOT NULL;
-- The sweeper looks for running runs whose worker is gone.
CREATE INDEX agent_runs_running_heartbeat_idx ON agent_runs (heartbeat_at) WHERE status = 'running';
CREATE INDEX agent_runs_parent_idx ON agent_runs (parent_run_id) WHERE parent_run_id IS NOT NULL;

CREATE TRIGGER agent_runs_set_updated_at BEFORE UPDATE ON agent_runs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('agent_runs');

CREATE TABLE agent_events (
    run_id       uuid        NOT NULL,
    seq          integer     NOT NULL,
    workspace_id uuid        NOT NULL,
    type         text        NOT NULL,
    payload      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, seq),
    CONSTRAINT agent_events_run_fkey FOREIGN KEY (workspace_id, run_id)
        REFERENCES agent_runs (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_events_seq_check CHECK (seq >= 1),
    CONSTRAINT agent_events_type_check CHECK (type IN ('status', 'assistant_message', 'tool_call', 'tool_result', 'error', 'artifact')),
    CONSTRAINT agent_events_payload_check CHECK (octet_length(payload::text) <= 131072)
);

SELECT cf_grant_app_dml('agent_events');

-- A tool call and its result exist once per tool_use id, whoever writes them (a worker that lost its run is fenced out,
-- and this is the second line of defence).
CREATE UNIQUE INDEX agent_events_tool_call_key ON agent_events (run_id, (payload ->> 'tool_use_id')) WHERE type = 'tool_call';
CREATE UNIQUE INDEX agent_events_tool_result_key ON agent_events (run_id, (payload ->> 'tool_use_id')) WHERE type = 'tool_result';

-- +goose Down

DROP TABLE agent_events;
DROP TABLE agent_runs;
