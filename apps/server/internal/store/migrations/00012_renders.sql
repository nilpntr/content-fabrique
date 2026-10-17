-- Renders (P3-06, architecture section 4, design note timeline-v1 section 6, D-24): one run of one version of a project's
-- timeline through a profile (preview from the proxies, final from the originals), and where its files are.
--
-- timeline_version: the version number of timeline_versions (immutable). The foreign key is (workspace, project, version), so
--                   a render can only point at a version of its own project in its own workspace.
-- profile:          preview | final (internal/render profiles).
-- status:           queued -> rendering -> ready | failed. Only a queued or rendering row has a River job behind it.
-- progress, stage:  0..100 and what the job is doing (staging, measuring, rendering, contact_sheet, uploading); the worker
--                   writes them while it works and the API shows them.
-- mp4_key, contact_sheet_key, time_map_key: object keys under ws/{workspace}/projects/{project}/renders/{render}/ (the server
--                   builds them from the ids; storage.RenderKey). Set together when the render is ready, never before.
-- duration_ms, width, height, size_bytes: of the MP4 (the plan's length and size, the file's size). contact_sheet_bytes and
--                   time_map_bytes are the sizes of the other two files.
-- contact_sheet:    the layout of the sheet: {columns, tile_width, tile_height, tiles: [{index, out_ms, item_id?, kind}]}, so a
--                   reader can map a tile to output time and from there, through time_map, to a timeline item.
-- sound_cues:       the cue_only sounds of the plan (D-03): never mixed in, handed to the creator by the export.
-- wall_ms, cpu_ms:  wall_ms is how long the attempt that made the render took (staging, render, contact sheet, upload). cpu_ms
--                   is the CPU time its sandbox runs were allotted (their wall time x the sandbox's CPU limit), not a
--                   measurement: the providers do not report CPU use.
-- requested_by_kind/id: 'user' with a usr_ TypeID, or 'agent' with the run id. Text, like timeline_versions.author_id.
-- objects_purged_at: set by the clean-up job once the objects of a render of a deleted project are removed.
--
-- One render per version and profile at a time: the partial unique index lets a second request find the first (dedupe) while
-- a failed render does not block a new attempt.
--
-- Grants: the app role may SELECT, INSERT and UPDATE (no DELETE): a render row goes only with its project, through the owner's
-- cascade. Tenancy: the composite foreign keys carry workspace_id and every query filters by it.

-- +goose Up

-- The target of the render's foreign key.
ALTER TABLE timeline_versions
    ADD CONSTRAINT timeline_versions_workspace_project_version_key UNIQUE (workspace_id, project_id, version);

CREATE TABLE renders (
    id                 uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id       uuid        NOT NULL,
    project_id         uuid        NOT NULL,
    timeline_version   integer     NOT NULL,
    profile            text        NOT NULL,
    status             text        NOT NULL DEFAULT 'queued',
    progress           smallint    NOT NULL DEFAULT 0,
    stage              text,
    mp4_key            text,
    contact_sheet_key  text,
    time_map_key       text,
    duration_ms        bigint,
    width              integer,
    height             integer,
    size_bytes         bigint,
    contact_sheet_bytes bigint,
    time_map_bytes     bigint,
    contact_sheet      jsonb,
    sound_cues         jsonb       NOT NULL DEFAULT '[]',
    cpu_ms             bigint,
    wall_ms            bigint,
    failure_reason     text,
    requested_by_kind  text        NOT NULL,
    requested_by_id    text        NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    started_at         timestamptz,
    finished_at        timestamptz,
    objects_purged_at  timestamptz,
    -- The target of later foreign keys (exports).
    CONSTRAINT renders_workspace_id_id_key UNIQUE (workspace_id, id),
    CONSTRAINT renders_project_fkey FOREIGN KEY (workspace_id, project_id)
        REFERENCES projects (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT renders_version_fkey FOREIGN KEY (workspace_id, project_id, timeline_version)
        REFERENCES timeline_versions (workspace_id, project_id, version) ON DELETE CASCADE,
    CONSTRAINT renders_workspace_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
    CONSTRAINT renders_version_check CHECK (timeline_version >= 1),
    CONSTRAINT renders_profile_check CHECK (profile IN ('preview', 'final')),
    CONSTRAINT renders_status_check CHECK (status IN ('queued', 'rendering', 'ready', 'failed')),
    CONSTRAINT renders_progress_check CHECK (progress BETWEEN 0 AND 100),
    CONSTRAINT renders_stage_check CHECK (stage IS NULL OR char_length(stage) BETWEEN 1 AND 32),
    CONSTRAINT renders_keys_check CHECK (
        coalesce(char_length(mp4_key), 0) <= 400 AND coalesce(char_length(contact_sheet_key), 0) <= 400
        AND coalesce(char_length(time_map_key), 0) <= 400),
    CONSTRAINT renders_sizes_check CHECK (
        (duration_ms IS NULL OR duration_ms >= 0) AND (size_bytes IS NULL OR size_bytes >= 0)
        AND (contact_sheet_bytes IS NULL OR contact_sheet_bytes >= 0) AND (time_map_bytes IS NULL OR time_map_bytes >= 0)
        AND (cpu_ms IS NULL OR cpu_ms >= 0) AND (wall_ms IS NULL OR wall_ms >= 0)),
    -- Ready means everything is there; the files are set together with the status.
    CONSTRAINT renders_ready_check CHECK (status <> 'ready' OR (
        progress = 100 AND mp4_key IS NOT NULL AND contact_sheet_key IS NOT NULL AND time_map_key IS NOT NULL
        AND duration_ms IS NOT NULL AND size_bytes IS NOT NULL AND contact_sheet_bytes IS NOT NULL AND time_map_bytes IS NOT NULL
        AND contact_sheet IS NOT NULL AND failure_reason IS NULL)),
    CONSTRAINT renders_failed_check CHECK (status <> 'failed' OR failure_reason IS NOT NULL),
    CONSTRAINT renders_failure_reason_check CHECK (failure_reason IS NULL OR char_length(failure_reason) BETWEEN 1 AND 64),
    CONSTRAINT renders_sound_cues_check CHECK (jsonb_typeof(sound_cues) = 'array'),
    CONSTRAINT renders_contact_sheet_check CHECK (contact_sheet IS NULL OR jsonb_typeof(contact_sheet) = 'object'),
    CONSTRAINT renders_requested_by_kind_check CHECK (requested_by_kind IN ('user', 'agent')),
    CONSTRAINT renders_requested_by_id_check CHECK (char_length(requested_by_id) BETWEEN 1 AND 128)
);

-- Dedupe: a version has at most one render per profile that is waiting, running or ready.
CREATE UNIQUE INDEX renders_active_key ON renders (project_id, timeline_version, profile)
    WHERE status IN ('queued', 'rendering', 'ready');

-- A project's renders, newest first (UUIDv7 ids sort by creation time).
CREATE INDEX renders_project_listing_idx ON renders (workspace_id, project_id, id DESC);

-- The per-workspace cap on renders in flight.
CREATE INDEX renders_in_flight_idx ON renders (workspace_id) WHERE status IN ('queued', 'rendering');

-- The clean-up of the objects of deleted projects.
CREATE INDEX renders_unpurged_idx ON renders (workspace_id, project_id) WHERE objects_purged_at IS NULL;

CREATE TRIGGER renders_set_updated_at BEFORE UPDATE ON renders
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Not cf_grant_app_dml: no DELETE (see above).
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON TABLE renders TO %I', public.cf_app_role());
END
$$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE renders;
ALTER TABLE timeline_versions DROP CONSTRAINT timeline_versions_workspace_project_version_key;
