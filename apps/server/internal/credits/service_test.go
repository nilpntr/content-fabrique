package credits_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
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
		if _, err = r.svc.Hold(ctx, tx, q, run); err != nil {
			return err
		}
		return r.svc.CheckUncharged(ctx, tx, ws.ID, q)
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
			return r.svc.RecordFinalExport(ctx, tx, ws.ID, project, ids.UserID{}, render, 1)
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

// addVersion stores the next timeline version of the project, made by the run (an agent version) or by a person (run is the zero value).
func (r *rig) addVersion(ws store.Workspace, project ids.ProjectID, n int, run *store.AgentRun) {
	r.t.Helper()
	in := store.NewTimelineVersion{Version: n, Parent: n - 1, Spec: json.RawMessage(`{}`), AuthorKind: "user", AuthorID: "usr_test"}
	if run != nil {
		in.AuthorKind, in.AuthorID = "agent", ids.Encode(ids.PrefixRun, run.ID)
	}
	if _, err := r.st.AddTimelineVersion(context.Background(), ws.ID, project, in); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) exportable(ws store.Workspace, project ids.ProjectID, version int) error {
	r.t.Helper()
	ctx := context.Background()
	return r.st.InTx(ctx, func(tx *store.Store) error {
		return r.svc.RecordFinalExport(ctx, tx, ws.ID, project, ids.UserID{}, uuid.Must(uuid.NewV7()), version)
	})
}

// A final export must point at paid work: versions made by a run that was charged, and a person's edits on top of them, can be exported;
// versions made by a run that failed or was released, and the edits made on top of those, cannot, until a later run is charged.
func TestFinalExportMustPointAtPaidWork(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ws := r.workspace("paid")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 100)
	project := r.project(ws)

	r.addVersion(ws, project, 1, nil) // a person's first version: no agent work in it
	a, _, err := r.run(ws, project, false)
	if err != nil {
		t.Fatal(err)
	}
	r.addVersion(ws, project, 2, &a)
	// While the run works its version is not paid for yet.
	if err := r.exportable(ws, project, 2); !errors.Is(err, credits.ErrEditNotCharged) {
		t.Fatalf("export during the first run: %v", err)
	}
	r.finish(ws, a, store.AgentSucceeded)
	r.addVersion(ws, project, 3, nil) // a person's edit on top of paid work
	for _, v := range []int{1, 2, 3} {
		if err := r.exportable(ws, project, v); err != nil {
			t.Fatalf("version %d after a charged run: %v", v, err)
		}
	}
	// The charge says which version it paid for.
	var paid *int
	if err := r.st.Pool().QueryRow(ctx, `SELECT timeline_version FROM credit_ledger WHERE workspace_id = $1 AND action_id = $2 AND kind = 'charge'`, ws.ID, a.ID).Scan(&paid); err != nil || paid == nil || *paid != 2 {
		t.Fatalf("the charge paid for version %v (%v), want 2", paid, err)
	}

	// A revision that fails after it made a version: released, so neither its version nor a person's edit on top of it can be exported.
	b, _, err := r.run(ws, project, false)
	if err != nil {
		t.Fatal(err)
	}
	r.addVersion(ws, project, 4, &b)
	r.finish(ws, b, store.AgentFailed)
	r.addVersion(ws, project, 5, nil)
	for _, v := range []int{4, 5} {
		if err := r.exportable(ws, project, v); !errors.Is(err, credits.ErrVersionNotCharged) {
			t.Fatalf("version %d from a released run: %v", v, err)
		}
	}
	for _, v := range []int{1, 2, 3} { // earlier paid work stays exportable
		if err := r.exportable(ws, project, v); err != nil {
			t.Fatalf("version %d after a failed run: %v", v, err)
		}
	}

	// A later run that is charged covers what comes after it; the released run's versions below it stay refused.
	c, _, err := r.run(ws, project, false)
	if err != nil {
		t.Fatal(err)
	}
	r.addVersion(ws, project, 6, &c)
	r.finish(ws, c, store.AgentSucceeded)
	r.addVersion(ws, project, 7, nil)
	for _, v := range []int{6, 7} {
		if err := r.exportable(ws, project, v); err != nil {
			t.Fatalf("version %d after the charged run: %v", v, err)
		}
	}
	for _, v := range []int{4, 5} {
		if err := r.exportable(ws, project, v); !errors.Is(err, credits.ErrVersionNotCharged) {
			t.Fatalf("version %d, below the charged run: %v", v, err)
		}
	}

	// Another project of the workspace and another workspace have their own lineage.
	other := r.project(ws)
	if err := r.exportable(ws, other, 1); !errors.Is(err, credits.ErrEditNotCharged) {
		t.Fatalf("another project: %v", err)
	}
}

// A creator who cancels is charged for what the run made: a timeline version or a preview. A cancel before either is released, and the
// version a charged cancel made can be exported.
func TestCancelIsChargedAfterAVersionAndReleasedBeforeOne(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ws := r.workspace("cancel")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 100)
	project := r.project(ws)
	avail := func() int {
		b, err := r.st.CreditBalance(ctx, ws.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return b.Available
	}

	// Cancelled before it made anything: released.
	nothing, _, err := r.run(ws, project, false)
	if err != nil {
		t.Fatal(err)
	}
	r.finish(ws, nothing, store.AgentCancelled)
	if avail() != 100 {
		t.Fatalf("a cancel before any version cost %d", 100-avail())
	}
	if err := r.exportable(ws, project, 1); !errors.Is(err, credits.ErrEditNotCharged) {
		t.Fatalf("export after a released cancel: %v", err)
	}

	// Cancelled after a version, before any preview: charged, and its version can be exported.
	made, q, err := r.run(ws, project, false)
	if err != nil || q.Action != credits.EditFirst {
		t.Fatalf("the run after a released cancel is still the first edit: %+v %v", q, err)
	}
	r.addVersion(ws, project, 1, &made)
	r.finish(ws, made, store.AgentCancelled)
	if avail() != 94 {
		t.Fatalf("a cancel after a version left %d credits, want 94", avail())
	}
	if err := r.exportable(ws, project, 1); err != nil {
		t.Fatalf("the version of a charged cancel: %v", err)
	}
	// The next revision, cancelled after its version and before its preview, costs its 2 credits and cannot be had for free.
	rev, q, err := r.run(ws, project, false)
	if err != nil || q.Action != credits.EditRevision {
		t.Fatalf("revision: %+v %v", q, err)
	}
	r.addVersion(ws, project, 2, &rev)
	r.finish(ws, rev, store.AgentCancelled)
	if avail() != 92 {
		t.Fatalf("a cancelled revision left %d credits, want 92", avail())
	}
	if err := r.exportable(ws, project, 2); err != nil {
		t.Fatalf("the version of a charged revision: %v", err)
	}
}

// A run that says "nothing to change" but did make a version is charged: the version is in the project. One that made none is released.
func TestNoEditNeededIsReleasedOnlyWhenNothingWasMade(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ws := r.workspace("noedit")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 100)
	project := r.project(ws)
	end := func(run store.AgentRun) {
		t.Helper()
		c, err := r.st.ClaimAgentRun(ctx, ws.ID, run.ID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.EndAgentRun(ctx, ws.ID, run.ID, c.Epoch, store.RunEnd{Status: store.AgentSucceeded, Output: json.RawMessage(`{"summary":"x","no_edit_needed":true}`), Event: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	nothing, _, err := r.run(ws, project, false)
	if err != nil {
		t.Fatal(err)
	}
	end(nothing)
	if ok, _ := r.st.ProjectHasChargedEdit(ctx, ws.ID, project); ok {
		t.Fatal("a run that made nothing was charged")
	}
	made, q, err := r.run(ws, project, false)
	if err != nil || q.Action != credits.EditFirst { // still the first edit: nothing was charged
		t.Fatalf("%+v %v", q, err)
	}
	r.addVersion(ws, project, 1, &made)
	end(made)
	if ok, _ := r.st.ProjectHasChargedEdit(ctx, ws.ID, project); !ok {
		t.Fatal("a run that made a version was not charged")
	}
}

// endRun ends a claimed run the way the loop would: some model spend on its totals (cost micros), then the status.
func (r *rig) endRun(ws store.Workspace, run store.AgentRun, cost int64, end store.RunEnd) {
	r.t.Helper()
	ctx := context.Background()
	c, err := r.st.ClaimAgentRun(ctx, ws.ID, run.ID, time.Minute)
	if err != nil {
		r.t.Fatal(err)
	}
	if cost > 0 {
		if _, err := r.st.AppendAgentEvent(ctx, ws.ID, run.ID, c.Epoch, store.EventStatus, json.RawMessage(`{"state":"model_call"}`), store.EventDelta{CostMicros: cost}); err != nil {
			r.t.Fatal(err)
		}
	}
	if end.Event == nil {
		end.Event = json.RawMessage(`{}`)
	}
	if _, err := r.st.EndAgentRun(ctx, ws.ID, run.ID, c.Epoch, end); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) uncharged(ws store.Workspace) int {
	r.t.Helper()
	// Runs in flight count too: the helper above ends every run it starts, so none is left.
	n, err := r.st.CountUnchargedRuns(context.Background(), ws.ID, credits.EditorAgent, time.Now().UTC().Truncate(24*time.Hour), credits.UnchargedFailureReasons)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// Runs that spent money and ended without a charge are capped per workspace and UTC day. Counted: cancelled before anything was made,
// "nothing to change", and failures that are the creator's own. Not counted: runs that cost nothing, charged runs (an edit made, however it
// ended), failures that are ours, yesterday's, and other workspaces'.
func TestUnchargedRunsAreCountedAndCappedPerDay(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ws, other := r.workspace("cap"), r.workspace("cap-other")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 200)
	creditstest.Fund(t, r.st, other.ID, plans.Creator, 200)
	r.svc = creditstest.ServiceWith(t, r.st, func(l *credits.PriceList) {
		pl := l.Plans[plans.Creator]
		pl.UnchargedRunsPerDay = 3
		l.Plans[plans.Creator] = pl
	})
	project := r.project(ws)
	fail := func(reason string) store.RunEnd {
		return store.RunEnd{Status: store.AgentFailed, FailureReason: reason}
	}
	run := func() store.AgentRun {
		t.Helper()
		run, _, err := r.run(ws, project, false)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}

	// Not counted: nothing was spent; a failure of ours; a charged run.
	r.endRun(ws, run(), 0, store.RunEnd{Status: store.AgentCancelled})
	for _, ours := range []string{"budget", "model_error", "internal_error", "model_outcome_unknown", "max_tokens", "daily_cap", "platform_spend_paused"} {
		r.endRun(ws, run(), 5_000, fail(ours))
	}
	made := run()
	r.addVersion(ws, project, 1, &made)
	r.endRun(ws, made, 5_000, store.RunEnd{Status: store.AgentCancelled}) // a cancel that made a version is charged
	ok := run()
	r.addVersion(ws, project, 2, &ok)
	r.endRun(ws, ok, 5_000, store.RunEnd{Status: store.AgentSucceeded, Output: json.RawMessage(`{"summary":"x","timeline_version":2,"no_edit_needed":false}`)})
	if n := r.uncharged(ws); n != 0 {
		t.Fatalf("%d runs counted before any ended uncharged", n)
	}

	// Counted: cancelled before anything was made (with spend), nothing to change, and a failure of the creator's own.
	r.endRun(ws, run(), 5_000, store.RunEnd{Status: store.AgentCancelled})
	if n := r.uncharged(ws); n != 1 {
		t.Fatalf("after a cancel that cost money: %d", n)
	}
	r.endRun(ws, run(), 5_000, store.RunEnd{Status: store.AgentSucceeded, Output: json.RawMessage(`{"summary":"already fine","timeline_version":null,"no_edit_needed":true}`)})
	if n := r.uncharged(ws); n != 2 {
		t.Fatalf("after a no-edit run: %d", n)
	}
	// Two below the cap: one more run starts.
	last := run()
	r.endRun(ws, last, 5_000, fail("refusal"))
	if n := r.uncharged(ws); n != 3 {
		t.Fatalf("after a refusal: %d", n)
	}

	// At the cap the next start is refused with the cap in the error, and nothing is created or held.
	var before int
	if err := r.st.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_runs WHERE workspace_id = $1) + (SELECT count(*) FROM credit_holds WHERE workspace_id = $1)`, ws.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_, _, err := r.run(ws, project, false)
	var tm *credits.TooManyUnchargedError
	if !errors.Is(err, credits.ErrTooManyUncharged) || !errors.As(err, &tm) || tm.Cap != 3 {
		t.Fatalf("the fourth uncharged run's successor: %v", err)
	}
	var after int
	if err := r.st.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_runs WHERE workspace_id = $1) + (SELECT count(*) FROM credit_holds WHERE workspace_id = $1)`, ws.ID).Scan(&after); err != nil || after != before {
		t.Fatalf("rows %d before the refusal and %d after (%v)", before, after, err)
	}
	// A run without credits (development) is not subject to it, and another workspace is not affected.
	if _, _, err := r.run(ws, project, true); err != nil {
		t.Fatalf("a free run: %v", err)
	}
	if _, _, err := r.run(other, r.project(other), false); err != nil {
		t.Fatalf("another workspace: %v", err)
	}
	// Tomorrow it starts again.
	r.svc.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	if _, err := r.st.Pool().Exec(ctx, `UPDATE agent_runs SET finished_at = finished_at - interval '25 hours' WHERE workspace_id = $1 AND finished_at IS NOT NULL`, ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.run(ws, project, false); err != nil {
		t.Fatalf("the next day: %v", err)
	}
}

// Concurrent starts of one workspace cannot overshoot the cap: the hold's lock on the credit account serialises them, so each count sees
// the runs of the starts before it.
func TestUnchargedCapHoldsUnderConcurrentStarts(t *testing.T) {
	r := newRig(t)
	ws := r.workspace("burst")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 200)
	r.svc = creditstest.ServiceWith(t, r.st, func(l *credits.PriceList) {
		pl := l.Plans[plans.Creator]
		pl.UnchargedRunsPerDay = 3
		l.Plans[plans.Creator] = pl
	})
	const starts = 8
	projects := make([]ids.ProjectID, starts)
	for i := range projects {
		projects[i] = r.project(ws)
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		started int
		refused int
	)
	for _, p := range projects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := r.run(ws, p, false)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				started++
			case errors.Is(err, credits.ErrTooManyUncharged):
				refused++
			default:
				t.Errorf("start: %v", err)
			}
		}()
	}
	wg.Wait()
	// Every run in flight counts as one that may end uncharged, so exactly the cap gets in.
	if started != 3 || refused != starts-3 {
		t.Fatalf("%d started and %d refused, want 3 and %d", started, refused, starts-3)
	}
}

// A run created before the day's start that is still in flight counts for today, and the look-back is bounded.
func TestUnchargedCountSpansMidnight(t *testing.T) {
	r := newRig(t)
	ws := r.workspace("midnight")
	creditstest.Fund(t, r.st, ws.ID, plans.Creator, 50)
	if _, _, err := r.run(ws, r.project(ws), false); err != nil {
		t.Fatal(err)
	}
	count := func(since time.Time) int {
		t.Helper()
		n, err := r.st.CountUnchargedRuns(context.Background(), ws.ID, credits.EditorAgent, since, credits.UnchargedFailureReasons)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The run was created a moment ago; "today" starts a week after it. It is still counted (in flight), and eight days is the limit.
	if n := count(time.Now().Add(7 * 24 * time.Hour)); n != 1 {
		t.Fatalf("a run in flight from before the day started: %d, want 1", n)
	}
	if n := count(time.Now().Add(9 * 24 * time.Hour)); n != 0 {
		t.Fatalf("a run from nine days before: %d, want 0", n)
	}
}
