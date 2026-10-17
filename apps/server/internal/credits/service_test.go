package credits_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/credits/creditstest"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

type rig struct {
	t   *testing.T
	st  *store.Store
	svc *credits.Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "credits-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return &rig{t: t, st: st, svc: creditstest.Service(t, st)}
}

func (r *rig) workspace(name string) store.Workspace {
	r.t.Helper()
	w, err := r.st.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: "org_" + name + uuid.NewString(), Name: name})
	if err != nil {
		r.t.Fatal(err)
	}
	return w
}

func (r *rig) project(ws store.Workspace) ids.ProjectID {
	r.t.Helper()
	u, err := r.st.UpsertUserByClerkID(context.Background(), "user_"+uuid.NewString(), "creator@example.com")
	if err != nil {
		r.t.Fatal(err)
	}
	p, err := r.st.CreateProject(context.Background(), ws.ID, store.NewProject{Title: "p", Format: "9:16", CreatedBy: u.ID})
	if err != nil {
		r.t.Fatal(err)
	}
	return p.ID
}

// run creates a queued run with a hold, the way the runtime does: quote, create, hold, all in one transaction.
func (r *rig) run(ws store.Workspace, project ids.ProjectID, free bool) (store.AgentRun, credits.Quote, error) {
	r.t.Helper()
	ctx := context.Background()
	var (
		run store.AgentRun
		q   credits.Quote
	)
	err := r.st.InTx(ctx, func(tx *store.Store) error {
		var err error
		if q, err = r.svc.QuoteEdit(ctx, tx, ws.ID, project, free); err != nil {
			return err
		}
		budget := q.BudgetMicros
		if budget == 0 {
			budget = 100_000
		}
		if run, err = tx.CreateAgentRun(ctx, ws.ID, store.NewAgentRun{
			Agent: "editor", Stage: "editor", ProjectID: project.UUID, Input: json.RawMessage(`{}`), RoutingProfile: "dev-cheap", Model: "claude-haiku-5-5",
			Effort: "medium", MaxTokens: 1000, BudgetMicros: budget, BudgetTokens: 10_000, BudgetToolCalls: 5, BudgetWallClockMs: 60_000, BilledTo: store.BilledToTest,
		}); err != nil {
			return err
		}
		_, err = r.svc.Hold(ctx, tx, q, run)
		return err
	})
	return run, q, err
}

func (r *rig) finish(ws store.Workspace, run store.AgentRun, status string) {
	r.t.Helper()
	ctx := context.Background()
	c, err := r.st.ClaimAgentRun(ctx, ws.ID, run.ID, time.Minute)
	if err != nil {
		r.t.Fatal(err)
	}
	end := store.RunEnd{Status: status, Event: json.RawMessage(`{"state":"x"}`)}
	if status == store.AgentFailed {
		end.FailureReason = "budget"
	}
	if _, err := r.st.EndAgentRun(ctx, ws.ID, run.ID, c.Epoch, end); err != nil {
		r.t.Fatal(err)
	}
}

// The server decides between the first edit and a revision from the project: the first edit is the run on a project with no charged edit.
func TestQuoteDecidesFirstOrRevisionFromTheProject(t *testing.T) {
	r := newRig(t)
	ws := r.workspace("quote")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 100)
	project, other := r.project(ws), r.project(ws)

	first, q, err := r.run(ws, project, false)
	if err != nil || q.Action != credits.EditFirst || q.Credits != 6 || q.BudgetMicros != 960_000 || q.Plan != plans.Creator || q.ListVersion != "2026-10" {
		t.Fatalf("first run: %+v, %v", q, err)
	}
	if first.BudgetMicros != 960_000 {
		t.Fatalf("the run's budget is %d, want the plan's 960,000", first.BudgetMicros)
	}
	// While it works, and after it failed: still the first edit (a run that did not succeed is not a charged edit).
	r.finish(ws, first, store.AgentFailed)
	failedThenFirst, q, err := r.run(ws, project, false)
	if err != nil || q.Action != credits.EditFirst {
		t.Fatalf("after a failed first run: %+v, %v", q, err)
	}
	// It succeeds: the next one on this project is a revision, on another project the first edit still.
	r.finish(ws, failedThenFirst, store.AgentSucceeded)
	rev, q, err := r.run(ws, project, false)
	if err != nil || q.Action != credits.EditRevision || q.Credits != 2 || q.BudgetMicros != 320_000 || rev.BudgetMicros != 320_000 {
		t.Fatalf("revision: %+v, %v", q, err)
	}
	r.finish(ws, rev, store.AgentFailed) // the revision failed: released
	again, q, err := r.run(ws, project, false)
	if err != nil || q.Action != credits.EditRevision {
		t.Fatalf("after a failed revision: %+v, %v", q, err)
	}
	r.finish(ws, again, store.AgentSucceeded)
	if _, q, err := r.run(ws, other, false); err != nil || q.Action != credits.EditFirst {
		t.Fatalf("another project: %+v, %v", q, err)
	}
	// 100 credits: 6 charged for the first edit, 2 for the revision, and 6 held for the other project's first run.
	b, _ := r.st.CreditBalance(context.Background(), ws.ID, time.Now())
	if b.Available != 100-6-2-6 || b.Held != 6 {
		t.Fatalf("balance %+v", b)
	}
}

// The price and the budget are the plan's, not the caller's: each plan's own budget for the action.
func TestQuoteUsesThePlansPriceAndBudget(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		plan   plans.Plan
		budget int64
	}{{plans.Trial, 750_000}, {plans.Studio, 2_000_000}, {plans.Creator, 960_000}, {plans.Pro, 790_000}} {
		ws := r.workspace(string(tc.plan))
		creditstest.Fund(t, r.st, ws.ID, tc.plan, 50)
		run, q, err := r.run(ws, r.project(ws), false)
		if err != nil || q.Credits != 6 || q.BudgetMicros != tc.budget || run.BudgetMicros != tc.budget || q.Plan != tc.plan {
			t.Errorf("%s: %+v, %v", tc.plan, q, err)
		}
	}
}

func TestQuoteRefusesWithoutCreditsOrPlan(t *testing.T) {
	r := newRig(t)
	ws := r.workspace("broke")
	project := r.project(ws)
	if _, _, err := r.run(ws, project, false); !errors.Is(err, credits.ErrNoPlan) {
		t.Fatalf("no plan: %v", err)
	}
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 5) // a first edit costs 6
	if _, _, err := r.run(ws, project, false); !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("5 credits for a first edit: %v", err)
	}
	// Nothing was created by the refused run, and no hold is left.
	var n int
	row := r.st.Pool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM agent_runs WHERE workspace_id = $1) + (SELECT count(*) FROM credit_holds WHERE workspace_id = $1)`, ws.ID)
	if err := row.Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows left behind by a refused run (%v)", n, err)
	}
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 1)
	if _, _, err := r.run(ws, project, false); err != nil {
		t.Fatalf("6 credits for a first edit: %v", err)
	}
}

// A run without credits needs no plan, costs nothing, keeps the caller's budget, and is still recorded as an edit.
func TestQuoteFreeRun(t *testing.T) {
	r := newRig(t)
	ws := r.workspace("free")
	project := r.project(ws)
	run, q, err := r.run(ws, project, true)
	if err != nil || q.Credits != 0 || q.BudgetMicros != 0 || !q.Free || q.Action != credits.EditFirst {
		t.Fatalf("free run: %+v, %v", q, err)
	}
	if run.BudgetMicros != 100_000 {
		t.Fatalf("the caller's budget was replaced: %d", run.BudgetMicros)
	}
	r.finish(ws, run, store.AgentSucceeded)
	if _, q, err := r.run(ws, project, true); err != nil || q.Action != credits.EditRevision {
		t.Fatalf("the next free run: %+v, %v", q, err)
	}
	b, _ := r.st.CreditBalance(context.Background(), ws.ID, time.Now())
	if b.Available != 0 {
		t.Fatalf("a free run moved credits: %+v", b)
	}
}

func TestQuoteOfAnotherWorkspacesProjectIsNotFound(t *testing.T) {
	r := newRig(t)
	a, b := r.workspace("a"), r.workspace("b")
	creditstest.Fund(t, r.st, a.ID, plans.Creator, 50)
	creditstest.Fund(t, r.st, b.ID, plans.Creator, 50)
	project := r.project(a)
	if _, _, err := r.run(b, project, false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("B quoting A's project: %v", err)
	}
}

// A final export needs a charged edit and is recorded on the ledger at 0 credits, once per render.
func TestRecordFinalExport(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ws := r.workspace("export")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 50)
	project := r.project(ws)
	render := uuid.Must(uuid.NewV7())
	export := func() error {
		return r.st.InTx(ctx, func(tx *store.Store) error {
			return r.svc.RecordFinalExport(ctx, tx, ws.ID, project, ids.UserID{}, render)
		})
	}
	if err := export(); !errors.Is(err, credits.ErrEditNotCharged) {
		t.Fatalf("an export with no edit: %v", err)
	}
	run, _, err := r.run(ws, project, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := export(); !errors.Is(err, credits.ErrEditNotCharged) {
		t.Fatalf("an export while the first edit is still running: %v", err)
	}
	r.finish(ws, run, store.AgentSucceeded)
	for range 2 {
		if err := export(); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := r.st.CreditLedger(ctx, ws.ID, 50)
	var exports int
	for _, row := range rows {
		if row.Action != nil && *row.Action == string(credits.ExportFinal) {
			exports++
			if row.Credits != 0 || row.Kind != store.CreditCharge {
				t.Errorf("export row = %+v", row)
			}
		}
	}
	if exports != 1 {
		t.Fatalf("%d export rows for one render, want 1", exports)
	}
	b, _ := r.st.CreditBalance(ctx, ws.ID, time.Now())
	if b.Available != 50-6 {
		t.Fatalf("an export changed the balance: %+v", b)
	}
}
