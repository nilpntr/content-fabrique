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
