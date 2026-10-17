-- Transcripts (P2-05). EVERY query filters by workspace_id, like the asset queries.

-- Moves the asset's transcript status and clears the failure reason. No row: the asset is deleted or not in this workspace.
-- name: SetTranscriptStatus :one
UPDATE assets SET transcript_status = @status::text, transcript_failure_reason = NULL
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
RETURNING id;

-- The transcript failed, with a reason code, but only for the ingest the job was made for and only while it is pending: a late
-- failure of an old job must not mark the transcript of a newer ingest, nor overwrite a transcript another job has finished meanwhile.
-- No row: the asset is gone, has been ingested again since, or its transcript is not pending.
-- name: FailTranscript :one
UPDATE assets SET transcript_status = 'failed', transcript_failure_reason = @reason::text
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
  AND ingested_at IS NOT DISTINCT FROM sqlc.narg('source_ingested_at')::timestamptz
  AND transcript_status = 'pending'
RETURNING id;

-- SaveTranscript locks the asset row first: two transcriptions of one asset (an old ingest's and a new one's) save one after the
-- other, and the one made from an ingest that is no longer the asset's is refused.
-- name: GetAssetForTranscriptSave :one
SELECT * FROM assets
WHERE workspace_id = @workspace_id AND id = @id AND deleted_at IS NULL
FOR UPDATE;

-- name: UpsertTranscript :one
INSERT INTO transcripts (workspace_id, asset_id, source_ingested_at, speech, language, words, structure, provider, model, audio_ms, speech_ms)
VALUES (@workspace_id, @asset_id, sqlc.narg('source_ingested_at')::timestamptz, @speech, @language, @words, @structure, @provider, @model, @audio_ms, @speech_ms)
ON CONFLICT (asset_id) DO UPDATE
SET source_ingested_at = EXCLUDED.source_ingested_at, speech = EXCLUDED.speech, language = EXCLUDED.language, words = EXCLUDED.words,
    structure = EXCLUDED.structure, provider = EXCLUDED.provider, model = EXCLUDED.model, audio_ms = EXCLUDED.audio_ms,
    speech_ms = EXCLUDED.speech_ms
RETURNING *;

-- An asset that no longer has audio has no transcript.
-- name: DeleteTranscript :exec
DELETE FROM transcripts WHERE workspace_id = @workspace_id AND asset_id = @asset_id;

-- The transcript of a live asset of this workspace.
-- name: GetTranscript :one
SELECT t.* FROM transcripts t
JOIN assets a ON a.workspace_id = t.workspace_id AND a.id = t.asset_id
WHERE t.workspace_id = @workspace_id AND t.asset_id = @asset_id AND a.deleted_at IS NULL;

-- Audio the workspace has had transcribed in the window [from, to), from the ledger: every call counts, whoever transcribed it
-- and whether or not a transcript came of it.
-- name: SumTranscribedMs :one
SELECT COALESCE(sum(audio_ms), 0)::bigint AS audio_ms
FROM usage_ledger
WHERE workspace_id = @workspace_id AND kind = 'transcription' AND created_at >= @from_time AND created_at < @to_time;
