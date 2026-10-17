-- Plans and the paywall (P0-16). A workspace without a row has no plan.

-- The entitlements of one (not deleted) workspace. "active" is decided here, with the database clock, at the moment of
-- the read: plan is not none, status is active, and the period has not ended (a null end never ends). Missing row:
-- plan 'none', status 'none', source 'none'. No row at all (no such workspace, or a deleted one) is pgx.ErrNoRows.
-- name: GetWorkspacePlan :one
SELECT w.id AS workspace_id,
       COALESCE(p.plan, 'none')::text AS plan,
       COALESCE(p.status, 'none')::text AS status,
       COALESCE(p.source, 'none')::text AS source,
       p.current_period_end AS current_period_end,
       COALESCE(p.granted_by, '')::text AS granted_by,
       (p.workspace_id IS NOT NULL
           AND p.plan <> 'none'
           AND p.status = 'active'
           AND (p.current_period_end IS NULL OR p.current_period_end > now()))::boolean AS active
FROM workspaces w
LEFT JOIN workspace_plans p ON p.workspace_id = w.id
WHERE w.id = @workspace_id AND w.deleted_at IS NULL;

-- Sets the plan of a workspace (insert or replace). The row is keyed by the workspace, so one workspace's grant can
-- never touch another's. A row whose source is not 'manual' (the merchant of record owns it) is left alone unless
-- @force: then no row is affected, and the caller reports a conflict.
-- name: UpsertWorkspacePlan :execrows
INSERT INTO workspace_plans (workspace_id, plan, status, source, current_period_end, granted_by)
VALUES (@workspace_id, @plan, @status, @source, @current_period_end, @granted_by)
ON CONFLICT (workspace_id) DO UPDATE
SET plan = EXCLUDED.plan,
    status = EXCLUDED.status,
    source = EXCLUDED.source,
    current_period_end = EXCLUDED.current_period_end,
    granted_by = EXCLUDED.granted_by
WHERE workspace_plans.source = 'manual' OR @force::boolean;

-- The (not deleted) users a person could be: the one Clerk user id, else every user with that e-mail address (any case).
-- name: FindPeople :many
SELECT u.id, u.clerk_user_id
FROM users u
WHERE u.deleted_at IS NULL
  AND u.clerk_user_id = @person
UNION
SELECT u.id, u.clerk_user_id
FROM users u
WHERE u.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM users c WHERE c.deleted_at IS NULL AND c.clerk_user_id = @person)
  AND u.email <> '' AND lower(u.email) = lower(@person)
ORDER BY 2;

-- The workspaces of one user, with enough to tell them apart: when it was made, the user's role, how many members.
-- Deleted workspaces are left out.
-- name: ListWorkspacesOfUser :many
SELECT w.id, w.name, w.clerk_org_id, w.created_at, m.role,
       (SELECT count(*) FROM workspace_members c WHERE c.workspace_id = w.id)::int AS member_count
FROM workspace_members m
JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = @user_id AND w.deleted_at IS NULL
ORDER BY w.created_at, w.id;
