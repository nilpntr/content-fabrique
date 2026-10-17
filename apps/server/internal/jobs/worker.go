package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// DirectPoolSize is the size of the worker's pool on DATABASE_DIRECT_URL, and it is small on purpose. The pool exists
// for what needs a session a transaction pooler does not keep: River's listener (one connection, held for as long as
// the worker runs) and its short bookkeeping queries (fetch, complete, leader election, maintenance). The jobs' own
// reads and writes go through the pooled Store, not through this pool, so it does not grow with the number of workers,
// and a managed Postgres has a low connection limit that direct connections count against.
const DirectPoolSize = 6

// WorkerOptions configures StartWorker.
type WorkerOptions struct {
	// Config is the queue layout. The zero value means DefaultConfig.
	Config Config
	// DirectURL is DATABASE_DIRECT_URL: the direct URL of the app role. River listens on it (LISTEN/NOTIFY).
	DirectURL string
	// Store is the app role's pooled store, for the jobs' own reads and writes.
	Store  *store.Store
	Logger *slog.Logger
	// Objects is object storage, for the jobs that remove objects. Nil when R2_* is not set.
	Objects ObjectStore
	// Ingest is the media pipeline, for the jobs that ingest assets. Nil when the sandbox or object storage is not set up.
	Ingest AssetIngester
	// QuickLook is the quick look, for the quick_look job. Nil when the sandbox, object storage or model access is not set up.
	QuickLook QuickLooker
	// Transcribe is the transcription pipeline, for the transcribe job. Nil when the sandbox or object storage is not set up.
	Transcribe Transcriber
	// Render is the render pipeline, for the jobs that render timelines. Nil when the sandbox or object storage is not set up.
	Render Renderer
	// UIAddr is the listen address of River UI, such as "127.0.0.1:8082". Empty turns River UI off.
	UIAddr string
	// UIPassword is RIVER_UI_PASSWORD. River UI refuses to start without it.
	UIPassword string
	// CheckLogin, when set, is called with a Store over the direct pool before anything starts; an error stops
	// StartWorker. cmd/worker uses it for the privileged-login check on staging and production.
	CheckLogin func(ctx context.Context, direct *store.Store) error
	// Register adds workers to the bundle (tests).
	Register func(*river.Workers) error
}

// Worker is the running job side of the worker process: the River client, its pool and, when asked for, River UI.
type Worker struct {
	runner *Runner
	pool   *pgxpool.Pool
	direct *store.Store
	logger *slog.Logger

	ui       *http.Server
	uiAddr   string
	uiCancel context.CancelFunc
	uiDone   chan struct{}
}

// StartWorker builds the River client over a pool on the direct URL, starts it (it keeps retrying in the background
// while Postgres is unreachable) and, when UIAddr is set, serves River UI behind Basic auth. It fails before starting
// anything when the options cannot work: no direct URL, River UI asked for without a password, or CheckLogin refusing.
func StartWorker(ctx context.Context, opts WorkerOptions) (*Worker, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cfg := opts.Config
	if len(cfg.Queues) == 0 {
		cfg = DefaultConfig()
	}
	if opts.DirectURL == "" {
		return nil, errors.New("DATABASE_DIRECT_URL is required: the worker's job queue listens on a direct (not pooled) connection")
	}
	if opts.UIAddr != "" && opts.UIPassword == "" {
		return nil, ErrNoUIPassword
	}

	pcfg, err := pgxpool.ParseConfig(opts.DirectURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_DIRECT_URL: %w", err) // pgx masks the password
	}
	pcfg.MaxConns = DirectPoolSize
	pcfg.ConnConfig.RuntimeParams["application_name"] = "worker-river"
	if pcfg.ConnConfig.ConnectTimeout == 0 {
		pcfg.ConnConfig.ConnectTimeout = 5 * time.Second
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg) // connects lazily
	if err != nil {
		return nil, fmt.Errorf("river pool: %w", err)
	}
	direct := store.New(pool)
	if opts.CheckLogin != nil {
		if err := opts.CheckLogin(ctx, direct); err != nil {
			pool.Close()
			return nil, fmt.Errorf("DATABASE_DIRECT_URL: %w", err)
		}
	}

	client, err := NewWorkerClient(pool, cfg, Deps{Store: opts.Store, Logger: logger, Objects: opts.Objects, Ingest: opts.Ingest, QuickLook: opts.QuickLook, Transcribe: opts.Transcribe, Render: opts.Render, Register: opts.Register})
	if err != nil {
		pool.Close()
		return nil, err
	}
	// The ingest marks a video ready through this Store; the same transaction enqueues its quick look (P1-04b).
	if opts.Store != nil {
		opts.Store.SetQuickLookHook(NewEnqueuer(client, logger).EnqueueQuickLook)
		opts.Store.SetTranscribeHook(NewEnqueuer(client, logger).EnqueueTranscribe)
	}
	w := &Worker{runner: NewRunner(client, pool, logger), pool: pool, direct: direct, logger: logger}

	if opts.UIAddr != "" {
		if err := w.startUI(ctx, opts.UIAddr, opts.UIPassword); err != nil {
			pool.Close()
			return nil, err
		}
	}
	w.runner.StartBackground(ctx)
	return w, nil
}

func (w *Worker) startUI(ctx context.Context, addr, password string) error {
	h, err := NewUIHandler(w.runner.Client(), password, w.logger)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("river ui: %w", err)
	}
	uiCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if err := h.Start(uiCtx); err != nil {
		cancel()
		_ = ln.Close()
		return fmt.Errorf("river ui: %w", err)
	}
	w.ui, w.uiAddr, w.uiCancel, w.uiDone = NewUIServer(addr, h), ln.Addr().String(), cancel, make(chan struct{})
	go func() {
		defer close(w.uiDone)
		if err := w.ui.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			w.logger.Error("river ui stopped", "error", err)
		}
	}()
	w.logger.Info("river ui listening (HTTP Basic auth)", "addr", w.uiAddr, "user", UIUser)
	return nil
}

// UIAddr is the address River UI listens on ("" when it is off). With ":0" it holds the port that was picked.
func (w *Worker) UIAddr() string { return w.uiAddr }

// Direct is a Store over the worker's direct pool, for its readiness check and login check.
func (w *Worker) Direct() *store.Store { return w.direct }

// Ready is the "river" readiness check.
func (w *Worker) Ready(ctx context.Context) error { return w.runner.Ready(ctx) }

// Stop shuts the job side down inside StopTimeout (see the budget in jobs.go): River UI and the River client stop at
// the same time, then the pool closes. A slow River UI client or a job that ignores cancellation costs at most its
// share of the budget; it is reported or cut off, not waited for.
func (w *Worker) Stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, StopTimeout)
	defer cancel()

	var (
		wg      sync.WaitGroup
		riverEr error
	)
	if w.ui != nil {
		wg.Go(func() {
			shut, cancel := context.WithTimeout(ctx, UIShutdownTimeout)
			defer cancel()
			if err := w.ui.Shutdown(shut); err != nil {
				_ = w.ui.Close() // a client that will not finish is cut off
			}
			w.uiCancel()
			<-w.uiDone
		})
	}
	wg.Go(func() { riverEr = w.runner.Stop(ctx) })
	wg.Wait()

	// Closing the pool waits for connections that are checked out; a job that ignored its cancellation may hold one.
	closed := make(chan struct{})
	go func() { w.pool.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(PoolCloseGrace):
		w.logger.Warn("river pool still has connections in use; leaving them to the process exit")
	}
	return riverEr
}
