-- The creator's style in the app (P6-05b, docs/design/style-profile-v1.md section 11): reference uploads, the AI-written summary and
-- the settings the creator controls.
--
-- assets.purpose: what an asset is for. `footage` (every asset so far) is a clip the editor may cut; `style_reference` is one of the
--               creator's own finished videos, uploaded only so that their style can be measured. A style reference is never footage:
--               the library does not list it, a project cannot use it, and its ingest does not run the quick look, the transcript or
--               the clip analysis. It is always a video.
--
-- style_references: one row per reference video that was (or is being) measured.
--   asset_id:   the reference's asset; NULL for a file measured by `cfctl style analyze` from this machine (development only), which
--               has no asset and keeps only its numbers.
--   status:     queued (waiting for the analysis job), analysing, ready (measures holds the result) or failed (failure_reason says
--               why, a code that is safe to show).
--   measures:   the styleprofile.Measures of the reference: numbers and the title text read from the picture (untrusted text). No media.
--   detail:     what the analysis did (the title read's outcome, the model, what it cost), for support.
-- A reference asset that is deleted soft keeps its row until the style service removes it; deleting the workspace deletes both.
--
-- creator_style_profiles.summary: the AI-written description of the style and the settings it proposes (styleprofile.Summary), with its
--               prompt version; NULL until one was written. reference_ids: the style_references the profile was made from, so the app
--               can tell a profile that is up to date from one that is still being learnt.
--
-- creator_style_settings: the settings the creator chose (styleprofile.Settings: the vibe, transitions, text animation, camera motion,
--               the title's colour, position and case, music and pace). Kept apart from the measures on purpose: learning the style
--               again never overwrites a choice of the creator's. A workspace may have settings and no profile (it picked a vibe and
--               uploaded nothing).
--
-- style_learn_passes: one row per "learn again" the creator started. The API counts a workspace's rows of the UTC day, under the
--               workspace's style lock, against the plan's style_relearns_per_day: every pass measures each reference again (a sandbox
--               pass and a metered title read) and writes one summary, so the passes are capped like the editor runs that end
--               without a charge.

-- +goose Up

ALTER TABLE assets
    ADD COLUMN purpose text NOT NULL DEFAULT 'footage',
    ADD CONSTRAINT assets_purpose_check CHECK (purpose IN ('footage', 'style_reference')),
    ADD CONSTRAINT assets_purpose_kind_check CHECK (purpose = 'footage' OR kind = 'video');

CREATE TABLE style_references (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id   uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    asset_id       uuid,
    name           text        NOT NULL,
    status         text        NOT NULL DEFAULT 'queued',
    failure_reason text,
    measures       jsonb,
    detail         jsonb       NOT NULL DEFAULT '{}',
    analysed_at    timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT style_references_asset_key UNIQUE (asset_id),
    CONSTRAINT style_references_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT style_references_status_check CHECK (status IN ('queued', 'analysing', 'ready', 'failed')),
    CONSTRAINT style_references_ready_check CHECK (status <> 'ready' OR measures IS NOT NULL),
    CONSTRAINT style_references_name_check CHECK (char_length(name) BETWEEN 1 AND 255),
    CONSTRAINT style_references_measures_check CHECK (measures IS NULL OR (jsonb_typeof(measures) = 'object' AND octet_length(measures::text) <= 262144)),
    CONSTRAINT style_references_detail_check CHECK (jsonb_typeof(detail) = 'object' AND octet_length(detail::text) <= 8192)
);

CREATE INDEX style_references_workspace_idx ON style_references (workspace_id, id);

CREATE TRIGGER style_references_set_updated_at BEFORE UPDATE ON style_references
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('style_references');

ALTER TABLE creator_style_profiles
    ADD COLUMN summary       jsonb,
    ADD COLUMN reference_ids uuid[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT creator_style_profiles_summary_check CHECK (summary IS NULL OR (jsonb_typeof(summary) = 'object' AND octet_length(summary::text) <= 16384));

CREATE TABLE creator_style_settings (
    workspace_id uuid        PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    settings     jsonb       NOT NULL DEFAULT '{}',
    updated_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT creator_style_settings_check CHECK (jsonb_typeof(settings) = 'object' AND octet_length(settings::text) <= 4096)
);

CREATE TRIGGER creator_style_settings_set_updated_at BEFORE UPDATE ON creator_style_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('creator_style_settings');

CREATE TABLE style_learn_passes (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    started_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX style_learn_passes_workspace_idx ON style_learn_passes (workspace_id, created_at);

SELECT cf_grant_app_dml('style_learn_passes');

-- +goose Down

DROP TABLE style_learn_passes;
DROP TABLE creator_style_settings;
ALTER TABLE creator_style_profiles
    DROP CONSTRAINT creator_style_profiles_summary_check,
    DROP COLUMN reference_ids,
    DROP COLUMN summary;
DROP TABLE style_references;
ALTER TABLE assets
    DROP CONSTRAINT assets_purpose_kind_check,
    DROP CONSTRAINT assets_purpose_check,
    DROP COLUMN purpose;
