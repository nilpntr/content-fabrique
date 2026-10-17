-- agent_runs.min_output_tokens (P7-01a): the routing entry's min_output_tokens, resolved with the rest of the entry when the run is
-- created. It is the smallest max_tokens a call of the run may be shortened to when the run's budget cannot pay for a full-size call
-- (docs/design/spend-control-v1.md, "Budget-fitted calls"). 0 means the client's default (2048).

-- +goose Up

ALTER TABLE agent_runs ADD COLUMN min_output_tokens integer NOT NULL DEFAULT 0 CONSTRAINT agent_runs_min_output_tokens_check CHECK (min_output_tokens >= 0);

-- +goose Down

ALTER TABLE agent_runs DROP COLUMN min_output_tokens;
