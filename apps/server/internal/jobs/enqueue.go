package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// Enqueuer inserts jobs in the caller's transaction. API code uses it inside Store.InTx, so the job is committed
// with the writes it belongs to, or not at all: that is what makes "enqueue and write" atomic without an outbox.
type Enqueuer struct {
	client *river.Client[pgx.Tx]
}

// NewEnqueuer wraps a River client. The insert-only client of the api (NewInsertClient) is enough; the worker's
// client works too, for jobs that enqueue jobs.
func NewEnqueuer(client *river.Client[pgx.Tx]) *Enqueuer { return &Enqueuer{client: client} }

// InsertTx inserts the job in tx. Nothing is visible to the workers until tx commits, and a rollback removes it.
func (e *Enqueuer) InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return e.client.InsertTx(ctx, tx, args, opts)
}

// InsertInStore inserts the job in the transaction behind tx, a Store handed out by Store.InTx. It returns
// store.ErrNoTx for the pool Store: a job enqueued outside a transaction would not be atomic with anything.
func (e *Enqueuer) InsertInStore(ctx context.Context, tx *store.Store, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	pgxTx, err := tx.Tx()
	if err != nil {
		return nil, err
	}
	return e.InsertTx(ctx, pgxTx, args, opts)
}

// WorkspaceCreated is the store's created-workspace hook (Store.SetWorkspaceCreatedHook): it enqueues the welcome job
// in the transaction that inserts the workspace. The job is unique by workspace, so it exists once.
func (e *Enqueuer) WorkspaceCreated(ctx context.Context, tx *store.Store, w store.Workspace) error {
	if _, err := e.InsertInStore(ctx, tx, WorkspaceWelcomeArgs{WorkspaceID: w.ID}, nil); err != nil {
		if errors.Is(err, store.ErrNoTx) {
			return err
		}
		return fmt.Errorf("enqueue workspace_welcome: %w", err)
	}
	return nil
}
