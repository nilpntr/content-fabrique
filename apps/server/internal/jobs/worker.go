package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// WorkerOptions configures StartWorker.
type WorkerOptions struct {
	// Config is the queue layout. The zero value means DefaultConfig.
	Config Config
	// DirectURL is DATABASE_DIRECT_URL: the direct URL of the app role. River listens on it (LISTEN/NOTIFY).
	DirectURL string
	// Store is the app role's pooled store, for the jobs' own reads and writes.
	Store  *store.Store
	Logger *slog.Logger
	// UIAddr is the listen address of River UI, such as "127.0.0.1:8082". Empty turns River UI off.
	UIAddr string
	// UIPassword is RIVER_UI_PASSWORD. River UI refuses to start without it.
	UIPassword string
}

// Worker is the running job side of the worker process: the River client, its pool and, when asked for, River UI.
type Worker struct {
	runner *Runner
	pool   *pgxpool.Pool
	logger *slog.Logger

	ui       *http.Server
	uiAddr   string
	uiCancel context.CancelFunc
	uiDone   chan struct{}
}

// StartWorker builds the River client over a pool on the direct URL, starts it (it keeps retrying in the background
// while Postgres is unreachable) and, when UIAddr is set, serves River UI behind Basic auth. It fails before starting
// anything when the options cannot work: no direct URL, or River UI asked for without a password.
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
	// One connection per running job at most, plus the listener, the leader election and the maintenance services.
	workers := 0
	for _, q := range cfg.Queues {
		workers += q.MaxWorkers
	}
	pcfg.MaxConns = int32(workers + 6)
	pcfg.ConnConfig.RuntimeParams["application_name"] = "worker-river"
	if pcfg.ConnConfig.ConnectTimeout == 0 {
		pcfg.ConnConfig.ConnectTimeout = 5 * time.Second
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg) // connects lazily
	if err != nil {
		return nil, fmt.Errorf("river pool: %w", err)
	}

	client, err := NewWorkerClient(pool, cfg, Deps{Store: opts.Store, Logger: logger})
	if err != nil {
		pool.Close()
		return nil, err
	}
	w := &Worker{runner: NewRunner(client, pool, logger), pool: pool, logger: logger}

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

// Ready is the "river" readiness check.
func (w *Worker) Ready(ctx context.Context) error { return w.runner.Ready(ctx) }

// Stop shuts the job side down: River UI first, then the client with its drain budget (StopTimeout), then the pool.
func (w *Worker) Stop(ctx context.Context) error {
	var errs []error
	if w.ui != nil {
		shut, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		if err := w.ui.Shutdown(shut); err != nil {
			_ = w.ui.Close()
		}
		cancel()
		w.uiCancel()
		<-w.uiDone
	}
	if err := w.runner.Stop(ctx); err != nil {
		errs = append(errs, err)
	}
	w.pool.Close()
	return errors.Join(errs...)
}
