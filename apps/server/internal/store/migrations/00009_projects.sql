-- Projects, the footage they were started with, and their timeline versions (P4-04b backend, P3-02 storage; architecture
-- section 4, D-24, D-25).
--
-- projects:          one video job. `format` is the output format chosen when the project was made (D-25); `brief` is the
--                    creator's free-text instructions (at most 4,000 characters). `current_timeline_version` is null until
--                    the first edit creates version 1: a timeline needs at least one clip, so the project starts without
--                    one. A delete is soft (deleted_at).
-- project_assets:    the footage the creator selected, in order. The editor only uses these assets. The composite foreign
--                    keys include workspace_id, so a row can neither point at a project nor at an asset of another
--                    workspace.
-- timeline_versions: the immutable versions of a project's timeline: the full spec, the ops that made it from its parent,
--                    the RFC 6902 diff and its summary lines (internal/timeline/ops.Version). `id` is the tl_ TypeID that
--                    renders will reference. A version is never changed: the app role may only INSERT and SELECT (no
--                    UPDATE, no DELETE), and a trigger refuses UPDATE for every other role as well. Deleting a project's
--                    versions happens only through the foreign key's cascade, which runs with the table owner's rights.
--
-- Tenancy: every table carries workspace_id and every query filters by it.

-- +goose Up

CREATE TABLE projects (
    id                       uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id             uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    title                    text        NOT NULL,
    format                   text        NOT NULL,
    brief                    text        NOT NULL DEFAULT '',
    status                   text        NOT NULL DEFAULT 'draft',
    current_timeline_version integer,
    created_by               uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    deleted_at               timestamptz,
    -- The target of the composite foreign keys below.
    CONSTRAINT projects_workspace_id_id_key UNIQUE (workspace_id, id),
    CONSTRAINT projects_title_check CHECK (char_length(title) BETWEEN 1 AND 255),
    CONSTRAINT projects_format_check CHECK (format IN ('9:16', '1:1', '4:5', '16:9')),
    CONSTRAINT projects_brief_check CHECK (char_length(brief) <= 4000),
    CONSTRAINT projects_status_check CHECK (status IN ('draft', 'editing', 'review', 'approved', 'exported')),
    CONSTRAINT projects_current_version_check CHECK (current_timeline_version IS NULL OR current_timeline_version >= 1)
);

-- The project list: one workspace, newest first (UUIDv7 ids sort by creation time), without deleted rows.
CREATE INDEX projects_workspace_listing_idx ON projects (workspace_id, id DESC) WHERE deleted_at IS NULL;

CREATE TRIGGER projects_set_updated_at BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('projects');

CREATE TABLE project_assets (
    workspace_id uuid        NOT NULL,
    project_id   uuid        NOT NULL,
    asset_id     uuid        NOT NULL,
    position     integer     NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT project_assets_pkey PRIMARY KEY (project_id, asset_id),
    CONSTRAINT project_assets_position_key UNIQUE (project_id, position),
    CONSTRAINT project_assets_project_fkey FOREIGN KEY (workspace_id, project_id)
        REFERENCES projects (workspace_id, id) ON DELETE CASCADE,
    -- No cascade: an asset row is only ever soft-deleted, and a hard delete must not silently empty a project.
    CONSTRAINT project_assets_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id),
    CONSTRAINT project_assets_workspace_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
    -- At most 50 assets per project (the API's cap).
    CONSTRAINT project_assets_position_check CHECK (position BETWEEN 0 AND 49)
);

CREATE INDEX project_assets_workspace_asset_idx ON project_assets (workspace_id, asset_id);

SELECT cf_grant_app_dml('project_assets');

CREATE TABLE timeline_versions (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id   uuid        NOT NULL,
    project_id     uuid        NOT NULL,
    version        integer     NOT NULL,
    parent_version integer,
    spec           jsonb       NOT NULL,
    ops            jsonb       NOT NULL DEFAULT '[]',
    diff           jsonb       NOT NULL DEFAULT '[]',
    summary        text[]      NOT NULL DEFAULT '{}',
    author_kind    text        NOT NULL,
    -- A usr_ TypeID for a user, a run id for the agent. Text, because the two are different kinds of id.
    author_id      text        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT timeline_versions_project_version_key UNIQUE (project_id, version),
    CONSTRAINT timeline_versions_workspace_id_id_key UNIQUE (workspace_id, id),
    CONSTRAINT timeline_versions_project_fkey FOREIGN KEY (workspace_id, project_id)
        REFERENCES projects (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT timeline_versions_workspace_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
    CONSTRAINT timeline_versions_version_check CHECK (version >= 1),
    -- A chain: version 1 has no parent, every other version follows the one before it.
    CONSTRAINT timeline_versions_parent_check CHECK (
        (version = 1 AND parent_version IS NULL) OR (version > 1 AND parent_version = version - 1)),
    CONSTRAINT timeline_versions_author_kind_check CHECK (author_kind IN ('user', 'agent')),
    CONSTRAINT timeline_versions_author_id_check CHECK (char_length(author_id) BETWEEN 1 AND 128),
    CONSTRAINT timeline_versions_spec_check CHECK (jsonb_typeof(spec) = 'object'),
    CONSTRAINT timeline_versions_ops_check CHECK (jsonb_typeof(ops) = 'array'),
    CONSTRAINT timeline_versions_diff_check CHECK (jsonb_typeof(diff) = 'array')
);

-- The project's current version points at a stored one.
ALTER TABLE projects
    ADD CONSTRAINT projects_current_version_fkey FOREIGN KEY (id, current_timeline_version)
        REFERENCES timeline_versions (project_id, version) DEFERRABLE INITIALLY DEFERRED;

-- Immutable: no UPDATE for anybody, the owner included (the app role does not even have the right).
-- +goose StatementBegin
CREATE FUNCTION timeline_versions_immutable() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog
    AS $$
BEGIN
    RAISE EXCEPTION 'timeline_versions rows are immutable: write a new version instead'
        USING ERRCODE = 'restrict_violation';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER timeline_versions_no_update BEFORE UPDATE ON timeline_versions
    FOR EACH ROW EXECUTE FUNCTION timeline_versions_immutable();

-- Not cf_grant_app_dml: that hands out UPDATE and DELETE too. This table is append-only for the app role.
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('GRANT SELECT, INSERT ON TABLE timeline_versions TO %I', public.cf_app_role());
END
$$;
-- +goose StatementEnd

-- +goose Down

ALTER TABLE projects DROP CONSTRAINT projects_current_version_fkey;
DROP TABLE timeline_versions;
DROP FUNCTION timeline_versions_immutable();
DROP TABLE project_assets;
DROP TABLE projects;
