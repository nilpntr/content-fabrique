-- Queries of the Clerk sync (webhooks and the just-in-time path). Every write is order-safe: it applies only when the
-- Clerk object it comes from is newer than the one that wrote the row (clerk_updated_at), and never to a deleted row.
-- A statement that does not apply returns no row.

-- name: SyncUser :one
INSERT INTO users (clerk_user_id, email, clerk_updated_at)
VALUES (@clerk_user_id, @email, @clerk_updated_at)
ON CONFLICT (clerk_user_id) DO UPDATE SET email = EXCLUDED.email, clerk_updated_at = EXCLUDED.clerk_updated_at
WHERE users.deleted_at IS NULL AND users.clerk_updated_at < EXCLUDED.clerk_updated_at
RETURNING *;

-- name: GetUserByClerkID :one
SELECT * FROM users WHERE clerk_user_id = @clerk_user_id;

-- The email is removed with the account: it is personal data and the user asked Clerk to delete it. An unknown user
-- gets a tombstone, so a user.created that arrives later (out of order) cannot bring the account back.
-- name: DeleteUserByClerkID :one
INSERT INTO users (clerk_user_id, email, deleted_at)
VALUES (@clerk_user_id, '', now())
ON CONFLICT (clerk_user_id) DO UPDATE SET email = '', deleted_at = COALESCE(users.deleted_at, now())
RETURNING *;

-- name: DeleteMembershipsOfUser :exec
DELETE FROM workspace_members WHERE user_id = @user_id;

-- A workspace seen for the first time. No row back means it exists already (SyncWorkspace then decides).
-- name: InsertWorkspaceIfNew :one
INSERT INTO workspaces (clerk_org_id, name, clerk_updated_at)
VALUES (@clerk_org_id, @name, @clerk_updated_at)
ON CONFLICT (clerk_org_id) DO NOTHING
RETURNING *;

-- name: SyncWorkspace :one
INSERT INTO workspaces (clerk_org_id, name, clerk_updated_at)
VALUES (@clerk_org_id, @name, @clerk_updated_at)
ON CONFLICT (clerk_org_id) DO UPDATE SET name = EXCLUDED.name, clerk_updated_at = EXCLUDED.clerk_updated_at
WHERE workspaces.deleted_at IS NULL AND workspaces.clerk_updated_at < EXCLUDED.clerk_updated_at
RETURNING *;

-- An unknown workspace gets a tombstone, as for users.
-- name: DeleteWorkspaceByClerkOrgID :one
INSERT INTO workspaces (clerk_org_id, name, deleted_at)
VALUES (@clerk_org_id, '', now())
ON CONFLICT (clerk_org_id) DO UPDATE SET deleted_at = COALESCE(workspaces.deleted_at, now())
RETURNING *;

-- name: DeleteMembershipsOfWorkspace :exec
DELETE FROM workspace_members WHERE workspace_id = @workspace_id;

-- The role comes from Clerk, from a verified token or a signed webhook, and nowhere else.
-- name: SyncWorkspaceMember :one
INSERT INTO workspace_members (workspace_id, user_id, role, clerk_updated_at)
VALUES (@workspace_id, @user_id, @role, @clerk_updated_at)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role, clerk_updated_at = EXCLUDED.clerk_updated_at
WHERE workspace_members.clerk_updated_at < EXCLUDED.clerk_updated_at
RETURNING *;

-- name: GetWorkspaceMember :one
SELECT * FROM workspace_members WHERE workspace_id = @workspace_id AND user_id = @user_id;

-- name: DeleteWorkspaceMember :exec
DELETE FROM workspace_members WHERE workspace_id = @workspace_id AND user_id = @user_id;

-- One round trip for the authenticated request: the user and the workspace of a verified token, and the user's
-- membership in it. No row means the user or the workspace is not synced yet.
-- name: ResolveSession :one
SELECT u.id AS user_id, u.email, u.deleted_at AS user_deleted_at,
       w.id AS workspace_id, w.name AS workspace_name, w.deleted_at AS workspace_deleted_at,
       m.role AS member_role, m.clerk_updated_at AS member_clerk_updated_at
FROM users u
CROSS JOIN workspaces w
LEFT JOIN workspace_members m ON m.user_id = u.id AND m.workspace_id = w.id
WHERE u.clerk_user_id = @clerk_user_id AND w.clerk_org_id = @clerk_org_id;

-- name: RecordWebhookEvent :execrows
INSERT INTO webhook_events (svix_id, event_type, subject, clerk_event_at, status, detail)
VALUES (@svix_id, @event_type, @subject, @clerk_event_at, @status, @detail)
ON CONFLICT (svix_id) DO NOTHING;

-- Was the membership of this pair deleted at or after the given time?
-- name: MembershipDeletedSince :one
SELECT EXISTS (
    SELECT 1 FROM webhook_events
    WHERE event_type = 'organizationMembership.deleted' AND status = 'applied' AND subject = @subject AND clerk_event_at >= @clerk_event_at
);

-- "<clerk org id>/<clerk user id>" of a membership, the key of its tombstones.
-- name: GetMembershipSubject :one
SELECT (w.clerk_org_id || '/' || u.clerk_user_id)::text AS subject
FROM workspaces w, users u
WHERE w.id = @workspace_id AND u.id = @user_id;

-- Serialises everything that creates or removes the membership of one pair until the transaction ends, so a delete
-- webhook and a just-in-time sync cannot interleave between the tombstone check and the insert.
-- name: LockMembership :exec
SELECT pg_advisory_xact_lock(hashtextextended(@subject::text, 0));

-- The webhook delivery log, trimmed by the webhook_events_prune job. Membership deletes that were applied are the
-- tombstones MembershipDeletedSince reads, so they live longer than the rest (the cutoffs are the job's).
-- name: PruneWebhookEvents :execrows
DELETE FROM webhook_events
WHERE CASE
          WHEN event_type = 'organizationMembership.deleted' AND status = 'applied' THEN received_at < @tombstone_before
          ELSE received_at < @other_before
      END;
