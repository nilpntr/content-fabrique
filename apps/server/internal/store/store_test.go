package store_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

// migrate applies every migration to the database as the connection's role and returns the migrator's version.
func migrate(t *testing.T, dsn string) int64 {
	t.Helper()
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	ctx := context.Background()
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	v, err := m.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// openStore opens the app pool over dsn and closes it when the test ends.
func openStore(t *testing.T, dsn string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "store-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return st
}

func TestOpenErrorsDoNotLeakThePassword(t *testing.T) {
	const secret = "s3cr3t-pa55"
	for _, dsn := range []string{
		"postgres://cf:" + secret + "@localhost:5432/cf?sslmode=bogus",
		"postgres://cf:" + secret + "@localhost:notaport/cf",
	} {
		_, err := store.Open(context.Background(), dsn, store.Options{})
		if err == nil {
			t.Fatalf("%q: expected an error", dsn)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks the password: %v", err)
		}
	}
	if _, err := store.NewMigrator("postgres://cf:" + secret + "@localhost:notaport/cf"); err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("migrator error leaks the password: %v", err)
	}
}

// The pool must not create named prepared statements: PlanetScale's pooled endpoint is a transaction-mode PgBouncer,
// where the next query can land on another server connection and fail with "prepared statement does not exist".
// pg_prepared_statements lists the named ones of a session, so it must stay empty after repeated queries.
func TestPoolUsesNoNamedPreparedStatements(t *testing.T) {
	srv := storetest.Start(t)
	database := srv.NewDatabase(t, "")
	st := openStore(t, srv.SuperDSN(database))
	ctx := context.Background()

	conn, err := st.Pool().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	count := func(c *pgx.Conn) int {
		t.Helper()
		for range 3 { // the second and later runs are where a statement cache would prepare
			var n int
			if err := c.QueryRow(ctx, `SELECT $1::int`, 1).Scan(&n); err != nil {
				t.Fatal(err)
			}
		}
		var n int
		if err := c.QueryRow(ctx, `SELECT count(*) FROM pg_prepared_statements`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(conn.Conn()); n != 0 {
		t.Fatalf("the pool left %d named prepared statements on its connection", n)
	}

	// Control: pgx's default mode does prepare, so the check above can fail.
	plain, err := pgx.Connect(ctx, srv.SuperDSN(database))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plain.Close(ctx) }()
	if n := count(plain); n == 0 {
		t.Fatal("control failed: pgx's default mode left no prepared statements, so this test proves nothing")
	}
}
