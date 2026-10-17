-- Plans and the paywall (P0-16): what a workspace is entitled to, and why.
--
-- One row per workspace, created by the first grant. A workspace with no row, or with plan 'none', has no plan and sees
-- the paywall. This table is the only source of truth for the plan: the free-text workspaces.plan column of schema v1
-- never meant anything and is dropped, so there is nothing to keep in step with it.
--
-- A plan is ACTIVE when plan <> 'none', status = 'active' and current_period_end is null or in the future. It is
-- evaluated at request time by the query that reads it (see queries/workspace_plans.sql), so an expired grant stops
-- working at that instant and no job has to run for it.
--
-- plan:   none, trial, studio, creator or pro (product plan section 10).
-- status: active, past_due or canceled. A manual revoke is plan 'none' with status 'canceled'.
-- source: manual (cfctl plan grant) or merchant (the merchant of record's webhooks, from P7-01; D-13 picks which one,
--         and a later migration may split the value).
-- current_period_end: when the paid period ends; null means it does not end (a manual grant without --until).
-- granted_by: who or what wrote the row last, for the audit trail ("cfctl:<operator>"). Never a secret.

-- +goose Up

CREATE TABLE workspace_plans (
    workspace_id       uuid        PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    plan               text        NOT NULL DEFAULT 'none',
    status             text        NOT NULL DEFAULT 'canceled',
    source             text        NOT NULL DEFAULT 'manual',
    current_period_end timestamptz,
    granted_by         text        NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT workspace_plans_plan_check CHECK (plan IN ('none', 'trial', 'studio', 'creator', 'pro')),
    CONSTRAINT workspace_plans_status_check CHECK (status IN ('active', 'past_due', 'canceled')),
    CONSTRAINT workspace_plans_source_check CHECK (source IN ('manual', 'merchant'))
);

CREATE TRIGGER workspace_plans_set_updated_at BEFORE UPDATE ON workspace_plans
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

SELECT cf_grant_app_dml('workspace_plans');

ALTER TABLE workspaces DROP COLUMN plan;

-- +goose Down

ALTER TABLE workspaces ADD COLUMN plan text NOT NULL DEFAULT 'free';

DROP TABLE workspace_plans;
