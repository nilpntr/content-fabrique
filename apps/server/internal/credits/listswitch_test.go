package credits_test

import (
	"context"
	"slices"
	"testing"
	"time"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/credits/creditstest"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// When the price list changes under workspaces that already have their allowance, an allowance granted under an older list can be smaller
// than the new list's. A trial's cannot be granted again (its key is "once"), so it would stay at the old size; a refilling plan's would
// stay old until the next period. The difference is added once, as an adjustment with an actor and a reason; plans whose allowance did not
// grow are left alone, and so are their balances and top-ups.
//
// Going from 2026-10 to 2026-10c changes no allowance (the extra careful edits are new actions; the standard ones and the plans are as
// they were), so the real switch adds nothing; the mechanism is tried with a list that does grow the trial and the studio plan.
func TestAListSwitchTopsUpOnlyWhereTheAllowanceGrewAndOnlyOnce(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	old, err := credits.LoadPriceList(configfiles.Plans(), "2026-10.yaml")
	if err != nil {
		t.Fatal(err)
	}
	oldSvc := credits.NewService(r.st, old)
	cur := creditstest.Service(t, r.st)

	spaces := map[plans.Plan]store.Workspace{}
	for _, p := range plans.Granted {
		ws := r.workspace(string(p))
		spaces[p] = ws
		creditstest.Fund(t, r.st, ws.ID, p, 0)
		res, err := oldSvc.SyncAllowance(ctx, ws.ID)
		if err != nil || res.Granted != old.Plans[p].AllowanceCredits {
			t.Fatalf("%s under 2026-10: %+v, %v", p, res, err)
		}
	}
	// The trial used a first edit (6 credits): 14 of its 20 are left. The creator has a top-up as well.
	run, q, err := r.runWith(oldSvc, spaces[plans.Trial], r.project(spaces[plans.Trial]))
	if err != nil || q.Credits != 6 {
		t.Fatalf("trial first edit: %+v, %v", q, err)
	}
	r.finish(spaces[plans.Trial], run, store.AgentSucceeded)
	creditstest.Fund(t, r.st, spaces[plans.Creator].ID, plans.Creator, 40)

	avail := func(p plans.Plan) int {
		b, err := r.st.CreditBalance(ctx, spaces[p].ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return b.Available
	}
	before := map[plans.Plan]int{}
	for _, p := range plans.Granted {
		before[p] = avail(p)
	}
	if before[plans.Trial] != 14 {
		t.Fatalf("trial balance before = %d, want 14", before[plans.Trial])
	}

	// The real switch (2026-10 to 2026-10c): no allowance changed, so nobody is behind and nothing is added.
	realSpecs := cur.List.AllowanceSpecs()
	if behind, err := r.st.WorkspacesBehindTheList(ctx, realSpecs, time.Now(), ids.WorkspaceID{}, 100); err != nil || len(behind) != 0 {
		t.Fatalf("after the real switch %v are behind the list (%v)", behind, err)
	}
	for _, p := range plans.Granted {
		if n, err := cur.SyncListSwitch(ctx, spaces[p].ID); err != nil || n != 0 {
			t.Fatalf("%s: the real switch added %d, %v; want nothing", p, n, err)
		}
	}

	// A list that grows the trial (20 to 30) and the studio plan (600 to 1800).
	bigger, _ := credits.LoadDefault()
	bigger.Version = "2099-01"
	for p, n := range map[plans.Plan]int{plans.Trial: 30, plans.Studio: 1800} {
		pl := bigger.Plans[p]
		pl.AllowanceCredits = n
		bigger.Plans[p] = pl
	}
	biggerSvc := credits.NewService(r.st, bigger)
	specs := bigger.AllowanceSpecs()
	behind, err := r.st.WorkspacesBehindTheList(ctx, specs, time.Now(), ids.WorkspaceID{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []ids.WorkspaceID{spaces[plans.Trial].ID, spaces[plans.Studio].ID}
	cmp := func(a, b ids.WorkspaceID) int { return slices.Compare(a.UUID[:], b.UUID[:]) }
	slices.SortFunc(behind, cmp)
	slices.SortFunc(want, cmp)
	if !slices.Equal(behind, want) {
		t.Fatalf("workspaces behind the list = %v, want the trial and the studio workspace %v", behind, want)
	}

	added := map[plans.Plan]int{}
	for _, p := range plans.Granted {
		if added[p], err = biggerSvc.SyncListSwitch(ctx, spaces[p].ID); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if added[plans.Trial] != 10 || added[plans.Studio] != 1200 || added[plans.Creator] != 0 || added[plans.Pro] != 0 {
		t.Fatalf("added %v, want trial 10 (30 - 20), studio 1200 (1800 - 600), creator and pro nothing", added)
	}
	for _, p := range plans.Granted {
		if want := before[p] + added[p]; avail(p) != want {
			t.Errorf("%s balance = %d, want %d", p, avail(p), want)
		}
	}

	// Once: a second pass, from this process or another, adds nothing and finds nobody behind.
	for _, p := range plans.Granted {
		if n, err := biggerSvc.SyncListSwitch(ctx, spaces[p].ID); err != nil || n != 0 {
			t.Errorf("%s: a second pass added %d, %v", p, n, err)
		}
	}
	if behind, err := r.st.WorkspacesBehindTheList(ctx, specs, time.Now(), ids.WorkspaceID{}, 100); err != nil || len(behind) != 0 {
		t.Errorf("after the top-up %v are still behind the list (%v)", behind, err)
	}

	// The row says who and why, names the list that made it, and does not expire before the allowance does.
	rows, err := r.st.CreditLedger(ctx, spaces[plans.Studio].ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	var adj *store.CreditLedgerRow
	for i := range rows {
		if rows[i].Kind == store.CreditAdjust {
			adj = &rows[i]
		}
	}
	if adj == nil || adj.Credits != 1200 || adj.Actor == nil || *adj.Actor != store.ListSwitchActor || adj.Reason == nil || *adj.Reason == "" ||
		adj.PlanListVersion == nil || *adj.PlanListVersion != "2099-01" || adj.ExpiresAt == nil {
		t.Fatalf("studio adjustment row = %+v", adj)
	}

	// Creator keeps its credits: nothing was taken or added.
	if avail(plans.Creator) != before[plans.Creator] {
		t.Errorf("creator balance %d -> %d", before[plans.Creator], avail(plans.Creator))
	}

	// A later list that grows the trial again adds only what is still missing (the 10 of the first switch count as part of the
	// allowance); one that shrinks it adds nothing and takes nothing.
	next, _ := credits.LoadDefault()
	next.Version = "2099-02"
	trial := next.Plans[plans.Trial]
	trial.AllowanceCredits = 40
	next.Plans[plans.Trial] = trial
	if n, err := credits.NewService(r.st, next).SyncListSwitch(ctx, spaces[plans.Trial].ID); err != nil || n != 10 {
		t.Fatalf("a list with a 40-credit trial added %d, %v; want 10 (40 - 20 - 10)", n, err)
	}
	trial.AllowanceCredits = 25
	next.Plans[plans.Trial] = trial
	next.Version = "2099-03"
	if n, err := credits.NewService(r.st, next).SyncListSwitch(ctx, spaces[plans.Trial].ID); err != nil || n != 0 {
		t.Fatalf("a list with a 25-credit trial added %d, %v; want nothing", n, err)
	}
}

// runWith starts an editor run quoted by the given service.
func (r *rig) runWith(svc *credits.Service, ws store.Workspace, project ids.ProjectID) (store.AgentRun, credits.Quote, error) {
	r.t.Helper()
	saved := r.svc
	r.svc = svc
	defer func() { r.svc = saved }()
	return r.run(ws, project, false)
}
