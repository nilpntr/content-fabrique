package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/credits/creditstest"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// An editor run costs credits (P7-01a): the runtime decides first edit or revision, takes the price and the budget from the plan, and sets
// the credits aside in the transaction that creates the run. The echo agent costs nothing.

// withBilledAgent registers an agent named like the editor (the one that costs credits) over the echo agent's definition.
func withBilledAgent(cfg *envConfig) {
	a := NewEchoAgent(&memSink{})
	a.Name = credits.EditorAgent
	cfg.extra = append(cfg.extra, a)
}

func (e *env) project() ids.ProjectID {
	e.t.Helper()
	u, err := e.st.UpsertUserByClerkID(context.Background(), "user_"+uuid.NewString(), "rt@example.com")
	if err != nil {
		e.t.Fatal(err)
	}
	p, err := e.st.CreateProject(context.Background(), e.ws.ID, store.NewProject{Title: "p", Format: "9:16", CreatedBy: u.ID})
	if err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

func (e *env) startEdit(project ids.ProjectID, b Budgets, noCredits bool) (store.AgentRun, error) {
	input, _ := json.Marshal(Input{Message: "edit"})
	return e.svc.Start(context.Background(), StartParams{
		Agent: credits.EditorAgent, WorkspaceID: e.ws.ID, ProjectID: project.UUID, Input: input, Budgets: b, NoCredits: noCredits,
	})
}

func (e *env) counts() (runs, holds int) {
	e.t.Helper()
	if err := e.st.Pool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM agent_runs), (SELECT count(*) FROM credit_holds)`).Scan(&runs, &holds); err != nil {
		e.t.Fatal(err)
	}
	return runs, holds
}

func TestAnEditorRunIsNeverStartedWithoutTheCreditsService(t *testing.T) {
	e := newEnv(t, newScripted(), withBilledAgent)
	project := e.project()
	// No Credits on the service: the editor refuses, even with a budget and even with NoCredits set.
	for _, noCredits := range []bool{false, true} {
		if _, err := e.startEdit(project, Budgets{Micros: 5_000_000}, noCredits); !errors.Is(err, ErrNoCredits) {
			t.Fatalf("noCredits=%v: %v", noCredits, err)
		}
	}
	if runs, holds := e.counts(); runs != 0 || holds != 0 {
		t.Fatalf("%d runs and %d holds after refusals", runs, holds)
	}
	// An agent that costs nothing starts as it always did.
	if _, err := e.svc.Start(context.Background(), StartParams{Agent: "echo", WorkspaceID: e.ws.ID, Input: json.RawMessage(`{"message":"hi"}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestAnEditorRunNeedsAProject(t *testing.T) {
	e := newEnv(t, newScripted(), withBilledAgent)
	e.svc.Credits = creditstest.Service(t, e.st)
	if _, err := e.startEdit(ids.ProjectID{}, Budgets{}, false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an editor run with no project: %v", err)
	}
}

func TestAnEditorRunHoldsItsCreditsAndTakesItsBudgetFromThePlan(t *testing.T) {
	e := newEnv(t, newScripted(), withBilledAgent)
	e.svc.Credits = creditstest.Service(t, e.st)
	creditstest.Fund(t, e.st, e.ws.ID, plans.Creator, 7)
	project := e.project()

	// The caller asks for $5; the plan's budget for a first edit is what the run gets.
	run, err := e.startEdit(project, Budgets{Micros: 5_000_000}, false)
	if err != nil {
		t.Fatal(err)
	}
	if run.BudgetMicros != 960_000 {
		t.Fatalf("budget = %d, want the creator plan's 960,000", run.BudgetMicros)
	}
	h, err := e.st.GetCreditHold(context.Background(), e.ws.ID, run.ID)
	if err != nil || h.Credits != 6 || h.Action != store.ActionEditFirst || h.Project != project || h.Plan != "creator" || h.ListVersion != "2026-10" {
		t.Fatalf("hold = %+v, %v", h, err)
	}
	if !h.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Fatalf("a queued run's hold expires at %v, too soon for a busy queue", h.ExpiresAt)
	}
	// 7 - 6 = 1 left: the next start is refused by the hold, no run and no hold is created, and the refusal is the error the API maps to 402.
	other := e.project()
	if _, err := e.startEdit(other, Budgets{}, false); !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("a second first edit with 1 credit: %v", err)
	}
	if runs, holds := e.counts(); runs != 1 || holds != 1 {
		t.Fatalf("%d runs and %d holds after a refused start, want 1 and 1", runs, holds)
	}
}

func TestAnEditorRunWithoutCreditsKeepsItsBudgetAndStillHasAHold(t *testing.T) {
	e := newEnv(t, newScripted(), withBilledAgent)
	e.svc.Credits = creditstest.Service(t, e.st)
	project := e.project()
	// No plan, no credits: refused, unless the run is free.
	if _, err := e.startEdit(project, Budgets{Micros: 123_000}, false); !errors.Is(err, credits.ErrNoPlan) {
		t.Fatalf("no plan: %v", err)
	}
	run, err := e.startEdit(project, Budgets{Micros: 123_000}, true)
	if err != nil || run.BudgetMicros != 123_000 {
		t.Fatalf("free run: %+v, %v", run, err)
	}
	h, err := e.st.GetCreditHold(context.Background(), e.ws.ID, run.ID)
	if err != nil || h.Credits != 0 {
		t.Fatalf("the free run's hold = %+v, %v", h, err)
	}
}

// The end of the run settles the hold: the loop's own EndAgentRun, not a test helper.
func TestARunThatFailsReleasesAndOneThatSucceedsCharges(t *testing.T) {
	e := newEnv(t, newScripted(), withBilledAgent)
	e.svc.Credits = creditstest.Service(t, e.st)
	creditstest.Fund(t, e.st, e.ws.ID, plans.Creator, 20)
	project := e.project()
	ctx := context.Background()
	avail := func() int {
		b, err := e.st.CreditBalance(ctx, e.ws.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return b.Available
	}
	end := func(run store.AgentRun, status, reason string) {
		t.Helper()
		c, err := e.st.ClaimAgentRun(ctx, e.ws.ID, run.ID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.EndAgentRun(ctx, e.ws.ID, run.ID, c.Epoch, store.RunEnd{Status: status, FailureReason: reason, Event: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	failed, err := e.startEdit(project, Budgets{}, false)
	if err != nil || avail() != 14 {
		t.Fatalf("start: %v, available %d", err, avail())
	}
	end(failed, store.AgentFailed, FailBudget)
	if avail() != 20 {
		t.Fatalf("a failed run cost %d credits", 20-avail())
	}
	ok, err := e.startEdit(project, Budgets{}, false)
	if err != nil || ok.BudgetMicros != 960_000 {
		t.Fatalf("the run after a failure is still the first edit: %+v, %v", ok, err)
	}
	end(ok, store.AgentSucceeded, "")
	if avail() != 14 {
		t.Fatalf("a successful first edit left %d credits, want 14", avail())
	}
	rev, err := e.startEdit(project, Budgets{}, false)
	if err != nil || rev.BudgetMicros != 320_000 || avail() != 12 {
		t.Fatalf("revision: %+v, %v, available %d", rev, err, avail())
	}
	// Cancelling it before it rendered anything releases it.
	if _, err := e.svc.Cancel(ctx, e.ws.ID, rev.ID); err != nil {
		t.Fatal(err)
	}
	if avail() != 14 {
		t.Fatalf("a cancelled revision with no preview left %d, want 14", avail())
	}
}
