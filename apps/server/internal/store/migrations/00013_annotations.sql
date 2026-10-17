-- Annotations (P1-08, architecture sections 4 and 7.2, D-24): comments on a clip, on a render or on a version of a timeline, each
-- on a range of time, with a kind, an optional pin on the frame, and a status the editor agent moves when it has dealt with it.
--
-- target_type, target_id: what the comment is about.
--                   asset            an asset (ast_): a range of the original clip. project_id and timeline_version are null.
--                   render           a render (ren_): a range of its output. project_id and timeline_version are set by the
--                                    server from the render (the version the render was made from).
--                   timeline_version a stored version (tl_): a range of the output of that version, for a version that has no
--                                    render yet (or whose render the reader does not have). project_id and timeline_version
--                                    are set by the server from the version.
--                   target_id is a plain uuid. The target is looked up in code, per type and in the caller's workspace, before
--                   a row is written (a polymorphic key cannot carry a foreign key), and project_id with timeline_version
--                   carry composite foreign keys that keep the project and the version inside the workspace.
-- start_ms, end_ms: a range of the target's time; a comment on a moment has end_ms = start_ms.
-- pin:              null, a point {"x", "y"} or a box {"x", "y", "w", "h"} on the frame, all normalised to [0, 1] from the top
--                   left. The service checks the numbers; the constraint only keeps the shape an object.
-- kind:             comment | include | exclude | prefer | instruction.
-- body:             the text, at most 2,000 characters, cleaned by the service (the same policy as a project's brief). A comment
--                   or an instruction has some; the other kinds may be a bare marker.
-- status:           open -> applied (the editor run in applied_in_run_id dealt with it, and addressed_in_version is the version
--                   that run made) | resolved (closed by hand); both can be reopened.
-- parent_id:        a reply points at the comment it answers; one level only (the service refuses a reply to a reply), and the
--                   reply copies the target and range of its parent. A reply is a comment (kind) without a pin.
-- author_kind, author_id: 'user' with a usr_ TypeID, or 'agent' with the run id (text, like timeline_versions.author_id).
-- applied_in_run_id: the editor run that was handed the comment and finished; a foreign key inside the workspace.
-- deleted_at:       a delete is soft, and takes the replies with it.
--
-- Grants: SELECT, INSERT and UPDATE for the app role, no DELETE (the rows go with their project or workspace through the owner's
-- cascade). Tenancy: workspace_id on the row and in every query.

-- +goose Up

CREATE TABLE annotations (
    id                   uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id         uuid        NOT NULL,
    target_type          text        NOT NULL,
    target_id            uuid        NOT NULL,
    project_id           uuid,
    timeline_version     integer,
    start_ms             bigint      NOT NULL,
    end_ms               bigint      NOT NULL,
    pin                  jsonb,
    kind                 text        NOT NULL DEFAULT 'comment',
    body                 text        NOT NULL DEFAULT '',
    status               text        NOT NULL DEFAULT 'open',
    parent_id            uuid,
    author_kind          text        NOT NULL,
    author_id            text        NOT NULL,
    applied_in_run_id    uuid,
    addressed_in_version integer,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    deleted_at           timestamptz,
    CONSTRAINT annotations_workspace_id_id_key UNIQUE (workspace_id, id),
    CONSTRAINT annotations_workspace_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
    CONSTRAINT annotations_project_fkey FOREIGN KEY (workspace_id, project_id)
        REFERENCES projects (workspace_id, id) ON DELETE CASCADE,
    -- MATCH SIMPLE: not checked for an asset annotation, whose project_id and timeline_version are null.
    CONSTRAINT annotations_version_fkey FOREIGN KEY (workspace_id, project_id, timeline_version)
        REFERENCES timeline_versions (workspace_id, project_id, version) ON DELETE CASCADE,
    CONSTRAINT annotations_parent_fkey FOREIGN KEY (workspace_id, parent_id)
        REFERENCES annotations (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT annotations_run_fkey FOREIGN KEY (workspace_id, applied_in_run_id)
        REFERENCES agent_runs (workspace_id, id),
    CONSTRAINT annotations_target_type_check CHECK (target_type IN ('asset', 'render', 'timeline_version')),
    -- An asset has no project and no version; a render or a version has both.
    CONSTRAINT annotations_scope_check CHECK (
        (target_type = 'asset' AND project_id IS NULL AND timeline_version IS NULL)
        OR (target_type <> 'asset' AND project_id IS NOT NULL AND timeline_version IS NOT NULL)),
    CONSTRAINT annotations_version_check CHECK (timeline_version IS NULL OR timeline_version >= 1),
    CONSTRAINT annotations_range_check CHECK (start_ms >= 0 AND end_ms >= start_ms),
    CONSTRAINT annotations_pin_check CHECK (pin IS NULL OR jsonb_typeof(pin) = 'object'),
    CONSTRAINT annotations_kind_check CHECK (kind IN ('comment', 'include', 'exclude', 'prefer', 'instruction')),
    CONSTRAINT annotations_body_check CHECK (char_length(body) <= 2000),
    CONSTRAINT annotations_status_check CHECK (status IN ('open', 'applied', 'resolved')),
    CONSTRAINT annotations_author_kind_check CHECK (author_kind IN ('user', 'agent')),
    CONSTRAINT annotations_author_id_check CHECK (char_length(author_id) BETWEEN 1 AND 128),
    CONSTRAINT annotations_parent_self_check CHECK (parent_id IS NULL OR parent_id <> id),
    -- A reply is plain text on its parent's range.
    CONSTRAINT annotations_reply_check CHECK (parent_id IS NULL OR (kind = 'comment' AND pin IS NULL)),
    -- "Applied" names the run; only an applied comment names the version that run made.
    CONSTRAINT annotations_applied_check CHECK (
        (status = 'applied') = (applied_in_run_id IS NOT NULL) AND (status = 'applied' OR addressed_in_version IS NULL)),
    CONSTRAINT annotations_addressed_check CHECK (addressed_in_version IS NULL OR addressed_in_version >= 1)
);

-- The comments of a target (a clip, a render, a version), oldest first.
CREATE INDEX annotations_target_idx ON annotations (workspace_id, target_type, target_id, id) WHERE deleted_at IS NULL;

-- The comments of a project, oldest first: the review screen, and the editor's run context.
CREATE INDEX annotations_project_idx ON annotations (workspace_id, project_id, id) WHERE project_id IS NOT NULL AND deleted_at IS NULL;

-- The replies of a comment.
CREATE INDEX annotations_parent_idx ON annotations (workspace_id, parent_id, id) WHERE parent_id IS NOT NULL;

CREATE TRIGGER annotations_set_updated_at BEFORE UPDATE ON annotations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Not cf_grant_app_dml: no DELETE (see above).
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON TABLE annotations TO %I', public.cf_app_role());
END
$$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE annotations;
