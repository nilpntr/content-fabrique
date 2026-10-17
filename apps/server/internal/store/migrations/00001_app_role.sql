-- The app role split (docs/06-setup-and-credentials.md section 3.4).
--
-- Migrations run as the migrate role, which owns the schema. The application connects as a different login role that
-- may only read and write rows. That login role is a member of a group role, made once by hand:
-- GRANT <database>_app TO <app login role>. Every migration grants the group role its rights on the tables it creates,
-- through cf_grant_app_dml below, so the migrations never name a login role.
--
-- The group role is per database: its name is the database name plus "_app" (cf_app for the local database "cf").
-- Roles are cluster-wide in Postgres. One shared group role would let the app login of one environment on a server
-- reach the database of another. CONNECT is revoked from PUBLIC and granted to the group role for the same reason.
--
-- There is deliberately no ALTER DEFAULT PRIVILEGES: a table the app should not fully write (an append-only ledger,
-- say) would get UPDATE and DELETE too. Each migration names what it hands out.

-- +goose Up

-- Postgres 18 added uuidv7(), which the id defaults call (D-24). Fail early and plainly on an older server.
-- Closing the database to everybody but the group role needs the migrate role to own the database and public schema.
-- Say so before changing anything, rather than leaving DDL open or failing half way.
-- +goose StatementBegin
DO $$
DECLARE
    app_role text := current_database() || '_app';
BEGIN
    IF current_setting('server_version_num')::int < 180000 THEN
        RAISE EXCEPTION 'Postgres 18 or newer is required (uuidv7()); this server is %', current_setting('server_version');
    END IF;
    IF length(app_role) > 63 THEN
        RAISE EXCEPTION 'the database name "%" is too long: the app group role "%" would exceed 63 characters',
            current_database(), app_role;
    END IF;
    IF NOT pg_has_role(current_user, (SELECT nspowner FROM pg_namespace WHERE nspname = 'public'), 'USAGE') THEN
        RAISE EXCEPTION 'this role (%) does not own the schema public, so it cannot close it to other roles '
            '(REVOKE CREATE ON SCHEMA public FROM PUBLIC). Run the migrations as the role that owns the database and '
            'its public schema, or make this role the owner. See docs/06-setup-and-credentials.md, section 3.4.',
            current_user;
    END IF;
    IF NOT pg_has_role(current_user, (SELECT datdba FROM pg_database WHERE datname = current_database()), 'USAGE') THEN
        RAISE EXCEPTION 'this role (%) does not own the database %, so it cannot restrict CONNECT to the app role. '
            'Run the migrations as the database owner. See docs/06-setup-and-credentials.md, section 3.4.',
            current_user, current_database();
    END IF;

    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = app_role) THEN
        BEGIN
            EXECUTE format('CREATE ROLE %I NOLOGIN', app_role);
        EXCEPTION WHEN insufficient_privilege THEN
            RAISE EXCEPTION 'the role % does not exist and this role cannot create it (it lacks CREATEROLE). '
                'Create it once as an administrator: CREATE ROLE % NOLOGIN; then GRANT % TO <the app login role>; '
                'then run the migrations again. See docs/06-setup-and-credentials.md, section 3.4.',
                app_role, app_role, app_role;
        END;
    END IF;

    -- Nobody but the owner creates objects (Postgres 15 and later default to this; older and upgraded databases do not),
    -- and only the app group role may connect besides the owner and superusers.
    REVOKE CREATE ON SCHEMA public FROM PUBLIC;
    EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', app_role);
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', current_database());
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), app_role);
END
$$;
-- +goose StatementEnd

-- The group role of this database.
CREATE FUNCTION cf_app_role() RETURNS name
    LANGUAGE sql STABLE AS $$ SELECT (current_database() || '_app')::name $$;

-- One line per table in later migrations: SELECT cf_grant_app_dml('table_name');
-- Grants DML only (no TRUNCATE, TRIGGER or REFERENCES) plus USAGE and SELECT on the sequences the table owns.
-- Runs with the caller's rights, so only the table's owner (the migrate role) can use it.
-- +goose StatementBegin
CREATE FUNCTION cf_grant_app_dml(tbl regclass) RETURNS void
    LANGUAGE plpgsql AS $$
DECLARE
    app_role name := cf_app_role();
    seq regclass;
BEGIN
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %s TO %I', tbl, app_role);
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

REVOKE ALL ON FUNCTION cf_grant_app_dml(regclass) FROM PUBLIC;

-- +goose Down

DROP FUNCTION cf_grant_app_dml(regclass);

-- The role stays: it is cluster-wide state an administrator made, and it may carry the app login's membership.
-- Only this database's rights are taken back.
-- +goose StatementBegin
DO $$
DECLARE
    app_role text := current_database() || '_app';
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = app_role) THEN
        EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM %I', current_database(), app_role);
        EXECUTE format('REVOKE USAGE ON SCHEMA public FROM %I', app_role);
    END IF;
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO PUBLIC', current_database());
END
$$;
-- +goose StatementEnd

DROP FUNCTION cf_app_role();
