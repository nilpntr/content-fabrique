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
--                 title, description and tags are then left as they were); skipped (the asset failed its ingest, or the job
--                 could not run for a day, so nothing is owed). quick_look.error says which failure.
-- quick_look:   jsonb detail: the frames the model saw ("frames": at_ms, ref, sharpness; ref is the object key in the
--                 editor's frames/ cache under the asset's prefix, so the keyframe grid (P2-04) can reuse them), the model, the
--                 prompt version, and the error code when it failed. Never the model's raw answer.
-- search:       a generated tsvector over title (A), tags (B), description (C) and the original file name (D). Every text is
--                 indexed twice: English stemming, so "row" finds "rows" and "rowing", and the simple configuration (no stop
--                 words, no stemming), so a half-typed word is a prefix of what is stored ("pressi" finds "pressing") and a
--                 stop word such as "the" is a word like any other. cf_search_query(words) builds the query: every word must
--                 match, as a prefix of a simple token or as a stem. GET /v1/assets?q= uses it.
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
        setweight(to_tsvector('english', title) || to_tsvector('simple', title), 'A')
        || setweight(to_tsvector('english', cf_text_array_join(tags)) || to_tsvector('simple', cf_text_array_join(tags)), 'B')
        || setweight(to_tsvector('english', description) || to_tsvector('simple', description), 'C')
        || setweight(to_tsvector('english', regexp_replace(filename, '[._/-]+', ' ', 'g'))
                     || to_tsvector('simple', regexp_replace(filename, '[._/-]+', ' ', 'g')), 'D')
    ) STORED,
    ADD CONSTRAINT assets_title_source_check CHECK (title_source IN ('filename', 'quicklook', 'user')),
    ADD CONSTRAINT assets_quick_look_status_check CHECK (quick_look_status IS NULL OR quick_look_status IN ('pending', 'succeeded', 'failed', 'skipped'));

CREATE INDEX assets_search_idx ON assets USING gin (search);

-- The query for a list of words typed in the search box (store.SearchWords makes them: letters and digits only, lower case).
-- Each word matches as a prefix of a simple token or as an English stem; all words must match. NULL for no words.
-- +goose StatementBegin
CREATE FUNCTION cf_search_query(words text[]) RETURNS tsquery
    LANGUAGE plpgsql STABLE STRICT
    AS $fn$
DECLARE
    q tsquery;
    t tsquery;
    w text;
BEGIN
    FOREACH w IN ARRAY words LOOP
        t := to_tsquery('simple', quote_literal(w) || ':*') || plainto_tsquery('english', w);
        IF q IS NULL THEN
            q := t;
        ELSE
            q := q && t;
        END IF;
    END LOOP;
    RETURN q;
END
$fn$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION cf_search_query(text[]);
DROP INDEX assets_search_idx;
ALTER TABLE assets
    DROP CONSTRAINT assets_quick_look_status_check,
    DROP CONSTRAINT assets_title_source_check,
    DROP COLUMN search,
    DROP COLUMN quick_look_at,
    DROP COLUMN quick_look,
    DROP COLUMN quick_look_status,
    DROP COLUMN title_source;
DROP FUNCTION cf_text_array_join(text[]);
