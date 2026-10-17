-- Annotations (P1-08). EVERY query filters by workspace_id: a row of another workspace is never returned or changed (the repository
-- turns "no rows" into ErrNotFound, so it is a 404 and not a 403).
--
-- A row is visible when it is not deleted and its scope is alive: the project of a render or version annotation is not deleted, the
-- asset of an asset annotation is not deleted. The conditions are written out in each query (a view would hide them from the
-- review of the tenancy).

-- The version a tl_ id names, with its project (not deleted): the target of a comment on a version.
-- name: GetTimelineVersionRef :one
SELECT v.id, v.project_id, v.version
FROM timeline_versions v
JOIN projects p ON p.workspace_id = v.workspace_id AND p.id = v.project_id AND p.deleted_at IS NULL
WHERE v.workspace_id = @workspace_id AND v.id = @id;

-- name: CreateAnnotation :one
INSERT INTO annotations (workspace_id, target_type, target_id, project_id, timeline_version, start_ms, end_ms, pin, kind, body, parent_id, author_kind, author_id)
-- project_id and parent_id: the nil uuid means none (the typed ids cannot be nil), as for agent_runs.project_id.
VALUES (@workspace_id, @target_type, @target_id, NULLIF(@project_id::uuid, '00000000-0000-0000-0000-000000000000'::uuid), sqlc.narg('timeline_version'), @start_ms, @end_ms, sqlc.narg('pin'), @kind, @body, NULLIF(@parent_id::uuid, '00000000-0000-0000-0000-000000000000'::uuid), @author_kind, @author_id)
RETURNING *;

-- name: GetAnnotation :one
SELECT a.*
FROM annotations a
WHERE a.workspace_id = @workspace_id AND a.id = @id AND a.deleted_at IS NULL
  AND (a.project_id IS NULL OR EXISTS (
        SELECT 1 FROM projects p WHERE p.workspace_id = a.workspace_id AND p.id = a.project_id AND p.deleted_at IS NULL))
  AND (a.target_type <> 'asset' OR EXISTS (
        SELECT 1 FROM assets s WHERE s.workspace_id = a.workspace_id AND s.id = a.target_id AND s.deleted_at IS NULL));

-- The same, locked until the end of the transaction: an edit and a delete of one comment queue on it.
-- name: GetAnnotationForUpdate :one
SELECT a.*
FROM annotations a
WHERE a.workspace_id = @workspace_id AND a.id = @id AND a.deleted_at IS NULL
  AND (a.project_id IS NULL OR EXISTS (
        SELECT 1 FROM projects p WHERE p.workspace_id = a.workspace_id AND p.id = a.project_id AND p.deleted_at IS NULL))
  AND (a.target_type <> 'asset' OR EXISTS (
        SELECT 1 FROM assets s WHERE s.workspace_id = a.workspace_id AND s.id = a.target_id AND s.deleted_at IS NULL))
FOR UPDATE OF a;

-- The comments (and replies) on one target, oldest first, after the cursor when there is one.
-- name: ListAnnotationsByTarget :many
SELECT a.*
FROM annotations a
WHERE a.workspace_id = @workspace_id AND a.target_type = @target_type AND a.target_id = @target_id AND a.deleted_at IS NULL
  AND (sqlc.narg('after')::uuid IS NULL OR a.id > sqlc.narg('after')::uuid)
  AND (a.project_id IS NULL OR EXISTS (
        SELECT 1 FROM projects p WHERE p.workspace_id = a.workspace_id AND p.id = a.project_id AND p.deleted_at IS NULL))
  AND (a.target_type <> 'asset' OR EXISTS (
        SELECT 1 FROM assets s WHERE s.workspace_id = a.workspace_id AND s.id = a.target_id AND s.deleted_at IS NULL))
ORDER BY a.id
LIMIT @row_limit;

-- Every comment (and reply) on the renders and versions of one project, oldest first, after the cursor when there is one.
-- name: ListAnnotationsByProject :many
SELECT a.*
FROM annotations a
JOIN projects p ON p.workspace_id = a.workspace_id AND p.id = a.project_id AND p.deleted_at IS NULL
WHERE a.workspace_id = @workspace_id AND a.project_id = @project_id AND a.deleted_at IS NULL
  AND (sqlc.narg('after')::uuid IS NULL OR a.id > sqlc.narg('after')::uuid)
ORDER BY a.id
LIMIT @row_limit;

-- Writes every field an edit may change. The service decides the new values from the locked row.
-- name: UpdateAnnotation :one
UPDATE annotations
SET start_ms = @start_ms, end_ms = @end_ms, pin = sqlc.narg('pin'), kind = @kind, body = @body, status = @status,
    applied_in_run_id = sqlc.narg('applied_in_run_id'), addressed_in_version = sqlc.narg('addressed_in_version')
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
RETURNING *;

-- Soft delete, replies included.
-- name: SoftDeleteAnnotation :execrows
UPDATE annotations SET deleted_at = now()
WHERE workspace_id = @workspace_id AND (id = @id OR parent_id = @id) AND deleted_at IS NULL;

-- A replying comment moves with its parent: when the parent's range is edited the replies follow, so a thread has one range.
-- name: MoveAnnotationReplies :exec
UPDATE annotations SET start_ms = @start_ms, end_ms = @end_ms
WHERE workspace_id = @workspace_id AND parent_id = @parent_id AND deleted_at IS NULL;

-- ---- The editor agent --------------------------------------------------------------------------------------------

-- The open comments of a project's renders and versions that an editor run has to deal with, oldest first: the top-level ones that
-- are open (a reply is part of its thread).
-- name: ListOpenAnnotationsForProject :many
SELECT a.*
FROM annotations a
JOIN projects p ON p.workspace_id = a.workspace_id AND p.id = a.project_id AND p.deleted_at IS NULL
WHERE a.workspace_id = @workspace_id AND a.project_id = @project_id AND a.deleted_at IS NULL
  AND a.parent_id IS NULL AND a.status = 'open'
ORDER BY a.id
LIMIT @row_limit;

-- The newest ready render of a version, whatever its profile (a preview and a final of one version have the same time map).
-- name: GetReadyRenderOfVersion :one
SELECT r.*
FROM renders r
JOIN projects p ON p.workspace_id = r.workspace_id AND p.id = r.project_id AND p.deleted_at IS NULL
WHERE r.workspace_id = @workspace_id AND r.project_id = @project_id AND r.timeline_version = @timeline_version AND r.status = 'ready'
ORDER BY r.id DESC
LIMIT 1;

-- An editor run finished: the revision requests (comments and instructions) that were open when the run started become applied,
-- by this run, in the version it made (null when it made none). A kind that states a standing constraint (include, exclude,
-- prefer) stays open. "When the run started" is the run's creation time, so a comment written during the run is not marked: the run
-- was not given it.
-- name: MarkAnnotationsApplied :many
UPDATE annotations a
SET status = 'applied', applied_in_run_id = @run_id, addressed_in_version = sqlc.narg('version')::integer
WHERE a.workspace_id = @workspace_id AND a.project_id = @project_id AND a.deleted_at IS NULL
  AND a.parent_id IS NULL AND a.status = 'open' AND a.kind IN ('comment', 'instruction')
  AND a.created_at <= (SELECT r.created_at FROM agent_runs r WHERE r.workspace_id = @workspace_id AND r.id = @run_id)
  AND EXISTS (SELECT 1 FROM projects p WHERE p.workspace_id = a.workspace_id AND p.id = a.project_id AND p.deleted_at IS NULL)
RETURNING a.*;

-- The editor's short answer under each comment it dealt with.
-- name: CreateAgentReplies :exec
INSERT INTO annotations (workspace_id, target_type, target_id, project_id, timeline_version, start_ms, end_ms, kind, body, parent_id, author_kind, author_id)
SELECT a.workspace_id, a.target_type, a.target_id, a.project_id, a.timeline_version, a.start_ms, a.end_ms, 'comment', @body::text, a.id, 'agent', @author_id::text
FROM annotations a
WHERE a.workspace_id = @workspace_id AND a.id = ANY(@parent_ids::uuid[]) AND a.deleted_at IS NULL;
