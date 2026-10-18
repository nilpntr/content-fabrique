-- The music analysis of an asset (P6-06, D-34, docs/design/audio-v1.md section 7).
--
-- A creator gives an edit a song: an audio file they upload (an asset of kind audio), or the sound of one of their videos. The
-- `analyze` job measures it once, without a model (package music: the tempo and a beat grid with downbeats and phrases, the strong
-- onsets, the drop and the loudness), and the editor cuts to it.
--
-- asset_music: one row per analysed asset (an audio asset always; a video asset when its sound is music).
--   version:            the version of the analysis code (music.Version); an older one is made again.
--   source_ingested_at: the assets.ingested_at of the file it was made from: an asset that is ingested again is analysed again.
--   analysis:           the music.Analysis document (numbers only: times in ms, a tempo, loudness). It holds no media and no text.
--
-- Tenancy as for the other asset tables: the composite foreign key ties the row to the workspace of its asset, every query filters by
-- workspace_id, and deleting the asset deletes the row.

-- +goose Up

CREATE TABLE asset_music (
    workspace_id       uuid        NOT NULL,
    asset_id           uuid        NOT NULL,
    version            integer     NOT NULL,
    source_ingested_at timestamptz,
    analysis           jsonb       NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_id),
    CONSTRAINT asset_music_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT asset_music_analysis_check CHECK (jsonb_typeof(analysis) = 'object' AND octet_length(analysis::text) <= 262144)
);

CREATE TRIGGER asset_music_set_updated_at BEFORE UPDATE ON asset_music
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('asset_music');

-- +goose Down

DROP TABLE asset_music;
