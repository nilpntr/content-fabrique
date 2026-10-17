package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// migrateLockName names the advisory lock that makes two release steps take turns on River's migrations. goose has its
// own lock for its line; the two lines are migrated one after the other by `cfctl migrate`.
const migrateLockName = "content-fabrique:river-migrate"

// Migrator migrates River's own tables (its "main" migration line) over one connection to DATABASE_MIGRATE_URL, the
// role that owns the schema. It is separate from the goose migrations on purpose: River versions its schema itself,
// so the goose files carry no River version numbers. Only `cfctl migrate` and the tests build one.
type Migrator struct {
	pool *pgxpool.Pool
	m    *rivermigrate.Migrator[pgx.Tx]
}

// NewMigrator connects to dsn (a direct URL). Errors never contain the URL.
func NewMigrator(ctx context.Context, dsn string) (*Migrator, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("DATABASE_MIGRATE_URL: cannot be parsed")
	}
	cfg.MaxConns = 3 // the lock connection, the migration, and a spare
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("river migrations: %w", err)
	}
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("river migrations: %w", err)
	}
	return &Migrator{pool: pool, m: m}, nil
}

// Close releases the connections.
func (m *Migrator) Close() { m.pool.Close() }

// Result is one River migration that ran.
type Result struct {
	Version   int
	Name      string
	Direction string // "up" or "down"
	Duration  time.Duration
}

// Status is the state of one River migration.
type Status struct {
	Version int
	Name    string
	Applied bool
}

// Up applies every pending River migration, then grants the app group role its rights on River's tables (see
// GrantApp). It must run after the goose migrations: the grant uses cf_grant_app_dml from migration 00001.
func (m *Migrator) Up(ctx context.Context) ([]Result, error) {
	var res []Result
	err := m.locked(ctx, func(ctx context.Context) error {
		r, err := m.m.Migrate(ctx, rivermigrate.DirectionUp, nil)
		if r != nil {
			res = toResults(r)
		}
		if err != nil {
			return err
		}
		return m.grantApp(ctx)
	})
	return res, err
}

// Down removes River's tables entirely (every River migration, newest first). The grants go with the tables. It must
// run before the goose migrations are rolled back to 0, which drops the function the grants use.
func (m *Migrator) Down(ctx context.Context) ([]Result, error) {
	var res []Result
	err := m.locked(ctx, func(ctx context.Context) error {
		r, err := m.m.Migrate(ctx, rivermigrate.DirectionDown, &rivermigrate.MigrateOpts{TargetVersion: -1})
		if r != nil {
			res = toResults(r)
		}
		return err
	})
	return res, err
}

// Status lists every River migration this binary knows and whether the database has it.
func (m *Migrator) Status(ctx context.Context) ([]Status, error) {
	existing, err := m.m.ExistingVersions(ctx)
	if err != nil {
		return nil, err
	}
	applied := make(map[int]bool, len(existing))
	for _, v := range existing {
		applied[v.Version] = true
	}
	var out []Status
	for _, v := range m.m.AllVersions() {
		out = append(out, Status{Version: v.Version, Name: v.Name, Applied: applied[v.Version]})
	}
	return out, nil
}

// Applied reports whether any River migration is applied: `cfctl migrate down` uses it to know that River's tables
// must go before the goose migrations are rolled back to 0.
func (m *Migrator) Applied(ctx context.Context) (bool, error) {
	existing, err := m.m.ExistingVersions(ctx)
	return len(existing) > 0, err
}

// locked runs fn holding the advisory lock on a connection of its own.
func (m *Migrator) locked(ctx context.Context, fn func(ctx context.Context) error) error {
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("river migrations: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, migrateLockName); err != nil {
		return fmt.Errorf("river migrations lock: %w", err)
	}
	defer func() {
		// Unlock explicitly: the connection goes back to the pool, not away.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, migrateLockName)
	}()
	return fn(ctx)
}

// grantSQL gives the app group role (<database>_app) DML on every river_* table and its sequences, the same rights
// cf_grant_app_dml gives the app's own tables, so the app role needs no DDL at runtime. river_migration is the one
// table the app only reads: nothing at runtime writes it.
const grantSQL = `
DO $$
DECLARE
    t regclass;
BEGIN
    FOR t IN
        SELECT c.oid::regclass
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND c.relname LIKE 'river\_%'
        ORDER BY c.relname
    LOOP
        PERFORM public.cf_grant_app_dml(t);
    END LOOP;
    IF to_regclass('public.river_migration') IS NOT NULL THEN
        EXECUTE format('REVOKE INSERT, UPDATE, DELETE ON TABLE public.river_migration FROM %I', public.cf_app_role());
    END IF;
END
$$`

func (m *Migrator) grantApp(ctx context.Context) error {
	if _, err := m.pool.Exec(ctx, grantSQL); err != nil {
		return fmt.Errorf("grant the app role its rights on River's tables: %w (run the goose migrations first)", err)
	}
	return nil
}

func toResults(r *rivermigrate.MigrateResult) []Result {
	out := make([]Result, 0, len(r.Versions))
	for _, v := range r.Versions {
		out = append(out, Result{Version: v.Version, Name: v.Name, Direction: string(r.Direction), Duration: v.Duration})
	}
	return out
}
