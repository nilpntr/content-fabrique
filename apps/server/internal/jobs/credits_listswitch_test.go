package jobs_test

import (
	"context"
	"testing"
	"time"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// A trial that was granted its 20 credits under an older price list is topped up by the periodic job when a list gives the trial more,
// once: its grant key ("once") blocks a second grant, so without the top-up it would stay at 20 for good. (The real switch from 2026-10
// to 2026-10c changes no allowance, so it adds nothing; the next list that does is tried here.)
func TestCreditsPeriodsJobTopsUpAnOldTrialOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.newWorkspace(t, "org_credits_oldtrial")
	if _, err := e.api.GrantPlan(ctx, store.GrantInput{WorkspaceID: trial.ID, Plan: plans.Trial, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	old, err := credits.LoadPriceList(configfiles.Plans(), "2026-10.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := e.api.SyncAllowance(ctx, trial.ID, old.AllowanceSpecs(), time.Now()); err != nil || res.Granted != 20 {
		t.Fatalf("the trial under 2026-10: %+v, %v", res, err)
	}
	next, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	next.Version = "2099-01"
	pl := next.Plans[plans.Trial]
	pl.AllowanceCredits = 30
	next.Plans[plans.Trial] = pl

	e.startWorker(t, jobs.WorkerOptions{Credits: next})
	e.runJob(t, jobs.CreditsPeriodsArgs{})
	eventually(t, "the top-up to the new list", func() bool { return e.ledgerCount(t, trial, `kind = 'adjust'`) == 1 })
	if b, err := e.api.CreditBalance(ctx, trial.ID, time.Now()); err != nil || b.Available != 30 {
		t.Fatalf("trial balance = %+v, %v; want the new list's 30", b, err)
	}
	var actor, reason string
	if err := e.super.QueryRow(ctx, `SELECT actor, reason FROM credit_ledger WHERE workspace_id = $1 AND kind = 'adjust'`, trial.ID).Scan(&actor, &reason); err != nil ||
		actor != store.ListSwitchActor || reason == "" {
		t.Fatalf("adjustment actor %q reason %q, %v", actor, reason, err)
	}

	// Again, as a second worker or a restart would: nothing more.
	for range 2 {
		e.runJob(t, jobs.CreditsPeriodsArgs{})
		time.Sleep(300 * time.Millisecond)
	}
	if n := e.ledgerCount(t, trial, `kind = 'adjust'`); n != 1 {
		t.Fatalf("%d adjustments after reruns, want 1", n)
	}
	if b, _ := e.api.CreditBalance(ctx, trial.ID, time.Now()); b.Available != 30 {
		t.Fatalf("trial balance after reruns = %d, want 30", b.Available)
	}
}

// The real switch to the current list adds nothing to a trial or a studio workspace that got its allowance under 2026-10.
func TestCreditsPeriodsJobAddsNothingForTheRealSwitch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	trial := e.newWorkspace(t, "org_credits_realswitch")
	if _, err := e.api.GrantPlan(ctx, store.GrantInput{WorkspaceID: trial.ID, Plan: plans.Trial, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	old, err := credits.LoadPriceList(configfiles.Plans(), "2026-10.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := e.api.SyncAllowance(ctx, trial.ID, old.AllowanceSpecs(), time.Now()); err != nil || res.Granted != 20 {
		t.Fatalf("the trial under 2026-10: %+v, %v", res, err)
	}
	e.startWorker(t, jobs.WorkerOptions{})
	e.runJob(t, jobs.CreditsPeriodsArgs{})
	time.Sleep(time.Second)
	if n := e.ledgerCount(t, trial, `kind = 'adjust'`); n != 0 {
		t.Fatalf("%d adjustments for a switch that changes no allowance", n)
	}
	if b, _ := e.api.CreditBalance(ctx, trial.ID, time.Now()); b.Available != 20 {
		t.Fatalf("trial balance = %d, want 20", b.Available)
	}
}
