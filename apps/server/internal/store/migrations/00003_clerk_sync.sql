-- Clerk sync (P0-07, D-12): soft deletes, the order-safety versions of synced rows, and the webhook delivery log.
--
-- clerk_updated_at is the `updated_at` of the Clerk object (Unix milliseconds) that last wrote the row. A sync write
-- applies only when it is newer, so a replayed or reordered webhook, or a just-in-time sync from a stale token,
-- cannot move a row back. 0 means "never synced from Clerk".
-- deleted_at marks a user or workspace that was deleted in Clerk. The row stays (data that belongs to the workspace
-- is not ours to drop on a webhook), but it can no longer sign in, and it is never undeleted by a sync.

-- +goose Up

ALTER TABLE workspaces
    ADD COLUMN deleted_at       timestamptz,
    ADD COLUMN clerk_updated_at bigint NOT NULL DEFAULT 0;

ALTER TABLE users
    ADD COLUMN deleted_at       timestamptz,
    ADD COLUMN clerk_updated_at bigint NOT NULL DEFAULT 0;

ALTER TABLE workspace_members
    ADD COLUMN clerk_updated_at bigint NOT NULL DEFAULT 0;

-- One row per Svix delivery id that was applied. The insert happens in the same transaction as the change, and the
-- primary key makes a replay (Svix retries, or a manual resend) a no-op. It also records when the last membership
-- delete for an (organization, user) pair happened, so a stale membership create or update that arrives after it
-- does not bring the membership back.
CREATE TABLE webhook_events (
    svix_id        text        PRIMARY KEY,
    event_type     text        NOT NULL,
    -- What the event is about, for the membership tombstone check: "<clerk org id>/<clerk user id>". Empty otherwise.
    subject        text        NOT NULL DEFAULT '',
    -- The event's own time in Unix milliseconds (the Clerk object's updated_at, or the envelope timestamp).
    clerk_event_at bigint      NOT NULL DEFAULT 0,
    received_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX webhook_events_membership_deleted_idx ON webhook_events (subject)
    WHERE event_type = 'organizationMembership.deleted';

SELECT cf_grant_app_dml('webhook_events');

-- +goose Down

DROP TABLE webhook_events;
ALTER TABLE workspace_members DROP COLUMN clerk_updated_at;
ALTER TABLE users DROP COLUMN clerk_updated_at, DROP COLUMN deleted_at;
ALTER TABLE workspaces DROP COLUMN clerk_updated_at, DROP COLUMN deleted_at;
