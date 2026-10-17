package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	// Registers the "pgx" database/sql driver that goose runs on.
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// MigrationsFS returns the embedded migration files, 00001_name.sql and so on.
func MigrationsFS() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err) // the directory is embedded above, so this cannot happen
	}
	return sub
}

// Migrator runs the goose migrations over one connection to DATABASE_MIGRATE_URL: the role that owns the schema.
// The application never builds one; only `cfctl migrate` and the tests do.
type Migrator struct {
	db *sql.DB
	p  *goose.Provider
}

// NewMigrator connects to dsn (a direct, not pooled, URL: the migrations hold an advisory lock for their whole
// run, which a transaction-mode pooler does not keep). Errors do not contain the URL.
func NewMigrator(dsn string) (*Migrator, error) {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("DATABASE_MIGRATE_URL: cannot be parsed")
	}
	// An advisory lock makes two concurrent release steps take turns instead of racing.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, MigrationsFS(), goose.WithSessionLocker(locker))
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return &Migrator{db: sqlDB, p: p}, nil
}

// Close releases the connection.
func (m *Migrator) Close() error { return m.p.Close() }

// Result is one migration that ran.
type Result struct {
	Version   int64
	Name      string // the file name
	Direction string // "up" or "down"
	Duration  time.Duration
}

func results(rs []*goose.MigrationResult) []Result {
	out := make([]Result, 0, len(rs))
	for _, r := range rs {
		if r == nil {
			continue
		}
		out = append(out, Result{Version: r.Source.Version, Name: path.Base(r.Source.Path), Direction: r.Direction, Duration: r.Duration})
	}
	return out
}

// Up applies every pending migration.
func (m *Migrator) Up(ctx context.Context) ([]Result, error) {
	rs, err := m.p.Up(ctx)
	return results(rs), err
}

// UpTo applies pending migrations up to and including version.
func (m *Migrator) UpTo(ctx context.Context, version int64) ([]Result, error) {
	rs, err := m.p.UpTo(ctx, version)
	return results(rs), err
}

// Down rolls back the latest migration. It returns no results when there is nothing to roll back.
func (m *Migrator) Down(ctx context.Context) ([]Result, error) {
	r, err := m.p.Down(ctx)
	if errors.Is(err, goose.ErrNoNextVersion) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return results([]*goose.MigrationResult{r}), nil
}

// DownTo rolls back every migration above version. Version 0 rolls back everything.
func (m *Migrator) DownTo(ctx context.Context, version int64) ([]Result, error) {
	rs, err := m.p.DownTo(ctx, version)
	return results(rs), err
}

// Redo rolls back the latest migration and applies it again.
func (m *Migrator) Redo(ctx context.Context) ([]Result, error) {
	down, err := m.Down(ctx)
	if err != nil || len(down) == 0 {
		return down, err
	}
	// Exactly the migration that was rolled back, not "everything pending".
	up, err := m.p.ApplyVersion(ctx, down[0].Version, true)
	if err != nil {
		return down, err
	}
	return append(down, results([]*goose.MigrationResult{up})...), nil
}

// Version returns the latest applied migration, or 0 when none is.
func (m *Migrator) Version(ctx context.Context) (int64, error) {
	return m.p.GetDBVersion(ctx)
}

// Status is the state of one migration file.
type Status struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt time.Time
}

// Status lists every migration file and whether it was applied.
func (m *Migrator) Status(ctx context.Context) ([]Status, error) {
	ss, err := m.p.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(ss))
	for _, s := range ss {
		out = append(out, Status{
			Version: s.Source.Version, Name: path.Base(s.Source.Path), Applied: s.State == goose.StateApplied, AppliedAt: s.AppliedAt,
		})
	}
	return out, nil
}

// Latest is the highest version among the embedded files, as goose lists them.
func (m *Migrator) Latest() int64 {
	var latest int64
	for _, s := range m.p.ListSources() {
		latest = max(latest, s.Version)
	}
	return latest
}
