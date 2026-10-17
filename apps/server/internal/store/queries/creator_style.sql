-- The creator style profile (P6-05). EVERY query filters by workspace_id.

-- name: GetCreatorStyle :one
SELECT * FROM creator_style_profiles WHERE workspace_id = @workspace_id;

-- name: UpsertCreatorStyle :one
INSERT INTO creator_style_profiles (workspace_id, version, profile, measures, references_count)
VALUES (@workspace_id, @version, @profile, @measures, @references_count)
ON CONFLICT (workspace_id) DO UPDATE
SET version = EXCLUDED.version, profile = EXCLUDED.profile, measures = EXCLUDED.measures, references_count = EXCLUDED.references_count
RETURNING *;

-- name: DeleteCreatorStyle :exec
DELETE FROM creator_style_profiles WHERE workspace_id = @workspace_id;
