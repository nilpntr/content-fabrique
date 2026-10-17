package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

// startRetryEvery is how often StartBackground tries again while Postgres is unreachable.
const startRetryEvery = 5 * time.Second

// Runner owns the worker's River client: start, graceful stop and the readiness check.
type Runner struct {
	client *river.Client[pgx.Tx]
	pool   *pgxpool.Pool
	logger *slog.Logger
	// migrated is the cached "River's tables are current" check.
	migrated func(context.Context) error

	started atomic.Bool
	stopped atomic.Bool

	startCancel context.CancelFunc // ends the start-retry loop
	startDone   chan struct{}      // closed when the start-retry loop has returned
}

// NewRunner returns a Runner for the client built by NewWorkerClient over pool.
func NewRunner(client *river.Client[pgx.Tx], pool *pgxpool.Pool, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{client: client, pool: pool, logger: logger, migrated: MigratedCheck(pool)}
}

// Client returns the River client, for River UI.
func (r *Runner) Client() *river.Client[pgx.Tx] { return r.client }

// start begins fetching and working jobs and running the periodic jobs. The probe of the database is done here with
// ctx, so cancelling ctx (Stop) ends a start that is waiting on an unreachable Postgres at once. The client itself is
// then started with a context that is never cancelled: River treats cancelling its start context as a hard stop that
// cancels running jobs, and only Stop may decide that.
func (r *Runner) start(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
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
			err := r.start(ctx)
			if err == nil {
				if ctx.Err() != nil {
					// Stop gave up waiting for this attempt and has returned: do not leave a client running behind it.
					hard, cancel := context.WithTimeout(context.WithoutCancel(ctx), CancelGrace)
					defer cancel()
					_ = r.client.StopAndCancel(hard)
				}
				return
			}
			if ctx.Err() != nil {
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

// Stop shuts down gracefully inside StopTimeout, counted from the call: it ends a pending start (waiting at most
// StartWait for it), then the client stops fetching, running jobs get SoftStopTimeout to finish, then River cancels
// their contexts and they get CancelGrace to return (an interrupted job is retried by the next worker). It returns an
// error when jobs are still running at the end; a job that ignores its context cannot hold the process longer.
func (r *Runner) Stop(ctx context.Context) error {
	if r.stopped.Swap(true) {
		return nil
	}
	if r.startCancel != nil {
		r.startCancel()
		select {
		case <-r.startDone:
		case <-time.After(StartWait):
			r.logger.Warn("river: a start attempt is still in progress; leaving it to stop itself")
		case <-ctx.Done():
		}
	}
	if !r.started.Load() {
		return nil
	}
	r.logger.Info("river stopping: no new jobs, waiting for running jobs", "soft_stop_timeout", SoftStopTimeout.String())
	stopCtx, cancel := context.WithTimeout(ctx, SoftStopTimeout+CancelGrace)
	defer cancel()
	// River cancels the jobs by itself once SoftStopTimeout has passed (Config.SoftStopTimeout).
	err := r.client.Stop(stopCtx)
	if err == nil {
		r.logger.Info("river stopped")
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("stop river: %w", err)
	}
	// Out of budget: make sure the jobs are cancelled and leave them.
	hardCtx, hardCancel := context.WithTimeout(context.WithoutCancel(ctx), 100*time.Millisecond)
	defer hardCancel()
	_ = r.client.StopAndCancel(hardCtx)
	return fmt.Errorf("river: jobs were still running %s after the stop began and were cancelled", SoftStopTimeout+CancelGrace)
}

// Ready is the "river" readiness check: the client runs and River's tables are migrated to at least the version this
// binary knows. (A newer schema is fine: the release step migrates before the new binaries start.)
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
	return r.migrated(ctx)
}

// MigratedCheck returns a readiness check that passes when River's tables are migrated to at least this build's
// version (CheckMigrated). A pass is trusted for readyCacheFor, so probes do not query every time; a failure is not
// cached. The api registers it too: creating a workspace enqueues a job, so an api without River's tables must not
// receive traffic.
func MigratedCheck(pool *pgxpool.Pool) func(context.Context) error {
	var okAt atomic.Int64 // unix time of the last pass
	return func(ctx context.Context) error {
		if last := okAt.Load(); last != 0 && time.Since(time.Unix(last, 0)) < readyCacheFor {
			return nil
		}
		if err := CheckMigrated(ctx, pool); err != nil {
			return err
		}
		okAt.Store(time.Now().Unix())
		return nil
	}
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

// latestVersion is the newest River migration this build embeds. It cannot change while the process runs.
var latestVersion = sync.OnceValues(func() (int, error) {
	m, err := rivermigrate.New(riverpgxv5.New(nil), nil)
	if err != nil {
		return 0, err
	}
	latest := 0
	for _, v := range m.AllVersions() {
		latest = max(latest, v.Version)
	}
	return latest, nil
})
