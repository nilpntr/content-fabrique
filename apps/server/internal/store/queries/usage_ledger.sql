-- The usage ledger (P4-00a). Every query filters by workspace_id.

-- One row per model call. A row whose request_id is already there (a retried insert whose first attempt did commit)
-- changes nothing, so a call is never counted twice.
-- name: InsertUsageLedger :exec
INSERT INTO usage_ledger (
    workspace_id, run_id, stage, kind, model, input_tokens, cache_write_tokens, cache_write_1h_tokens,
    cache_read_tokens, output_tokens, list_cost_micros, price_table_version, billed_to, request_id, estimated, created_at
) VALUES (
    @workspace_id, @run_id, @stage, @kind, @model, @input_tokens, @cache_write_tokens, @cache_write_1h_tokens,
    @cache_read_tokens, @output_tokens, @list_cost_micros, @price_table_version, @billed_to, @request_id, @estimated, @created_at
)
ON CONFLICT (request_id) WHERE request_id <> '' DO NOTHING;

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
