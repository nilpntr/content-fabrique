-- Quick look and library search (P1-04b, architecture section 4, decision D-22).
--
-- Right after the ingest marks a video ready, a quick_look job cuts four sharp frames, asks Haiku 5.5 once for a title, a
-- one-line description and up to eight tags, and stores them on the asset. This migration adds what that needs.
--
-- title_source: where the title came from, so a rerun never overwrites what a person typed.
--                 filename   the file name without its extension (what the upload made)
--                 quicklook  written by the quick look
--                 user       edited through PATCH /v1/assets/{id}; never overwritten
-- quick_look_status: null for an asset the quick look does not apply to (audio, images, and videos from before this
--                 migration until cfctl runs it); pending from the moment the ingest finishes until the job ends;
--                 succeeded; failed (the frames could not be cut, the model failed, or its answer did not validate: the
--                 title, description and tags are then left as they were). quick_look.error says which.
-- quick_look:   jsonb detail: the frames the quick look cut and cached in object storage ("frames": at_ms, name, sharpness;
--                 the names are objects under the asset's prefix, so the editor's keyframe grid (P2-04) can reuse them), the
--                 model, the prompt version, and the error code when it failed. Never the model's raw answer.
-- search:       a generated tsvector over title (A), tags (B), description (C) and the original file name (D), English
--                 stemming so "row" finds "rows" and "rowing". The list query matches prefixes: GET /v1/assets?q=ro.
--
-- Tenancy is unchanged: every query still filters by workspace_id, and the GIN index only narrows the rows of that filter.

-- +goose Up

-- array_to_string is only STABLE, which a generated column refuses; with a text[] of plain strings it is in fact immutable.
-- +goose StatementBegin
CREATE FUNCTION cf_text_array_join(parts text[]) RETURNS text
    LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
    RETURN array_to_string(parts, ' ');
-- +goose StatementEnd

ALTER TABLE assets
    ADD COLUMN title_source      text NOT NULL DEFAULT 'filename',
    ADD COLUMN quick_look_status text,
    ADD COLUMN quick_look        jsonb,
    ADD COLUMN quick_look_at     timestamptz,
    ADD COLUMN search            tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', title), 'A')
        || setweight(to_tsvector('english', cf_text_array_join(tags)), 'B')
        || setweight(to_tsvector('english', description), 'C')
        || setweight(to_tsvector('english', regexp_replace(filename, '[._/-]+', ' ', 'g')), 'D')
    ) STORED,
    ADD CONSTRAINT assets_title_source_check CHECK (title_source IN ('filename', 'quicklook', 'user')),
    ADD CONSTRAINT assets_quick_look_status_check CHECK (quick_look_status IS NULL OR quick_look_status IN ('pending', 'succeeded', 'failed')),
    ADD CONSTRAINT assets_description_check CHECK (char_length(description) <= 1000),
    ADD CONSTRAINT assets_tags_check CHECK (cardinality(tags) <= 32);

CREATE INDEX assets_search_idx ON assets USING gin (search);

-- +goose Down

DROP INDEX assets_search_idx;
ALTER TABLE assets
    DROP CONSTRAINT assets_tags_check,
    DROP CONSTRAINT assets_description_check,
    DROP CONSTRAINT assets_quick_look_status_check,
    DROP CONSTRAINT assets_title_source_check,
    DROP COLUMN search,
    DROP COLUMN quick_look_at,
    DROP COLUMN quick_look,
    DROP COLUMN quick_look_status,
    DROP COLUMN title_source;
DROP FUNCTION cf_text_array_join(text[]);
