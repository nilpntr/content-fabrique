-- The usage ledger (P4-00a, architecture 11a, decision D-18): one row per model call, written by internal/agents/llm.
--
-- It is the source of truth for what model calls cost: the per-workspace daily cap reads it, the agent runtime's run
-- totals will, and so will credits and the margin report. Rows are only ever appended.
--
-- run_id:      the agent run (P4-01), null for a call outside a run. No foreign key yet, because agent_runs does not
--              exist; P4-01 adds it.
-- stage:       the routing stage that made the call (editor, revision, ideation ...), never a model name.
-- kind:        what was metered. Only 'llm' so far; transcription, rendering and the rest add values.
-- *_tokens:    as the API reports them. input_tokens excludes cache reads and writes. cache_write_tokens is the total of
--              both cache writes and cache_write_1h_tokens the part written with the one-hour lifetime, because the
--              two lifetimes are priced differently.
-- list_cost_micros: the cost at list price, in millionths of a US dollar, computed from the price table the row names
--              in price_table_version ('unpriced' when LLM_ALLOW_UNPRICED let an unpriced model run: cost 0).
-- billed_to:   who pays. platform (we do), workspace_key (the creator's own Anthropic key, P4-00b) or test (development,
--              ci and bench traffic, so test spend never mixes into customer margin).
-- request_id:  Anthropic's request-id header. Unique when present, so a retried insert cannot double-count a call.
-- estimated:   true when the request was sent and no usage came back (a timeout, a reset, a body cut off), so the call
--              may have run and been billed. The row then holds the call's reserved worst-case cost (input_tokens is the
--              counted or bounded prompt, output_tokens is max_tokens), which keeps the daily cap and run budgets
--              conservative. A reconciliation against Anthropic's usage report can correct such rows.
--
-- Billing history is not deleted with a workspace: the foreign key RESTRICTs. A workspace is soft-deleted today
-- (workspaces.deleted_at), so nothing deletes rows. A purge of a workspace (a GDPR erasure, say) has to move or
-- anonymise its ledger rows deliberately, in the same change that adds the purge.

-- +goose Up

CREATE TABLE usage_ledger (
    id                    uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id          uuid        NOT NULL REFERENCES workspaces (id) ON DELETE RESTRICT,
    run_id                uuid,
    stage                 text        NOT NULL,
    kind                  text        NOT NULL DEFAULT 'llm',
    model                 text        NOT NULL,
    input_tokens          bigint      NOT NULL,
    cache_write_tokens    bigint      NOT NULL DEFAULT 0,
    cache_write_1h_tokens bigint      NOT NULL DEFAULT 0,
    cache_read_tokens     bigint      NOT NULL DEFAULT 0,
    output_tokens         bigint      NOT NULL,
    list_cost_micros      bigint      NOT NULL,
    price_table_version   text        NOT NULL,
    billed_to             text        NOT NULL,
    request_id            text        NOT NULL DEFAULT '',
    estimated             boolean     NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT usage_ledger_kind_check CHECK (kind IN ('llm')),
    CONSTRAINT usage_ledger_billed_to_check CHECK (billed_to IN ('platform', 'workspace_key', 'test')),
    CONSTRAINT usage_ledger_tokens_check CHECK (
        input_tokens >= 0 AND cache_write_tokens >= 0 AND cache_read_tokens >= 0 AND output_tokens >= 0
        AND cache_write_1h_tokens BETWEEN 0 AND cache_write_tokens),
    CONSTRAINT usage_ledger_cost_check CHECK (list_cost_micros >= 0),
    CONSTRAINT usage_ledger_stage_check CHECK (char_length(stage) BETWEEN 1 AND 100),
    CONSTRAINT usage_ledger_model_check CHECK (char_length(model) BETWEEN 1 AND 100)
);

-- The daily cap sums one workspace over a time window.
CREATE INDEX usage_ledger_workspace_time_idx ON usage_ledger (workspace_id, created_at);
-- A run's total.
CREATE INDEX usage_ledger_run_idx ON usage_ledger (run_id) WHERE run_id IS NOT NULL;
CREATE UNIQUE INDEX usage_ledger_request_id_key ON usage_ledger (request_id) WHERE request_id <> '';

SELECT cf_grant_app_dml('usage_ledger');

-- +goose Down

DROP TABLE usage_ledger;
