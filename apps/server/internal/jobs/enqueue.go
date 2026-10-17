package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// Enqueuer inserts jobs in the caller's transaction. API code uses it inside Store.InTx, so the job is committed
// with the writes it belongs to, or not at all: that is what makes "enqueue and write" atomic without an outbox.
type Enqueuer struct {
	client *river.Client[pgx.Tx]
	logger *slog.Logger
}

// NewEnqueuer wraps a River client. The insert-only client of the api (NewInsertClient) is enough; the worker's
// client works too, for jobs that enqueue jobs. A nil logger means slog.Default.
func NewEnqueuer(client *river.Client[pgx.Tx], logger *slog.Logger) *Enqueuer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Enqueuer{client: client, logger: logger}
}

// InsertInStore inserts the job in the transaction behind tx, a Store handed out by Store.InTx. Nothing is visible to
// the workers until that transaction commits, and a rollback removes the job.
//
// It returns store.ErrNoTx for the pool Store (a job enqueued outside a transaction would not be atomic with
// anything), and store.ErrQueueNotMigrated, after a loud log line, when River's tables are missing.
func (e *Enqueuer) InsertInStore(ctx context.Context, tx *store.Store, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	pgxTx, err := tx.Tx()
	if err != nil {
		return nil, err
	}
	res, err := e.client.InsertTx(ctx, pgxTx, args, opts)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrUndefinedTable {
		e.logger.ErrorContext(ctx, "cannot enqueue a job: River's tables are missing. The release step must run `cfctl migrate up` (an `up --to N` skips River). The write is rolled back",
			"kind", args.Kind(), "sqlstate", pgErr.Code)
		return nil, fmt.Errorf("%w: %w", store.ErrQueueNotMigrated, err)
	}
	return res, err
}

// pgerrUndefinedTable is SQLSTATE 42P01.
const pgerrUndefinedTable = "42P01"

// WorkspaceCreated is the store's created-workspace hook (Store.SetWorkspaceCreatedHook): it enqueues the welcome job
// in the transaction that inserts the workspace. The job is unique by workspace, so it exists once.
func (e *Enqueuer) WorkspaceCreated(ctx context.Context, tx *store.Store, w store.Workspace) error {
	_, err := e.InsertInStore(ctx, tx, WorkspaceWelcomeArgs{WorkspaceID: w.ID}, nil)
	if err != nil {
		return fmt.Errorf("enqueue workspace_welcome: %w", err)
	}
	return nil
}

// JobInfo names the River job attempt a worker is running. The workers that meter work put it in the context (WithJobInfo), so the
// ledger rows of an attempt get stable idempotency keys that a retry of the same write reproduces and another attempt does not.
type JobInfo struct {
	ID      int64
	Attempt int
}

type jobInfoKey struct{}

// WithJobInfo returns a context that carries the job attempt.
func WithJobInfo(ctx context.Context, info JobInfo) context.Context {
	return context.WithValue(ctx, jobInfoKey{}, info)
}

// JobInfoFrom reads the job attempt from a context made by WithJobInfo.
func JobInfoFrom(ctx context.Context) (JobInfo, bool) {
	info, ok := ctx.Value(jobInfoKey{}).(JobInfo)
	return info, ok
}
