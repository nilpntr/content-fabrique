package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// readyCacheFor is how long a passing migration check is trusted, so /readyz polling does not query every time.
const readyCacheFor = 30 * time.Second

// Runner owns the worker's River client: start, graceful stop and the readiness check.
type Runner struct {
	client *river.Client[pgx.Tx]
	pool   *pgxpool.Pool
	logger *slog.Logger

	started    atomic.Bool
	stopped    atomic.Bool
	migrOKUnix atomic.Int64 // when the River tables were last seen current

	startCancel context.CancelFunc // ends the start-retry loop
	startDone   chan struct{}      // closed when the start-retry loop has returned
}

// startRetryEvery is how often StartBackground tries again while Postgres is unreachable.
const startRetryEvery = 5 * time.Second

// NewRunner returns a Runner for the client built by NewWorkerClient over pool.
func NewRunner(client *river.Client[pgx.Tx], pool *pgxpool.Pool, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{client: client, pool: pool, logger: logger}
}

// Client returns the River client, for River UI.
func (r *Runner) Client() *river.Client[pgx.Tx] { return r.client }

// Start begins fetching and working jobs and running the periodic jobs. ctx bounds the start only: cancelling it
// later does not stop the client (River would treat that as a hard stop that cancels running jobs); Stop does.
func (r *Runner) Start(ctx context.Context) error {
	if err := r.client.Start(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("start river: %w", err)
	}
	r.started.Store(true)
	r.logger.Info("river started")
	return nil
}

// StartBackground starts the client without failing when it cannot yet: River checks the database on start, and a
// Postgres that is down at boot must not turn into a crash loop (the same rule as the api's pool). It tries once at
// once, then every few seconds until it works or Stop is called; Ready fails until then.
func (r *Runner) StartBackground(ctx context.Context) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.startCancel = cancel
	r.startDone = make(chan struct{})
	go func() {
		defer close(r.startDone)
		for {
			err := r.Start(ctx)
			if err == nil || ctx.Err() != nil {
				return
			}
			r.logger.Warn("river did not start; retrying", "error", err, "retry_in", startRetryEvery.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(startRetryEvery):
			}
		}
	}()
}

// Stop shuts down gracefully inside StopTimeout: the client stops fetching, running jobs get SoftStopTimeout to
// finish, then their contexts are cancelled and they get the rest to return (an interrupted job is retried by the
// next worker). It returns an error when jobs were still running at the end.
func (r *Runner) Stop(ctx context.Context) error {
	if r.startCancel != nil {
		r.startCancel()
		<-r.startDone
	}
	if !r.started.Load() || r.stopped.Swap(true) {
		return nil
	}
	r.logger.Info("river stopping: no new jobs, waiting for running jobs", "soft_stop_timeout", SoftStopTimeout.String())
	stopCtx, cancel := context.WithTimeout(ctx, StopTimeout)
	defer cancel()
	// River escalates to cancelling the jobs by itself once SoftStopTimeout has passed (Config.SoftStopTimeout).
	err := r.client.Stop(stopCtx)
	if err == nil {
		r.logger.Info("river stopped")
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("stop river: %w", err)
	}
	// Out of budget: cancel the jobs now and give them a last moment.
	hardCtx, hardCancel := context.WithTimeout(context.WithoutCancel(ctx), CancelGrace)
	defer hardCancel()
	if err := r.client.StopAndCancel(hardCtx); err != nil {
		return fmt.Errorf("river: jobs still running after %s: %w", StopTimeout+CancelGrace, err)
	}
	r.logger.Warn("river stopped after cancelling running jobs")
	return nil
}

// Ready is the "river" readiness check: the client is running and River's tables are migrated to at least the version
// this binary knows. (A newer schema is fine: the release step migrates before the new binaries start.)
func (r *Runner) Ready(ctx context.Context) error {
	if !r.started.Load() {
		return errors.New("river client not started")
	}
	if r.stopped.Load() {
		return errors.New("river client stopped")
	}
	select {
	case <-r.client.Stopped():
		return errors.New("river client stopped unexpectedly")
	default:
	}
	if last := r.migrOKUnix.Load(); last != 0 && time.Since(time.Unix(last, 0)) < readyCacheFor {
		return nil
	}
	if err := CheckMigrated(ctx, r.pool); err != nil {
		return err
	}
	r.migrOKUnix.Store(time.Now().Unix())
	return nil
}

// CheckMigrated returns an error unless River's tables exist and are migrated to at least the latest version this
// binary knows. It only reads, so the app role can run it.
func CheckMigrated(ctx context.Context, pool *pgxpool.Pool) error {
	want, err := latestVersion()
	if err != nil {
		return err
	}
	var have int
	err = pool.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM river_migration WHERE line = 'main'`).Scan(&have)
	if err != nil {
		return fmt.Errorf("river tables: %w (run cfctl migrate up)", err)
	}
	if have < want {
		return fmt.Errorf("river tables are at migration %d, this build needs %d (run cfctl migrate up)", have, want)
	}
	return nil
}

func latestVersion() (int, error) {
	m, err := rivermigrate.New(riverpgxv5.New(nil), nil)
	if err != nil {
		return 0, err
	}
	latest := 0
	for _, v := range m.AllVersions() {
		latest = max(latest, v.Version)
	}
	return latest, nil
}
