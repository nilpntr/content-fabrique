-- Adding an existing member changes the role.
-- name: AddWorkspaceMember :one
INSERT INTO workspace_members (workspace_id, user_id, role)
VALUES (@workspace_id, @user_id, @role)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role
RETURNING *;

-- name: ListWorkspaceMembers :many
SELECT m.workspace_id, m.user_id, m.role, m.created_at, u.clerk_user_id, u.email
FROM workspace_members m
JOIN users u ON u.id = m.user_id
WHERE m.workspace_id = @workspace_id
ORDER BY m.created_at, m.user_id;
