-- name: GetCreatorProfile :one
SELECT * FROM creator_profiles WHERE workspace_id = @workspace_id;

-- name: UpsertCreatorProfile :one
INSERT INTO creator_profiles (workspace_id, niche, audience_regions, tone, language, brand, defaults)
VALUES (@workspace_id, @niche, @audience_regions, @tone, @language, @brand, @defaults)
ON CONFLICT (workspace_id) DO UPDATE SET
    niche = EXCLUDED.niche,
    audience_regions = EXCLUDED.audience_regions,
    tone = EXCLUDED.tone,
    language = EXCLUDED.language,
    brand = EXCLUDED.brand,
    defaults = EXCLUDED.defaults
RETURNING *;
