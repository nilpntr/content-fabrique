// Package jobstest migrates a throwaway test database the way `cfctl migrate up` does: the goose migrations, then River's
// tables with the app role's grants. Tests that enqueue jobs use it instead of store.Migrator alone.
package jobstest

import (
	"context"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Migrate applies every goose migration and then River's, over dsn (a role that owns the schema).
func Migrate(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("goose up: %v", err)
	}
	rm, err := jobs.NewMigrator(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	if _, err := rm.Up(ctx); err != nil {
		t.Fatalf("river up: %v", err)
	}
}
