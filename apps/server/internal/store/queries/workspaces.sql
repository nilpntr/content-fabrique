-- The id comes from the column default, uuidv7() (Postgres 18).
-- name: CreateWorkspace :one
INSERT INTO workspaces (clerk_org_id, name, plan, region, billing_mode)
VALUES (@clerk_org_id, @name, @plan, @region, @billing_mode)
RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = @id;

-- The entry point that turns the active Clerk organization into our workspace id. It is the one lookup that is not
-- already scoped by a WorkspaceID.
-- name: GetWorkspaceByClerkOrgID :one
SELECT * FROM workspaces WHERE clerk_org_id = @clerk_org_id;
