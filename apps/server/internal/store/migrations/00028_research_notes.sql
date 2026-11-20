-- The creator research engine, phase R2 (docs/design/creator-research-engine.md): the watch sheet's notes. A person who watched a public
-- post may add a short note in their own words to their annotation of it. It is never the creator's caption or lines: at most 280
-- characters, one line and no link (a link could point to a media file; the post's own link is in research_posts). Package research
-- checks the same before every write (research.CheckNotes). The note goes with its annotation: the sweeper and `cfctl research forget`
-- delete it with the post.

-- +goose Up

ALTER TABLE research_annotations ADD COLUMN notes text;

ALTER TABLE research_annotations ADD CONSTRAINT research_annotations_notes_check
    CHECK (notes IS NULL OR (char_length(notes) <= 280 AND notes !~* '(https?://|www\.)' AND notes !~ '[\r\n]'));

-- +goose Down

ALTER TABLE research_annotations DROP COLUMN notes;
