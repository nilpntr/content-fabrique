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

// A trial that was granted its 20 credits under price list 2026-10 is topped up to the current list's trial by the periodic job, once:
// its grant key ("once") blocks a second grant, so without the top-up it would stay at 20 for good.
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

	e.startWorker(t, jobs.WorkerOptions{})
	e.runJob(t, jobs.CreditsPeriodsArgs{})
	eventually(t, "the top-up to the current list", func() bool { return e.ledgerCount(t, trial, `kind = 'adjust'`) == 1 })
	if b, err := e.api.CreditBalance(ctx, trial.ID, time.Now()); err != nil || b.Available != 30 {
		t.Fatalf("trial balance = %+v, %v; want the current list's 30", b, err)
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
