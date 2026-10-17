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
SET plan = @plan, period_anchor = @period_anchor, allowance_until = @allowance_until
WHERE workspace_id = @workspace_id;

-- The sources the workspace can use now (not past their expiry) with what is left of each, in the order they are used: the allowance
-- first, then the others, earliest expiry first (a source that never expires, the trial's allowance, last of its kind). A source's
-- credits left are its own credits plus every row that names it (charges and expiries are negative, refunds positive).
-- name: ListCreditSources :many
SELECT s.id, s.kind, s.credits AS granted, s.expires_at, s.created_at, s.plan,
       (s.credits + COALESCE(u.used, 0))::integer AS remaining
FROM credit_ledger s
LEFT JOIN LATERAL (
    SELECT SUM(c.credits) AS used FROM credit_ledger c WHERE c.grant_id = s.id AND c.workspace_id = s.workspace_id
) u ON true
WHERE s.workspace_id = @workspace_id AND s.credits > 0 AND s.kind IN ('grant_allowance', 'grant_topup', 'adjust')
  AND (s.expires_at IS NULL OR s.expires_at > @now)
ORDER BY (s.kind <> 'grant_allowance'), s.expires_at NULLS LAST, s.created_at, s.id;

-- The credits set aside by the holds that still count (past their expiry a hold stops counting, like a spend hold).
-- name: SumActiveCreditHolds :one
SELECT COALESCE(SUM(credits), 0)::integer AS credits
FROM credit_holds
WHERE workspace_id = @workspace_id AND expires_at > @now;

-- One ledger row. A key that is already in the ledger inserts nothing (no row comes back).
-- name: InsertCreditLedger :one
INSERT INTO credit_ledger (
    workspace_id, kind, credits, grant_id, action, action_id, project_id, member_user_id, plan, plan_list_version,
    expires_at, actor, reason, idempotency_key, created_at
) VALUES (
    @workspace_id, @kind, @credits, sqlc.narg('grant_id'), sqlc.narg('action'), sqlc.narg('action_id'), sqlc.narg('project_id'),
    sqlc.narg('member_user_id'), sqlc.narg('plan'), sqlc.narg('plan_list_version'),
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

-- name: GetCreditHoldByAction :one
SELECT * FROM credit_holds WHERE workspace_id = @workspace_id AND action_id = @action_id;

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

-- Deletes the holds past their expiry, across workspaces (the sweep). They had stopped counting already.
-- name: DeleteExpiredCreditHolds :one
WITH gone AS (
    DELETE FROM credit_holds WHERE expires_at <= @now RETURNING credits
)
SELECT count(*)::bigint AS holds, COALESCE(sum(credits), 0)::bigint AS credits FROM gone;

-- The sources whose expiry has passed and that have no expire row yet, across workspaces, oldest first (the expiry job).
-- name: ListDueCreditExpiries :many
SELECT s.id, s.workspace_id, s.expires_at
FROM credit_ledger s
WHERE s.credits > 0 AND s.kind IN ('grant_allowance', 'grant_topup', 'adjust') AND s.expires_at IS NOT NULL AND s.expires_at <= @now
  AND NOT EXISTS (SELECT 1 FROM credit_ledger e WHERE e.grant_id = s.id AND e.kind = 'expire')
ORDER BY s.expires_at, s.id
LIMIT @row_limit;

-- A source, with what is left of it.
-- name: GetCreditSource :one
SELECT s.id, s.kind, s.credits AS granted, s.expires_at, s.created_at, s.plan,
       (s.credits + COALESCE(u.used, 0))::integer AS remaining
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
       OR (wp.source = 'merchant' AND wp.current_period_end IS DISTINCT FROM ca.allowance_until))
ORDER BY wp.workspace_id
LIMIT @row_limit;

-- Does the run have a preview artifact (the creator saw something). The editor records {"kind": "preview"} for each version it renders.
-- name: AgentRunHasPreview :one
SELECT EXISTS (
    SELECT 1 FROM agent_events
    WHERE workspace_id = @workspace_id AND run_id = @run_id AND type = 'artifact' AND payload ->> 'kind' = 'preview'
) AS previewed;

-- The ledger of a workspace, newest first (cfctl credits show).
-- name: ListCreditLedger :many
SELECT * FROM credit_ledger WHERE workspace_id = @workspace_id ORDER BY created_at DESC, id DESC LIMIT @row_limit;
