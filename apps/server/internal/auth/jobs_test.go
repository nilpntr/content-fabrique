package auth

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// newJobsStore is newStore with River's tables, and the created-workspace hook of cmd/api wired in.
func newJobsStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st := store.New(pool)
	client, err := jobs.NewInsertClient(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.SetWorkspaceCreatedHook(jobs.NewEnqueuer(client, nil).WorkspaceCreated)
	return st, pool
}

func welcomeJobCount(t *testing.T, pool *pgxpool.Pool, workspaceID string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'workspace_welcome' AND args->>'workspace_id' = $1`, workspaceID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The just-in-time sync enqueues the welcome job in the transaction that creates the workspace: one job per new
// workspace, none for a teammate who joins it, none for the next request.
func TestSyncerEnqueuesOneWelcomeJobPerNewWorkspace(t *testing.T) {
	ctx := context.Background()
	st, pool := newJobsStore(t)
	dir := newFakeDir()
	dir.users["user_ada"] = ClerkUser{ID: "user_ada", Email: "ada@example.com", UpdatedAt: 1}
	dir.users["user_bob"] = ClerkUser{ID: "user_bob", Email: "bob@example.com", UpdatedAt: 1}
	dir.orgs["org_fit"] = ClerkOrg{ID: "org_fit", Name: "Fit Studio", UpdatedAt: 1}
	dir.orgs["org_lift"] = ClerkOrg{ID: "org_lift", Name: "Lift Club", UpdatedAt: 1}
	s := NewSyncer(st, dir)

	first, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 10))
	if err != nil {
		t.Fatal(err)
	}
	if n := welcomeJobCount(t, pool, first.WorkspaceID.String()); n != 1 {
		t.Fatalf("welcome jobs after the first sign-in = %d, want 1", n)
	}
	// The next request, and a teammate joining the existing workspace, enqueue nothing.
	for _, id := range []Identity{identity("user_ada", "org_fit", "org:admin", 20), identity("user_bob", "org_fit", "org:member", 30)} {
		if _, err := s.Resolve(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if n := welcomeJobCount(t, pool, first.WorkspaceID.String()); n != 1 {
		t.Fatalf("welcome jobs after more requests = %d, want still 1", n)
	}
	// Another new workspace gets its own.
	other, err := s.Resolve(ctx, identity("user_ada", "org_lift", "org:admin", 40))
	if err != nil {
		t.Fatal(err)
	}
	if n := welcomeJobCount(t, pool, other.WorkspaceID.String()); n != 1 {
		t.Fatalf("welcome jobs of the second workspace = %d, want 1", n)
	}
	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job`).Scan(&total); err != nil || total != 2 {
		t.Fatalf("jobs in total = %d, %v; want 2", total, err)
	}
}
