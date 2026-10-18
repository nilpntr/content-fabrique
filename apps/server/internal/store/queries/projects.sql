-- Projects, their footage and their timeline versions (P4-04b, P3-02). EVERY query filters by workspace_id: a row of another
-- workspace is never returned or changed (the repository turns "no rows" into ErrNotFound, so it is a 404 and not a 403).

-- name: CreateProject :one
INSERT INTO projects (workspace_id, title, format, brief, created_by)
VALUES (@workspace_id, @title, @format, @brief, @created_by)
RETURNING *;

-- The footage, in the order given. position counts from 0. The composite foreign keys reject an asset of another workspace.
-- name: AddProjectAssets :exec
INSERT INTO project_assets (workspace_id, project_id, asset_id, position)
SELECT @workspace_id::uuid, @project_id::uuid, a.asset_id, (a.ord - 1)::integer
FROM unnest(@asset_ids::uuid[]) WITH ORDINALITY AS a (asset_id, ord);

-- The (not deleted) assets of this workspace that the ids name, unlocked: the first look, to tell unknown ids from assets that
-- are not ready. Ids of other workspaces and unknown ids simply do not come back.
-- name: ListAssetsForProject :many
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND id = ANY(@asset_ids::uuid[]) AND deleted_at IS NULL;

-- Locks the READY ones until the end of the transaction, so that none is deleted before the project that uses it is stored.
-- Only ready assets are locked: an asset still being ingested is being updated by the worker, which must not wait for a
-- project to be created.
-- name: LockReadyAssetsForProject :many
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND id = ANY(@asset_ids::uuid[]) AND deleted_at IS NULL AND status = 'ready'
FOR SHARE;

-- A (not deleted) project of this workspace.
-- name: GetProject :one
SELECT * FROM projects
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL;

-- The same, locked until the end of the transaction: the writers of a project's timeline queue on it.
-- name: GetProjectForUpdate :one
SELECT * FROM projects
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
FOR UPDATE;

-- The project page: newest first (UUIDv7 ids sort by creation time), after the cursor when there is one.
-- name: ListProjects :many
SELECT p.*,
       (SELECT count(*)
        FROM project_assets pa
        JOIN assets a ON a.workspace_id = pa.workspace_id AND a.id = pa.asset_id
        WHERE pa.workspace_id = p.workspace_id AND pa.project_id = p.id AND a.deleted_at IS NULL)::integer AS asset_count
FROM projects p
WHERE p.workspace_id = @workspace_id
  AND p.deleted_at IS NULL
  AND (sqlc.narg('before')::uuid IS NULL OR p.id < sqlc.narg('before')::uuid)
ORDER BY p.id DESC
LIMIT @row_limit;

-- name: SoftDeleteProject :one
UPDATE projects SET deleted_at = now()
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
RETURNING id;

-- Moves the project to a new version, only if it is still at the parent the version was made from (null for the first): the
-- one check that two writers cannot both pass. No row: the project is gone, or another writer moved it first.
-- name: SetCurrentTimelineVersion :one
UPDATE projects SET current_timeline_version = @version
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
  AND current_timeline_version IS NOT DISTINCT FROM sqlc.narg('parent')::integer
RETURNING *;

-- The footage of a project with what the library knows about each asset. A deleted asset is still listed (the project
-- keeps its place) with deleted set, so the page can say it is gone.
-- name: ListProjectAssets :many
SELECT pa.position, a.id AS asset_id, a.title, a.kind, a.status, a.duration_ms, (a.deleted_at IS NOT NULL)::boolean AS deleted
FROM project_assets pa
JOIN assets a ON a.workspace_id = pa.workspace_id AND a.id = pa.asset_id
WHERE pa.workspace_id = @workspace_id AND pa.project_id = @project_id
ORDER BY pa.position;

-- The assets an edit may use: the project's footage that is ready and not deleted, with what the timeline validator needs.
-- name: ListProjectCatalogAssets :many
SELECT a.id AS asset_id, a.kind, a.title, a.duration_ms
FROM project_assets pa
JOIN assets a ON a.workspace_id = pa.workspace_id AND a.id = pa.asset_id
WHERE pa.workspace_id = @workspace_id AND pa.project_id = @project_id
  AND a.status = 'ready' AND a.deleted_at IS NULL AND a.duration_ms IS NOT NULL;

-- name: InsertTimelineVersion :one
INSERT INTO timeline_versions (workspace_id, project_id, version, parent_version, spec, ops, diff, summary, author_kind, author_id, idempotency_key, result)
VALUES (@workspace_id, @project_id, @version, @parent_version, @spec, @ops, @diff, @summary, @author_kind, @author_id, sqlc.narg('idempotency_key')::text, sqlc.narg('result')::jsonb)
RETURNING *;

-- name: GetTimelineVersion :one
SELECT * FROM timeline_versions
WHERE workspace_id = @workspace_id AND project_id = @project_id AND version = @version;

-- The version list: newest first, without the spec, ops and diff.
-- name: ListTimelineVersions :many
SELECT id, workspace_id, project_id, version, parent_version, summary, author_kind, author_id, created_at
FROM timeline_versions
WHERE workspace_id = @workspace_id AND project_id = @project_id
  AND (sqlc.narg('before')::integer IS NULL OR version < sqlc.narg('before')::integer)
ORDER BY version DESC
LIMIT @row_limit;

-- The footage of a project as the editor agent sees it: the library's title and description (text a creator wrote, untrusted),
-- the probed size and rotation, and whether there is sound. Only ready, not deleted assets with a duration are returned, in
-- the creator's order: the same set the edit catalog holds.
-- name: ListProjectFootage :many
SELECT pa.position, a.id AS asset_id, a.kind, a.title, a.description, a.exercise, a.facets, a.duration_ms, a.width, a.height, a.rotation, a.has_audio
FROM project_assets pa
JOIN assets a ON a.workspace_id = pa.workspace_id AND a.id = pa.asset_id
WHERE pa.workspace_id = @workspace_id AND pa.project_id = @project_id
  AND a.status = 'ready' AND a.deleted_at IS NULL AND a.duration_ms IS NOT NULL
ORDER BY pa.position;

-- The latest version a given author made, if there is one: for the editor agent the author id is the run's TypeID, so this says
-- whether the run has made a version.
-- name: GetTimelineVersionByAuthor :one
SELECT * FROM timeline_versions
WHERE workspace_id = @workspace_id AND project_id = @project_id AND author_kind = @author_kind AND author_id = @author_id
ORDER BY version DESC
LIMIT 1;

-- The version a tool call made, by the call's idempotency key: how a repeated call finds what the first one did.
-- name: GetTimelineVersionByIdempotencyKey :one
SELECT * FROM timeline_versions
WHERE workspace_id = @workspace_id AND project_id = @project_id AND idempotency_key = @idempotency_key;
