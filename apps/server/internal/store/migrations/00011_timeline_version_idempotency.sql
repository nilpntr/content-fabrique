-- The idempotency of the editor agent's edits (P4-02a). A tool call that a resumed run makes twice must make one version: the
-- version stores the call's key (run id and tool-use id) next to its result (the ids the ops created and the warnings), and the
-- key is unique per project, so the repeat finds the version and answers exactly as the first call did. `author_id` stays what the
-- API says it is, a usr_ TypeID or the run's TypeID; the key is a separate column.
--
-- Both columns are written with the INSERT and never updated (the table stays append-only). They are null for a version that
-- a person made.

-- +goose Up

ALTER TABLE timeline_versions
    ADD COLUMN idempotency_key text,
    ADD COLUMN result          jsonb,
    ADD CONSTRAINT timeline_versions_idempotency_key_check CHECK (idempotency_key IS NULL OR char_length(idempotency_key) BETWEEN 1 AND 128),
    ADD CONSTRAINT timeline_versions_result_check CHECK (result IS NULL OR jsonb_typeof(result) = 'object');

CREATE UNIQUE INDEX timeline_versions_idempotency_key_idx ON timeline_versions (project_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

-- Finding the versions an agent run made: author_id is the run's TypeID.
CREATE INDEX timeline_versions_author_idx ON timeline_versions (project_id, author_kind, author_id);

-- +goose Down

DROP INDEX timeline_versions_author_idx;
DROP INDEX timeline_versions_idempotency_key_idx;
ALTER TABLE timeline_versions
    DROP CONSTRAINT timeline_versions_result_check,
    DROP CONSTRAINT timeline_versions_idempotency_key_check,
    DROP COLUMN result,
    DROP COLUMN idempotency_key;
