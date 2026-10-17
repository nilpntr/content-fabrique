package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// appTables are the tables the migrations create, besides goose's own bookkeeping table.
var appTables = []string{"creator_profiles", "users", "webhook_events", "workspace_members", "workspaces"}

func publicTables(t *testing.T, dsn string) []string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// up, down to 0, up again: the round trip CI runs with cfctl.
func TestMigrationsRoundTrip(t *testing.T) {
	srv := storetest.Start(t)
	database := srv.NewDatabase(t, "")
	dsn := srv.SuperDSN(database)
	ctx := context.Background()

	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	if v, err := m.Version(ctx); err != nil || v != 0 {
		t.Fatalf("fresh database version = %d, %v", v, err)
	}
	if res, err := m.Up(ctx); err != nil || len(res) == 0 {
		t.Fatalf("up = %v, %v", res, err)
	}
	if v, err := m.Version(ctx); err != nil || v != m.Latest() {
		t.Fatalf("version after up = %d (want %d), %v", v, m.Latest(), err)
	}
	if got := publicTables(t, dsn); !slices.Equal(got, appTables) {
		t.Fatalf("tables after up = %v, want %v", got, appTables)
	}
	// A second up has nothing to do.
	if res, err := m.Up(ctx); err != nil || len(res) != 0 {
		t.Fatalf("second up = %v, %v", res, err)
	}

	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	if v, _ := m.Version(ctx); v != 0 {
		t.Fatalf("version after down to 0 = %d", v)
	}
	if got := publicTables(t, dsn); len(got) != 0 {
		t.Fatalf("tables left after down to 0: %v", got)
	}
	// Down at version 0 is not an error.
	if res, err := m.Down(ctx); err != nil || len(res) != 0 {
		t.Fatalf("down at 0 = %v, %v", res, err)
	}

	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	if got := publicTables(t, dsn); !slices.Equal(got, appTables) {
		t.Fatalf("tables after the round trip = %v", got)
	}

	// redo and the status listing.
	if res, err := m.Redo(ctx); err != nil || len(res) != 2 || res[0].Direction != "down" || res[1].Direction != "up" {
		t.Fatalf("redo = %v, %v", res, err)
	}
	ss, err := m.Status(ctx)
	if err != nil || len(ss) != int(m.Latest()) {
		t.Fatalf("status = %v, %v", ss, err)
	}
	for _, s := range ss {
		if !s.Applied {
			t.Errorf("%s is not applied", s.Name)
		}
	}
}

// Redo rolls back the latest applied migration and applies that one again. It must not go on to apply others that
// are pending: here migration 2 is pending while 1 is redone.
func TestRedoRedoesExactlyOne(t *testing.T) {
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	ctx := context.Background()
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	if _, err := m.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	res, err := m.Redo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Direction != "down" || res[0].Version != 1 || res[1].Direction != "up" || res[1].Version != 1 {
		t.Fatalf("redo = %+v, want down 1 then up 1", res)
	}
	if v, _ := m.Version(ctx); v != 1 {
		t.Fatalf("version after redo = %d, want 1 (migration 2 must stay pending)", v)
	}
	if got := publicTables(t, dsn); len(got) != 0 {
		t.Fatalf("redo applied more than one migration: tables %v", got)
	}

	// Nothing applied: redo has nothing to roll back after a down to 0.
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if res, err := m.Redo(ctx); err != nil || len(res) != 0 {
		t.Fatalf("redo at version 0 = %+v, %v", res, err)
	}
}

func TestMigrationFilesAreNumberedWithoutGaps(t *testing.T) {
	// goose would also catch a duplicate; this catches a gap, which would mean a lost file.
	srv := storetest.Start(t)
	m, err := store.NewMigrator(srv.SuperDSN(srv.NewDatabase(t, "")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	ss, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range ss {
		if s.Version != int64(i+1) {
			t.Fatalf("migration %d is %s: versions must run 1, 2, 3 ...", i+1, s.Name)
		}
	}
}
