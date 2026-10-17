-- Schema v1: workspaces, users, memberships and creator profiles (architecture section 4, D-01, D-12, D-24).
--
-- Keys are UUIDv7 generated in Go (internal/ids), so no column has a default and there are no sequences.
-- Every tenant-owned table carries workspace_id. Clerk's own ids are stored as text next to ours and never reused.

-- +goose Up

-- Keeps updated_at honest whatever the statement forgot to set. Runs with the caller's rights, which is enough: it
-- reads and writes only the row being updated.
-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TABLE workspaces (
    id           uuid        PRIMARY KEY,
    clerk_org_id text        NOT NULL,
    name         text        NOT NULL,
    plan         text        NOT NULL DEFAULT 'free',
    region       text        NOT NULL DEFAULT 'eu',
    billing_mode text        NOT NULL DEFAULT 'managed',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT workspaces_clerk_org_id_key UNIQUE (clerk_org_id),
    CONSTRAINT workspaces_billing_mode_check CHECK (billing_mode IN ('managed', 'byok'))
);

CREATE TABLE users (
    id            uuid        PRIMARY KEY,
    clerk_user_id text        NOT NULL,
    email         text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_clerk_user_id_key UNIQUE (clerk_user_id)
);

CREATE TABLE workspace_members (
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id),
    CONSTRAINT workspace_members_role_check CHECK (role IN ('owner', 'editor', 'viewer'))
);

-- "Which workspaces is this user in" runs on every sign-in; the primary key serves the other direction.
CREATE INDEX workspace_members_user_id_idx ON workspace_members (user_id);

-- One profile per workspace, created on first save.
CREATE TABLE creator_profiles (
    workspace_id     uuid        PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    niche            jsonb       NOT NULL DEFAULT '{}',
    audience_regions text[]      NOT NULL DEFAULT '{}',
    tone             text        NOT NULL DEFAULT '',
    language         text        NOT NULL DEFAULT 'en',
    brand            jsonb       NOT NULL DEFAULT '{}',
    defaults         jsonb       NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER workspaces_set_updated_at BEFORE UPDATE ON workspaces
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER workspace_members_set_updated_at BEFORE UPDATE ON workspace_members
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER creator_profiles_set_updated_at BEFORE UPDATE ON creator_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The app role reads and writes rows and nothing else: no TRUNCATE, TRIGGER or REFERENCES, no DDL.
GRANT SELECT, INSERT, UPDATE, DELETE ON workspaces, users, workspace_members, creator_profiles TO cf_app;

-- +goose Down

DROP TABLE creator_profiles;
DROP TABLE workspace_members;
DROP TABLE users;
DROP TABLE workspaces;
DROP FUNCTION set_updated_at();
