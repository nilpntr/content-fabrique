-- The creator style profile (P6-05) and the style in the app (P6-05b): the profile, the reference videos it is learnt from and the
-- settings the creator chose. EVERY query filters by workspace_id: a row of another workspace is never returned or changed.

-- name: GetCreatorStyle :one
SELECT * FROM creator_style_profiles WHERE workspace_id = @workspace_id;

-- name: UpsertCreatorStyle :one
INSERT INTO creator_style_profiles (workspace_id, version, profile, measures, references_count, summary, reference_ids)
VALUES (@workspace_id, @version, @profile, @measures, @references_count, sqlc.narg('summary')::jsonb, @reference_ids::uuid[])
ON CONFLICT (workspace_id) DO UPDATE
SET version = EXCLUDED.version, profile = EXCLUDED.profile, measures = EXCLUDED.measures, references_count = EXCLUDED.references_count,
    summary = EXCLUDED.summary, reference_ids = EXCLUDED.reference_ids
RETURNING *;

-- name: DeleteCreatorStyle :exec
DELETE FROM creator_style_profiles WHERE workspace_id = @workspace_id;

-- ---- reference videos ---------------------------------------------------------------------------------------------------------

-- The style reference assets of a workspace that are not deleted, oldest first.
-- name: ListStyleReferenceAssets :many
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND purpose = 'style_reference' AND deleted_at IS NULL
ORDER BY id;

-- Every measured or measuring reference of a workspace, oldest first (asset references and the files of cfctl).
-- name: ListStyleReferences :many
SELECT * FROM style_references WHERE workspace_id = @workspace_id ORDER BY id;

-- The reference row of an asset.
-- name: GetStyleReferenceByAsset :one
SELECT * FROM style_references WHERE workspace_id = @workspace_id AND asset_id = @asset_id;

-- A reference asset is ready to be measured (or measured again): its row is queued. The composite foreign key refuses an asset of
-- another workspace.
-- name: QueueStyleReference :one
INSERT INTO style_references (workspace_id, asset_id, name, status)
VALUES (@workspace_id, @asset_id, @name, 'queued')
ON CONFLICT (asset_id) DO UPDATE SET status = 'queued', failure_reason = NULL, name = EXCLUDED.name
WHERE style_references.workspace_id = EXCLUDED.workspace_id
RETURNING *;

-- Every asset reference of the workspace goes back to the queue (learning the style again).
-- name: RequeueStyleReferences :many
UPDATE style_references SET status = 'queued', failure_reason = NULL
WHERE workspace_id = @workspace_id AND asset_id IS NOT NULL
RETURNING *;

-- The analysis of a reference started. No row: it was removed meanwhile.
-- name: StartStyleReference :one
UPDATE style_references SET status = 'analysing', failure_reason = NULL
WHERE workspace_id = @workspace_id AND asset_id = @asset_id
RETURNING *;

-- name: FinishStyleReference :one
UPDATE style_references SET status = 'ready', failure_reason = NULL, measures = @measures, detail = @detail, analysed_at = now()
WHERE workspace_id = @workspace_id AND asset_id = @asset_id
RETURNING *;

-- name: FailStyleReference :one
UPDATE style_references SET status = 'failed', failure_reason = @failure_reason, detail = @detail
WHERE workspace_id = @workspace_id AND asset_id = @asset_id
RETURNING *;

-- A file measured by cfctl: no asset, ready at once.
-- name: InsertFileStyleReference :one
INSERT INTO style_references (workspace_id, asset_id, name, status, measures, detail, analysed_at)
VALUES (@workspace_id, NULL, @name, 'ready', @measures, @detail, now())
RETURNING *;

-- name: DeleteFileStyleReferences :exec
DELETE FROM style_references WHERE workspace_id = @workspace_id AND asset_id IS NULL;

-- name: DeleteStyleReferenceByAsset :exec
DELETE FROM style_references WHERE workspace_id = @workspace_id AND asset_id = @asset_id;

-- name: DeleteStyleReference :exec
DELETE FROM style_references WHERE workspace_id = @workspace_id AND id = @id;

-- name: DeleteStyleReferences :exec
DELETE FROM style_references WHERE workspace_id = @workspace_id;

-- Soft-deletes every reference asset of the workspace (resetting the style), and returns them so their objects can be removed.
-- name: SoftDeleteStyleReferenceAssets :many
UPDATE assets SET deleted_at = now()
WHERE workspace_id = @workspace_id AND purpose = 'style_reference' AND deleted_at IS NULL
RETURNING *;

-- ---- learning again ------------------------------------------------------------------------------------------------------------

-- Serialises a workspace's "learn again" (the in-flight check and the day's count hold however many requests race).
-- name: LockWorkspaceStyle :exec
SELECT pg_advisory_xact_lock(hashtextextended('style:' || @workspace_id::text, 0));

-- The references of a workspace that are being measured: queued or analysing, with an asset that is ready and not deleted, and a row
-- that changed after @since (a row older than that belongs to a job that gave up, and holds nothing back).
-- name: CountStyleReferencesInFlight :one
SELECT count(*) FROM style_references r
JOIN assets a ON a.workspace_id = r.workspace_id AND a.id = r.asset_id
WHERE r.workspace_id = @workspace_id AND r.status IN ('queued', 'analysing') AND r.updated_at > @since
  AND a.deleted_at IS NULL AND a.status = 'ready';

-- name: CountStyleLearnPasses :one
SELECT count(*) FROM style_learn_passes WHERE workspace_id = @workspace_id AND created_at >= @since;

-- name: InsertStyleLearnPass :exec
INSERT INTO style_learn_passes (workspace_id, started_by) VALUES (@workspace_id, sqlc.narg('started_by')::uuid);

-- ---- the creator's settings ---------------------------------------------------------------------------------------------------

-- name: GetCreatorStyleSettings :one
SELECT * FROM creator_style_settings WHERE workspace_id = @workspace_id;

-- name: UpsertCreatorStyleSettings :one
INSERT INTO creator_style_settings (workspace_id, settings, updated_by)
VALUES (@workspace_id, @settings, sqlc.narg('updated_by')::uuid)
ON CONFLICT (workspace_id) DO UPDATE SET settings = EXCLUDED.settings, updated_by = EXCLUDED.updated_by
RETURNING *;

-- name: DeleteCreatorStyleSettings :exec
DELETE FROM creator_style_settings WHERE workspace_id = @workspace_id;
