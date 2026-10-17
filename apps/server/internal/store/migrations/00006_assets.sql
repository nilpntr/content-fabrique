-- Assets and their resumable multipart uploads (P1-01, architecture sections 4 and 5, D-24).
--
-- assets:  one row per file a creator uploads (video, audio or image). The object lives in R2 under r2_key, which the
--          server derives from the workspace and asset ids (ws/{workspace}/assets/{asset}/original) and never takes from
--          a client. status: uploading -> uploaded -> processing -> ready, or failed (failure_reason says why). A delete
--          is soft (deleted_at); a job removes the objects afterwards. The ingest columns (duration, codecs ...) come with
--          P1-04.
-- uploads: the multipart state of an asset, one row per asset (a new upload of the same file is a new asset). It lives
--          next to the asset so a reload can resume: part_size and part_count say how the file is cut, r2_upload_id is
--          R2's own id (never shown to a client), expires_at ends the upload (24 hours; a job aborts what is past it).
--          Exactly one of three states: active (both timestamps null), completed or aborted.
--
-- Tenancy: both tables carry workspace_id, every query filters by it, and uploads reference assets through
-- (workspace_id, asset_id), so an upload row can never point at an asset of another workspace.

-- +goose Up

CREATE TABLE assets (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id   uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind           text        NOT NULL,
    status         text        NOT NULL DEFAULT 'uploading',
    filename       text        NOT NULL,
    content_type   text        NOT NULL,
    size_bytes     bigint      NOT NULL,
    sha256         text,
    title          text        NOT NULL,
    description    text        NOT NULL DEFAULT '',
    tags           text[]      NOT NULL DEFAULT '{}',
    r2_key         text        NOT NULL,
    failure_reason text,
    created_by     uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,
    -- The target of uploads' composite foreign key. (id alone is unique already; the pair is what ties a child row to
    -- its workspace.)
    CONSTRAINT assets_workspace_id_id_key UNIQUE (workspace_id, id),
    CONSTRAINT assets_r2_key_key UNIQUE (r2_key),
    CONSTRAINT assets_kind_check CHECK (kind IN ('video', 'audio', 'image')),
    CONSTRAINT assets_status_check CHECK (status IN ('uploading', 'uploaded', 'processing', 'ready', 'failed')),
    CONSTRAINT assets_filename_check CHECK (octet_length(filename) BETWEEN 1 AND 255),
    CONSTRAINT assets_content_type_check CHECK (octet_length(content_type) BETWEEN 1 AND 255),
    CONSTRAINT assets_size_bytes_check CHECK (size_bytes > 0),
    CONSTRAINT assets_sha256_check CHECK (sha256 IS NULL OR sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT assets_title_check CHECK (char_length(title) BETWEEN 1 AND 255)
);

-- The library listing: one workspace, newest first (ids are UUIDv7, so id order is creation order), without deleted rows.
CREATE INDEX assets_workspace_listing_idx ON assets (workspace_id, id DESC) WHERE deleted_at IS NULL;

CREATE TRIGGER assets_set_updated_at BEFORE UPDATE ON assets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('assets');

CREATE TABLE uploads (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid        NOT NULL,
    asset_id     uuid        NOT NULL,
    r2_upload_id text        NOT NULL,
    part_size    bigint      NOT NULL,
    part_count   integer     NOT NULL,
    expires_at   timestamptz NOT NULL,
    completed_at timestamptz,
    aborted_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uploads_asset_key UNIQUE (asset_id),
    CONSTRAINT uploads_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT uploads_workspace_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
    CONSTRAINT uploads_part_size_check CHECK (part_size > 0),
    CONSTRAINT uploads_part_count_check CHECK (part_count BETWEEN 1 AND 10000),
    CONSTRAINT uploads_state_check CHECK (completed_at IS NULL OR aborted_at IS NULL)
);

-- The clean-up job looks for active uploads past their expiry, across workspaces; the cap on uploads in progress counts
-- the active ones of one workspace.
CREATE INDEX uploads_active_expiry_idx ON uploads (expires_at, id) WHERE completed_at IS NULL AND aborted_at IS NULL;
CREATE INDEX uploads_active_workspace_idx ON uploads (workspace_id) WHERE completed_at IS NULL AND aborted_at IS NULL;

CREATE TRIGGER uploads_set_updated_at BEFORE UPDATE ON uploads
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('uploads');

-- +goose Down

DROP TABLE uploads;
DROP TABLE assets;
