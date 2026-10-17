-- The creator style profile (P6-05, docs/design/style-profile-v1.md).
--
-- creator_style_profiles: one row per workspace, the profile of the creator's finished videos (what their cuts, text, effects, look and
--               sound are like), measured from reference videos the creator gave us and aggregated.
--   version:    the version of the shape of profile (styleprofile.ProfileVersion); a reader checks it before it reads the rest.
--   profile:    the aggregated Profile (medians and ranges per dimension, with a confidence): what the editor reads.
--   measures:   the measures of each reference as a jsonb array (styleprofile.Measures), so the profile can be aggregated again, one
--               reference can be removed, and the evidence is there to show. It holds the creator's file names and the title text read
--               from the picture, both untrusted text; it holds no media.
--   references_count: how many references the profile was made from (the length of measures).
-- Deleting the workspace deletes the profile; the references themselves are not stored (this row is all that is derived from them).

-- +goose Up

CREATE TABLE creator_style_profiles (
    workspace_id     uuid        PRIMARY KEY,
    version          integer     NOT NULL,
    profile          jsonb       NOT NULL,
    measures         jsonb       NOT NULL DEFAULT '[]',
    references_count integer     NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT creator_style_profiles_workspace_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
    CONSTRAINT creator_style_profiles_profile_check CHECK (jsonb_typeof(profile) = 'object'),
    CONSTRAINT creator_style_profiles_measures_check CHECK (jsonb_typeof(measures) = 'array'),
    CONSTRAINT creator_style_profiles_size_check CHECK (references_count >= 0 AND octet_length(profile::text) <= 262144 AND octet_length(measures::text) <= 1048576)
);

CREATE TRIGGER creator_style_profiles_set_updated_at BEFORE UPDATE ON creator_style_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('creator_style_profiles');

-- +goose Down

DROP TABLE creator_style_profiles;
