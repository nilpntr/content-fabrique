-- Credits (P7-01a, docs/design/spend-control-v1.md sections 3 to 5 and 10). See migration 00018 for the model.
--
-- EVERY query filters by workspace_id (or by an action id that a workspace-filtered query found first): a ledger row, a hold or an
-- account of another workspace is never read or changed. The sweep and the expiry job are the two that look across workspaces, and
-- they act on each row with the row's own workspace.
--
-- The lock is the account row. A transaction that changes the balance calls LockCreditAccount first, so two of them take turns.

-- Makes sure the account row exists, so there is something to lock.
-- name: EnsureCreditAccount :exec
INSERT INTO credit_accounts (workspace_id) VALUES (@workspace_id)
ON CONFLICT (workspace_id) DO NOTHING;

-- name: LockCreditAccount :one
SELECT * FROM credit_accounts WHERE workspace_id = @workspace_id FOR UPDATE;

-- name: GetCreditAccount :one
SELECT * FROM credit_accounts WHERE workspace_id = @workspace_id;

-- name: SetCreditAccountPeriod :exec
UPDATE credit_accounts
SET plan = @plan, period_anchor = @period_anchor, allowance_until = @allowance_until, plan_generation = @plan_generation
WHERE workspace_id = @workspace_id;

-- The sources the workspace can use now (not past their expiry) with what is left of each, in the order they are used: the allowance
-- first, then the others, earliest expiry first (a source that never expires, the trial's allowance, last of its kind). A source's
-- credits left are its own credits plus every row that names it (charges and expiries are negative, refunds positive).
-- name: ListCreditSources :many
SELECT s.id, s.kind, s.credits AS granted, s.expires_at, s.created_at, s.plan,
       (s.credits + COALESCE(u.used, 0))::integer AS remaining,
       EXISTS (SELECT 1 FROM credit_ledger e WHERE e.grant_id = s.id AND e.kind = 'expire') AS expired
FROM credit_ledger s
LEFT JOIN LATERAL (
    SELECT SUM(c.credits) AS used FROM credit_ledger c WHERE c.grant_id = s.id AND c.workspace_id = s.workspace_id
) u ON true
WHERE s.workspace_id = @workspace_id AND s.credits > 0 AND s.kind IN ('grant_allowance', 'grant_topup', 'adjust')
  AND (s.expires_at IS NULL OR s.expires_at > @now)
ORDER BY (s.kind <> 'grant_allowance'), s.expires_at NULLS LAST, s.created_at, s.id;

-- The credits set aside by the holds that still count: a hold counts until its expiry, and for as long as its run is still active (queued,
-- running or waiting for its creator), however long that takes, so a long queue or an outage cannot free the credits of a run that will
-- still finish and be charged. Only the part that is not frozen counts: a frozen part rests on a source that expired and is out of the
-- live sources already.
-- name: SumActiveCreditHolds :one
SELECT COALESCE(SUM(h.credits - h.frozen_credits), 0)::integer AS credits
FROM credit_holds h
WHERE h.workspace_id = @workspace_id
  AND (h.expires_at > @now
       OR EXISTS (SELECT 1 FROM agent_runs r WHERE r.workspace_id = h.workspace_id AND r.id = h.action_id AND r.status IN ('queued', 'running', 'waiting_input')));

-- One ledger row. A key that is already in the ledger inserts nothing (no row comes back).
-- name: InsertCreditLedger :one
INSERT INTO credit_ledger (
    workspace_id, kind, credits, grant_id, action, action_id, project_id, member_user_id, plan, plan_list_version, timeline_version,
    expires_at, actor, reason, idempotency_key, created_at
) VALUES (
    @workspace_id, @kind, @credits, sqlc.narg('grant_id'), sqlc.narg('action'), sqlc.narg('action_id'), sqlc.narg('project_id'),
    sqlc.narg('member_user_id'), sqlc.narg('plan'), sqlc.narg('plan_list_version'), sqlc.narg('timeline_version'),
    sqlc.narg('expires_at'), sqlc.narg('actor'), sqlc.narg('reason'), @idempotency_key, @created_at
)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING id;

-- name: GetCreditLedgerByKey :one
SELECT * FROM credit_ledger WHERE workspace_id = @workspace_id AND idempotency_key = @idempotency_key;

-- The charge rows of an action (one per source it took from).
-- name: ListCreditChargesOfAction :many
SELECT * FROM credit_ledger
WHERE workspace_id = @workspace_id AND action_id = @action_id AND kind = 'charge'
ORDER BY id;

-- The refund rows of an action.
-- name: ListCreditRefundsOfAction :many
SELECT * FROM credit_ledger
WHERE workspace_id = @workspace_id AND action_id = @action_id AND kind = 'refund'
ORDER BY id;

-- Has the project an edit that was charged (the first edit, or a revision) and not given back. A zero-credit charge counts: it is how
-- a run that was started without credits (development) records that the edit happened.
-- name: ProjectHasChargedEdit :one
SELECT EXISTS (
    SELECT 1 FROM credit_ledger c
    WHERE c.workspace_id = @workspace_id AND c.project_id = @project_id AND c.kind = 'charge'
      AND c.action IN ('edit.first', 'edit.revision')
      AND NOT EXISTS (SELECT 1 FROM credit_ledger r WHERE r.workspace_id = c.workspace_id AND r.kind = 'refund' AND r.action_id = c.action_id)
) AS charged;

-- The newest timeline version an agent run authored (the author id of the editor's versions is the run's TypeID), 0 when it made none.
-- A run that made a version did work the creator can keep, whether or not a preview was rendered.
-- name: AgentRunTimelineVersion :one
SELECT COALESCE(max(version), 0)::integer AS version
FROM timeline_versions
WHERE workspace_id = @workspace_id AND author_kind = 'agent' AND author_id = @author_id;

-- The newest agent-authored version at or before the given one: whose work the version contains. No row: no agent work in it.
-- name: NearestAgentVersion :one
SELECT version, author_id FROM timeline_versions
WHERE workspace_id = @workspace_id AND project_id = @project_id AND author_kind = 'agent' AND version <= @version
ORDER BY version DESC
LIMIT 1;

-- Was the action charged, and not given back.
-- name: ActionIsCharged :one
SELECT EXISTS (
    SELECT 1 FROM credit_ledger c
    WHERE c.workspace_id = @workspace_id AND c.action_id = @action_id AND c.kind = 'charge'
      AND NOT EXISTS (SELECT 1 FROM credit_ledger r WHERE r.workspace_id = c.workspace_id AND r.kind = 'refund' AND r.action_id = c.action_id)
) AS charged;

-- How many runs of the agent the workspace has had finish since the given time that spent money (cost_micros > 0) and ended without a
-- charge: cancelled (a cancel that made a version is charged, so none is left here), succeeded having said there was nothing to change,
-- or failed for one of the given reasons (the creator's own, not ours). Runs that cost nothing, such as one cancelled in the queue, do not count.
-- name: CountUnchargedRuns :one
SELECT count(*)::integer AS runs
FROM agent_runs r
WHERE r.workspace_id = @workspace_id AND r.agent = @agent AND r.finished_at >= @since AND r.cost_micros > 0
  AND (r.status = 'cancelled'
       OR (r.status = 'succeeded' AND r.output ->> 'no_edit_needed' = 'true')
       OR (r.status = 'failed' AND r.failure_reason = ANY(@reasons::text[])))
  AND NOT EXISTS (SELECT 1 FROM credit_ledger c WHERE c.workspace_id = r.workspace_id AND c.action_id = r.id AND c.kind = 'charge');

-- name: GetCreditHoldByAction :one
SELECT * FROM credit_holds WHERE workspace_id = @workspace_id AND action_id = @action_id;

-- The active holds of a workspace, oldest first (the same rule as SumActiveCreditHolds).
-- name: ListActiveCreditHolds :many
SELECT h.id, h.action_id, h.credits, h.frozen_source_id, h.frozen_credits, h.created_at
FROM credit_holds h
WHERE h.workspace_id = @workspace_id
  AND (h.expires_at > @now
       OR EXISTS (SELECT 1 FROM agent_runs r WHERE r.workspace_id = h.workspace_id AND r.id = h.action_id AND r.status IN ('queued', 'running', 'waiting_input')))
ORDER BY h.created_at, h.id;

-- Leaves credits of an expired source frozen for a hold. A hold rests on one source at most.
-- name: FreezeCreditHold :execrows
UPDATE credit_holds SET frozen_source_id = @source_id, frozen_credits = frozen_credits + @credits
WHERE workspace_id = @workspace_id AND id = @id AND credits - frozen_credits >= @credits
  AND (frozen_source_id IS NULL OR frozen_source_id = @source_id);

-- A second hold for the same action (or the same key) inserts nothing; the caller reads the first one.
-- name: InsertCreditHold :one
INSERT INTO credit_holds (workspace_id, action, action_id, credits, project_id, member_user_id, plan, plan_list_version, expires_at, idempotency_key)
VALUES (@workspace_id, @action, @action_id, @credits, sqlc.narg('project_id'), sqlc.narg('member_user_id'), sqlc.narg('plan'), sqlc.narg('plan_list_version'), @expires_at, @idempotency_key)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: DeleteCreditHold :execrows
DELETE FROM credit_holds WHERE workspace_id = @workspace_id AND action_id = @action_id;

-- The heartbeat of the run that owns a hold: the expiry only moves forward, and only when less than renew_below is left (so a
-- heartbeat every few seconds writes about once in ten minutes). A hold that is gone is not recreated.
-- name: ExtendCreditHold :execrows
UPDATE credit_holds SET expires_at = @expires_at
WHERE workspace_id = @workspace_id AND action_id = @action_id AND expires_at < @renew_below AND expires_at < @expires_at;

-- Deletes the holds past their expiry, across workspaces (the sweep), except those of a run that is still active: that run will settle
-- its hold. A hold nobody settles (its run row is gone or finished, or it never had one) goes as soon as it expires; one whose run
-- has been "active" for 30 days past the expiry (stale_before) goes too, so a run that is stuck for good cannot keep credits for ever.
-- name: DeleteExpiredCreditHolds :one
WITH gone AS (
    DELETE FROM credit_holds h
    WHERE h.expires_at <= @now AND h.frozen_credits = 0
      AND (h.expires_at <= @stale_before
           OR NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.workspace_id = h.workspace_id AND r.id = h.action_id AND r.status IN ('queued', 'running', 'waiting_input')))
    RETURNING h.credits
)
SELECT count(*)::bigint AS holds, COALESCE(sum(credits), 0)::bigint AS credits FROM gone;

-- The expired holds that have frozen credits, across workspaces (the sweep handles each under its workspace's lock, so the frozen credits
-- expire with the hold). Same rule as DeleteExpiredCreditHolds.
-- name: ListExpiredCreditHoldsWithFrozen :many
SELECT h.id, h.workspace_id, h.action_id
FROM credit_holds h
WHERE h.expires_at <= @now AND h.frozen_credits > 0
  AND (h.expires_at <= @stale_before
       OR NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.workspace_id = h.workspace_id AND r.id = h.action_id AND r.status IN ('queued', 'running', 'waiting_input')))
ORDER BY h.expires_at, h.id
LIMIT @row_limit;

-- Deletes one expired hold (the rule of DeleteExpiredCreditHolds, checked again under the lock) and returns it.
-- name: DeleteExpiredCreditHoldByID :one
DELETE FROM credit_holds h
WHERE h.workspace_id = @workspace_id AND h.id = @id AND h.expires_at <= @now
  AND (h.expires_at <= @stale_before
       OR NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.workspace_id = h.workspace_id AND r.id = h.action_id AND r.status IN ('queued', 'running', 'waiting_input')))
RETURNING h.*;

-- The sources whose expiry has passed and that have no expire row yet, across workspaces, oldest first (the expiry job).
-- name: ListDueCreditExpiries :many
SELECT s.id, s.workspace_id, s.expires_at
FROM credit_ledger s
WHERE s.credits > 0 AND s.kind IN ('grant_allowance', 'grant_topup', 'adjust') AND s.expires_at IS NOT NULL AND s.expires_at <= @now
  AND NOT EXISTS (SELECT 1 FROM credit_ledger e WHERE e.grant_id = s.id AND e.kind = 'expire')
ORDER BY s.expires_at, s.id
LIMIT @row_limit;

-- The same for one workspace (a settle takes care of its own workspace's expiries before it charges).
-- name: ListDueCreditExpiriesOfWorkspace :many
SELECT s.id, s.expires_at
FROM credit_ledger s
WHERE s.workspace_id = @workspace_id AND s.credits > 0 AND s.kind IN ('grant_allowance', 'grant_topup', 'adjust') AND s.expires_at IS NOT NULL AND s.expires_at <= @now
  AND NOT EXISTS (SELECT 1 FROM credit_ledger e WHERE e.grant_id = s.id AND e.kind = 'expire')
ORDER BY s.expires_at, s.id;

-- A source, with what is left of it.
-- name: GetCreditSource :one
SELECT s.id, s.kind, s.credits AS granted, s.expires_at, s.created_at, s.plan,
       (s.credits + COALESCE(u.used, 0))::integer AS remaining,
       EXISTS (SELECT 1 FROM credit_ledger e WHERE e.grant_id = s.id AND e.kind = 'expire') AS expired
FROM credit_ledger s
LEFT JOIN LATERAL (
    SELECT SUM(c.credits) AS used FROM credit_ledger c WHERE c.grant_id = s.id AND c.workspace_id = s.workspace_id
) u ON true
WHERE s.workspace_id = @workspace_id AND s.id = @id AND s.credits > 0 AND s.kind IN ('grant_allowance', 'grant_topup', 'adjust');

-- Workspaces whose allowance may need granting: they have an active plan, and either no account yet, or an account for another plan,
-- or an allowance period that has ended, or (a plan managed by the merchant of record) a period end that is not the one granted. The
-- job re-checks each one under the account lock; this only narrows the list.
-- name: ListWorkspacesNeedingAllowance :many
SELECT wp.workspace_id
FROM workspace_plans wp
JOIN workspaces w ON w.id = wp.workspace_id AND w.deleted_at IS NULL
LEFT JOIN credit_accounts ca ON ca.workspace_id = wp.workspace_id
WHERE wp.plan <> 'none' AND wp.status = 'active' AND (wp.current_period_end IS NULL OR wp.current_period_end > @now)
  AND (ca.workspace_id IS NULL
       OR ca.plan <> wp.plan
       OR (ca.allowance_until IS NOT NULL AND ca.allowance_until <= @now)
       OR (wp.source = 'merchant' AND wp.plan <> 'trial' AND wp.current_period_end IS NOT NULL AND wp.current_period_end IS DISTINCT FROM ca.allowance_until))
  AND wp.workspace_id > @after
ORDER BY wp.workspace_id
LIMIT @row_limit;

-- The ledger of a workspace, newest first (cfctl credits show).
-- name: ListCreditLedger :many
SELECT * FROM credit_ledger WHERE workspace_id = @workspace_id ORDER BY created_at DESC, id DESC LIMIT @row_limit;
