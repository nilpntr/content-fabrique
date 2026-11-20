-- The creator research engine, phase R1 (docs/05-decisions.md D-41): fitness creators on TikTok, Instagram and YouTube whose videos look
-- like our creators', what we observed about their public posts, and the findings written from those observations.
--
-- SHARED across workspaces, like the trend data (architecture 7.3): these tables have no workspace_id, because the research is about other
-- people's public posts, not about a customer. Only the platform's own code (jobs, cfctl) writes them; the API never reads them.
--
-- COMPLIANCE, enforced here and again in package research (the checks below are the last line, not the only one):
--   * Public data only, links and numbers only. There is no column for a media file, a media URL, a thumbnail or a transcript. A post is a
--     link to a post page (research_posts.url) with a few facts; an observation is a handful of counts (research_evidence.metrics, whose keys
--     the CHECK limits to five names and whose values must be numbers) and the hash of the response it came from (raw_ref), never the response.
--   * Sources are a fixed list. A source that is not in the CHECK cannot write evidence, and each source has the one method it can use.
--   * Retention is on every observation (retain_until) and a sweeper deletes what is past it. A YouTube observation may be kept for 30 days at
--     most (the CHECK), everything else for the configured time (RESEARCH_RETENTION_DAYS, 12 months unless changed). The posts, annotations and
--     findings that rested on deleted evidence go too (store.SweepResearch).
--   * A creator is removed on request with `cfctl research forget <handle>`: the creator, their posts, annotations and evidence cascade.
--
-- research_creators:    one row per creator and platform. status: candidate (found, not yet looked at), approved (a person approved sampling
--                         them; approved_by says who), dropped. why_similar is our own words; similarity is 0 to 1.
-- research_posts:       one row per post page. external_id is the platform's id of the post; posted_at is when it was posted and
--                         posted_at_source how we know (id: derived from the post id; api; manual), both NULL when unknown.
-- research_evidence:    one row per observation of a post or of a creator (exactly one of the two).
-- research_annotations: what a person (or a worker reading the caption) noted about a post's hook and form. hook_text_pattern is a title
--                         FORM such as "<TOPIC> 101" (slots and capital letters only), never the creator's words.
-- research_findings:    a claim of the playbook, always with the evidence it rests on (at least one id; there is no foreign key on an array,
--                         so the sweeper deletes the findings of deleted evidence).

-- research_forgotten:   the tombstones of `cfctl research forget`: the platform and the SHA-256 of the normalized handle, nothing else. A creator
--                         with a tombstone is refused by the store (creator and post), so a forgotten person is never ingested again.
--                         `forget --undo` removes the tombstone. The hash is of a public handle, so it hides nothing from someone who
--                         already has the handle; it only keeps the handle itself out of the table.

-- +goose Up

CREATE TABLE research_creators (
    id           uuid             PRIMARY KEY DEFAULT uuidv7(),
    platform     text             NOT NULL,
    handle       text             NOT NULL,
    profile_url  text             NOT NULL,
    why_similar  text             NOT NULL DEFAULT '',
    similarity   double precision NOT NULL DEFAULT 0,
    status       text             NOT NULL DEFAULT 'candidate',
    approved_by  text,
    approved_at  timestamptz,
    created_at   timestamptz      NOT NULL DEFAULT now(),
    updated_at   timestamptz      NOT NULL DEFAULT now(),
    CONSTRAINT research_creators_key UNIQUE (platform, handle),
    CONSTRAINT research_creators_platform_check CHECK (platform IN ('tiktok', 'instagram', 'youtube')),
    CONSTRAINT research_creators_handle_check CHECK (handle ~ '^[a-z0-9._][a-z0-9._-]{0,63}$'),
    CONSTRAINT research_creators_url_check CHECK (profile_url ~ '^https://[^[:space:]]{4,255}$'),
    CONSTRAINT research_creators_why_check CHECK (char_length(why_similar) <= 500),
    CONSTRAINT research_creators_similarity_check CHECK (similarity BETWEEN 0 AND 1),
    CONSTRAINT research_creators_status_check CHECK (status IN ('candidate', 'approved', 'dropped')),
    CONSTRAINT research_creators_approved_check CHECK (status <> 'approved' OR (approved_by IS NOT NULL AND char_length(approved_by) BETWEEN 1 AND 80 AND approved_at IS NOT NULL))
);

CREATE TRIGGER research_creators_set_updated_at BEFORE UPDATE ON research_creators
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('research_creators');

CREATE TABLE research_posts (
    id               uuid             PRIMARY KEY DEFAULT uuidv7(),
    platform         text             NOT NULL,
    url              text             NOT NULL,
    external_id      text             NOT NULL,
    creator_id       uuid             NOT NULL REFERENCES research_creators (id) ON DELETE CASCADE,
    posted_at        timestamptz,
    posted_at_source text,
    duration_s       double precision,
    created_at       timestamptz      NOT NULL DEFAULT now(),
    CONSTRAINT research_posts_key UNIQUE (platform, external_id),
    CONSTRAINT research_posts_url_key UNIQUE (url),
    CONSTRAINT research_posts_platform_check CHECK (platform IN ('tiktok', 'instagram', 'youtube')),
    CONSTRAINT research_posts_url_check CHECK (url ~ '^https://[^[:space:]?#]{4,255}(\?v=[A-Za-z0-9_-]{11})?$'),
    CONSTRAINT research_posts_external_id_check CHECK (external_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    CONSTRAINT research_posts_posted_check CHECK ((posted_at IS NULL) = (posted_at_source IS NULL)),
    CONSTRAINT research_posts_posted_source_check CHECK (posted_at_source IS NULL OR posted_at_source IN ('id', 'api', 'manual')),
    CONSTRAINT research_posts_duration_check CHECK (duration_s IS NULL OR duration_s BETWEEN 0 AND 7200)
);

CREATE INDEX research_posts_creator_idx ON research_posts (creator_id);

SELECT cf_grant_app_dml('research_posts');

CREATE TABLE research_evidence (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    post_id      uuid        REFERENCES research_posts (id) ON DELETE CASCADE,
    creator_id   uuid        REFERENCES research_creators (id) ON DELETE CASCADE,
    source       text        NOT NULL,
    method       text        NOT NULL,
    observed_at  timestamptz NOT NULL,
    observer     text        NOT NULL,
    metrics      jsonb       NOT NULL DEFAULT '{}',
    raw_ref      text,
    retain_until timestamptz NOT NULL,
    run_id       uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT research_evidence_target_check CHECK (num_nonnulls(post_id, creator_id) = 1),
    CONSTRAINT research_evidence_source_check CHECK (source IN ('oembed', 'watch_sheet', 'web', 'ig_business_discovery', 'youtube_api', 'creator_oauth')),
    CONSTRAINT research_evidence_method_check CHECK (method IN ('api', 'manual')),
    -- A person fills the watch sheet; the four platform sources are API calls; a web search can be either.
    CONSTRAINT research_evidence_watch_sheet_check CHECK (source <> 'watch_sheet' OR method = 'manual'),
    CONSTRAINT research_evidence_api_check CHECK (source NOT IN ('oembed', 'ig_business_discovery', 'youtube_api', 'creator_oauth') OR method = 'api'),
    CONSTRAINT research_evidence_observer_check CHECK (char_length(observer) BETWEEN 1 AND 80),
    -- Only the five counts, only numbers: no URL, no text, no media can sit in this column.
    CONSTRAINT research_evidence_metrics_check CHECK (
        jsonb_typeof(metrics) = 'object'
        AND octet_length(metrics::text) <= 512
        AND (metrics - ARRAY['views', 'likes', 'comments', 'saves', 'shares']) = '{}'::jsonb
        AND NOT jsonb_path_exists(metrics, '$.* ? (@.type() != "number" || @ < 0)')
    ),
    -- The hash of the response (hex SHA-256), never the response.
    CONSTRAINT research_evidence_raw_ref_check CHECK (raw_ref IS NULL OR raw_ref ~ '^[0-9a-f]{64}$'),
    CONSTRAINT research_evidence_retain_check CHECK (retain_until > observed_at),
    -- The YouTube API terms: refresh or delete within 30 days. Counted in seconds, so the session time zone (DST) cannot change the answer.
    CONSTRAINT research_evidence_youtube_retain_check CHECK (source <> 'youtube_api' OR extract(epoch FROM (retain_until - observed_at)) <= 2592000)
);

CREATE INDEX research_evidence_retain_idx ON research_evidence (retain_until);
CREATE INDEX research_evidence_post_idx ON research_evidence (post_id) WHERE post_id IS NOT NULL;
CREATE INDEX research_evidence_creator_idx ON research_evidence (creator_id) WHERE creator_id IS NOT NULL;

SELECT cf_grant_app_dml('research_evidence');

CREATE TABLE research_annotations (
    id                uuid             PRIMARY KEY DEFAULT uuidv7(),
    post_id           uuid             NOT NULL REFERENCES research_posts (id) ON DELETE CASCADE,
    format_id         text,
    hook_kind         text,
    hook_text_pattern text,
    text_pattern      text,
    cuts_estimate     integer,
    first_cut_s       double precision,
    caption_len       integer,
    hashtags_n        integer,
    cta               text,
    topic             text,
    annotator         text             NOT NULL,
    confidence        double precision NOT NULL DEFAULT 1,
    created_at        timestamptz      NOT NULL DEFAULT now(),
    CONSTRAINT research_annotations_key UNIQUE (post_id, annotator),
    CONSTRAINT research_annotations_format_check CHECK (format_id IS NULL OR format_id ~ '^[a-z][a-z0-9_]{1,39}$'),
    CONSTRAINT research_annotations_hook_kind_check CHECK (hook_kind IS NULL OR hook_kind ~ '^[a-z][a-z0-9_]{0,31}$'),
    -- A title form: capital letters, digits, punctuation and slots like <TOPIC>. No lower-case word of the creator's can be stored.
    CONSTRAINT research_annotations_hook_pattern_check CHECK (hook_text_pattern IS NULL OR (char_length(hook_text_pattern) <= 60 AND hook_text_pattern !~ '[a-z]')),
    CONSTRAINT research_annotations_text_pattern_check CHECK (text_pattern IS NULL OR text_pattern IN ('single_title', 'story_captions', 'dialogue', 'none')),
    CONSTRAINT research_annotations_cuts_check CHECK (cuts_estimate IS NULL OR cuts_estimate BETWEEN 0 AND 500),
    CONSTRAINT research_annotations_first_cut_check CHECK (first_cut_s IS NULL OR first_cut_s BETWEEN 0 AND 7200),
    CONSTRAINT research_annotations_caption_len_check CHECK (caption_len IS NULL OR caption_len BETWEEN 0 AND 10000),
    CONSTRAINT research_annotations_hashtags_check CHECK (hashtags_n IS NULL OR hashtags_n BETWEEN 0 AND 200),
    CONSTRAINT research_annotations_cta_check CHECK (cta IS NULL OR cta IN ('save', 'follow', 'comment', 'none')),
    CONSTRAINT research_annotations_topic_check CHECK (topic IS NULL OR topic ~ '^[a-z][a-z_]{0,23}:[a-z][a-z0-9_]{0,39}$'),
    CONSTRAINT research_annotations_annotator_check CHECK (char_length(annotator) BETWEEN 1 AND 80),
    CONSTRAINT research_annotations_confidence_check CHECK (confidence BETWEEN 0 AND 1)
);

SELECT cf_grant_app_dml('research_annotations');

CREATE TABLE research_findings (
    id               uuid             PRIMARY KEY DEFAULT uuidv7(),
    playbook_version text             NOT NULL,
    claim            text             NOT NULL,
    dimension        text             NOT NULL,
    effect_value     double precision NOT NULL,
    effect_unit      text             NOT NULL,
    n_posts          integer          NOT NULL,
    n_creators       integer          NOT NULL,
    confidence       text             NOT NULL,
    evidence_ids     uuid[]           NOT NULL,
    created_at       timestamptz      NOT NULL DEFAULT now(),
    CONSTRAINT research_findings_version_check CHECK (char_length(playbook_version) BETWEEN 1 AND 32),
    CONSTRAINT research_findings_claim_check CHECK (char_length(claim) BETWEEN 1 AND 600),
    CONSTRAINT research_findings_dimension_check CHECK (dimension IN ('hook', 'length', 'cadence', 'format', 'caption', 'time', 'saves_shares')),
    CONSTRAINT research_findings_unit_check CHECK (char_length(effect_unit) BETWEEN 1 AND 32),
    CONSTRAINT research_findings_n_check CHECK (n_posts >= 0 AND n_creators >= 0 AND n_creators <= n_posts),
    CONSTRAINT research_findings_confidence_check CHECK (confidence IN ('low', 'medium', 'high')),
    CONSTRAINT research_findings_evidence_check CHECK (cardinality(evidence_ids) BETWEEN 1 AND 1000)
);

CREATE INDEX research_findings_version_idx ON research_findings (playbook_version);
CREATE INDEX research_findings_evidence_idx ON research_findings USING gin (evidence_ids);

SELECT cf_grant_app_dml('research_findings');

CREATE TABLE research_forgotten (
    platform    text        NOT NULL,
    handle_hash text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (platform, handle_hash),
    CONSTRAINT research_forgotten_platform_check CHECK (platform IN ('tiktok', 'instagram', 'youtube')),
    CONSTRAINT research_forgotten_hash_check CHECK (handle_hash ~ '^[0-9a-f]{64}$')
);

SELECT cf_grant_app_dml('research_forgotten');

-- +goose Down

DROP TABLE research_forgotten;
DROP TABLE research_findings;
DROP TABLE research_annotations;
DROP TABLE research_evidence;
DROP TABLE research_posts;
DROP TABLE research_creators;
