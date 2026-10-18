package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func (e *env) runJob(t *testing.T, args interface {
	Kind() string
}) {
	t.Helper()
	ctx := context.Background()
	if err := e.api.InTx(ctx, func(tx *store.Store) error {
		switch a := args.(type) {
		case jobs.CreditsPeriodsArgs:
			_, err := e.enq.InsertInStore(ctx, tx, a, nil)
			return err
		case jobs.CreditHoldsSweepArgs:
			_, err := e.enq.InsertInStore(ctx, tx, a, nil)
			return err
		}
		t.Fatalf("unknown job %T", args)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) ledgerCount(t *testing.T, ws store.Workspace, where string) (n int) {
	t.Helper()
	if err := e.super.QueryRow(context.Background(), `SELECT count(*) FROM credit_ledger WHERE workspace_id = $1 AND `+where, ws.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The periodic job grants the allowance of workspaces with an active plan (and only them), once, and expires what ran out of time.
func TestCreditsPeriodsJobGrantsAndExpires(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	paid := e.newWorkspace(t, "org_credits_paid")
	trial := e.newWorkspace(t, "org_credits_trial")
	none := e.newWorkspace(t, "org_credits_none")
	for ws, plan := range map[store.Workspace]plans.Plan{paid: plans.Creator, trial: plans.Trial} {
		if _, err := e.api.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plan, GrantedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	// A top-up that ran out of time yesterday, with credits left: the job writes its expiry.
	old := time.Now().AddDate(0, 0, -3)
	if _, _, err := e.api.GrantTopUp(ctx, store.GrantTopUpParams{Workspace: paid.ID, Credits: 25, ExpiresAt: time.Now().Add(-24 * time.Hour), Actor: "test", Reason: "old", Now: old}); err != nil {
		t.Fatal(err)
	}
	e.startWorker(t, jobs.WorkerOptions{})
	e.runJob(t, jobs.CreditsPeriodsArgs{})
	eventually(t, "the allowances and the expiry", func() bool {
		return e.ledgerCount(t, paid, `kind = 'grant_allowance'`) == 1 && e.ledgerCount(t, trial, `kind = 'grant_allowance'`) == 1 && e.ledgerCount(t, paid, `kind = 'expire'`) == 1
	})
	if n := e.ledgerCount(t, none, `true`); n != 0 {
		t.Fatalf("a workspace with no plan got %d ledger rows", n)
	}
	var credits int
	if err := e.super.QueryRow(ctx, `SELECT credits FROM credit_ledger WHERE workspace_id = $1 AND kind = 'expire'`, paid.ID).Scan(&credits); err != nil || credits != -25 {
		t.Fatalf("expire row = %d, %v", credits, err)
	}
	if b, err := e.api.CreditBalance(ctx, paid.ID, time.Now()); err != nil || b.Available != 150 || b.Allowance != 150 {
		t.Fatalf("paid balance = %+v, %v", b, err)
	}
	if b, err := e.api.CreditBalance(ctx, trial.ID, time.Now()); err != nil || b.Available != 30 {
		t.Fatalf("trial balance = %+v, %v", b, err)
	}

	// Running it again, as a new leader or a second worker would, adds nothing.
	for range 2 {
		e.runJob(t, jobs.CreditsPeriodsArgs{})
		time.Sleep(300 * time.Millisecond)
	}
	if n := e.ledgerCount(t, paid, `kind IN ('grant_allowance', 'expire')`); n != 2 {
		t.Fatalf("%d allowance and expiry rows after reruns, want 2", n)
	}
	if n := e.ledgerCount(t, trial, `true`); n != 1 {
		t.Fatalf("the trial has %d rows, want its one grant", n)
	}
}

// The sweep deletes the credit holds past their expiry and leaves the live ones; the credits of a swept hold are released, not charged.
func TestCreditHoldsSweepReleasesExpiredHolds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w := e.newWorkspace(t, "org_credit_sweep")
	if _, _, err := e.api.GrantTopUp(ctx, store.GrantTopUpParams{Workspace: w.ID, Credits: 20, ExpiresAt: time.Now().AddDate(0, 1, 0), Actor: "test", Reason: "test", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	crashed, live := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hold := func(id uuid.UUID, started time.Time) {
		t.Helper()
		if _, err := e.api.HoldCredits(ctx, store.HoldCreditsParams{Workspace: w.ID, Action: store.ActionEditFirst, ActionID: id, Credits: 6, Now: started, TTL: 10 * time.Minute}); err != nil {
			t.Fatal(err)
		}
	}
	hold(crashed, time.Now().Add(-time.Hour)) // its run died an hour ago; the hold expired 50 minutes ago
	hold(live, time.Now())
	count := func() (n int) {
		if err := e.super.QueryRow(ctx, `SELECT count(*) FROM credit_holds`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 2 {
		t.Fatalf("holds = %d", count())
	}
	e.startWorker(t, jobs.WorkerOptions{})
	e.runJob(t, jobs.CreditHoldsSweepArgs{})
	eventually(t, "the sweep to delete the expired hold", func() bool { return count() == 1 })
	var id uuid.UUID
	if err := e.super.QueryRow(ctx, `SELECT action_id FROM credit_holds`).Scan(&id); err != nil || id != live {
		t.Fatalf("left: %s, %v", id, err)
	}
	if n := e.ledgerCount(t, w, `kind = 'charge'`); n != 0 {
		t.Fatalf("the sweep charged %d rows", n)
	}
	if b, _ := e.api.CreditBalance(ctx, w.ID, time.Now()); b.Available != 14 {
		t.Fatalf("available = %d, want 14 (20 minus the live hold)", b.Available)
	}
}
