-- Clip analysis (P2-02 shots, P2-03 quality signals, the deterministic slice of P2-09 reps; architecture sections 4 and 5,
-- decision D-10).
--
-- After the ingest marks a video ready, an `analyze` job makes one ffmpeg pass over the proxy and derives, without a model, what
-- the editor needs to cut on the beat of the movement: motion, exposure and sharpness per 100 ms, shots, and the reps, still
-- holds and dead time inside them.
--
-- asset_signals: one row per analysed asset. `series` holds the per-sample series (step_ms apart: motion, luma, contrast, sharp,
--                scene) as compact arrays; `summary` the one-glance result the editor's context line is made from. `version` is
--                the version of the analysis code that made it (analysis.Version), so a backfill can re-run older ones.
-- shots:         the continuous takes of an asset (start_ms, end_ms) with their signals (exposure and the correction that would
--                bring it to the target, sharpness, motion). docs/03 lists keyframe_key too; keyframes (P2-04) add it.
-- moments:       a stretch of an asset with a kind (rep, hold, dead, shot), a 0..1 score, labels, attrs and a short description
--                (the reason the editor reads: "rep 3 of 8, clean, full range"). `source` is analysis here; user annotations and the
--                agent's own marks come later (docs/03 section 4). The full-text search vector and the embedding of the docs come
--                with the moment builder (P2-06); this is the subset the editor's find_moments needs.
--
-- Tenancy as for the other asset tables: the composite foreign key ties a row to the workspace of its asset and every query
-- filters by workspace_id. Analysis rows are replaced as a whole by a re-run (one transaction).

-- +goose Up

CREATE TABLE asset_signals (
    workspace_id uuid        NOT NULL,
    asset_id     uuid        NOT NULL,
    version      integer     NOT NULL,
    step_ms      integer     NOT NULL,
    duration_ms  integer     NOT NULL,
    series       jsonb       NOT NULL,
    summary      jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_id),
    CONSTRAINT asset_signals_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT asset_signals_step_check CHECK (step_ms > 0),
    CONSTRAINT asset_signals_duration_check CHECK (duration_ms >= 0)
);

CREATE TRIGGER asset_signals_set_updated_at BEFORE UPDATE ON asset_signals
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('asset_signals');

CREATE TABLE shots (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid        NOT NULL,
    asset_id     uuid        NOT NULL,
    idx          integer     NOT NULL,
    start_ms     integer     NOT NULL,
    end_ms       integer     NOT NULL,
    signals      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT shots_asset_idx_key UNIQUE (asset_id, idx),
    CONSTRAINT shots_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT shots_range_check CHECK (idx >= 0 AND start_ms >= 0 AND end_ms > start_ms)
);

SELECT cf_grant_app_dml('shots');

CREATE TABLE moments (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id uuid        NOT NULL,
    asset_id     uuid        NOT NULL,
    kind         text        NOT NULL,
    source       text        NOT NULL DEFAULT 'analysis',
    start_ms     integer     NOT NULL,
    end_ms       integer     NOT NULL,
    score        real        NOT NULL DEFAULT 0,
    labels       text[]      NOT NULL DEFAULT '{}',
    attrs        jsonb       NOT NULL DEFAULT '{}',
    description  text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT moments_asset_fkey FOREIGN KEY (workspace_id, asset_id)
        REFERENCES assets (workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT moments_kind_check CHECK (kind IN ('rep', 'hold', 'dead', 'shot')),
    CONSTRAINT moments_source_check CHECK (source IN ('analysis', 'user', 'agent')),
    CONSTRAINT moments_range_check CHECK (start_ms >= 0 AND end_ms > start_ms),
    CONSTRAINT moments_score_check CHECK (score >= 0 AND score <= 1)
);

-- The timeline of an asset, and the best moments of a kind among a project's assets.
CREATE INDEX moments_asset_start_idx ON moments (workspace_id, asset_id, start_ms);
CREATE INDEX moments_kind_score_idx ON moments (workspace_id, kind, score DESC);

SELECT cf_grant_app_dml('moments');

-- +goose Down

DROP TABLE moments;
DROP TABLE shots;
DROP TABLE asset_signals;
