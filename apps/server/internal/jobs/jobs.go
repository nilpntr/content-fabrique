// Package jobs is the River job queue (decision D-04): the client setup, the job argument types, the worker
// registration and the periodic jobs.
//
// Two kinds of client share one Postgres:
//
//   - The api builds an insert-only client over its pooled pool (NewInsertClient). It never fetches or works jobs and
//     holds no listener, so it runs behind a transaction pooler. Its one job is InsertTx: a job is inserted in the same
//     transaction as the API's own writes, and exists exactly when they commit (Enqueuer).
//   - The worker builds the full client (NewWorkerClient) over a pool on DATABASE_DIRECT_URL, because River's
//     LISTEN/NOTIFY needs a session a transaction pooler does not keep. It fetches, works and runs the periodic jobs.
//
// River's tables belong to the migrate role: `cfctl migrate` creates them after the goose migrations (Migrator) and
// grants the app group role its rights on them. Nothing here issues DDL at runtime.
package jobs

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Queue names. A queue is a workload class with its own concurrency limit (D-04); later tasks add theirs here.
const (
	// QueueDefault holds short, cheap jobs such as the welcome job.
	QueueDefault = "default"
	// QueueMedia holds the asset ingest: minutes of CPU per job in a sandbox, so few at a time.
	QueueMedia = "media"
	// QueueMaintenance holds housekeeping, so a flood of other work never delays it and it never uses much.
	QueueMaintenance = "maintenance"
)

// Shutdown budget of the worker's job side. The process budget of package httpapi is about 9 s: 1 s drain delay plus
// 6 s HTTP shutdown, which run at the same time as this, then 2 s of telemetry flush. Worker.Stop therefore finishes
// inside StopTimeout (6 s), whatever the jobs and the River UI clients do:
//
//   - a start attempt still in progress gets StartWait to end;
//   - running jobs get SoftStopTimeout after the client stops fetching, then River cancels their contexts and they get
//     CancelGrace to return (a cancelled job is retried later by whichever worker is next);
//   - River UI shuts down at the same time as all of that, in at most UIShutdownTimeout;
//   - the pool is closed last and is given PoolCloseGrace (a job that ignores cancellation may still hold a connection).
const (
	StartWait         = 500 * time.Millisecond
	SoftStopTimeout   = 4 * time.Second
	CancelGrace       = 1 * time.Second
	PoolCloseGrace    = 500 * time.Millisecond
	UIShutdownTimeout = 2 * time.Second
	StopTimeout       = StartWait + SoftStopTimeout + CancelGrace + PoolCloseGrace
)

// Queue is the concurrency of one queue.
type Queue struct {
	Name       string
	MaxWorkers int
}

// Config names the queues the worker works and how many jobs of each run at once.
type Config struct {
	Queues []Queue
}

// DefaultMediaConcurrency is how many ingests run at once on one worker unless MEDIA_CONCURRENCY says otherwise.
const DefaultMediaConcurrency = 2

// DefaultConfig is the queue layout of the worker.
func DefaultConfig() Config {
	return Config{Queues: []Queue{
		{Name: QueueDefault, MaxWorkers: 10},
		{Name: QueueMaintenance, MaxWorkers: 2},
		{Name: QueueMedia, MaxWorkers: DefaultMediaConcurrency},
	}}
}

func (c Config) riverQueues() (map[string]river.QueueConfig, error) {
	if len(c.Queues) == 0 {
		return nil, errors.New("jobs: no queues configured")
	}
	out := make(map[string]river.QueueConfig, len(c.Queues))
	for _, q := range c.Queues {
		if q.Name == "" || q.MaxWorkers < 1 {
			return nil, fmt.Errorf("jobs: queue %q needs a name and at least one worker", q.Name)
		}
		if _, dup := out[q.Name]; dup {
			return nil, fmt.Errorf("jobs: queue %q is configured twice", q.Name)
		}
		out[q.Name] = river.QueueConfig{MaxWorkers: q.MaxWorkers}
	}
	return out, nil
}

// NewInsertClient returns the api's client: it can insert jobs (Insert, InsertTx) and cannot work them. pool is the
// api's pooled pool; the client opens no connection of its own and starts nothing.
func NewInsertClient(pool *pgxpool.Pool, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
}

// WorkerClientConfig is the River configuration of the worker's client: the queues of cfg, every worker of this package, the
// periodic jobs and the stop and rescue timings. It is separate from NewWorkerClient so a test can look at it.
func WorkerClientConfig(cfg Config, deps Deps) (*river.Config, error) {
	queues, err := cfg.riverQueues()
	if err != nil {
		return nil, err
	}
	workers, err := Workers(deps)
	if err != nil {
		return nil, err
	}
	return &river.Config{
		Logger:          deps.logger(),
		Queues:          queues,
		Workers:         workers,
		PeriodicJobs:    PeriodicJobs(),
		SoftStopTimeout: SoftStopTimeout,
		// Longer than the longest job (the ingest, IngestTimeout), so River never hands a job that is still running to a
		// second worker.
		RescueStuckJobsAfter: JobRescueAfter,
	}, nil
}

// NewWorkerClient returns the worker's client: the queues of cfg, every worker of this package and the periodic
// jobs. pool must be on the direct URL (the listener holds a connection of it). The caller starts it (Runner).
func NewWorkerClient(pool *pgxpool.Pool, cfg Config, deps Deps) (*river.Client[pgx.Tx], error) {
	rc, err := WorkerClientConfig(cfg, deps)
	if err != nil {
		return nil, err
	}
	return river.NewClient(riverpgxv5.New(pool), rc)
}
