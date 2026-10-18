-- The music analysis of assets (P6-06). EVERY query filters by workspace_id.

-- name: UpsertAssetMusic :one
INSERT INTO asset_music (workspace_id, asset_id, version, source_ingested_at, analysis)
VALUES (@workspace_id, @asset_id, @version, @source_ingested_at, @analysis)
ON CONFLICT (asset_id) DO UPDATE
SET version = EXCLUDED.version, source_ingested_at = EXCLUDED.source_ingested_at, analysis = EXCLUDED.analysis
WHERE asset_music.workspace_id = EXCLUDED.workspace_id
RETURNING asset_id;

-- name: GetAssetMusicMeta :one
SELECT version, source_ingested_at FROM asset_music
WHERE workspace_id = @workspace_id AND asset_id = @asset_id;

-- name: ListAssetMusic :many
SELECT asset_id, version, analysis FROM asset_music
WHERE workspace_id = @workspace_id AND asset_id = ANY(@asset_ids::uuid[])
ORDER BY asset_id;

-- name: DeleteAssetMusic :exec
DELETE FROM asset_music WHERE workspace_id = @workspace_id AND asset_id = @asset_id;
