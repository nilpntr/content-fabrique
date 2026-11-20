-- The creator research engine (R1, D-41). Shared across workspaces: these tables have no workspace_id, and only the platform's own
-- code (jobs, cfctl) calls these queries.

-- name: UpsertResearchCreator :one
-- A creator seen again keeps their status; only the words about them are refreshed.
INSERT INTO research_creators (platform, handle, profile_url, why_similar, similarity)
VALUES (@platform, @handle, @profile_url, @why_similar, @similarity)
ON CONFLICT (platform, handle) DO UPDATE
SET profile_url = EXCLUDED.profile_url, why_similar = EXCLUDED.why_similar, similarity = EXCLUDED.similarity
RETURNING *;

-- name: GetResearchCreator :one
SELECT * FROM research_creators WHERE platform = @platform AND handle = @handle;

-- name: ListResearchCreators :many
SELECT * FROM research_creators WHERE @status::text = '' OR status = @status ORDER BY platform, handle;

-- name: SetResearchCreatorStatus :one
UPDATE research_creators
SET status = @status::text,
    approved_by = CASE WHEN @status::text = 'approved' THEN @approved_by::text ELSE NULL END,
    approved_at = CASE WHEN @status::text = 'approved' THEN now() ELSE NULL END
WHERE id = @id
RETURNING *;

-- name: UpsertResearchPost :one
INSERT INTO research_posts (platform, url, external_id, creator_id, posted_at, posted_at_source, duration_s)
VALUES (@platform, @url, @external_id, @creator_id, @posted_at, @posted_at_source, @duration_s)
ON CONFLICT (platform, external_id) DO UPDATE
SET posted_at = COALESCE(EXCLUDED.posted_at, research_posts.posted_at),
    posted_at_source = CASE WHEN EXCLUDED.posted_at IS NULL THEN research_posts.posted_at_source ELSE EXCLUDED.posted_at_source END,
    duration_s = COALESCE(EXCLUDED.duration_s, research_posts.duration_s)
WHERE research_posts.creator_id = EXCLUDED.creator_id
RETURNING *;

-- name: GetResearchPostByURL :one
SELECT * FROM research_posts WHERE url = @url;

-- name: GetResearchPostPlatform :one
SELECT platform FROM research_posts WHERE id = @id;

-- name: GetResearchCreatorPlatform :one
SELECT platform FROM research_creators WHERE id = @id;

-- name: GetResearchCreatorByID :one
SELECT * FROM research_creators WHERE id = @id;

-- name: CountResearchEvidenceIDs :one
SELECT count(*)::bigint FROM research_evidence WHERE id = ANY(@ids::uuid[]);

-- The tombstones of `forget`.

-- name: InsertResearchForgotten :exec
INSERT INTO research_forgotten (platform, handle_hash) VALUES (@platform, @handle_hash) ON CONFLICT DO NOTHING;

-- name: IsResearchForgotten :one
SELECT EXISTS (SELECT 1 FROM research_forgotten WHERE platform = @platform AND handle_hash = @handle_hash);

-- name: DeleteResearchForgotten :execrows
DELETE FROM research_forgotten WHERE platform = @platform AND handle_hash = @handle_hash;

-- name: InsertResearchEvidence :one
INSERT INTO research_evidence (post_id, creator_id, source, method, observed_at, observer, metrics, raw_ref, retain_until, run_id)
VALUES (@post_id, @creator_id, @source, @method, @observed_at, @observer, @metrics, @raw_ref, @retain_until, @run_id)
RETURNING *;

-- name: ListResearchEvidenceForPost :many
SELECT * FROM research_evidence WHERE post_id = @post_id ORDER BY observed_at, id;

-- name: UpsertResearchAnnotation :one
INSERT INTO research_annotations (post_id, format_id, hook_kind, hook_text_pattern, text_pattern, cuts_estimate, first_cut_s, caption_len, hashtags_n, cta, topic, notes, annotator, confidence)
VALUES (@post_id, @format_id, @hook_kind, @hook_text_pattern, @text_pattern, @cuts_estimate, @first_cut_s, @caption_len, @hashtags_n, @cta, @topic, @notes, @annotator, @confidence)
ON CONFLICT (post_id, annotator) DO UPDATE
SET format_id = EXCLUDED.format_id, hook_kind = EXCLUDED.hook_kind, hook_text_pattern = EXCLUDED.hook_text_pattern,
    text_pattern = EXCLUDED.text_pattern, cuts_estimate = EXCLUDED.cuts_estimate, first_cut_s = EXCLUDED.first_cut_s,
    caption_len = EXCLUDED.caption_len, hashtags_n = EXCLUDED.hashtags_n, cta = EXCLUDED.cta, topic = EXCLUDED.topic,
    notes = EXCLUDED.notes, confidence = EXCLUDED.confidence
RETURNING *;

-- name: ListResearchAnnotationsForPost :many
SELECT * FROM research_annotations WHERE post_id = @post_id ORDER BY annotator;

-- name: InsertResearchFinding :one
INSERT INTO research_findings (playbook_version, claim, dimension, effect_value, effect_unit, n_posts, n_creators, confidence, evidence_ids)
VALUES (@playbook_version, @claim, @dimension, @effect_value, @effect_unit, @n_posts, @n_creators, @confidence, @evidence_ids)
RETURNING *;

-- name: ListResearchFindings :many
SELECT * FROM research_findings WHERE playbook_version = @playbook_version ORDER BY created_at, id;

-- The retention sweep. Each step is a query of its own so that they run in one transaction (store.SweepResearch).

-- An observation goes when its own retain_until has passed, or when the retention in force now would already have deleted it (the setting
-- was lowered): YouTube rows are cut at ytCutoff, every other source at defaultCutoff.
-- name: DeleteExpiredResearchEvidence :many
DELETE FROM research_evidence
WHERE retain_until <= @now
   OR (source = 'youtube_api' AND observed_at <= @youtube_cutoff)
   OR (source <> 'youtube_api' AND observed_at <= @default_cutoff)
RETURNING id;

-- A finding whose evidence is gone (any id no longer exists) cannot be checked any more, and a finding older than the retention is data
-- about third parties too: both go, and the playbook is regenerated.
-- name: DeleteOrphanResearchFindings :execrows
DELETE FROM research_findings f
WHERE f.created_at <= @findings_cutoff
   OR EXISTS (SELECT 1 FROM unnest(f.evidence_ids) AS e(id) WHERE NOT EXISTS (SELECT 1 FROM research_evidence r WHERE r.id = e.id));

-- name: DeleteResearchFindingsByEvidence :execrows
DELETE FROM research_findings WHERE evidence_ids && @evidence_ids::uuid[];

-- A post whose last observation expired has nothing left to stand on. The grace keeps a post that was just added and is waiting for its
-- first observation.
-- name: DeleteOrphanResearchPosts :execrows
DELETE FROM research_posts p
WHERE p.created_at < @before
  AND NOT EXISTS (SELECT 1 FROM research_evidence e WHERE e.post_id = p.id);

-- A candidate or dropped creator with no post and no observation, and untouched for the retention time, is only a handle: delete it.
-- name: DeleteIdleResearchCreators :execrows
DELETE FROM research_creators c
WHERE c.status <> 'approved'
  AND c.updated_at < @before
  AND NOT EXISTS (SELECT 1 FROM research_posts p WHERE p.creator_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM research_evidence e WHERE e.creator_id = c.id);

-- Takedown (`cfctl research forget`).

-- name: ListResearchCreatorsByHandle :many
SELECT * FROM research_creators WHERE handle = @handle AND (@platform::text = '' OR platform = @platform) ORDER BY platform;

-- name: ListResearchEvidenceIDsOfCreator :many
SELECT e.id FROM research_evidence e
WHERE e.creator_id = @creator_id OR e.post_id IN (SELECT p.id FROM research_posts p WHERE p.creator_id = @creator_id);

-- name: CountResearchOfCreator :one
SELECT
    (SELECT count(*) FROM research_posts p WHERE p.creator_id = @creator_id)::bigint AS posts,
    (SELECT count(*) FROM research_annotations a JOIN research_posts p ON p.id = a.post_id WHERE p.creator_id = @creator_id)::bigint AS annotations,
    (SELECT count(*) FROM research_evidence e WHERE e.creator_id = @creator_id OR e.post_id IN (SELECT p.id FROM research_posts p WHERE p.creator_id = @creator_id))::bigint AS evidence;

-- name: DeleteResearchCreator :execrows
DELETE FROM research_creators WHERE id = @id;
