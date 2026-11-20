-- Proof edits and clip-level feedback (docs/design/proof-edit-v1.md, the creator's rule 15 in docs/design/gino-edit-rules.md).
--
-- renders.proof: a preview with a small number burned into every shot (render.Proof), so the creator can answer "never use shot 5
--               again" or "shot 6 a few seconds later". Only a preview can be a proof; the final render ("make it final") never has
--               numbers. A version may have a plain preview and a proof at the same time (the editor's own preview and the creator's
--               proof are not the same file), so the dedupe index now holds proof too.
--
-- clip_feedback: what the creator said about a shot of a proof, stored against the clip and the range of its source (never the shot's
--               number, which changes with every version), so every later edit of the workspace honours it: check_edit's clip_feedback
--               check fails an edit that plays the range, and the editor reads the rules with get_clip_feedback.
--   kind:       never_use   the range [start_ms, end_ms) of the clip is never used again (the shot's source range, or the whole clip).
--               start_later the take should start later: [start_ms, end_ms) is the part before the new start (the shot's in point
--                           to in point + the offset), which no shot may play; the rest of the take is fine.
--   start_ms, end_ms: the range of the clip's own time the feedback is about (end exclusive).
--   project_id, timeline_version, shot_number, item_id: where it was given (the shot as it was numbered on that version's proof), for
--               the shot list and the history; they are never read to apply it. The version's foreign key is not kept: a feedback row
--               outlives nothing it needs.
--   note:       the creator's own words, optional, untrusted text (it reaches the editor in a data block only).
--   created_by_kind/id: 'user' with a usr_ TypeID (the API, the web, cfctl), or 'agent' with a run id, like renders.requested_by.
-- Saying the same thing twice about the same range is one row (the unique key): the second write updates the note and returns it.
-- A row is deleted when the creator takes it back (the app role may DELETE); it goes with its workspace, clip or project otherwise.

-- +goose Up

ALTER TABLE renders ADD COLUMN proof boolean NOT NULL DEFAULT false;
ALTER TABLE renders ADD CONSTRAINT renders_proof_check CHECK (NOT proof OR profile = 'preview');

DROP INDEX renders_active_key;
CREATE UNIQUE INDEX renders_active_key ON renders (project_id, timeline_version, profile, proof)
    WHERE status IN ('queued', 'rendering', 'ready');

CREATE TABLE clip_feedback (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id     uuid        NOT NULL,
    asset_id         uuid        NOT NULL,
    kind             text        NOT NULL,
    start_ms         bigint      NOT NULL,
    end_ms           bigint      NOT NULL,
    project_id       uuid        NOT NULL,
    timeline_version integer     NOT NULL,
    shot_number      integer     NOT NULL,
    item_id          text        NOT NULL,
    note             text        NOT NULL DEFAULT '',
    created_by_kind  text        NOT NULL,
    created_by_id    text        NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT clip_feedback_workspace_fkey FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,
    CONSTRAINT clip_feedback_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT clip_feedback_project_fkey FOREIGN KEY (workspace_id, project_id)
        REFERENCES projects (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT clip_feedback_kind_check CHECK (kind IN ('never_use', 'start_later')),
    CONSTRAINT clip_feedback_range_check CHECK (start_ms >= 0 AND end_ms > start_ms AND end_ms <= 86400000),
    CONSTRAINT clip_feedback_shot_check CHECK (timeline_version >= 1 AND shot_number >= 1 AND char_length(item_id) BETWEEN 1 AND 64),
    CONSTRAINT clip_feedback_note_check CHECK (char_length(note) <= 500),
    CONSTRAINT clip_feedback_created_by_kind_check CHECK (created_by_kind IN ('user', 'agent')),
    CONSTRAINT clip_feedback_created_by_id_check CHECK (char_length(created_by_id) BETWEEN 1 AND 128)
);

-- One row per thing said about one range of one clip.
CREATE UNIQUE INDEX clip_feedback_range_key ON clip_feedback (workspace_id, asset_id, kind, start_ms, end_ms);

-- A workspace's feedback, newest first (UUIDv7 ids sort by creation time).
CREATE INDEX clip_feedback_listing_idx ON clip_feedback (workspace_id, id DESC);

CREATE TRIGGER clip_feedback_set_updated_at BEFORE UPDATE ON clip_feedback
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('clip_feedback');

-- +goose Down

DROP TABLE clip_feedback;
DROP INDEX renders_active_key;
-- A version may have had a plain preview and a proof at once; the proofs go so the old key holds again.
DELETE FROM renders WHERE proof;
CREATE UNIQUE INDEX renders_active_key ON renders (project_id, timeline_version, profile)
    WHERE status IN ('queued', 'rendering', 'ready');
ALTER TABLE renders DROP CONSTRAINT renders_proof_check;
ALTER TABLE renders DROP COLUMN proof;
