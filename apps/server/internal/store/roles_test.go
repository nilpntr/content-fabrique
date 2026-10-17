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
//   - a separate app login role, a member of the group role cf_app, that the services use (DATABASE_URL).
//
// Acceptance: the app role reads and writes rows, and gets a permission error on CREATE TABLE.
const (
	migrateRole = "cf_test_migrate"
	appLogin    = "cf_test_app"
	rolePass    = "role-pass"
)

// permissionDenied is SQLSTATE 42501 insufficient_privilege.
const permissionDenied = "42501"

type roleSetup struct {
	// migrateCreateRole gives the migrate role CREATEROLE, as a managed Postgres often does.
	migrateCreateRole bool
	// preCreateGroup makes cf_app and adds the app login to it by hand, before the first migration.
	preCreateGroup bool
	// legacyPublicCreate grants CREATE on schema public to PUBLIC first, as Postgres 14 and older defaulted to.
	legacyPublicCreate bool
}

// setupRoles prepares a cluster with the roles and a database owned by the migrate role, and returns the database name.
// Roles are cluster-wide, so it removes them (and cf_app) before and after.
func setupRoles(t *testing.T, srv *storetest.Server, rs roleSetup) string {
	t.Helper()
	all := []string{"cf_app", migrateRole, appLogin}
	srv.DropRoles(t, all...)
	// Registered before the database, so it runs after the database is dropped.
	t.Cleanup(func() { srv.DropRoles(t, all...) })

	migrateAttrs := "LOGIN"
	if rs.migrateCreateRole {
		migrateAttrs += " CREATEROLE"
	}
	srv.Exec(t, "postgres",
		fmt.Sprintf(`CREATE ROLE %s %s PASSWORD '%s'`, migrateRole, migrateAttrs, rolePass),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, appLogin, rolePass),
	)
	if rs.preCreateGroup {
		srv.Exec(t, "postgres", `CREATE ROLE cf_app NOLOGIN`, `GRANT cf_app TO `+appLogin)
	}
	database := srv.NewDatabase(t, migrateRole)
	if rs.legacyPublicCreate {
		srv.Exec(t, database, `GRANT CREATE ON SCHEMA public TO PUBLIC`)
	}
	return database
}

func TestRoleSplit(t *testing.T) {
	srv := storetest.Start(t)

	// A migrate role that cannot create roles, with no cf_app yet: the first migration must stop with a message
	// that says what to do, and leave nothing behind.
	t.Run("cf_app missing and no CREATEROLE", func(t *testing.T) {
		database := setupRoles(t, srv, roleSetup{})
		m, err := store.NewMigrator(srv.DSN(database, migrateRole, rolePass))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = m.Close() }()
		_, err = m.Up(context.Background())
		if err == nil {
			t.Fatal("up succeeded without cf_app and without CREATEROLE")
		}
		for _, want := range []string{"cf_app", "CREATEROLE", "CREATE ROLE cf_app NOLOGIN", "docs/06-setup-and-credentials.md"} {
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

	// The staging path: an administrator made cf_app and the membership once; the migrate role (no CREATEROLE) runs
	// the migrations.
	t.Run("staging: administrator made cf_app", func(t *testing.T) {
		database := setupRoles(t, srv, roleSetup{preCreateGroup: true})
		assertRoleSplit(t, srv, database)
	})

	// The migration creates cf_app itself when the role is allowed to; membership is still granted by hand.
	t.Run("migrate role with CREATEROLE creates cf_app", func(t *testing.T) {
		database := setupRoles(t, srv, roleSetup{migrateCreateRole: true})
		migrate(t, srv.DSN(database, migrateRole, rolePass))
		srv.Exec(t, "postgres", `GRANT cf_app TO `+appLogin)
		assertAppRole(t, srv, database)
	})

	// An upgraded database whose public schema still lets everybody create objects: the first migration closes it.
	t.Run("legacy public schema is closed", func(t *testing.T) {
		database := setupRoles(t, srv, roleSetup{preCreateGroup: true, legacyPublicCreate: true})
		assertRoleSplit(t, srv, database)
	})
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

	// 1. It can read and write rows: the whole repository suite, with every query as the app role.
	repositorySuite(t, openStore(t, appDSN))

	// Plain SQL as well, for the four verbs of the criterion.
	for _, stmt := range []string{
		`INSERT INTO workspaces (id, clerk_org_id, name) VALUES ('0192b2c4-0000-7000-8000-000000000001', 'org_dml', 'dml')`,
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
