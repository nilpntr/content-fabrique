package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

type stuckArgs struct{}

func (stuckArgs) Kind() string { return "test_stuck" }

// stuckWorker ignores its context: it runs until the test releases it.
type stuckWorker struct {
	river.WorkerDefaults[stuckArgs]
	started chan struct{}
	release chan struct{}
}

func (w *stuckWorker) Work(context.Context, *river.Job[stuckArgs]) error {
	close(w.started)
	<-w.release
	return nil
}

// A job that ignores cancellation and a River UI client that never finishes its request cost one shared budget: the
// two stop at the same time and Worker.Stop is back inside StopTimeout, which the process budget (1 s drain + 6 s HTTP
// shutdown + 2 s flush = 9 s) has room for.
func TestWorkerStopStaysInsideTheBudget(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sw := &stuckWorker{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(sw.release) })

	w, err := jobs.StartWorker(ctx, jobs.WorkerOptions{
		DirectURL: e.dsn, Store: e.api, Logger: quiet, UIAddr: "127.0.0.1:0", UIPassword: "pw",
		Register: func(ws *river.Workers) error { return river.AddWorkerSafely(ws, sw) },
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "river to be ready", func() bool { return w.Ready(ctx) == nil })
	client, err := jobs.NewInsertClient(e.api.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, stuckArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sw.started:
	case <-time.After(15 * time.Second):
		t.Fatal("the stuck job never started")
	}

	// A slow client: half a request, never finished. http.Server.Shutdown waits for it until it is cut off.
	slow, err := net.Dial("tcp", w.UIAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slow.Close() }()
	if _, err := fmt.Fprint(slow, "GET / HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	err = w.Stop(ctx)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("Stop = %v, want an error that the job was still running", err)
	}
	// The job is waited for (SoftStopTimeout, then CancelGrace) and not much longer; one after the other with the UI's
	// 2 s it would be over the limit.
	if elapsed < jobs.SoftStopTimeout || elapsed > jobs.StopTimeout+500*time.Millisecond {
		t.Fatalf("Stop took %s, want between %s and %s", elapsed, jobs.SoftStopTimeout, jobs.StopTimeout)
	}
}

// Stop does not wait on a start attempt that is stuck on an unreachable Postgres.
func TestStopDoesNotWaitForAStuckStart(t *testing.T) {
	// A server that accepts connections and never answers: the Postgres handshake hangs.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var held atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held.Add(1)
			defer func() { _ = c.Close() }()
		}
	}()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, fmt.Sprintf("postgres://u:p@%s/db?sslmode=disable&connect_timeout=30", ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	st := store.New(pool)
	client, err := jobs.NewWorkerClient(pool, jobs.DefaultConfig(), jobs.Deps{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	r := jobs.NewRunner(client, pool, quiet)
	r.StartBackground(ctx)
	eventually(t, "the start attempt to reach the server", func() bool { return held.Load() > 0 })

	if r.Ready(ctx) == nil {
		t.Fatal("ready while not started")
	}
	start := time.Now()
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Stop took %s with a stuck start, want about the cancel itself", elapsed)
	}
}

// River's tables missing: a new workspace is refused with a clear error and rolled back, the cached readiness check
// fails (and does not cache the failure) until the tables are there.
func TestMissingRiverTablesFailClearly(t *testing.T) {
	srv := storetest.Start(t)
	ctx := context.Background()
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil { // goose only, as after `cfctl migrate up --to N`
		t.Fatal(err)
	}
	_ = m.Close()
	e := connect(t, dsn, dsn)
	e.api.SetWorkspaceCreatedHook(e.enq.WorkspaceCreated)

	_, _, err = e.api.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: "org_nq", Name: "NQ", ClerkUpdatedAt: 1})
	if !errors.Is(err, store.ErrQueueNotMigrated) {
		t.Fatalf("SyncWorkspace without River's tables = %v, want ErrQueueNotMigrated", err)
	}
	if _, err := e.api.GetWorkspaceByClerkOrgID(ctx, "org_nq"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the workspace survived the failed enqueue: %v", err)
	}

	check := jobs.MigratedCheck(e.api.Pool())
	if err := check(ctx); err == nil || !strings.Contains(err.Error(), "cfctl migrate") {
		t.Fatalf("readiness without River's tables = %v", err)
	}
	rm, err := jobs.NewMigrator(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	if _, err := rm.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := check(ctx); err != nil {
		t.Fatalf("readiness after the migration (a failure must not be cached): %v", err)
	}
	if _, _, err := e.api.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: "org_nq", Name: "NQ", ClerkUpdatedAt: 1}); err != nil {
		t.Fatalf("SyncWorkspace after: %v", err)
	}
}

func TestStartWorkerHonoursTheLoginCheck(t *testing.T) {
	e := newEnv(t)
	refuse := errors.New("login is privileged")
	_, err := jobs.StartWorker(context.Background(), jobs.WorkerOptions{
		DirectURL: e.dsn, Store: e.api, Logger: quiet,
		CheckLogin: func(_ context.Context, direct *store.Store) error {
			if direct == nil {
				t.Error("no direct store passed to CheckLogin")
			}
			return refuse
		},
	})
	if !errors.Is(err, refuse) || !strings.Contains(err.Error(), "DATABASE_DIRECT_URL") {
		t.Fatalf("StartWorker = %v, want the check's refusal naming DATABASE_DIRECT_URL", err)
	}
}
