-- Credits (P7-01a, docs/design/spend-control-v1.md sections 3 to 5 and 10, decision D-31): what the customer pays with.
--
-- Three tables, one unit (integer credits). The dollar ledger (usage_ledger, spend_holds, spend_counters) is the other side; the two are
-- joined by action_id.
--
-- credit_accounts: one row per workspace. It exists to be LOCKED: every transaction that reads the balance in order to change it (a hold,
--     a settle, a grant, an expiry) takes this row FOR UPDATE first, so two requests for the last credits take turns and exactly one wins.
--     It also remembers what the periodic grant needs: the plan the current allowance belongs to, the anchor of a manual plan's monthly
--     periods, and when the current allowance period ends. Lock order: credit_accounts, then (in other transactions, never in the same
--     one) the spend counters. No transaction takes both.
--
-- credit_ledger: append-only for the app role (SELECT and INSERT, cf_grant_app_append_only). One row per event.
--     kind:     grant_allowance (a plan's allowance for a period; trial: once), grant_topup (bought or given; expires), charge (credits used
--               by an action), refund (a charge given back; a zero-credit refund marks a free or expired charge as given back, so it stops counting as a charged edit), expire (unused credits of a grant that ran out of time, or that a plan change
--               replaced), adjust (an operator's correction, with actor and reason).
--     credits:  signed. Grants and positive adjusts add, charges and expiries take away, refunds add back.
--     SOURCES and USE. A row that adds credits (grant_allowance, grant_topup and a positive adjust) is a SOURCE. Every row that takes
--               from or gives back to a source names it in grant_id, so the credits left in a source are its own credits plus the sum of
--               the rows that name it, and its expiry is exact (an expire row takes exactly what is left). A charge that spans two sources is
--               two rows, one per source, keys 'charge:<action id>:<n>'. This is the design choice of tracking consumption per grant (rather
--               than an allocation table): the ledger is its own allocation table, and the balance is a sum of it.
--     Order of use: the allowance first, then the other sources, earliest expiry first. Nothing is ever taken below zero: a charge for
--               more than is left (the allowance expired while the run was working) takes what is left and says so in reason.
--     A zero-credit charge (export.final, or a run started with cfctl agent run --no-credits) is a row too: it records that the action
--               happened, which is what "a charged edit" is.
--     expires_at: on a source, when it stops being usable (null: never; only a trial's allowance).
--     plan, plan_list_version: the plan and the price-list version that priced the row.
--     actor, reason: who adjusted and why (required for adjust); reason also explains a short charge.
--     idempotency_key: unique, so a replayed settle, grant or expiry changes nothing.
--
-- credit_holds: credits set aside for an action that is under way. Active holds are subtracted from the balance. The hold of a run is
--     deleted in the same transaction that settles it (charge row) or releases it (no row), and a sweep deletes holds past their expiry.
--     A hold's expiry follows its run: a heartbeat moves it forward, and a run that waits for its creator keeps it for days.
--     credits may be 0 (a --no-credits run still has a hold, so that no editor run is ever without one).

-- agent_runs.min_output_tokens: the routing entry's min_output_tokens, resolved with the rest of the entry when the run is created: the
-- smallest max_tokens a call of the run may be shortened to when the run's budget cannot pay for a full-size call. 0: the client's default.

-- +goose Up

ALTER TABLE agent_runs ADD COLUMN min_output_tokens integer NOT NULL DEFAULT 0 CONSTRAINT agent_runs_min_output_tokens_check CHECK (min_output_tokens >= 0);

CREATE TABLE credit_accounts (
    workspace_id    uuid        PRIMARY KEY REFERENCES workspaces (id) ON DELETE RESTRICT,
    -- The plan whose allowance is current ('' before the first grant). A different plan in workspace_plans replaces the allowance.
    plan            text        NOT NULL DEFAULT '',
    -- A manual plan's periods are monthly from here (set at the first grant, and again when the plan changes).
    period_anchor   timestamptz,
    -- When the current allowance period ends (null: the allowance does not refill, a trial's).
    allowance_until timestamptz,
    -- How many times the plan has changed. It is part of the allowance's ledger key from the first change on, so a plan that comes back
    -- within the same period (a downgrade after an upgrade) gets its allowance again instead of finding the old key.
    plan_generation integer     NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER credit_accounts_set_updated_at BEFORE UPDATE ON credit_accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Rows are kept and changed in place; never deleted.
SELECT cf_grant_app_no_delete('credit_accounts');

CREATE TABLE credit_ledger (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id     uuid        NOT NULL REFERENCES workspaces (id) ON DELETE RESTRICT,
    kind             text        NOT NULL,
    credits          integer     NOT NULL,
    grant_id         uuid,
    action           text,
    action_id        uuid,
    project_id       uuid,
    member_user_id   uuid,
    plan             text,
    plan_list_version text,
    -- On a charge for an edit: the newest timeline version the run authored when it was charged, i.e. the work the charge paid for. Null
    -- when the run made none, and on every other kind of row.
    timeline_version integer,
    expires_at       timestamptz,
    actor            text,
    reason           text,
    idempotency_key  text        NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- A row that names a source names one of the same workspace: the composite key refuses any other, so credits cannot cross workspaces
    -- even through a bug (a null grant_id is not checked).
    CONSTRAINT credit_ledger_workspace_id_key UNIQUE (workspace_id, id),
    CONSTRAINT credit_ledger_grant_fkey FOREIGN KEY (workspace_id, grant_id) REFERENCES credit_ledger (workspace_id, id),
    CONSTRAINT credit_ledger_kind_check CHECK (kind IN ('grant_allowance', 'grant_topup', 'charge', 'refund', 'expire', 'adjust')),
    -- The sign of each kind. A charge or an expiry may be zero (a free action; nothing left to expire).
    CONSTRAINT credit_ledger_sign_check CHECK (
        CASE kind
            WHEN 'grant_allowance' THEN credits > 0
            WHEN 'grant_topup' THEN credits > 0
            WHEN 'refund' THEN credits >= 0
            WHEN 'charge' THEN credits <= 0
            WHEN 'expire' THEN credits <= 0
            ELSE credits <> 0
        END),
    -- Rows that give back to or take from a source name it. A positive adjust is a source itself; a negative adjust takes from one.
    CONSTRAINT credit_ledger_grant_id_check CHECK (
        CASE
            WHEN kind IN ('grant_allowance', 'grant_topup') THEN grant_id IS NULL
            WHEN kind = 'adjust' AND credits > 0 THEN grant_id IS NULL
            WHEN kind = 'refund' THEN credits = 0 OR grant_id IS NOT NULL
            WHEN kind IN ('expire', 'adjust') THEN grant_id IS NOT NULL
            WHEN kind = 'charge' THEN credits = 0 OR grant_id IS NOT NULL
            ELSE true
        END),
    -- A source that is not the trial's allowance expires.
    CONSTRAINT credit_ledger_expires_check CHECK (
        CASE
            WHEN kind = 'grant_topup' OR (kind = 'adjust' AND credits > 0) THEN expires_at IS NOT NULL
            WHEN kind = 'grant_allowance' THEN true
            ELSE expires_at IS NULL
        END),
    CONSTRAINT credit_ledger_adjust_check CHECK (kind <> 'adjust' OR (char_length(btrim(coalesce(actor, ''))) > 0 AND char_length(btrim(coalesce(reason, ''))) > 0)),
    CONSTRAINT credit_ledger_timeline_version_check CHECK (timeline_version IS NULL OR (kind = 'charge' AND timeline_version >= 1)),
    CONSTRAINT credit_ledger_action_check CHECK (action IS NULL OR action IN ('edit.first', 'edit.revision', 'export.final', 'ideation.run')),
    CONSTRAINT credit_ledger_charge_action_check CHECK (kind NOT IN ('charge', 'refund') OR (action IS NOT NULL AND action_id IS NOT NULL)),
    CONSTRAINT credit_ledger_text_check CHECK (char_length(coalesce(actor, '')) <= 200 AND char_length(coalesce(reason, '')) <= 1000),
    CONSTRAINT credit_ledger_key_check CHECK (char_length(idempotency_key) BETWEEN 1 AND 200)
);

CREATE UNIQUE INDEX credit_ledger_idempotency_key_key ON credit_ledger (idempotency_key);
-- The credits left in a source are its credits plus the rows that name it.
CREATE INDEX credit_ledger_grant_idx ON credit_ledger (grant_id) WHERE grant_id IS NOT NULL;
-- The live sources of a workspace, and the expiry job.
CREATE INDEX credit_ledger_sources_idx ON credit_ledger (workspace_id, expires_at) WHERE credits > 0 AND kind IN ('grant_allowance', 'grant_topup', 'adjust');
CREATE INDEX credit_ledger_expiry_idx ON credit_ledger (expires_at) WHERE credits > 0 AND kind IN ('grant_allowance', 'grant_topup', 'adjust') AND expires_at IS NOT NULL;
-- Was this action charged already; has this project a charged edit.
CREATE INDEX credit_ledger_action_idx ON credit_ledger (action_id) WHERE action_id IS NOT NULL;
CREATE INDEX credit_ledger_project_idx ON credit_ledger (workspace_id, project_id) WHERE kind = 'charge' AND project_id IS NOT NULL;
-- A workspace's history, newest first (the usage views of P7-01b).
CREATE INDEX credit_ledger_workspace_idx ON credit_ledger (workspace_id, created_at DESC);

SELECT cf_grant_app_append_only('credit_ledger');

CREATE TABLE credit_holds (
    id                uuid        PRIMARY KEY DEFAULT uuidv7(),
    workspace_id      uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    action            text        NOT NULL,
    action_id         uuid        NOT NULL,
    credits           integer     NOT NULL,
    project_id        uuid,
    member_user_id    uuid,
    plan              text,
    plan_list_version text,
    expires_at        timestamptz NOT NULL,
    idempotency_key   text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT credit_holds_action_check CHECK (action IN ('edit.first', 'edit.revision', 'export.final', 'ideation.run')),
    CONSTRAINT credit_holds_credits_check CHECK (credits >= 0),
    CONSTRAINT credit_holds_key_check CHECK (char_length(idempotency_key) BETWEEN 1 AND 200)
);

CREATE UNIQUE INDEX credit_holds_idempotency_key_key ON credit_holds (idempotency_key);
CREATE UNIQUE INDEX credit_holds_action_key ON credit_holds (action_id);
CREATE INDEX credit_holds_workspace_idx ON credit_holds (workspace_id, expires_at);
-- The sweep.
CREATE INDEX credit_holds_expires_idx ON credit_holds (expires_at);

SELECT cf_grant_app_dml('credit_holds');

-- +goose Down

ALTER TABLE agent_runs DROP COLUMN min_output_tokens;

DROP TABLE credit_holds;
DROP TABLE credit_ledger;
DROP TABLE credit_accounts;
