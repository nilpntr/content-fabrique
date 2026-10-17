-- Transcripts (P2-05). EVERY query filters by workspace_id, like the asset queries.

-- Moves the asset's transcript status. No row: the asset is deleted or not in this workspace.
-- name: SetTranscriptStatus :one
UPDATE assets SET transcript_status = @status::text
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
RETURNING id;

-- name: UpsertTranscript :one
INSERT INTO transcripts (workspace_id, asset_id, speech, language, words, structure, provider, model, audio_ms, speech_ms)
VALUES (@workspace_id, @asset_id, @speech, @language, @words, @structure, @provider, @model, @audio_ms, @speech_ms)
ON CONFLICT (asset_id) DO UPDATE
SET speech = EXCLUDED.speech, language = EXCLUDED.language, words = EXCLUDED.words, structure = EXCLUDED.structure,
    provider = EXCLUDED.provider, model = EXCLUDED.model, audio_ms = EXCLUDED.audio_ms, speech_ms = EXCLUDED.speech_ms
RETURNING *;

-- The transcript of a live asset of this workspace.
-- name: GetTranscript :one
SELECT t.* FROM transcripts t
JOIN assets a ON a.workspace_id = t.workspace_id AND a.id = t.asset_id
WHERE t.workspace_id = @workspace_id AND t.asset_id = @asset_id AND a.deleted_at IS NULL;
