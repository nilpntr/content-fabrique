-- Clip analysis (P2-02, P2-03, P2-09). EVERY query filters by workspace_id, like the other asset queries: the job acts with the
-- workspace of its arguments, and a row of another workspace is no row.

-- name: UpsertAssetSignals :one
INSERT INTO asset_signals (workspace_id, asset_id, version, step_ms, duration_ms, series, summary)
VALUES (@workspace_id, @asset_id, @version, @step_ms, @duration_ms, @series, @summary)
ON CONFLICT (asset_id) DO UPDATE
SET version = EXCLUDED.version, step_ms = EXCLUDED.step_ms, duration_ms = EXCLUDED.duration_ms,
    series = EXCLUDED.series, summary = EXCLUDED.summary
WHERE asset_signals.workspace_id = EXCLUDED.workspace_id
RETURNING asset_id;

-- A re-run replaces the shots and the analysis moments of the asset (moments of other sources stay).
-- name: DeleteAssetShots :exec
DELETE FROM shots WHERE workspace_id = @workspace_id AND asset_id = @asset_id;

-- name: DeleteAssetAnalysisMoments :exec
DELETE FROM moments WHERE workspace_id = @workspace_id AND asset_id = @asset_id AND source = 'analysis';

-- name: InsertShot :exec
INSERT INTO shots (workspace_id, asset_id, idx, start_ms, end_ms, signals)
VALUES (@workspace_id, @asset_id, @idx, @start_ms, @end_ms, @signals);

-- name: InsertMoment :exec
INSERT INTO moments (workspace_id, asset_id, kind, source, start_ms, end_ms, score, labels, attrs, description)
VALUES (@workspace_id, @asset_id, @kind, 'analysis', @start_ms, @end_ms, @score, @labels, @attrs, @description);

-- The one-glance summary of each asset, for the editor's context line.
-- name: ListAssetAnalysisSummaries :many
SELECT asset_id, version, duration_ms, summary FROM asset_signals
WHERE workspace_id = @workspace_id AND asset_id = ANY(@asset_ids::uuid[])
ORDER BY asset_id;

-- name: GetAssetSignals :one
SELECT * FROM asset_signals WHERE workspace_id = @workspace_id AND asset_id = @asset_id;

-- name: ListAssetShots :many
SELECT * FROM shots WHERE workspace_id = @workspace_id AND asset_id = @asset_id ORDER BY idx;

-- The moments of the given assets, best first. kind is empty for every kind but dead and shot (those are not clips to cut), or a
-- list of kinds; min_score filters; the caller has checked that the assets belong to the project asking.
-- name: ListMoments :many
SELECT * FROM moments
WHERE workspace_id = @workspace_id AND asset_id = ANY(@asset_ids::uuid[])
  AND kind = ANY(@kinds::text[]) AND score >= @min_score::real
ORDER BY score DESC, asset_id, start_ms
LIMIT @row_limit;

-- All the moments of one asset in time order, for `cfctl assets analyze --show` and the tests.
-- name: ListAssetMoments :many
SELECT * FROM moments WHERE workspace_id = @workspace_id AND asset_id = @asset_id ORDER BY start_ms, kind;

-- The ready videos of a workspace for the backfill (cfctl assets analyze), oldest first: those with no analysis of the current
-- version, or every one with include_done.
-- name: ListAssetsForAnalysis :many
SELECT a.* FROM assets a
LEFT JOIN asset_signals s ON s.workspace_id = a.workspace_id AND s.asset_id = a.id
WHERE a.workspace_id = @workspace_id AND a.kind = 'video' AND a.status = 'ready' AND a.deleted_at IS NULL
  AND (@include_done::boolean OR s.asset_id IS NULL OR s.version < @version::integer)
ORDER BY a.id
LIMIT @row_limit;
