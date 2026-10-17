-- Plans and the paywall (P0-16). A workspace without a row has no plan.

-- The entitlements of one workspace. "active" is decided here, with the database clock, at the moment of the read:
-- plan is not none, status is active, and the period has not ended (a null end never ends). Missing row: plan 'none'.
-- name: GetWorkspacePlan :one
SELECT w.id AS workspace_id,
       COALESCE(p.plan, 'none')::text AS plan,
       COALESCE(p.status, 'none')::text AS status,
       COALESCE(p.source, 'manual')::text AS source,
       p.current_period_end AS current_period_end,
       COALESCE(p.granted_by, '')::text AS granted_by,
       (p.workspace_id IS NOT NULL
           AND p.plan <> 'none'
           AND p.status = 'active'
           AND (p.current_period_end IS NULL OR p.current_period_end > now()))::boolean AS active
FROM workspaces w
LEFT JOIN workspace_plans p ON p.workspace_id = w.id
WHERE w.id = @workspace_id;

-- Sets the plan of a workspace (insert or replace). The row is keyed by the workspace, so one workspace's grant can
-- never touch another's.
-- name: UpsertWorkspacePlan :exec
INSERT INTO workspace_plans (workspace_id, plan, status, source, current_period_end, granted_by)
VALUES (@workspace_id, @plan, @status, @source, @current_period_end, @granted_by)
ON CONFLICT (workspace_id) DO UPDATE
SET plan = EXCLUDED.plan,
    status = EXCLUDED.status,
    source = EXCLUDED.source,
    current_period_end = EXCLUDED.current_period_end,
    granted_by = EXCLUDED.granted_by;

-- The workspaces a person belongs to, found by e-mail address (any case) or Clerk user id. Deleted users and
-- workspaces are left out. One row per workspace, however many matching user records are members.
-- name: ListWorkspacesOfPerson :many
SELECT w.id, w.name, w.clerk_org_id
FROM workspaces w
WHERE w.deleted_at IS NULL
  AND EXISTS (
      SELECT 1
      FROM workspace_members m
      JOIN users u ON u.id = m.user_id
      WHERE m.workspace_id = w.id
        AND u.deleted_at IS NULL
        AND (u.clerk_user_id = @person OR (u.email <> '' AND lower(u.email) = lower(@person)))
  )
ORDER BY w.created_at, w.id;

-- Whether any (not deleted) user record matches, so "no such person" can be told apart from "in no workspace".
-- name: PersonExists :one
SELECT EXISTS (
    SELECT 1 FROM users u
    WHERE u.deleted_at IS NULL AND (u.clerk_user_id = @person OR (u.email <> '' AND lower(u.email) = lower(@person)))
)::boolean;
