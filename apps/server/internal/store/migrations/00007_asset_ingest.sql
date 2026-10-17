-- Ingest results and derivatives (P1-04, architecture section 5).
--
-- assets gets what the ingest job learns from the file (probed duration, display size, frame rate, rotation, codecs, whether
-- a decodable audio track exists, HDR) and its progress, which the library shows (and P1-05 streams) while status is
-- `processing`. `probe` keeps ffprobe's normalised result whole (technical properties only, never tags such as location).
--   rotation:     degrees clockwise (0, 90, 180, 270) the stored picture needs to be upright; width and height are already the
--                 upright (display) size.
--   has_audio:    true when a decodable audio stream was found and used. A file whose only audio is undecodable (APAC alone)
--                 is false.
--   ingest_stage: what the job is doing now (downloading, probing, proxy, derivatives, uploading), null when not processing.
--
-- asset_derivatives: one row per generated file of an asset (kind -> object key). A re-run of the ingest replaces the rows of
-- its kinds; the key is ws/{workspace}/assets/{asset}/<name>. Tenancy as for uploads: the composite foreign key ties a row to
-- the workspace of its asset, and every query filters by workspace_id.

-- +goose Up

ALTER TABLE assets
    ADD COLUMN duration_ms     bigint,
    ADD COLUMN width           integer,
    ADD COLUMN height          integer,
    ADD COLUMN fps             double precision,
    ADD COLUMN rotation        integer,
    ADD COLUMN video_codec     text,
    ADD COLUMN audio_codec     text,
    ADD COLUMN has_audio       boolean,
    ADD COLUMN hdr             boolean,
    ADD COLUMN probe           jsonb,
    ADD COLUMN ingest_progress smallint    NOT NULL DEFAULT 0,
    ADD COLUMN ingest_stage    text,
    ADD COLUMN ingested_at     timestamptz,
    ADD CONSTRAINT assets_ingest_progress_check CHECK (ingest_progress BETWEEN 0 AND 100),
    ADD CONSTRAINT assets_rotation_check CHECK (rotation IS NULL OR rotation IN (0, 90, 180, 270)),
    ADD CONSTRAINT assets_duration_check CHECK (duration_ms IS NULL OR duration_ms >= 0),
    ADD CONSTRAINT assets_dimensions_check CHECK ((width IS NULL OR width > 0) AND (height IS NULL OR height > 0));

CREATE TABLE asset_derivatives (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid        NOT NULL,
    asset_id     uuid        NOT NULL,
    kind         text        NOT NULL,
    r2_key       text        NOT NULL,
    content_type text        NOT NULL,
    size_bytes   bigint      NOT NULL,
    meta         jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT asset_derivatives_asset_kind_key UNIQUE (asset_id, kind),
    CONSTRAINT asset_derivatives_r2_key_key UNIQUE (r2_key),
    CONSTRAINT asset_derivatives_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT asset_derivatives_workspace_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
    CONSTRAINT asset_derivatives_kind_check CHECK (kind IN
        ('proxy_720', 'proxy_360', 'poster', 'sprite', 'sprite_vtt', 'waveform', 'audio_16k', 'keyframe', 'hover_preview')),
    CONSTRAINT asset_derivatives_size_check CHECK (size_bytes >= 0)
);

CREATE INDEX asset_derivatives_workspace_asset_idx ON asset_derivatives (workspace_id, asset_id);

CREATE TRIGGER asset_derivatives_set_updated_at BEFORE UPDATE ON asset_derivatives
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('asset_derivatives');

-- +goose Down

DROP TABLE asset_derivatives;
ALTER TABLE assets
    DROP CONSTRAINT assets_dimensions_check,
    DROP CONSTRAINT assets_duration_check,
    DROP CONSTRAINT assets_rotation_check,
    DROP CONSTRAINT assets_ingest_progress_check,
    DROP COLUMN ingested_at,
    DROP COLUMN ingest_stage,
    DROP COLUMN ingest_progress,
    DROP COLUMN probe,
    DROP COLUMN hdr,
    DROP COLUMN has_audio,
    DROP COLUMN audio_codec,
    DROP COLUMN video_codec,
    DROP COLUMN rotation,
    DROP COLUMN fps,
    DROP COLUMN height,
    DROP COLUMN width,
    DROP COLUMN duration_ms;
