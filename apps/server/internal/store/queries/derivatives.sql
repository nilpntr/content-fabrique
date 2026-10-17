-- Ingest (P1-04). EVERY query filters by workspace_id, like the asset queries: the job acts with the workspace of its
-- arguments, and a row of another workspace is no row.

-- The ingest job takes the asset: uploaded -> processing. A retry finds it processing already, and a re-run finds it ready;
-- all three start over. A failed or deleted asset is no row: the job gives up.
-- name: ClaimAssetForIngest :one
UPDATE assets
SET status = 'processing', failure_reason = NULL, ingest_progress = 0, ingest_stage = 'starting'
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
  AND status IN ('uploaded', 'processing', 'ready')
RETURNING *;

-- Progress for the library. No row: the asset is not processing any more (deleted, failed or finished by someone else).
-- name: SetIngestProgress :one
UPDATE assets SET ingest_progress = @progress::smallint, ingest_stage = @stage::text
WHERE workspace_id = @workspace_id AND id = @id AND status = 'processing' AND deleted_at IS NULL
RETURNING id;

-- processing -> ready with what the probe found. No row: the asset was deleted (or failed) while the job worked.
-- name: FinishAssetIngest :one
UPDATE assets
SET status = 'ready', failure_reason = NULL, ingest_progress = 100, ingest_stage = NULL, ingested_at = now(),
    duration_ms = @duration_ms::bigint, width = NULLIF(@width::integer, 0), height = NULLIF(@height::integer, 0), fps = @fps::double precision,
    rotation = @rotation::integer, video_codec = sqlc.narg('video_codec')::text, audio_codec = sqlc.narg('audio_codec')::text,
    has_audio = @has_audio::boolean, hdr = @hdr::boolean, probe = @probe::jsonb
WHERE workspace_id = @workspace_id AND id = @id AND status = 'processing' AND deleted_at IS NULL
RETURNING *;

-- uploaded|processing -> failed with a user-safe reason; the asset stays in the library so the user sees why. No row: it
-- was deleted or already finished.
-- name: FailAssetIngest :one
UPDATE assets
SET status = 'failed', failure_reason = @failure_reason::text, ingest_stage = NULL
WHERE workspace_id = @workspace_id AND id = @id AND status IN ('uploaded', 'processing') AND deleted_at IS NULL
RETURNING *;

-- name: UpsertAssetDerivative :one
INSERT INTO asset_derivatives (workspace_id, asset_id, kind, r2_key, content_type, size_bytes, meta)
VALUES (@workspace_id, @asset_id, @kind, @r2_key, @content_type, @size_bytes, @meta)
ON CONFLICT (asset_id, kind) DO UPDATE
SET r2_key = EXCLUDED.r2_key, content_type = EXCLUDED.content_type, size_bytes = EXCLUDED.size_bytes, meta = EXCLUDED.meta
WHERE asset_derivatives.workspace_id = EXCLUDED.workspace_id
RETURNING *;

-- The derivatives a re-run no longer produces (a clip that lost its audio track, say).
-- name: DeleteStaleAssetDerivatives :exec
DELETE FROM asset_derivatives
WHERE workspace_id = @workspace_id AND asset_id = @asset_id AND NOT (kind = ANY(@keep::text[]));

-- name: ListAssetDerivatives :many
SELECT * FROM asset_derivatives
WHERE workspace_id = @workspace_id AND asset_id = ANY(@asset_ids::uuid[])
ORDER BY asset_id, kind;

-- A failed asset has no derivatives: the rows go with the failure (the ingest removes the objects).
-- name: DeleteAssetDerivatives :exec
DELETE FROM asset_derivatives WHERE workspace_id = @workspace_id AND asset_id = @asset_id;

-- The quick look (P1-04b). EVERY query filters by workspace_id like the rest.

-- Inside the transaction that marks a video ready: the quick look is now owed. No row: it is not a video, or the quick look
-- has run already (succeeded or failed), so no job is enqueued.
-- name: MarkQuickLookPending :one
UPDATE assets SET quick_look_status = 'pending'
WHERE workspace_id = @workspace_id AND id = @id AND kind = 'video' AND deleted_at IS NULL
  AND (quick_look_status IS NULL OR quick_look_status = 'pending')
RETURNING *;

-- The quick look succeeded. The title changes only while it is not the user's; the description and tags are the quick look's.
-- One statement, so a title edited at the same moment wins: the CASE sees the row as it is when the update applies.
-- name: ApplyQuickLook :one
UPDATE assets
SET title = CASE WHEN title_source = 'user' THEN title ELSE @title::text END,
    title_source = CASE WHEN title_source = 'user' THEN title_source ELSE 'quicklook' END,
    description = @description::text, tags = @tags::text[],
    quick_look_status = 'succeeded', quick_look = @detail::jsonb, quick_look_at = now()
WHERE workspace_id = @workspace_id AND id = @id AND status = 'ready' AND deleted_at IS NULL
RETURNING *;

-- The quick look failed: nothing but the status and the detail (an error code, the frames if they were cut) is written. Any
-- state of the asset: a job that gave up waiting for an asset that never became ready marks it too.
-- name: FailQuickLook :one
UPDATE assets SET quick_look_status = 'failed', quick_look = @detail::jsonb, quick_look_at = now()
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
RETURNING *;

-- The asset failed its ingest (or the job could not run for a day) while its quick look was owed: nothing is owed any more.
-- No row: the quick look was not pending.
-- name: SkipQuickLook :one
UPDATE assets SET quick_look_status = 'skipped'
WHERE workspace_id = @workspace_id AND id = @id AND quick_look_status = 'pending' AND deleted_at IS NULL
RETURNING *;

-- PATCH /v1/assets/{id}: a person edited the title, which the quick look never overwrites after this.
-- name: UpdateAssetTitle :one
UPDATE assets SET title = @title::text, title_source = 'user'
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
RETURNING *;

-- The ready videos of a workspace for cfctl's backfill, oldest first; only those without a successful quick look unless include_done.
-- name: ListAssetsForQuickLook :many
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND kind = 'video' AND status = 'ready' AND deleted_at IS NULL
  AND (@include_done::boolean OR quick_look_status IS DISTINCT FROM 'succeeded')
ORDER BY id
LIMIT @row_limit;

-- Where the person is in the sampled frames of several videos (the quick look's "subjects" member), for the editor's checks and the
-- compiler's title placement. A video with none is not in the result.
-- name: ListAssetSubjects :many
SELECT id AS asset_id, (quick_look -> 'subjects')::jsonb AS subjects FROM assets
WHERE workspace_id = @workspace_id AND id = ANY(@asset_ids::uuid[]) AND status = 'ready' AND deleted_at IS NULL
  AND quick_look_status = 'succeeded' AND (quick_look -> 'subjects') IS NOT NULL;
