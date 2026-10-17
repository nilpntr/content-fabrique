-- Spend holds and counters (P7-09a, docs/design/spend-control-v1.md sections 6 and 10).
--
-- The lock order is fixed: the platform counter row first, then the workspace's. Reserve takes both FOR UPDATE; settle takes
-- them through the upserts below in the same order, so two transactions can never wait on each other.
-- Every workspace query filters by workspace_id: a hold or a counter of another workspace is never read or changed.

-- Makes sure the row exists, so there is something to lock.
-- name: EnsureWorkspaceSpendCounter :exec
INSERT INTO spend_counters (scope, workspace_id, period) VALUES ('workspace', @workspace_id, @period)
ON CONFLICT (workspace_id, period) WHERE scope = 'workspace' DO NOTHING;

-- name: EnsurePlatformSpendCounter :exec
INSERT INTO spend_counters (scope, period) VALUES ('platform', @period)
ON CONFLICT (period) WHERE scope = 'platform' DO NOTHING;

-- name: LockWorkspaceSpendCounter :one
SELECT * FROM spend_counters WHERE scope = 'workspace' AND workspace_id = @workspace_id AND period = @period FOR UPDATE;

-- name: LockPlatformSpendCounter :one
SELECT * FROM spend_counters WHERE scope = 'platform' AND period = @period FOR UPDATE;

-- What the live holds of the workspace set aside in the period. micros counts platform and test holds (the workspace's daily
-- cap); audio counts every hold (the transcription minutes).
-- name: SumWorkspaceSpendHolds :one
SELECT COALESCE(sum(micros) FILTER (WHERE billed_to IN ('platform', 'test')), 0)::bigint AS micros,
       COALESCE(sum(audio_ms), 0)::bigint AS audio_ms
FROM spend_holds
WHERE scope = 'workspace' AND workspace_id = @workspace_id AND period = @period AND expires_at > @now;

-- What the live platform-billed holds of every workspace set aside in the period (the breaker).
-- name: SumPlatformSpendHolds :one
SELECT COALESCE(sum(micros), 0)::bigint AS micros
FROM spend_holds
WHERE billed_to = 'platform' AND period = @period AND expires_at > @now;

-- A second reserve with the same key finds the first one's hold.
-- name: InsertSpendHold :one
INSERT INTO spend_holds (scope, workspace_id, billed_to, kind, period, micros, audio_ms, expires_at, idempotency_key)
VALUES (@scope, @workspace_id, @billed_to, @kind, @period, @micros, @audio_ms, @expires_at, @idempotency_key)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING *;

-- name: GetSpendHoldByKey :one
SELECT * FROM spend_holds WHERE idempotency_key = @idempotency_key;

-- The heartbeat of a call in flight: the expiry only moves forward, and a hold that is gone is not recreated.
-- name: ExtendSpendHold :execrows
UPDATE spend_holds SET expires_at = GREATEST(expires_at, @expires_at)
WHERE idempotency_key = @idempotency_key AND scope = 'workspace' AND workspace_id = @workspace_id;

-- Deletes the hold of a workspace. A key that belongs to another workspace's hold is not touched.
-- name: DeleteWorkspaceSpendHold :execrows
DELETE FROM spend_holds WHERE idempotency_key = @idempotency_key AND scope = 'workspace' AND workspace_id = @workspace_id;

-- The sweep: holds past their expiry. The totals are for the log.
-- name: DeleteExpiredSpendHolds :one
WITH gone AS (
    DELETE FROM spend_holds WHERE expires_at <= @now RETURNING micros, audio_ms
)
SELECT count(*)::bigint AS holds, COALESCE(sum(micros), 0)::bigint AS micros, COALESCE(sum(audio_ms), 0)::bigint AS audio_ms FROM gone;

-- Adds to the counter in one statement: the upsert takes the row lock and increments under it.
-- name: AddWorkspaceSpend :one
INSERT INTO spend_counters (scope, workspace_id, period, platform_micros, test_micros, audio_ms)
VALUES ('workspace', @workspace_id, @period, @platform_micros, @test_micros, @audio_ms)
ON CONFLICT (workspace_id, period) WHERE scope = 'workspace' DO UPDATE SET
    platform_micros = spend_counters.platform_micros + EXCLUDED.platform_micros,
    test_micros = spend_counters.test_micros + EXCLUDED.test_micros,
    audio_ms = spend_counters.audio_ms + EXCLUDED.audio_ms,
    updated_at = now()
RETURNING *;

-- name: AddPlatformSpend :one
INSERT INTO spend_counters (scope, period, platform_micros) VALUES ('platform', @period, @platform_micros)
ON CONFLICT (period) WHERE scope = 'platform' DO UPDATE SET
    platform_micros = spend_counters.platform_micros + EXCLUDED.platform_micros,
    updated_at = now()
RETURNING *;

-- The alert flags are set by whoever sees the crossing first; the row count says whether this caller was it.
-- name: MarkPlatformAlerted80 :execrows
UPDATE spend_counters SET alerted_80_at = @now WHERE scope = 'platform' AND period = @period AND alerted_80_at IS NULL;

-- name: MarkPlatformTripped :execrows
UPDATE spend_counters SET tripped_at = @now WHERE scope = 'platform' AND period = @period AND tripped_at IS NULL;

-- name: GetWorkspaceSpendCounter :one
SELECT * FROM spend_counters WHERE scope = 'workspace' AND workspace_id = @workspace_id AND period = @period;

-- name: GetPlatformSpendCounter :one
SELECT * FROM spend_counters WHERE scope = 'platform' AND period = @period;
