package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// The role split of docs/06 section 3.4, as it is on staging and production:
//
//   - a migrate role that owns the schema and runs the migrations (DATABASE_MIGRATE_URL), and
//   - a separate app login role, a member of the database's group role <database>_app, that the services use
//     (DATABASE_URL).
//
// Acceptance: the app role reads and writes rows, and gets a permission error on CREATE TABLE.
const (
	migrateRole = "cf_test_migrate"
	appLogin    = "cf_test_app"
	otherLogin  = "cf_test_other" // a login role that belongs to no group
	rolePass    = "role-pass"
)

// permissionDenied is SQLSTATE 42501 insufficient_privilege.
const permissionDenied = "42501"

// groupRole is the per-database group role the first migration looks for: the database name plus "_app".
func groupRole(database string) string { return database + "_app" }

type roleSetup struct {
	// migrateCreateRole gives the migrate role CREATEROLE, as a managed Postgres often does.
	migrateCreateRole bool
	// preCreateGroup makes the group role and adds the app login to it by hand, before the first migration.
	preCreateGroup bool
	// legacyPublicCreate grants CREATE on schema public to PUBLIC first, as Postgres 14 and older defaulted to.
	legacyPublicCreate bool
}

// dropTestRoles removes the test's roles, including the group roles of the test databases (t_<12 hex>_app).
func dropTestRoles(t *testing.T, srv *storetest.Server) {
	t.Helper()
	srv.Exec(t, "postgres", `DO $$
DECLARE r text;
BEGIN
    FOR r IN SELECT rolname FROM pg_roles WHERE rolname ~ '^t_[0-9a-f]{12}_app$' LOOP
        EXECUTE format('DROP ROLE %I', r);
    END LOOP;
END
$$`)
	srv.DropRoles(t, migrateRole, appLogin, otherLogin)
}

// setupRoles prepares a cluster with the roles and a database owned by the migrate role, and returns the database
// name. Roles are cluster-wide, so it removes them before and after (after the databases are dropped).
func setupRoles(t *testing.T, srv *storetest.Server, rs roleSetup) string {
	t.Helper()
	dropTestRoles(t, srv)
	// Registered before the database, so it runs after the database is dropped.
	t.Cleanup(func() { dropTestRoles(t, srv) })

	migrateAttrs := "LOGIN"
	if rs.migrateCreateRole {
		migrateAttrs += " CREATEROLE"
	}
	srv.Exec(t, "postgres",
		fmt.Sprintf(`CREATE ROLE %s %s PASSWORD '%s'`, migrateRole, migrateAttrs, rolePass),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, appLogin, rolePass),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, otherLogin, rolePass),
	)
	database := srv.NewDatabase(t, migrateRole)
	if rs.preCreateGroup {
		srv.Exec(t, "postgres", `CREATE ROLE `+groupRole(database)+` NOLOGIN`, `GRANT `+groupRole(database)+` TO `+appLogin)
	}
	if rs.legacyPublicCreate {
		srv.Exec(t, database, `GRANT CREATE ON SCHEMA public TO PUBLIC`)
	}
	return database
}

func TestRoleSplit(t *testing.T) {
	srv := storetest.Start(t)

	// A migrate role that cannot create roles, with no group role yet: the first migration must stop with a message
	// that says what to do, and leave nothing behind.
	t.Run("group role missing and no CREATEROLE", func(t *testing.T) {
		database := setupRoles(t, srv, roleSetup{})
		m, err := store.NewMigrator(srv.DSN(database, migrateRole, rolePass))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = m.Close() }()
		_, err = m.Up(context.Background())
		if err == nil {
			t.Fatal("up succeeded without the group role and without CREATEROLE")
		}
		for _, want := range []string{groupRole(database), "CREATEROLE", "CREATE ROLE " + groupRole(database) + " NOLOGIN", "docs/06-setup-and-credentials.md"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not mention %q:\n%v", want, err)
			}
		}
		if v, _ := m.Version(context.Background()); v != 0 {
			t.Errorf("version after the failed migration = %d, want 0", v)
		}
		if got := publicTables(t, srv.DSN(database, migrateRole, rolePass)); len(got) != 0 {
			t.Errorf("tables left behind: %v", got)
		}
	})

	// Closing schema public and the database needs ownership of both. A migrate role that does not own the schema must
	// fail with a message, not leave DDL open or break the next migration.
	t.Run("migrate role does not own schema public", func(t *testing.T) {
		database := setupRoles(t, srv, roleSetup{migrateCreateRole: true})
		srv.Exec(t, database, `ALTER SCHEMA public OWNER TO `+storetest.SuperUser, `GRANT CREATE ON SCHEMA public TO `+migrateRole)
		m, err := store.NewMigrator(srv.DSN(database, migrateRole, rolePass))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = m.Close() }()
		_, err = m.Up(context.Background())
		if err == nil || !strings.Contains(err.Error(), "does not own the schema public") || !strings.Contains(err.Error(), "docs/06-setup-and-credentials.md") {
			t.Fatalf("up err = %v, want a message about owning schema public", err)
		}
		if got := publicTables(t, srv.SuperDSN(database)); len(got) != 0 {
			t.Errorf("tables left behind: %v", got)
		}
	})

	// The staging path: an administrator made the group role and the membership once; the migrate role (no CREATEROLE)
	// runs the migrations.
	t.Run("staging: administrator made the group role", func(t *testing.T) {
		database := setupRoles(t, srv, roleSetup{preCreateGroup: true})
		assertRoleSplit(t, srv, database)
	})

	// The migration creates the group role itself when the role is allowed to; membership is still granted by hand.
	t.Run("migrate role with CREATEROLE creates the group role", func(t *testing.T) {
		database := setupRoles(t, srv, roleSetup{migrateCreateRole: true})
		migrate(t, srv.DSN(database, migrateRole, rolePass))
		var login bool
		if err := queryOne(t, srv.SuperDSN(database), `SELECT rolcanlogin FROM pg_roles WHERE rolname = '`+groupRole(database)+`'`, &login); err != nil || login {
			t.Fatalf("group role %s: canlogin = %v, err = %v; want it created NOLOGIN", groupRole(database), login, err)
		}
		srv.Exec(t, "postgres", `GRANT `+groupRole(database)+` TO `+appLogin)
		assertAppRole(t, srv, database)
		assertGrantHelper(t, srv, database)
	})

	// An upgraded database whose public schema still lets everybody create objects: the first migration closes it.
	t.Run("legacy public schema is closed", func(t *testing.T) {
		database := setupRoles(t, srv, roleSetup{preCreateGroup: true, legacyPublicCreate: true})
		assertRoleSplit(t, srv, database)
	})

	// Roles are cluster-wide. The group role is per database, so the app login of one database on a server cannot
	// connect to another database on it.
	t.Run("one database's app role cannot reach another", func(t *testing.T) {
		a := setupRoles(t, srv, roleSetup{migrateCreateRole: true})
		b := srv.NewDatabase(t, migrateRole)
		migrate(t, srv.DSN(a, migrateRole, rolePass))
		migrate(t, srv.DSN(b, migrateRole, rolePass))
		if groupRole(a) == groupRole(b) {
			t.Fatal("two databases share one group role")
		}
		srv.Exec(t, "postgres", `GRANT `+groupRole(a)+` TO `+appLogin)

		assertConnectDenied(t, srv.DSN(b, appLogin, rolePass), "the app login of database A reached database B")
		assertAppRole(t, srv, a) // and database A works for it
		// A role in no group reaches neither.
		assertConnectDenied(t, srv.DSN(a, otherLogin, rolePass), "a login without a group role reached database A")
		assertConnectDenied(t, srv.DSN(b, otherLogin, rolePass), "a login without a group role reached database B")
	})
}

func queryOne(t *testing.T, dsn, query string, dest ...any) error {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	return conn.QueryRow(ctx, query).Scan(dest...)
}

func assertConnectDenied(t *testing.T, dsn, what string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err == nil {
		_ = conn.Close(ctx)
		t.Errorf("%s", what)
		return
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != permissionDenied {
		t.Errorf("%s: error = %v, want SQLSTATE %s", what, err, permissionDenied)
	}
}

// assertRoleSplit migrates as the migrate role, then checks the app role.
func assertRoleSplit(t *testing.T, srv *storetest.Server, database string) {
	t.Helper()
	migrateDSN := srv.DSN(database, migrateRole, rolePass)
	migrate(t, migrateDSN)
	assertAppRole(t, srv, database)

	// Down to 0 and up again as the migrate role keeps working, and the grants come back with the tables.
	m, err := store.NewMigrator(migrateDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if _, err := m.DownTo(context.Background(), 0); err != nil {
		t.Fatalf("down to 0 as the migrate role: %v", err)
	}
	if _, err := m.Up(context.Background()); err != nil {
		t.Fatalf("up again as the migrate role: %v", err)
	}
	assertAppRole(t, srv, database)
}

// assertGrantHelper checks cf_grant_app_dml on a table with an identity column, the way a later migration will use it.
func assertGrantHelper(t *testing.T, srv *storetest.Server, database string) {
	t.Helper()
	ctx := context.Background()
	migrateConn, err := pgx.Connect(ctx, srv.DSN(database, migrateRole, rolePass))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrateConn.Close(ctx) }()
	for _, stmt := range []string{
		`CREATE TABLE helper_probe (n int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, label text)`,
		`SELECT cf_grant_app_dml('helper_probe')`,
	} {
		if _, err := migrateConn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	appConn, err := pgx.Connect(ctx, srv.DSN(database, appLogin, rolePass))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appConn.Close(ctx) }()
	// INSERT draws from the identity sequence, which needs the USAGE the helper granted on it.
	for _, stmt := range []string{
		`INSERT INTO helper_probe (label) VALUES ('a')`,
		`UPDATE helper_probe SET label = 'b'`,
		`SELECT * FROM helper_probe`,
		`DELETE FROM helper_probe`,
	} {
		if _, err := appConn.Exec(ctx, stmt); err != nil {
			t.Errorf("app role: %s: %v", stmt, err)
		}
	}
	for _, stmt := range []string{`TRUNCATE helper_probe`, `SELECT cf_grant_app_dml('workspaces')`, `DROP TABLE helper_probe`} {
		_, err := appConn.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != permissionDenied {
			t.Errorf("app role: %s: error = %v, want SQLSTATE %s", stmt, err, permissionDenied)
		}
	}
}

// assertAppRole connects as the app login role and checks both halves of the acceptance criterion.
func assertAppRole(t *testing.T, srv *storetest.Server, database string) {
	t.Helper()
	ctx := context.Background()
	appDSN := srv.DSN(database, appLogin, rolePass)

	conn, err := pgx.Connect(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// It is a plain login role: not a superuser, no CREATEROLE, and it does not own the tables.
	var super, createRole bool
	if err := conn.QueryRow(ctx, `SELECT rolsuper, rolcreaterole FROM pg_roles WHERE rolname = current_user`).Scan(&super, &createRole); err != nil {
		t.Fatal(err)
	}
	if super || createRole {
		t.Fatalf("the app role is too powerful: superuser=%v createrole=%v", super, createRole)
	}

	// A login outside the group cannot even connect: CONNECT was revoked from PUBLIC.
	assertConnectDenied(t, srv.DSN(database, otherLogin, rolePass), "a login without the group role connected")

	// 1. It can read and write rows: the whole repository suite, with every query as the app role.
	repositorySuite(t, openStore(t, appDSN))

	// Plain SQL as well, for the four verbs of the criterion.
	for _, stmt := range []string{
		`INSERT INTO workspaces (clerk_org_id, name) VALUES ('org_dml', 'dml')`,
		`SELECT count(*) FROM workspaces`,
		`UPDATE workspaces SET name = 'dml2' WHERE clerk_org_id = 'org_dml'`,
		`DELETE FROM workspaces WHERE clerk_org_id = 'org_dml'`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Errorf("the app role cannot run %q: %v", stmt, err)
		}
	}

	// 2. It gets a permission error on DDL. CREATE TABLE is the acceptance criterion; the rest are the ways round it.
	denied := []string{
		`CREATE TABLE app_made (id int)`,
		`CREATE TABLE public.app_made_too AS SELECT 1 AS id`,
		`ALTER TABLE workspaces ADD COLUMN app_made int`,
		`ALTER TABLE workspaces RENAME TO app_renamed`,
		`DROP TABLE workspaces`,
		`DROP TABLE creator_profiles`,
		`TRUNCATE workspaces`,
		`CREATE INDEX app_made_idx ON workspaces (name)`,
		`CREATE FUNCTION app_made() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`CREATE VIEW app_made_view AS SELECT 1 AS id`,
		`CREATE SCHEMA app_made`,
		`DROP TRIGGER workspaces_set_updated_at ON workspaces`,
		`CREATE TRIGGER app_made BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at()`,
		// The grant helper is the schema owner's tool.
		`SELECT cf_grant_app_dml('workspaces')`,
		// The migration bookkeeping is the migrate role's alone.
		`SELECT * FROM goose_db_version`,
		`DELETE FROM goose_db_version`,
	}
	for _, stmt := range denied {
		_, err := conn.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != permissionDenied {
			t.Errorf("%q: error = %v, want SQLSTATE %s (permission denied)", stmt, err, permissionDenied)
			continue
		}
		if strings.HasPrefix(stmt, "CREATE TABLE app_made (") {
			t.Logf("CREATE TABLE as the app role: %s", pgErr.Message)
		}
	}

	// The tables are still there and unchanged.
	if got := publicTables(t, appDSN); strings.Join(got, ",") != strings.Join(appTables, ",") {
		t.Errorf("tables = %v, want %v", got, appTables)
	}
}
