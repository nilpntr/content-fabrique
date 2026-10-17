-- A signed webhook delivery that cannot be used (a payload we do not understand) is recorded too, with status 'failed',
-- so Svix gets a 2xx and does not retry it for days or disable the endpoint. Only 'applied' deliveries count as
-- membership tombstones. detail holds a short reason that never contains payload values.

-- +goose Up

ALTER TABLE webhook_events
    ADD COLUMN status text NOT NULL DEFAULT 'applied',
    ADD COLUMN detail text NOT NULL DEFAULT '',
    ADD CONSTRAINT webhook_events_status_check CHECK (status IN ('applied', 'failed'));

-- +goose Down

ALTER TABLE webhook_events
    DROP CONSTRAINT webhook_events_status_check,
    DROP COLUMN detail,
    DROP COLUMN status;
