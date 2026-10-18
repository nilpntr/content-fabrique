package credits_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/credits/creditstest"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// An extra careful edit (quality max) is its own action: the quote, the hold and the charge use that action's credits, budget and list
// version, and the project counts as having a charged edit all the same (the next edit of it is a revision, at either quality).
func TestAMaxEditHoldsAndChargesItsOwnAction(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ws := r.workspace("maxedit")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 100)
	project := r.project(ws)
	avail := func() int {
		b, err := r.st.CreditBalance(ctx, ws.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return b.Available
	}

	first, q, err := r.runQ(ws, project, false, credits.Max)
	if err != nil || q.Action != credits.EditFirstMax || q.Quality != credits.Max || q.Credits != 16 || q.BudgetMicros != 2_000_000 ||
		q.Plan != plans.Creator || q.ListVersion != "2026-10c" || first.BudgetMicros != 2_000_000 {
		t.Fatalf("max first edit: %+v, %+v, %v", q, first.BudgetMicros, err)
	}
	h, err := r.st.GetCreditHold(ctx, ws.ID, first.ID)
	if err != nil || h.Action != store.ActionEditFirstMax || h.Credits != 16 || h.ListVersion != "2026-10c" {
		t.Fatalf("hold = %+v, %v", h, err)
	}
	if avail() != 84 {
		t.Fatalf("available while held = %d, want 84", avail())
	}
	r.finish(ws, first, store.AgentSucceeded)

	// The project has a charged edit now, so the next one is a revision; a standard revision costs the standard price.
	std, q, err := r.runQ(ws, project, false, credits.Standard)
	if err != nil || q.Action != credits.EditRevision || q.Credits != 2 || q.BudgetMicros != 320_000 {
		t.Fatalf("standard revision after a max first edit: %+v, %v", q, err)
	}
	r.finish(ws, std, store.AgentSucceeded)
	// A max revision.
	rev, q, err := r.runQ(ws, project, false, credits.Max)
	if err != nil || q.Action != credits.EditRevisionMax || q.Credits != 7 || q.BudgetMicros != 800_000 || rev.BudgetMicros != 800_000 {
		t.Fatalf("max revision: %+v, %v", q, err)
	}
	r.finish(ws, rev, store.AgentSucceeded)

	rows, err := r.st.CreditLedger(ctx, ws.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		if row.Kind == store.CreditCharge && row.Action != nil && row.PlanListVersion != nil {
			got[fmt.Sprintf("%s@%s", *row.Action, *row.PlanListVersion)] = fmt.Sprint(row.Credits)
		}
	}
	for want, credits := range map[string]string{"edit.first.max@2026-10c": "-16", "edit.revision@2026-10c": "-2", "edit.revision.max@2026-10c": "-7"} {
		if got[want] != credits {
			t.Errorf("charge row %s = %q, want %s (rows %v)", want, got[want], credits, got)
		}
	}
	if avail() != 100-16-2-7 {
		t.Fatalf("balance = %d, want %d", avail(), 100-16-2-7)
	}
	// A max first edit that failed was released: the project has no charged edit, so the next one is a first edit again.
	other := r.project(ws)
	failed, _, err := r.runQ(ws, other, false, credits.Max)
	if err != nil {
		t.Fatal(err)
	}
	r.finish(ws, failed, store.AgentFailed)
	if avail() != 100-16-2-7 {
		t.Fatalf("a failed max edit cost %d", 100-16-2-7-avail())
	}
	if _, q, err := r.runQ(ws, other, false, credits.Standard); err != nil || q.Action != credits.EditFirst {
		t.Fatalf("after a failed max first edit: %+v, %v", q, err)
	}
}

// The trial does not include extra careful edits: a clear error, and nothing is created or held. The studio plan (on its own key) and the
// managed plans do. A quality that is neither standard nor max, and a list that has no max edits, are refused too.
func TestMaxQualityIsRefusedOnTheTrial(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	count := func(ws store.Workspace) (n int) {
		row := r.st.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_runs WHERE workspace_id = $1) + (SELECT count(*) FROM credit_holds WHERE workspace_id = $1)`, ws.ID)
		if err := row.Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	trial := r.workspace("trial")
	creditstest.Fund(t, r.st, trial.ID, plans.Trial, 100)
	if _, _, err := r.runQ(trial, r.project(trial), false, credits.Max); !errors.Is(err, credits.ErrQualityNotInPlan) {
		t.Fatalf("trial, max: %v", err)
	}
	if n := count(trial); n != 0 {
		t.Fatalf("%d rows left behind by a refused max edit", n)
	}
	if _, q, err := r.runQ(trial, r.project(trial), false, credits.Standard); err != nil || q.Credits != 6 {
		t.Fatalf("trial, standard: %+v, %v", q, err)
	}

	for _, p := range []plans.Plan{plans.Studio, plans.Creator, plans.Pro} {
		ws := r.workspace(string(p))
		creditstest.Fund(t, r.st, ws.ID, p, 100)
		if _, q, err := r.runQ(ws, r.project(ws), false, credits.Max); err != nil || q.Action != credits.EditFirstMax || q.Credits != 16 {
			t.Errorf("%s, max: %+v, %v", p, q, err)
		}
	}

	ws := r.workspace("badquality")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 100)
	if _, _, err := r.runQ(ws, r.project(ws), false, credits.Quality("ultra")); !errors.Is(err, credits.ErrQualityInvalid) {
		t.Fatalf("quality ultra: %v", err)
	}
	// An empty quality is standard.
	if _, q, err := r.runQ(ws, r.project(ws), false, ""); err != nil || q.Action != credits.EditFirst || q.Quality != credits.Standard {
		t.Fatalf("empty quality: %+v, %v", q, err)
	}
	// A list that prices no max edits (2026-10) refuses them.
	old := loadOld(t)
	if _, _, err := r.runWith2(credits.NewService(r.st, old), ws, credits.Max); !errors.Is(err, credits.ErrQualityNotInPlan) {
		t.Fatalf("2026-10, max: %v", err)
	}
}

// A development run (cfctl agent run --no-credits) may ask for max: it costs nothing, keeps the caller's budget, and is still an edit of
// the project.
func TestAFreeRunMayBeMax(t *testing.T) {
	r := newRig(t)
	ws := r.workspace("freemax")
	_, q, err := r.runQ(ws, r.project(ws), true, credits.Max)
	if err != nil || !q.Free || q.Credits != 0 || q.Action != credits.EditFirstMax || q.Quality != credits.Max {
		t.Fatalf("free max run: %+v, %v", q, err)
	}
}

// runWith2 starts a first edit of the quality on the project of a new workspace's, quoted by the given service.
func (r *rig) runWith2(svc *credits.Service, ws store.Workspace, quality credits.Quality) (store.AgentRun, credits.Quote, error) {
	r.t.Helper()
	saved := r.svc
	r.svc = svc
	defer func() { r.svc = saved }()
	return r.runQ(ws, r.project(ws), false, quality)
}
