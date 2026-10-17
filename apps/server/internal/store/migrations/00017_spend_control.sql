-- Spend control foundation (P7-09a, docs/design/spend-control-v1.md sections 6 and 10, decision D-31).
--
-- usage_ledger, generalised.
--   kind:        what was metered: llm, transcription, render (the render jobs' CPU), media (ingest, quick look and analysis
--                CPU), storage and egress (P7-09b meters those two; the kinds are ready).
--   quantity, unit: how much, in the smallest whole unit so that money and quantity stay integers: tokens (llm), audio_ms
--                (hosted transcription), vcpu_ms (render, media and local transcription: compute), byte_days (a storage
--                snapshot) and bytes (egress). The design note says seconds, GB-days and GB for the report; the report divides.
--                Existing llm rows keep their token columns, and quantity holds their total.
--   project_id, member_user_id, action_id: attribution, all nullable and without foreign keys, because billing history must
--                outlive a project or a member. action_id is the agent run's id until P7-01a introduces real actions.
--   idempotency_key: one per metered event ('llm:<hold id>', 'transcribe:<job>:<attempt>:<chunk>', 'render:<render id>', ...),
--                unique, so a retried insert (or a replayed settle) is a no-op. Rows from before this migration get
--                'legacy:<id>'. request_id keeps its own unique index as a second guard.
--
-- Append-only. The app role gets SELECT and INSERT on usage_ledger and nothing else (cf_grant_app_append_only): a correction
-- is a new compensating row. The migrate role (the owner) can still change it, for migrations.
--
-- spend_holds:  money and audio set aside for a call that is in flight, in the database, so that every API and worker process
--               sees the same reservations (the in-process reservations of internal/agents/llm could not).
--   scope:      'workspace' (workspace_id set) or 'platform' (no workspace: spend that only the platform breaker sees; nothing makes one
--               yet, because every ledger row belongs to a workspace).
--   billed_to:  platform | workspace_key | test, as in usage_ledger. A platform hold counts against the breaker and the
--               workspace's daily cap, a test hold against the workspace's daily cap only, and no hold is made for workspace_key.
--   period:     the counter period the hold was made in ('day:YYYY-MM-DD' in UTC).
--   micros, audio_ms: the worst case of the call, in millionths of a US dollar, and the audio it will transcribe.
--   expires_at: a hold whose caller crashed stops counting at this time and is deleted by the sweep.
--
-- spend_counters: what was spent, per scope and period, updated in the same transaction as the ledger row, so a cap check
--               never sums the ledger.
--   platform_micros: ledger cost of billed_to = platform rows; test_micros: of billed_to = test rows; audio_ms: transcribed audio.
--                A workspace's daily cap counts platform_micros + test_micros; the platform breaker counts the platform row's
--                platform_micros only. workspace_key rows are in the ledger and in no counter.
--   alerted_80_at, tripped_at (platform rows): when the breaker passed 80 % of its cap and when it first refused a call, so
--                each alert is logged once per period.
--
-- Grants: spend_holds gets full DML (holds are created and deleted). spend_counters gets SELECT, INSERT and UPDATE, no DELETE.

-- +goose Up

-- The grant helpers. cf_grant_app_dml (migration 00001) gives the app role all four verbs; these give fewer. Both revoke what the role
-- had, grant their verbs, and hand out the sequences the table owns, through one private function. Like cf_grant_app_dml they run with
-- the caller's rights, so only the table's owner (the migrate role) can use them.
-- +goose StatementBegin
CREATE FUNCTION cf_grant_app_verbs(tbl regclass, verbs text) RETURNS void
    LANGUAGE plpgsql SET search_path = pg_catalog, public
    AS $$
DECLARE
    app_role name := public.cf_app_role();
    seq regclass;
BEGIN
    IF verbs NOT IN ('SELECT, INSERT', 'SELECT, INSERT, UPDATE') THEN
        RAISE EXCEPTION 'cf_grant_app_verbs: % is not one of the verb lists this helper hands out', verbs;
    END IF;
    EXECUTE format('REVOKE ALL ON TABLE %s FROM %I', tbl, app_role);
    EXECUTE format('GRANT %s ON TABLE %s TO %I', verbs, tbl, app_role);
    FOR seq IN
        SELECT d.objid::regclass
        FROM pg_depend d
        JOIN pg_class s ON s.oid = d.objid AND s.relkind = 'S'
        WHERE d.refobjid = tbl AND d.classid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
    LOOP
        EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE %s TO %I', seq, app_role);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- SELECT and INSERT only: an append-only table.
CREATE FUNCTION cf_grant_app_append_only(tbl regclass) RETURNS void
    LANGUAGE sql SET search_path = pg_catalog, public
    AS $$ SELECT public.cf_grant_app_verbs(tbl, 'SELECT, INSERT') $$;

-- SELECT, INSERT and UPDATE, no DELETE: rows that are kept and changed in place.
CREATE FUNCTION cf_grant_app_no_delete(tbl regclass) RETURNS void
    LANGUAGE sql SET search_path = pg_catalog, public
    AS $$ SELECT public.cf_grant_app_verbs(tbl, 'SELECT, INSERT, UPDATE') $$;

REVOKE ALL ON FUNCTION cf_grant_app_verbs(regclass, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION cf_grant_app_append_only(regclass) FROM PUBLIC;
REVOKE ALL ON FUNCTION cf_grant_app_no_delete(regclass) FROM PUBLIC;

ALTER TABLE usage_ledger
    ADD COLUMN quantity        bigint NOT NULL DEFAULT 0,
    ADD COLUMN unit            text   NOT NULL DEFAULT 'tokens',
    ADD COLUMN project_id      uuid,
    ADD COLUMN member_user_id  uuid,
    ADD COLUMN action_id       uuid,
    ADD COLUMN idempotency_key text,
    ALTER COLUMN input_tokens SET DEFAULT 0,
    ALTER COLUMN output_tokens SET DEFAULT 0;

UPDATE usage_ledger SET idempotency_key = 'legacy:' || id::text;
UPDATE usage_ledger SET quantity = input_tokens + cache_write_tokens + cache_read_tokens + output_tokens WHERE kind = 'llm';
UPDATE usage_ledger SET quantity = audio_ms, unit = 'audio_ms' WHERE kind = 'transcription';

ALTER TABLE usage_ledger
    ALTER COLUMN idempotency_key SET NOT NULL,
    DROP CONSTRAINT usage_ledger_kind_check,
    ADD CONSTRAINT usage_ledger_kind_check CHECK (kind IN ('llm', 'transcription', 'render', 'media', 'storage', 'egress')),
    ADD CONSTRAINT usage_ledger_unit_check CHECK (unit IN ('tokens', 'audio_ms', 'vcpu_ms', 'byte_days', 'bytes')),
    ADD CONSTRAINT usage_ledger_llm_unit_check CHECK (kind <> 'llm' OR unit = 'tokens'),
    ADD CONSTRAINT usage_ledger_quantity_check CHECK (quantity >= 0),
    ADD CONSTRAINT usage_ledger_key_check CHECK (char_length(idempotency_key) BETWEEN 1 AND 200);

CREATE UNIQUE INDEX usage_ledger_idempotency_key_key ON usage_ledger (idempotency_key);
-- A project's cost, and a member's.
CREATE INDEX usage_ledger_project_idx ON usage_ledger (workspace_id, project_id, created_at) WHERE project_id IS NOT NULL;

SELECT cf_grant_app_append_only('usage_ledger');

CREATE TABLE spend_holds (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    scope           text        NOT NULL,
    workspace_id    uuid        REFERENCES workspaces (id) ON DELETE CASCADE,
    billed_to       text        NOT NULL,
    kind            text        NOT NULL,
    period          text        NOT NULL,
    micros          bigint      NOT NULL DEFAULT 0,
    audio_ms        bigint,
    expires_at      timestamptz NOT NULL,
    idempotency_key text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT spend_holds_scope_check CHECK (scope IN ('workspace', 'platform')),
    CONSTRAINT spend_holds_scope_workspace_check CHECK ((scope = 'workspace') = (workspace_id IS NOT NULL)),
    CONSTRAINT spend_holds_billed_to_check CHECK (billed_to IN ('platform', 'workspace_key', 'test')),
    CONSTRAINT spend_holds_kind_check CHECK (kind IN ('llm', 'transcription', 'render', 'media', 'storage', 'egress')),
    CONSTRAINT spend_holds_period_check CHECK (period ~ '^(day:[0-9]{4}-[0-9]{2}-[0-9]{2}|bill:[0-9A-Za-z_.:-]{1,40})$'),
    CONSTRAINT spend_holds_amount_check CHECK (micros >= 0 AND (audio_ms IS NULL OR audio_ms >= 0)),
    CONSTRAINT spend_holds_key_check CHECK (char_length(idempotency_key) BETWEEN 1 AND 200)
);

CREATE UNIQUE INDEX spend_holds_idempotency_key_key ON spend_holds (idempotency_key);
-- The cap checks sum the live holds of one period.
CREATE INDEX spend_holds_period_idx ON spend_holds (period, workspace_id);
-- The sweep.
CREATE INDEX spend_holds_expires_idx ON spend_holds (expires_at);

SELECT cf_grant_app_dml('spend_holds');

CREATE TABLE spend_counters (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    scope           text        NOT NULL,
    workspace_id    uuid        REFERENCES workspaces (id) ON DELETE RESTRICT,
    period          text        NOT NULL,
    platform_micros bigint      NOT NULL DEFAULT 0,
    test_micros     bigint      NOT NULL DEFAULT 0,
    audio_ms        bigint      NOT NULL DEFAULT 0,
    alerted_80_at   timestamptz,
    tripped_at      timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT spend_counters_scope_check CHECK (scope IN ('workspace', 'platform')),
    CONSTRAINT spend_counters_scope_workspace_check CHECK ((scope = 'workspace') = (workspace_id IS NOT NULL)),
    CONSTRAINT spend_counters_period_check CHECK (period ~ '^(day:[0-9]{4}-[0-9]{2}-[0-9]{2}|bill:[0-9A-Za-z_.:-]{1,40})$'),
    CONSTRAINT spend_counters_amount_check CHECK (platform_micros >= 0 AND test_micros >= 0 AND audio_ms >= 0)
);

-- One row per workspace and period, and one platform row per period.
CREATE UNIQUE INDEX spend_counters_workspace_key ON spend_counters (workspace_id, period) WHERE scope = 'workspace';
CREATE UNIQUE INDEX spend_counters_platform_key ON spend_counters (period) WHERE scope = 'platform';

SELECT cf_grant_app_no_delete('spend_counters');

-- The counters start from what the ledger already holds, so the day's caps do not reset when this migration deploys: one workspace row
-- per workspace and UTC day and one platform row per day, from the existing rows. (Until now the daily cap summed every row of the
-- workspace; from here on a workspace_key row counts toward no cap, which is what BYOK needs.)
INSERT INTO spend_counters (scope, workspace_id, period, platform_micros, test_micros, audio_ms)
SELECT 'workspace', workspace_id, 'day:' || to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'),
       COALESCE(sum(list_cost_micros) FILTER (WHERE billed_to = 'platform'), 0),
       COALESCE(sum(list_cost_micros) FILTER (WHERE billed_to = 'test'), 0),
       COALESCE(sum(audio_ms) FILTER (WHERE kind = 'transcription'), 0)
FROM usage_ledger
GROUP BY workspace_id, 'day:' || to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD');

INSERT INTO spend_counters (scope, period, platform_micros)
SELECT 'platform', 'day:' || to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), sum(list_cost_micros)
FROM usage_ledger
WHERE billed_to = 'platform'
GROUP BY 'day:' || to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD');

-- Who started an agent run, for the ledger's member_user_id. Null for a run nobody started by hand (cfctl).
ALTER TABLE agent_runs ADD COLUMN started_by uuid REFERENCES users (id) ON DELETE SET NULL;

-- +goose Down

ALTER TABLE agent_runs DROP COLUMN started_by;

DROP TABLE spend_counters;
DROP TABLE spend_holds;

-- Rows of the new kinds cannot be kept under the old constraint.
DELETE FROM usage_ledger WHERE kind NOT IN ('llm', 'transcription');

DROP INDEX usage_ledger_project_idx;
DROP INDEX usage_ledger_idempotency_key_key;
ALTER TABLE usage_ledger
    DROP CONSTRAINT usage_ledger_key_check,
    DROP CONSTRAINT usage_ledger_quantity_check,
    DROP CONSTRAINT usage_ledger_llm_unit_check,
    DROP CONSTRAINT usage_ledger_unit_check,
    DROP CONSTRAINT usage_ledger_kind_check,
    ADD CONSTRAINT usage_ledger_kind_check CHECK (kind IN ('llm', 'transcription')),
    ALTER COLUMN input_tokens DROP DEFAULT,
    ALTER COLUMN output_tokens DROP DEFAULT,
    DROP COLUMN idempotency_key,
    DROP COLUMN action_id,
    DROP COLUMN member_user_id,
    DROP COLUMN project_id,
    DROP COLUMN unit,
    DROP COLUMN quantity;

SELECT cf_grant_app_dml('usage_ledger');

DROP FUNCTION cf_grant_app_no_delete(regclass);
DROP FUNCTION cf_grant_app_append_only(regclass);
DROP FUNCTION cf_grant_app_verbs(regclass, text);
