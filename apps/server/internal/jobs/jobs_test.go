package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) {
	code := storetest.Run(m)
	storagetest.Stop() // the MinIO of the assets job tests, if one was started
	os.Exit(code)
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// env is a migrated database with the api side (pooled store, insert-only client, enqueuer) and a super pool to look
// at river_job with. The worker is started by the test that wants one.
type env struct {
	dsn   string // the URL everything connects with
	super *pgxpool.Pool
	api   *store.Store
	enq   *jobs.Enqueuer
}

// newEnv migrates a fresh database as the superuser and connects the api side as the same login.
func newEnv(t *testing.T) *env {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	return connect(t, dsn, dsn)
}

// connect builds the api side over appDSN; super looks at the tables over superDSN.
func connect(t *testing.T, appDSN, superDSN string) *env {
	t.Helper()
	ctx := context.Background()
	superPool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(superPool.Close)
	st, err := store.Open(ctx, appDSN, store.Options{AppName: "api-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	client, err := jobs.NewInsertClient(st.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	return &env{dsn: appDSN, super: superPool, api: st, enq: jobs.NewEnqueuer(client, quiet)}
}

// startWorker runs the worker side against the database, the way cmd/worker does, and stops it with the test.
func (e *env) startWorker(t *testing.T, opts jobs.WorkerOptions) *jobs.Worker {
	t.Helper()
	opts.DirectURL, opts.Store, opts.Logger = e.dsn, e.api, quiet
	w, err := jobs.StartWorker(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Stop(context.Background()); err != nil {
			t.Errorf("worker stop: %v", err)
		}
	})
	return w
}

func (e *env) newWorkspace(t *testing.T, org string) store.Workspace {
	t.Helper()
	w, err := e.api.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: org, Name: org, Region: "eu", BillingMode: store.BillingManaged})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

type jobRow struct {
	State      string
	WelcomedAt string
}

// welcomeJobs returns the welcome jobs River holds for the workspace, whatever their state.
func (e *env) welcomeJobs(t *testing.T, ws ids.WorkspaceID) []jobRow {
	t.Helper()
	rows, err := e.super.Query(context.Background(),
		`SELECT state::text, COALESCE(metadata->'output'->>'welcomed_at', '') FROM river_job WHERE kind = 'workspace_welcome' AND args->>'workspace_id' = $1`, ws.String())
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[jobRow])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ── the acceptance criterion ────────────────────────────────────────────────────────────────────────────────────

// A job enqueued in an API transaction is processed by the worker.
func TestJobEnqueuedInAPITransactionIsProcessedByWorker(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.newWorkspace(t, "org_commit")
	e.startWorker(t, jobs.WorkerOptions{})

	err := e.api.InTx(ctx, func(tx *store.Store) error {
		// Another write in the same transaction, to show they travel together.
		if _, err := tx.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_same_tx", Name: "same tx", Region: "eu", BillingMode: store.BillingManaged}); err != nil {
			return err
		}
		if _, err := e.enq.InsertInStore(ctx, tx, jobs.WorkspaceWelcomeArgs{WorkspaceID: ws.ID}, nil); err != nil {
			return err
		}
		// Not visible to anybody else before the commit.
		if got := e.welcomeJobs(t, ws.ID); len(got) != 0 {
			t.Errorf("job visible before commit: %v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	eventually(t, "the worker to complete the welcome job", func() bool {
		got := e.welcomeJobs(t, ws.ID)
		return len(got) == 1 && got[0].State == "completed"
	})
	got := e.welcomeJobs(t, ws.ID)
	if _, err := time.Parse(time.RFC3339Nano, got[0].WelcomedAt); err != nil {
		t.Fatalf("welcomed_at = %q, want the time the worker recorded: %v", got[0].WelcomedAt, err)
	}
}

// A rolled-back transaction takes its job with it: the worker never sees it.
func TestJobEnqueuedInRolledBackTransactionIsNeverProcessed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.newWorkspace(t, "org_rollback")
	var inserts atomic.Int32
	e.startWorker(t, jobs.WorkerOptions{})

	boom := errors.New("boom")
	err := e.api.InTx(ctx, func(tx *store.Store) error {
		if _, err := e.enq.InsertInStore(ctx, tx, jobs.WorkspaceWelcomeArgs{WorkspaceID: ws.ID}, nil); err != nil {
			return err
		}
		inserts.Add(1)
		return boom
	})
	if !errors.Is(err, boom) || inserts.Load() != 1 {
		t.Fatalf("InTx = %v after %d inserts", err, inserts.Load())
	}
	// Give a worker every chance to pick it up (it polls every second and listens for inserts).
	time.Sleep(2500 * time.Millisecond)
	if got := e.welcomeJobs(t, ws.ID); len(got) != 0 {
		t.Fatalf("a rolled-back job exists: %v", got)
	}
}

func TestInsertInStoreNeedsATransaction(t *testing.T) {
	e := newEnv(t)
	_, err := e.enq.InsertInStore(context.Background(), e.api, jobs.WorkspaceWelcomeArgs{}, nil)
	if !errors.Is(err, store.ErrNoTx) {
		t.Fatalf("InsertInStore on the pool store = %v, want ErrNoTx", err)
	}
}

func TestWelcomeJobIsUniquePerWorkspace(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.newWorkspace(t, "org_unique")
	for range 3 {
		err := e.api.InTx(ctx, func(tx *store.Store) error {
			_, err := e.enq.InsertInStore(ctx, tx, jobs.WorkspaceWelcomeArgs{WorkspaceID: ws.ID}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := e.welcomeJobs(t, ws.ID); len(got) != 1 {
		t.Fatalf("welcome jobs = %v, want exactly one", got)
	}
}

// ── the workspace-created hook (what the just-in-time sync and the webhook run) ─────────────────────────────────

func TestSyncWorkspaceEnqueuesOneWelcomeJobPerNewWorkspace(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.api.SetWorkspaceCreatedHook(e.enq.WorkspaceCreated)

	a, applied, err := e.api.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: "org_a", Name: "A", ClerkUpdatedAt: 10})
	if err != nil || !applied {
		t.Fatalf("create: applied = %v, %v", applied, err)
	}
	// A rename, a replay and a stale version create nothing.
	for _, in := range []store.SyncWorkspaceInput{
		{ClerkOrgID: "org_a", Name: "A renamed", ClerkUpdatedAt: 20},
		{ClerkOrgID: "org_a", Name: "A renamed", ClerkUpdatedAt: 20},
		{ClerkOrgID: "org_a", Name: "A old", ClerkUpdatedAt: 5},
	} {
		if _, _, err := e.api.SyncWorkspace(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	b, _, err := e.api.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: "org_b", Name: "B", ClerkUpdatedAt: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.welcomeJobs(t, a.ID); len(got) != 1 || got[0].State != "available" {
		t.Errorf("jobs of A = %v, want exactly one, not worked yet", got)
	}
	if got := e.welcomeJobs(t, b.ID); len(got) != 1 {
		t.Errorf("jobs of B = %v, want exactly one", got)
	}
	var n int
	if err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'workspace_welcome'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("welcome jobs in total = %d, %v; want 2", n, err)
	}

	// A workspace Clerk deleted before it was ever synced is a tombstone, not a new workspace.
	if _, err := e.api.DeleteWorkspace(ctx, "org_gone"); err != nil {
		t.Fatal(err)
	}
	if err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("jobs after a tombstone = %d, %v; want still 2", n, err)
	}
}

// The workspace and its job are one transaction: if the job cannot be enqueued, there is no workspace.
func TestFailedEnqueueRollsTheWorkspaceBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	boom := errors.New("queue unavailable")
	e.api.SetWorkspaceCreatedHook(func(context.Context, *store.Store, store.Workspace) error { return boom })

	if _, _, err := e.api.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: "org_x", Name: "X", ClerkUpdatedAt: 1}); !errors.Is(err, boom) {
		t.Fatalf("SyncWorkspace = %v, want the hook's error", err)
	}
	if _, err := e.api.GetWorkspaceByClerkOrgID(ctx, "org_x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("workspace after a failed hook = %v, want it rolled back", err)
	}
	// And with the hook working again the next sync creates it and its job.
	e.api.SetWorkspaceCreatedHook(e.enq.WorkspaceCreated)
	w, _, err := e.api.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: "org_x", Name: "X", ClerkUpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.welcomeJobs(t, w.ID); len(got) != 1 {
		t.Fatalf("jobs = %v", got)
	}
}

func TestNewWorkspaceIsWelcomedByTheWorker(t *testing.T) {
	e := newEnv(t)
	e.api.SetWorkspaceCreatedHook(e.enq.WorkspaceCreated)
	e.startWorker(t, jobs.WorkerOptions{})
	w, _, err := e.api.SyncWorkspace(context.Background(), store.SyncWorkspaceInput{ClerkOrgID: "org_welcome", Name: "Welcome", ClerkUpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new workspace to be welcomed", func() bool {
		got := e.welcomeJobs(t, w.ID)
		return len(got) == 1 && got[0].State == "completed" && got[0].WelcomedAt != ""
	})
}

// ── periodic jobs and the prune ─────────────────────────────────────────────────────────────────────────────────

func TestPeriodicJobsAreRegistered(t *testing.T) {
	if got, want := jobs.PeriodicJobIDs(), []string{"webhook_events_prune", "uploads_expire", "agent_runs_resume", "renders_sweep"}; !slices.Equal(got, want) {
		t.Fatalf("PeriodicJobIDs = %v, want %v", got, want)
	}
	if n := len(jobs.PeriodicJobs()); n != len(jobs.PeriodicJobIDs()) {
		t.Fatalf("%d periodic jobs for %d ids", n, len(jobs.PeriodicJobIDs()))
	}
	// The worker client accepts them (River refuses duplicate ids and invalid schedules at construction).
	e := newEnv(t)
	if _, err := jobs.NewWorkerClient(e.api.Pool(), jobs.DefaultConfig(), jobs.Deps{Store: e.api}); err != nil {
		t.Fatal(err)
	}
}

func TestPruneWebhookEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.startWorker(t, jobs.WorkerOptions{})

	day := 24 * time.Hour
	insert := func(id, typ, status string, age time.Duration) {
		t.Helper()
		_, err := e.super.Exec(ctx, `INSERT INTO webhook_events (svix_id, event_type, subject, status, received_at) VALUES ($1, $2, 'org/user', $3, now() - $4::interval)`,
			id, typ, status, fmt.Sprintf("%d seconds", int(age.Seconds())))
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("recent", "user.created", "applied", day)
	insert("old-applied", "user.created", "applied", 31*day)
	insert("old-failed", "user.created", "failed", 31*day)
	insert("tombstone-60d", "organizationMembership.deleted", "applied", 60*day)
	insert("tombstone-100d", "organizationMembership.deleted", "applied", 100*day)
	insert("failed-membership-delete-31d", "organizationMembership.deleted", "failed", 31*day)

	// The periodic job fires daily at 03:15 UTC; the test enqueues the same job now.
	client, err := jobs.NewInsertClient(e.api.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, jobs.WebhookEventsPruneArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the prune job to complete", func() bool {
		var n int
		err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'webhook_events_prune' AND state = 'completed'`).Scan(&n)
		return err == nil && n == 1
	})

	rows, err := e.super.Query(ctx, `SELECT svix_id FROM webhook_events ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	left, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"recent", "tombstone-60d"}; !slices.Equal(left, want) {
		t.Fatalf("webhook_events left = %v, want %v (a membership delete outlives the rest, up to 90 days)", left, want)
	}
	var output string
	if err := e.super.QueryRow(ctx, `SELECT metadata->'output'->>'deleted' FROM river_job WHERE kind = 'webhook_events_prune'`).Scan(&output); err != nil || output != "4" {
		t.Fatalf("recorded output deleted = %q, %v; want 4", output, err)
	}
}

// ── shutdown ────────────────────────────────────────────────────────────────────────────────────────────────────

type sleepArgs struct{}

func (sleepArgs) Kind() string { return "test_sleep" }

type sleepWorker struct {
	river.WorkerDefaults[sleepArgs]
	started chan struct{}
	done    atomic.Bool
}

func (w *sleepWorker) Work(ctx context.Context, _ *river.Job[sleepArgs]) error {
	close(w.started)
	select {
	case <-time.After(1500 * time.Millisecond):
		w.done.Store(true)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop waits for a running job to finish instead of cancelling it, inside the drain budget.
func TestStopWaitsForRunningJobs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sw := &sleepWorker{started: make(chan struct{})}
	workers := river.NewWorkers()
	river.AddWorker(workers, sw)
	client, err := river.NewClient(riverpgxv5.New(e.api.Pool()), &river.Config{
		Logger: quiet, Queues: map[string]river.QueueConfig{jobs.QueueDefault: {MaxWorkers: 2}}, Workers: workers, SoftStopTimeout: jobs.SoftStopTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := jobs.NewRunner(client, e.api.Pool(), quiet)
	r.StartBackground(ctx)
	eventually(t, "river to be ready", func() bool { return r.Ready(ctx) == nil })

	if _, err := client.Insert(ctx, sleepArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sw.started:
	case <-time.After(15 * time.Second):
		t.Fatal("the job never started")
	}
	start := time.Now()
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !sw.done.Load() {
		t.Fatalf("Stop returned after %s with the job unfinished: it was cancelled instead of drained", time.Since(start))
	}
	if r.Ready(ctx) == nil {
		t.Fatal("readiness stays ok after Stop")
	}
	var state string
	if err := e.super.QueryRow(ctx, `SELECT state::text FROM river_job WHERE kind = 'test_sleep'`).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("job state = %q, %v; want completed", state, err)
	}
}

// ── readiness and migrations ────────────────────────────────────────────────────────────────────────────────────

func TestReadinessNeedsMigratedRiverTables(t *testing.T) {
	srv := storetest.Start(t)
	ctx := context.Background()
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	// goose only: no River tables yet.
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := jobs.CheckMigrated(ctx, pool); err == nil || !strings.Contains(err.Error(), "cfctl migrate") {
		t.Fatalf("CheckMigrated before River's migrations = %v, want a hint to run cfctl migrate", err)
	}
	rm, err := jobs.NewMigrator(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	if _, err := rm.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := jobs.CheckMigrated(ctx, pool); err != nil {
		t.Fatalf("CheckMigrated after: %v", err)
	}
}

func TestRiverMigrationsRoundTrip(t *testing.T) {
	srv := storetest.Start(t)
	ctx := context.Background()
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)

	rm, err := jobs.NewMigrator(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	status, err := rm.Status(ctx)
	if err != nil || len(status) == 0 {
		t.Fatalf("status = %v, %v", status, err)
	}
	for _, s := range status {
		if !s.Applied {
			t.Errorf("migration %d %s pending after up", s.Version, s.Name)
		}
	}
	if res, err := rm.Up(ctx); err != nil || len(res) != 0 {
		t.Fatalf("second up = %v, %v; want nothing to do", res, err)
	}
	if res, err := rm.Down(ctx); err != nil || len(res) != len(status) {
		t.Fatalf("down = %d results, %v; want %d", len(res), err, len(status))
	}
	if st, err := rm.Status(ctx); err != nil || slices.ContainsFunc(st, func(s jobs.Status) bool { return s.Applied }) {
		t.Fatalf("status after down = %v, %v", st, err)
	}
	var n int
	if err := queryRow(t, dsn, `SELECT count(*) FROM pg_tables WHERE tablename LIKE 'river\_%'`, &n); err != nil || n != 0 {
		t.Fatalf("river tables after down = %d, %v", n, err)
	}
	if _, err := rm.Up(ctx); err != nil {
		t.Fatalf("up after down: %v", err)
	}
}

func queryRow(t *testing.T, dsn, query string, dest ...any) error {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	return conn.QueryRow(ctx, query).Scan(dest...)
}

// ── the role split ──────────────────────────────────────────────────────────────────────────────────────────────

const (
	migrateRole = "jobs_test_migrate"
	appLogin    = "jobs_test_app"
	rolePass    = "role-pass"
	permDenied  = "42501"
)

// The app login (a member of <database>_app) enqueues and works jobs with DML only; River's tables came from the
// migrate role.
func TestAppRoleRunsJobsWithoutDDL(t *testing.T) {
	srv := storetest.Start(t)
	ctx := context.Background()
	drop := func() {
		srv.Exec(t, "postgres", `DO $$ DECLARE r text; BEGIN
FOR r IN SELECT rolname FROM pg_roles WHERE rolname ~ '^t_[0-9a-f]{12}_app$' LOOP EXECUTE format('DROP ROLE %I', r); END LOOP; END $$`)
		srv.DropRoles(t, migrateRole, appLogin)
	}
	drop()
	t.Cleanup(drop)
	srv.Exec(t, "postgres",
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, migrateRole, rolePass),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, appLogin, rolePass),
	)
	database := srv.NewDatabase(t, migrateRole)
	srv.Exec(t, "postgres", `CREATE ROLE `+database+`_app NOLOGIN`, `GRANT `+database+`_app TO `+appLogin)

	migrateDSN, appDSN := srv.DSN(database, migrateRole, rolePass), srv.DSN(database, appLogin, rolePass)
	jobstest.Migrate(t, migrateDSN)

	e := connect(t, appDSN, srv.SuperDSN(database))
	e.api.SetWorkspaceCreatedHook(e.enq.WorkspaceCreated)
	e.startWorker(t, jobs.WorkerOptions{})

	// A new workspace through the app login: the row, the job, the worker, the output.
	w, _, err := e.api.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: "org_role", Name: "Role", ClerkUpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the app login's worker to complete the job", func() bool {
		got := e.welcomeJobs(t, w.ID)
		return len(got) == 1 && got[0].State == "completed"
	})

	// No DDL at runtime, and River's bookkeeping table is read-only for the app.
	appConn, err := pgx.Connect(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appConn.Close(ctx) }()
	for _, stmt := range []string{
		`CREATE TABLE river_extra (n int)`,
		`DROP TABLE river_job`,
		`ALTER TABLE river_job ADD COLUMN extra int`,
		`TRUNCATE river_job`,
		`INSERT INTO river_migration (line, version) VALUES ('main', 999)`,
		`DELETE FROM river_migration`,
	} {
		_, err := appConn.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != permDenied {
			t.Errorf("app login: %s: error = %v, want SQLSTATE %s", stmt, err, permDenied)
		}
	}
	var have int
	if err := appConn.QueryRow(ctx, `SELECT max(version) FROM river_migration WHERE line = 'main'`).Scan(&have); err != nil || have == 0 {
		t.Errorf("app login reading river_migration: %d, %v", have, err)
	}

	// The migrate role can take River out and put it back; the grants come back with the tables.
	rm, err := jobs.NewMigrator(ctx, migrateDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	if _, err := rm.Down(ctx); err != nil {
		t.Fatalf("river down as the migrate role: %v", err)
	}
	if _, err := rm.Up(ctx); err != nil {
		t.Fatalf("river up as the migrate role: %v", err)
	}
	if err := jobs.CheckMigrated(ctx, e.api.Pool()); err != nil {
		t.Fatalf("app login after re-migrating: %v", err)
	}
}

// ── River UI ────────────────────────────────────────────────────────────────────────────────────────────────────

func TestRiverUIBehindBasicAuth(t *testing.T) {
	e := newEnv(t)
	const password = "ui-password-1234"
	w := e.startWorker(t, jobs.WorkerOptions{UIAddr: "127.0.0.1:0", UIPassword: password})
	base := "http://" + w.UIAddr()

	type answer struct {
		StatusCode int
		Challenge  string
	}
	get := func(path string, auth func(*http.Request)) answer {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != nil {
			auth(req)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return answer{StatusCode: resp.StatusCode, Challenge: resp.Header.Get("WWW-Authenticate")}
	}

	for _, path := range []string{"/", "/api/version", "/api/jobs"} {
		if resp := get(path, nil); resp.StatusCode != http.StatusUnauthorized || resp.Challenge == "" {
			t.Errorf("GET %s without credentials = %d, want 401 with a challenge", path, resp.StatusCode)
		}
		if resp := get(path, func(r *http.Request) { r.SetBasicAuth(jobs.UIUser, "wrong") }); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with a wrong password = %d, want 401", path, resp.StatusCode)
		}
		if resp := get(path, func(r *http.Request) { r.SetBasicAuth("someone", password) }); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with a wrong user = %d, want 401", path, resp.StatusCode)
		}
	}
	ok := func(r *http.Request) { r.SetBasicAuth(jobs.UIUser, password) }
	for _, path := range []string{"/", "/api/version", "/api/jobs"} {
		if resp := get(path, ok); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s with credentials = %d, want 200", path, resp.StatusCode)
		}
	}
}

func TestRiverUIRefusesToStartWithoutPassword(t *testing.T) {
	e := newEnv(t)
	_, err := jobs.StartWorker(context.Background(), jobs.WorkerOptions{DirectURL: e.dsn, Store: e.api, Logger: quiet, UIAddr: "127.0.0.1:0"})
	if !errors.Is(err, jobs.ErrNoUIPassword) {
		t.Fatalf("StartWorker with a UI address and no password = %v, want ErrNoUIPassword", err)
	}
	// Off by default: no address, no UI, no password needed.
	w, err := jobs.StartWorker(context.Background(), jobs.WorkerOptions{DirectURL: e.dsn, Store: e.api, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if w.UIAddr() != "" {
		t.Errorf("UIAddr = %q with the UI off", w.UIAddr())
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.StartWorker(context.Background(), jobs.WorkerOptions{Store: e.api}); err == nil || !strings.Contains(err.Error(), "DATABASE_DIRECT_URL") {
		t.Fatalf("StartWorker without a direct URL = %v", err)
	}
}
