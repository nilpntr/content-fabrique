// Package store is the Postgres layer: the pgx pool, the goose migrations, and the repository functions.
//
// The SQL lives in queries/*.sql and is compiled by sqlc into package db (committed; see sqlc.yaml). Tenant-owned
// data is reached only through the Store methods in repository.go, which take a WorkspaceID (architecture section 4).
package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Options tunes the pool. The zero value is fine.
type Options struct {
	// AppName shows up in pg_stat_activity, for example "api" or "worker".
	AppName string
	// MaxConns caps the pool. Zero keeps pgx's default (the larger of 4 and the CPU count).
	MaxConns int32
}

// Store is the pool plus the compiled queries. Methods on a Store returned by InTx run inside that transaction.
type Store struct {
	pool *pgxpool.Pool // nil inside a transaction
	q    *db.Queries
}

// Open creates the pool for DATABASE_URL, the pooled (request traffic) URL of the app role. It does not connect:
// the pool dials on first use, so a process can start, report not-ready on /readyz and recover while Postgres is
// down. Call Ping to check.
func Open(ctx context.Context, dsn string, opts Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// pgx masks the password in this error.
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}

	// The query exec mode is the one setting that has to survive a transaction-mode pooler. PlanetScale's pooled
	// endpoint is PgBouncer in transaction mode: consecutive statements of a client can run on different server
	// connections, so anything that lives on the server connection breaks. pgx's default (QueryExecModeCacheStatement)
	// prepares NAMED statements, and the next query then fails with "prepared statement ... does not exist". Cache
	// describe uses only the UNNAMED statement: the first use of a query sends one Parse/Describe to learn the
	// parameter and column types (cached per connection), and every execution after that sends Parse, Bind and
	// Execute together in a single message, so it never relies on state left behind on the server. Unlike the simple
	// protocol it still sends typed parameters and binary results, so uuid, jsonb and text[] keep working without
	// client-side quoting.
	//
	// Also unavailable behind the pooled URL: LISTEN/NOTIFY, session-level advisory locks, SET without LOCAL, and
	// temporary tables. Code that needs them uses DATABASE_DIRECT_URL (the SSE broker, from P0-06).
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe

	if opts.AppName != "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = opts.AppName
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	// Our defaults apply only where the URL (or the PG* environment, for the timeout) said nothing, so an operator can
	// tune them without a code change: connect_timeout, pool_max_conn_lifetime and pool_health_check_period.
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	}
	if !dsnSets(dsn, "pool_max_conn_lifetime") {
		cfg.MaxConnLifetime = 30 * time.Minute // lets a pooler or a failover replace connections without a restart
	}
	if !dsnSets(dsn, "pool_health_check_period") {
		cfg.HealthCheckPeriod = 30 * time.Second
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}
	return &Store{pool: pool, q: db.New(pool)}, nil
}

// dsnSets reports whether the connection string names the parameter, in URL form (?name=value) or keyword form
// (name=value name=value).
func dsnSets(dsn, name string) bool {
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		return u.Query().Has(name)
	}
	for _, field := range strings.Fields(dsn) {
		if key, _, ok := strings.Cut(field, "="); ok && key == name {
			return true
		}
	}
	return false
}

// New wraps an existing pool, for tests and for callers that configure the pool themselves.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool, q: db.New(pool)} }

// Pool returns the underlying pool, for code that needs it directly (River, from P0-06).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping checks that Postgres answers. It backs the "postgres" readiness check.
func (s *Store) Ping(ctx context.Context) error {
	if s.pool == nil {
		return errors.New("store: no pool inside a transaction")
	}
	return s.pool.Ping(ctx)
}

// Close closes the pool, waiting for checked-out connections to come back.
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// InTx runs fn in one transaction and commits when it returns nil. The Store passed to fn is bound to the
// transaction. A returned error, or a panic, rolls back.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) (err error) {
	if s.pool == nil {
		return errors.New("store: InTx inside a transaction")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(&Store{q: s.q.WithTx(tx)}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
