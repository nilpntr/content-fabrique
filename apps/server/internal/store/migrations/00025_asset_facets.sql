-- Clip labels from a fixed vocabulary (package taxonomy, config/tags/v1.yaml) instead of free-form tags.
--
-- The quick look used to write up to eight free-form tags per clip, and a library of five clips showed 22 of them ("back view" and
-- "rear view", "back training" and "back workout", "gym", "dark background"). From quicklook.v4 it answers in a few facets with fixed
-- values, and one exercise name.
--
-- facets:       the clip's labels as "facet:value" strings ("type:lift", "body:back", "angle:side"), in the vocabulary's order; at most
--                 a few per facet. Written by the quick look (validated against the vocabulary) and by the backfill below; a value the
--                 vocabulary does not know is never stored. GET /v1/assets?body=back filters with facets @> '{body:back}' (GIN index).
-- exercise:     the exercise as a coach names it ("seated cable row"), or the pose's name; empty when the clip shows none or the model was
--                 not sure. Untrusted model text, cleaned like the title. Shown as the card's heading, never as a chip.
-- facet_words:  the labels and ids of the facets ("Lifting lift Back back Side view side"), written with them, so that a search for
--                 "lifting" or "side view" finds the clip. The vocabulary lives in Go; the database only indexes these words.
-- legacy_tags:  the old free-form tags (the column used to be called tags), kept for a while so the backfill can be run again with a
--                 better synonym map. Hidden: the API does not return them and the search does not read them.
--
-- search is rebuilt over title (A), exercise (A), facet_words (B), description (C) and the file name (D); the old tags leave it.
--
-- Migration 00026 (a Go migration, store/migrate_facets.go) fills facets and exercise of the existing clips from legacy_tags and the
-- title through the synonym map, without a model call.

-- +goose Up

DROP INDEX assets_search_idx;
ALTER TABLE assets DROP COLUMN search;
ALTER TABLE assets RENAME COLUMN tags TO legacy_tags;
ALTER TABLE assets
    ADD COLUMN facets      text[] NOT NULL DEFAULT '{}',
    ADD COLUMN exercise    text   NOT NULL DEFAULT '',
    ADD COLUMN facet_words text   NOT NULL DEFAULT '',
    ADD CONSTRAINT assets_facets_check CHECK (cardinality(facets) <= 16),
    ADD CONSTRAINT assets_exercise_check CHECK (char_length(exercise) <= 80),
    ADD CONSTRAINT assets_facet_words_check CHECK (char_length(facet_words) <= 1000);
ALTER TABLE assets
    ADD COLUMN search tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', title) || to_tsvector('simple', title), 'A')
        || setweight(to_tsvector('english', exercise) || to_tsvector('simple', exercise), 'A')
        || setweight(to_tsvector('english', facet_words) || to_tsvector('simple', facet_words), 'B')
        || setweight(to_tsvector('english', description) || to_tsvector('simple', description), 'C')
        || setweight(to_tsvector('english', regexp_replace(filename, '[._/-]+', ' ', 'g'))
                     || to_tsvector('simple', regexp_replace(filename, '[._/-]+', ' ', 'g')), 'D')
    ) STORED;
CREATE INDEX assets_search_idx ON assets USING gin (search);
CREATE INDEX assets_facets_idx ON assets USING gin (facets);

-- +goose Down

DROP INDEX assets_facets_idx;
DROP INDEX assets_search_idx;
ALTER TABLE assets DROP COLUMN search;
ALTER TABLE assets
    DROP CONSTRAINT assets_facet_words_check,
    DROP CONSTRAINT assets_exercise_check,
    DROP CONSTRAINT assets_facets_check,
    DROP COLUMN facet_words,
    DROP COLUMN exercise,
    DROP COLUMN facets;
ALTER TABLE assets RENAME COLUMN legacy_tags TO tags;
ALTER TABLE assets
    ADD COLUMN search tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', title) || to_tsvector('simple', title), 'A')
        || setweight(to_tsvector('english', cf_text_array_join(tags)) || to_tsvector('simple', cf_text_array_join(tags)), 'B')
        || setweight(to_tsvector('english', description) || to_tsvector('simple', description), 'C')
        || setweight(to_tsvector('english', regexp_replace(filename, '[._/-]+', ' ', 'g'))
                     || to_tsvector('simple', regexp_replace(filename, '[._/-]+', ' ', 'g')), 'D')
    ) STORED;
CREATE INDEX assets_search_idx ON assets USING gin (search);
