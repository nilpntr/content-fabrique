package credits_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/credits/creditstest"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The newest price list prices every new charge, and a ledger row keeps the version that priced it: a run quoted under 2026-10 and still
// working when the default moves on is charged what its hold said (6 credits, version 2026-10), and the next run on the project is quoted
// from the current list (a revision is 8 credits, version 2026-10c).
func TestANewListPricesNewChargesAndOldLedgerRowsKeepTheirVersion(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	old, err := credits.LoadPriceList(configfiles.Plans(), "2026-10.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ws := r.workspace("versions")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 100)
	project := r.project(ws)

	// A first edit quoted and held under the old list.
	r.svc = credits.NewService(r.st, old)
	first, q, err := r.run(ws, project, false)
	if err != nil || q.Credits != 6 || q.BudgetMicros != 960_000 || q.ListVersion != "2026-10" {
		t.Fatalf("under 2026-10: %+v, %v", q, err)
	}
	// The default list takes over while it works; the run still ends on the price it was held at.
	r.svc = creditstest.Service(t, r.st)
	r.finish(ws, first, store.AgentSucceeded)
	rev, q, err := r.run(ws, project, false)
	if err != nil || q.Action != credits.EditRevision || q.Credits != 8 || q.BudgetMicros != 1_000_000 || q.ListVersion != "2026-10c" {
		t.Fatalf("under the default: %+v, %v", q, err)
	}
	r.finish(ws, rev, store.AgentSucceeded)

	rows, err := r.st.CreditLedger(ctx, ws.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		if row.Kind == store.CreditCharge && row.Action != nil && row.PlanListVersion != nil {
			got[*row.Action] = fmt.Sprintf("%d@%s", row.Credits, *row.PlanListVersion)
		}
	}
	if got[string(credits.EditFirst)] != "-6@2026-10" || got[string(credits.EditRevision)] != "-8@2026-10c" {
		t.Fatalf("charge rows %v, want the first edit at -6@2026-10 and the revision at -8@2026-10c", got)
	}
	if b, _ := r.st.CreditBalance(ctx, ws.ID, time.Now()); b.Available != 100-6-8 {
		t.Fatalf("balance %+v, want %d", b, 100-6-8)
	}
}
