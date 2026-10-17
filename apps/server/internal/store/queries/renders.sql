-- Renders (P3-06). EVERY query filters by workspace_id: a row of another workspace is never returned or changed (the repository
-- turns "no rows" into ErrNotFound, so it is a 404 and not a 403). A render of a deleted project is no row for the API
-- (GetRender, ListRenders), and the worker cannot claim or finish it.

-- Serialises the requesters of renders in one workspace until the end of the transaction, so counting the renders in flight and
-- inserting a new one cannot interleave with another requester (the cap is exact). A transaction-level lock works behind a
-- transaction pooler.
-- name: LockWorkspaceRenders :exec
SELECT pg_advisory_xact_lock(hashtextextended('renders:' || @workspace_id::text, 0));

-- name: CreateRender :one
INSERT INTO renders (workspace_id, project_id, timeline_version, profile, requested_by_kind, requested_by_id)
VALUES (@workspace_id, @project_id, @timeline_version, @profile, @requested_by_kind, @requested_by_id)
RETURNING *;

-- The render a new request for the same version and profile reuses: waiting, running or ready.
-- name: GetActiveRender :one
SELECT * FROM renders
WHERE workspace_id = @workspace_id AND project_id = @project_id AND timeline_version = @timeline_version AND profile = @profile
  AND status IN ('queued', 'rendering', 'ready');

-- name: GetRender :one
SELECT r.*
FROM renders r
JOIN projects p ON p.workspace_id = r.workspace_id AND p.id = r.project_id AND p.deleted_at IS NULL
WHERE r.workspace_id = @workspace_id AND r.id = @id;

-- A project's renders, newest first, after the cursor when there is one, optionally of one version and/or one profile.
-- name: ListRenders :many
SELECT r.*
FROM renders r
JOIN projects p ON p.workspace_id = r.workspace_id AND p.id = r.project_id AND p.deleted_at IS NULL
WHERE r.workspace_id = @workspace_id AND r.project_id = @project_id
  AND (sqlc.narg('before')::uuid IS NULL OR r.id < sqlc.narg('before')::uuid)
  AND (sqlc.narg('timeline_version')::integer IS NULL OR r.timeline_version = sqlc.narg('timeline_version')::integer)
  AND (sqlc.narg('profile')::text IS NULL OR r.profile = sqlc.narg('profile')::text)
ORDER BY r.id DESC
LIMIT @row_limit;

-- The renders of the workspace that are waiting or running: the cap on what one workspace may have in flight.
-- name: CountRendersInFlight :one
SELECT count(*)::integer FROM renders
WHERE workspace_id = @workspace_id AND status IN ('queued', 'rendering');

-- The job takes the render: queued -> rendering. A retry finds it rendering already and starts over. A ready or failed render
-- is no row (a duplicate job has nothing to do), nor is one of a deleted project.
-- name: ClaimRender :one
UPDATE renders
SET status = 'rendering', progress = 0, stage = 'staging', started_at = now(), failure_reason = NULL
FROM projects p
WHERE renders.workspace_id = @workspace_id AND renders.id = @id AND renders.status IN ('queued', 'rendering')
  AND p.workspace_id = renders.workspace_id AND p.id = renders.project_id AND p.deleted_at IS NULL
RETURNING renders.*;

-- Progress for the API. No row: the render is not running any more (finished, failed, or its project was deleted), which is how
-- the job learns that it can stop.
-- name: SetRenderProgress :one
UPDATE renders SET progress = @progress::smallint, stage = @stage::text
FROM projects p
WHERE renders.workspace_id = @workspace_id AND renders.id = @id AND renders.status = 'rendering'
  AND p.workspace_id = renders.workspace_id AND p.id = renders.project_id AND p.deleted_at IS NULL
RETURNING renders.id;

-- rendering -> ready with the files. No row: the render failed meanwhile, or its project was deleted while the job worked.
-- name: FinishRender :one
UPDATE renders
SET status = 'ready', progress = 100, stage = NULL, finished_at = now(), failure_reason = NULL, objects_purged_at = NULL,
    mp4_key = @mp4_key, contact_sheet_key = @contact_sheet_key, time_map_key = @time_map_key,
    duration_ms = @duration_ms, width = @width, height = @height, size_bytes = @size_bytes,
    contact_sheet_bytes = @contact_sheet_bytes, time_map_bytes = @time_map_bytes,
    contact_sheet = @contact_sheet, sound_cues = @sound_cues, cpu_ms = @cpu_ms, wall_ms = @wall_ms
FROM projects p
WHERE renders.workspace_id = @workspace_id AND renders.id = @id AND renders.status = 'rendering'
  AND p.workspace_id = renders.workspace_id AND p.id = renders.project_id AND p.deleted_at IS NULL
RETURNING renders.*;

-- queued|rendering -> failed with a user-safe reason. No row: it finished or failed already.
-- name: FailRender :one
UPDATE renders
SET status = 'failed', stage = NULL, failure_reason = @failure_reason::text, finished_at = now(),
    wall_ms = sqlc.narg('wall_ms'), cpu_ms = sqlc.narg('cpu_ms')
WHERE workspace_id = @workspace_id AND id = @id AND status IN ('queued', 'rendering')
RETURNING *;

-- The footage a render may read: the project's own ready assets, with the key of each original and of its 720p proxy (null for
-- an audio file, which has none). The worker resolves every asset of the timeline against this list and nothing else.
-- name: ListRenderInputs :many
SELECT a.id AS asset_id, a.kind, a.size_bytes, a.r2_key, a.has_audio, a.duration_ms,
       d.r2_key AS proxy_key, d.size_bytes AS proxy_size_bytes
FROM project_assets pa
JOIN assets a ON a.workspace_id = pa.workspace_id AND a.id = pa.asset_id
LEFT JOIN asset_derivatives d ON d.workspace_id = a.workspace_id AND d.asset_id = a.id AND d.kind = 'proxy_720'
WHERE pa.workspace_id = @workspace_id AND pa.project_id = @project_id
  AND a.status = 'ready' AND a.deleted_at IS NULL AND a.duration_ms IS NOT NULL;

-- Renders whose objects the clean-up removes: a failed render (an attempt may have left files behind) and a ready render of a deleted
-- project, while their objects are not marked purged. Across workspaces: the clean-up acts for none in particular.
-- name: ListRendersToPurge :many
SELECT r.*
FROM renders r
JOIN projects p ON p.workspace_id = r.workspace_id AND p.id = r.project_id
WHERE r.objects_purged_at IS NULL AND (r.status = 'failed' OR (r.status = 'ready' AND p.deleted_at IS NOT NULL))
ORDER BY r.id
LIMIT @row_limit;

-- Renders that were queued or rendering before the given time: the sweeper looks for the ones whose job is gone. Across workspaces.
-- name: ListStaleRenders :many
SELECT * FROM renders
WHERE status IN ('queued', 'rendering') AND created_at < @before::timestamptz
ORDER BY id
LIMIT @row_limit;

-- name: MarkRenderObjectsPurged :exec
UPDATE renders SET objects_purged_at = now()
WHERE workspace_id = @workspace_id AND id = @id AND objects_purged_at IS NULL;
