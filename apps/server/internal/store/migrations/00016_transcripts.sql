-- Transcripts (P2-05, architecture section 4, decision D-27).
--
-- transcripts:  one row per asset that was checked for speech (the transcribe job, after the ingest). The words are the output of
--               the transcription provider, whichever it was (whisper.cpp locally, Workers AI hosted): one provider interface, one
--               shape. A clip with no speech has a row too, with speech = false, an empty language and no words, so "checked, nothing
--               to transcribe" is told apart from "not checked yet" (assets.transcript_status).
--   words:      a jsonb array of {text, start_ms, end_ms, confidence}, in order, times in milliseconds from the start of the asset.
--   structure:  the speech structure computed from the words (D-28): {"sentences": [...], "fillers": [...], "pauses": [...]}, word
--               indexes into words. The editor reads it for jump cuts; it is derived, so a newer rule set can recompute it.
--   source_ingested_at: the assets.ingested_at of the ingest whose audio was transcribed. SaveTranscript locks the asset and
--               refuses a transcript made from an ingest that is no longer the asset's (ErrStaleTranscript), the way SaveAnalysis does.
--   speech_ms:  how much speech the voice-activity check found; audio_ms the length of the audio that was checked.
--   language:   the language the provider detected or was told (ISO 639-1, 'en'); empty when there is no speech.
--
-- assets.transcript_status: none (not checked: no audio, or the ingest has not finished), pending (the job is queued or running),
--               no_speech (checked; the clip has no speech), ready (words are available), failed (the job gave up or was blocked).
-- assets.transcript_failure_reason: why it failed, a code that is safe to show: spend_cap and minutes_cap (the workspace's daily
--               limits, D-27), price_unset (a hosted provider without a verified price), unusable_audio, gave_up. Null otherwise.
--
-- usage_ledger: kind 'transcription' joins 'llm' (D-27). A row holds the audio it metered (audio_ms) and the provider; the token
--               columns are 0. Local transcription costs 0 (list_cost_micros 0, price_table_version 'local').

-- +goose Up

ALTER TABLE assets
    ADD COLUMN transcript_status text NOT NULL DEFAULT 'none',
    ADD COLUMN transcript_failure_reason text,
    ADD CONSTRAINT assets_transcript_status_check CHECK (transcript_status IN ('none', 'pending', 'no_speech', 'ready', 'failed'));

CREATE TABLE transcripts (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid        NOT NULL,
    asset_id     uuid        NOT NULL,
    source_ingested_at timestamptz,
    speech       boolean     NOT NULL,
    language     text        NOT NULL DEFAULT '',
    words        jsonb       NOT NULL DEFAULT '[]',
    structure    jsonb       NOT NULL DEFAULT '{}',
    provider     text        NOT NULL,
    model        text        NOT NULL,
    audio_ms     bigint      NOT NULL,
    speech_ms    bigint      NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT transcripts_asset_key UNIQUE (asset_id),
    CONSTRAINT transcripts_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT transcripts_workspace_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
    CONSTRAINT transcripts_words_check CHECK (jsonb_typeof(words) = 'array'),
    CONSTRAINT transcripts_audio_check CHECK (audio_ms >= 0 AND speech_ms >= 0),
    CONSTRAINT transcripts_speech_check CHECK (speech OR (language = '' AND words = '[]'::jsonb))
);

CREATE TRIGGER transcripts_set_updated_at BEFORE UPDATE ON transcripts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('transcripts');

ALTER TABLE usage_ledger
    ADD COLUMN provider text   NOT NULL DEFAULT '',
    ADD COLUMN audio_ms bigint NOT NULL DEFAULT 0,
    DROP CONSTRAINT usage_ledger_kind_check,
    ADD CONSTRAINT usage_ledger_kind_check CHECK (kind IN ('llm', 'transcription')),
    ADD CONSTRAINT usage_ledger_audio_check CHECK (audio_ms >= 0);

-- +goose Down

-- Rows of the new kind cannot be kept under the old constraint.
DELETE FROM usage_ledger WHERE kind = 'transcription';
ALTER TABLE usage_ledger
    DROP CONSTRAINT usage_ledger_audio_check,
    DROP CONSTRAINT usage_ledger_kind_check,
    DROP COLUMN audio_ms,
    DROP COLUMN provider,
    ADD CONSTRAINT usage_ledger_kind_check CHECK (kind IN ('llm'));

DROP TABLE transcripts;
ALTER TABLE assets
    DROP CONSTRAINT assets_transcript_status_check,
    DROP COLUMN transcript_failure_reason,
    DROP COLUMN transcript_status;
