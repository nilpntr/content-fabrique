-- The usage ledger (P4-00a). Every query filters by workspace_id.

-- One row per metered event. A row whose idempotency_key (or request_id) is already there (a retried insert whose first attempt
-- did commit, or a replayed settle) changes nothing and reports 0 rows, so an event is never counted twice. The caller adds the
-- row's cost to the counters only when this reports 1.
-- name: InsertUsageLedger :execrows
INSERT INTO usage_ledger (
    workspace_id, run_id, stage, kind, model, input_tokens, cache_write_tokens, cache_write_1h_tokens,
    cache_read_tokens, output_tokens, list_cost_micros, price_table_version, billed_to, request_id, estimated, provider, audio_ms,
    quantity, unit, project_id, member_user_id, action_id, idempotency_key, created_at
) VALUES (
    @workspace_id, @run_id, @stage, @kind, @model, @input_tokens, @cache_write_tokens, @cache_write_1h_tokens,
    @cache_read_tokens, @output_tokens, @list_cost_micros, @price_table_version, @billed_to, @request_id, @estimated, @provider, @audio_ms,
    @quantity, @unit, @project_id, @member_user_id, @action_id, @idempotency_key, @created_at
)
ON CONFLICT DO NOTHING;

-- What the workspace spent in the window [from, to), at list price, whoever pays.
-- name: SumUsageCostMicros :one
SELECT COALESCE(sum(list_cost_micros), 0)::bigint AS cost_micros
FROM usage_ledger
WHERE workspace_id = @workspace_id AND created_at >= @from_time AND created_at < @to_time;

-- name: ListUsageLedgerForRun :many
SELECT * FROM usage_ledger
WHERE workspace_id = @workspace_id AND run_id = @run_id
ORDER BY id;

-- The most recent rows of a workspace, newest first (cfctl llm ping shows the one it just wrote).
-- name: ListRecentUsageLedger :many
SELECT * FROM usage_ledger
WHERE workspace_id = @workspace_id
ORDER BY id DESC
LIMIT @row_limit;
