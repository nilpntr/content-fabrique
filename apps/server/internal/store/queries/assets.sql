-- Assets and their multipart uploads (P1-01). EVERY query below filters by workspace_id: a row of another workspace is
-- never returned or changed (the repository turns "no rows" into ErrNotFound, so it is a 404 and not a 403). The two
-- exceptions, marked SYSTEM, are for the clean-up job, which works across workspaces and then acts per row with the
-- row's own workspace_id.

-- Serialises the creators of uploads in one workspace until the end of the transaction, so counting the uploads in
-- progress and inserting a new one cannot interleave with another creator (the cap is exact). A transaction-level lock
-- works behind a transaction pooler.
-- name: LockWorkspaceUploads :exec
SELECT pg_advisory_xact_lock(hashtextextended('asset-uploads:' || @workspace_id::text, 0));

-- name: CreateAsset :one
INSERT INTO assets (id, workspace_id, kind, filename, content_type, size_bytes, sha256, title, r2_key, created_by, purpose)
VALUES (@id, @workspace_id, @kind, @filename, @content_type, @size_bytes, @sha256, @title, @r2_key, @created_by, @purpose)
RETURNING *;

-- name: CreateUpload :one
INSERT INTO uploads (workspace_id, asset_id, r2_upload_id, part_size, part_count, expires_at)
VALUES (@workspace_id, @asset_id, @r2_upload_id, @part_size, @part_count, @expires_at)
RETURNING *;

-- A (not deleted) asset of this workspace.
-- name: GetAsset :one
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL;

-- The asset whatever its state, deleted ones included. For a request that lost a race with an abort or a delete and has
-- to say so (409) instead of "no such asset" (404).
-- name: GetAssetAnyState :one
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND id = @id;

-- name: GetUploadByAsset :one
SELECT * FROM uploads
WHERE workspace_id = @workspace_id AND asset_id = @asset_id;

-- The library page: newest first (UUIDv7 ids sort by creation time), after the cursor when there is one. With search words
-- (store.SearchWords) only the assets whose title, description, tags or file name match all of them (cf_search_query). Only the
-- assets of one purpose: the library is footage, and a style reference is never listed with it.
-- name: ListAssets :many
SELECT * FROM assets
WHERE workspace_id = @workspace_id
  AND deleted_at IS NULL
  AND purpose = @purpose
  AND (sqlc.narg('before')::uuid IS NULL OR id < sqlc.narg('before')::uuid)
  AND (sqlc.narg('words')::text[] IS NULL OR search @@ cf_search_query(sqlc.narg('words')::text[]))
ORDER BY id DESC
LIMIT @row_limit;

-- Uploads in progress: not completed, not aborted, not past their expiry, and the asset is not deleted.
-- name: CountActiveUploads :one
SELECT count(*)
FROM uploads u
JOIN assets a ON a.workspace_id = u.workspace_id AND a.id = u.asset_id
WHERE u.workspace_id = @workspace_id
  AND u.completed_at IS NULL AND u.aborted_at IS NULL
  AND u.expires_at > now()
  AND a.deleted_at IS NULL;

-- The upload becomes completed. No row: it was not active (completed, aborted, or the asset is not this workspace's).
-- name: MarkUploadCompleted :one
UPDATE uploads SET completed_at = now()
WHERE workspace_id = @workspace_id AND asset_id = @asset_id
  AND completed_at IS NULL AND aborted_at IS NULL
RETURNING *;

-- name: MarkUploadAborted :one
UPDATE uploads SET aborted_at = now()
WHERE workspace_id = @workspace_id AND asset_id = @asset_id
  AND completed_at IS NULL AND aborted_at IS NULL
RETURNING *;

-- uploading -> uploaded. No row: the asset was not uploading (or is deleted).
-- name: MarkAssetUploaded :one
UPDATE assets SET status = 'uploaded'
WHERE workspace_id = @workspace_id AND id = @id AND status = 'uploading' AND deleted_at IS NULL
RETURNING *;

-- uploading -> failed with a reason, and optionally hidden from the library (an aborted upload is: nothing is left of it).
-- name: MarkAssetFailed :one
UPDATE assets
SET status = 'failed',
    failure_reason = @failure_reason,
    deleted_at = CASE WHEN @hide::boolean THEN COALESCE(deleted_at, now()) ELSE deleted_at END
WHERE workspace_id = @workspace_id AND id = @id AND status = 'uploading' AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteAsset :one
UPDATE assets SET deleted_at = now()
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
RETURNING *;

-- The asset an object deletion job is about: it must be deleted, or failed (a failed asset has no object that anyone
-- may use: an expired, aborted or wrong-sized upload), so a job can never remove the objects of a live asset.
-- name: GetAssetForObjectDeletion :one
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND id = @id AND (deleted_at IS NOT NULL OR status = 'failed');

-- SYSTEM: active uploads past their expiry, in every workspace, after the cursor (id order), for the clean-up job.
-- name: ListExpiredUploads :many
SELECT u.id AS upload_id, u.workspace_id, u.asset_id, u.r2_upload_id, a.r2_key
FROM uploads u
JOIN assets a ON a.workspace_id = u.workspace_id AND a.id = u.asset_id
WHERE u.completed_at IS NULL AND u.aborted_at IS NULL
  AND u.expires_at <= @now::timestamptz
  AND u.id > @after
ORDER BY u.id
LIMIT @row_limit;

-- The style references of a workspace that count against its limit: not deleted and not failed (a failed upload or ingest can be
-- removed and does not hold a place).
-- name: CountStyleReferenceAssets :one
SELECT count(*) FROM assets
WHERE workspace_id = @workspace_id AND purpose = 'style_reference' AND deleted_at IS NULL AND status <> 'failed';
