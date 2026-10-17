-- Assets and their multipart uploads (P1-01). EVERY query below filters by workspace_id: a row of another workspace is
-- never returned or changed (the repository turns "no rows" into ErrNotFound, so it is a 404 and not a 403). The two
-- exceptions, marked SYSTEM, are for the clean-up job, which works across workspaces and then acts per row with the
-- row's own workspace_id.

-- The next asset id, so the object key (which contains it) is known before anything is written. The database makes the id,
-- as for every table (uuidv7()).
-- name: NewAssetID :one
SELECT uuidv7()::uuid AS id;

-- name: CreateAsset :one
INSERT INTO assets (id, workspace_id, kind, filename, content_type, size_bytes, sha256, title, r2_key, created_by)
VALUES (@id, @workspace_id, @kind, @filename, @content_type, @size_bytes, @sha256, @title, @r2_key, @created_by)
RETURNING *;

-- name: CreateUpload :one
INSERT INTO uploads (workspace_id, asset_id, r2_upload_id, part_size, part_count, expires_at)
VALUES (@workspace_id, @asset_id, @r2_upload_id, @part_size, @part_count, @expires_at)
RETURNING *;

-- A (not deleted) asset of this workspace.
-- name: GetAsset :one
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL;

-- name: GetUploadByAsset :one
SELECT * FROM uploads
WHERE workspace_id = @workspace_id AND asset_id = @asset_id;

-- The library page: newest first (UUIDv7 ids sort by creation time), after the cursor when there is one.
-- name: ListAssets :many
SELECT * FROM assets
WHERE workspace_id = @workspace_id
  AND deleted_at IS NULL
  AND (sqlc.narg('before')::uuid IS NULL OR id < sqlc.narg('before')::uuid)
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

-- The asset a deletion job is about: it must be deleted already, so a job can never remove the objects of a live asset.
-- name: GetDeletedAsset :one
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NOT NULL;

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
