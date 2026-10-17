-- The app role split (docs/06-setup-and-credentials.md section 3.4).
--
-- Migrations run as the migrate role, which owns the schema. The application connects as a different login role that
-- may only read and write rows. That login role is a member of the group role cf_app (made once, by hand:
-- GRANT cf_app TO <app login role>), and every migration grants cf_app its rights on the tables it creates.
-- Granting to the group, not to a login role, keeps the migrations free of role names that differ per environment.
--
-- There is deliberately no ALTER DEFAULT PRIVILEGES: a table the app should not fully write (an append-only ledger,
-- say) would get UPDATE and DELETE too. Each migration names the rights it hands out.

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'cf_app') THEN
        BEGIN
            CREATE ROLE cf_app NOLOGIN;
        EXCEPTION WHEN insufficient_privilege THEN
            RAISE EXCEPTION 'the role cf_app does not exist and this role cannot create it (it lacks CREATEROLE). '
                'Create it once as an administrator: CREATE ROLE cf_app NOLOGIN; then GRANT cf_app TO <the app login role>; '
                'then run the migrations again. See docs/06-setup-and-credentials.md, section 3.4.';
        END;
    END IF;
END
$$;
-- +goose StatementEnd

-- Nobody but the schema owner creates objects. Postgres 15 and later already default to this; older versions and
-- upgraded databases do not.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

GRANT USAGE ON SCHEMA public TO cf_app;

-- +goose Down

-- The role stays: it is cluster-wide, so another database may use it. Only this database's rights are removed.
REVOKE USAGE ON SCHEMA public FROM cf_app;
