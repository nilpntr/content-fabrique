-- Clip feedback (docs/design/proof-edit-v1.md): what the creator said about a shot of a proof, stored by clip and source range. EVERY
-- query filters by workspace_id: a row of another workspace is never returned or changed.

-- Saying the same thing about the same range again keeps one row (and its id): the note, where it was said and who said it last are
-- updated (updated_at says when). The composite foreign keys refuse a clip or a project of another workspace.
-- name: UpsertClipFeedback :one
INSERT INTO clip_feedback (workspace_id, asset_id, kind, start_ms, end_ms, project_id, timeline_version, shot_number, item_id, note,
                           created_by_kind, created_by_id)
VALUES (@workspace_id, @asset_id, @kind, @start_ms, @end_ms, @project_id, @timeline_version, @shot_number, @item_id, @note,
        @created_by_kind, @created_by_id)
ON CONFLICT (workspace_id, asset_id, kind, start_ms, end_ms) DO UPDATE
SET note = EXCLUDED.note, project_id = EXCLUDED.project_id, timeline_version = EXCLUDED.timeline_version,
    shot_number = EXCLUDED.shot_number, item_id = EXCLUDED.item_id,
    created_by_kind = EXCLUDED.created_by_kind, created_by_id = EXCLUDED.created_by_id
RETURNING *;

-- The workspace's feedback, newest first (by when it was first given), optionally on some clips only (an empty list: every clip),
-- after the cursor when there is one.
-- name: ListClipFeedback :many
SELECT * FROM clip_feedback
WHERE workspace_id = @workspace_id
  AND (cardinality(@asset_ids::uuid[]) = 0 OR asset_id = ANY (@asset_ids::uuid[]))
  AND (sqlc.narg('before')::uuid IS NULL OR id < sqlc.narg('before')::uuid)
ORDER BY id DESC
LIMIT @row_limit;

-- name: GetClipFeedback :one
SELECT * FROM clip_feedback WHERE workspace_id = @workspace_id AND id = @id;

-- name: DeleteClipFeedback :one
DELETE FROM clip_feedback WHERE workspace_id = @workspace_id AND id = @id
RETURNING id;
