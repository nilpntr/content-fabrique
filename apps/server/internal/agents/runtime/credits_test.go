package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
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
	a.Name, a.Stage = credits.EditorAgent, "editor"
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
	creditstest.Fund(t, e.st, e.ws.ID, plans.Creator, 17)
	project := e.project()

	// The caller asks for $5; the plan's budget for a first edit is what the run gets.
	run, err := e.startEdit(project, Budgets{Micros: 5_000_000}, false)
	if err != nil {
		t.Fatal(err)
	}
	if run.BudgetMicros != 2_000_000 {
		t.Fatalf("budget = %d, want the creator plan's 2,000,000", run.BudgetMicros)
	}
	h, err := e.st.GetCreditHold(context.Background(), e.ws.ID, run.ID)
	if err != nil || h.Credits != 16 || h.Action != store.ActionEditFirst || h.Project != project || h.Plan != "creator" || h.ListVersion != "2026-10c" {
		t.Fatalf("hold = %+v, %v", h, err)
	}
	if !h.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Fatalf("a queued run's hold expires at %v, too soon for a busy queue", h.ExpiresAt)
	}
	// 17 - 16 = 1 left: the next start is refused by the hold, no run and no hold is created, and the refusal is the error the API maps to 402.
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
	creditstest.Fund(t, e.st, e.ws.ID, plans.Creator, 60)
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
	if err != nil || avail() != 44 {
		t.Fatalf("start: %v, available %d", err, avail())
	}
	end(failed, store.AgentFailed, FailBudget)
	if avail() != 60 {
		t.Fatalf("a failed run cost %d credits", 60-avail())
	}
	ok, err := e.startEdit(project, Budgets{}, false)
	if err != nil || ok.BudgetMicros != 2_000_000 {
		t.Fatalf("the run after a failure is still the first edit: %+v, %v", ok, err)
	}
	end(ok, store.AgentSucceeded, "")
	if avail() != 44 {
		t.Fatalf("a successful first edit left %d credits, want 44", avail())
	}
	rev, err := e.startEdit(project, Budgets{}, false)
	if err != nil || rev.BudgetMicros != 800_000 || avail() != 37 {
		t.Fatalf("revision: %+v, %v, available %d", rev, err, avail())
	}
	// Cancelling it before it rendered anything releases it.
	if _, err := e.svc.Cancel(ctx, e.ws.ID, rev.ID); err != nil {
		t.Fatal(err)
	}
	if avail() != 44 {
		t.Fatalf("a cancelled revision with no preview left %d, want 44", avail())
	}
}

// A revision runs on the profile's `revision` entry (a smaller max_tokens on the production profile), under the agent's own stage; a first
// edit on the `editor` entry. The routing entry can set the smallest max_tokens a call may be shortened to.
func TestARevisionRunsOnTheRevisionEntry(t *testing.T) {
	e := newEnv(t, newScripted(), withBilledAgent)
	e.svc.Credits = creditstest.Service(t, e.st)
	creditstest.Fund(t, e.st, e.ws.ID, plans.Creator, 50)
	project := e.project()
	ctx := context.Background()
	start := func() store.AgentRun {
		t.Helper()
		input, _ := json.Marshal(Input{Message: "edit"})
		run, err := e.svc.Start(ctx, StartParams{Agent: credits.EditorAgent, WorkspaceID: e.ws.ID, ProjectID: project.UUID, Input: input, Profile: "v1-start"})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	first := start()
	if first.MaxTokens != 32_000 || first.Effort != "max" || first.Model != "claude-opus-5-5" || first.BudgetMicros != 2_000_000 {
		t.Fatalf("first edit: max_tokens %d, effort %s, model %s, budget %d", first.MaxTokens, first.Effort, first.Model, first.BudgetMicros)
	}
	c, err := e.st.ClaimAgentRun(ctx, e.ws.ID, first.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.EndAgentRun(ctx, e.ws.ID, first.ID, c.Epoch, store.RunEnd{Status: store.AgentSucceeded, Event: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	rev := start()
	if rev.MaxTokens != 16_000 || rev.Effort != "max" || rev.Model != "claude-opus-5-5" || rev.BudgetMicros != 800_000 || rev.Stage != first.Stage {
		t.Fatalf("revision: max_tokens %d, effort %s, model %s, budget %d, stage %s (first edit's stage %s)", rev.MaxTokens, rev.Effort, rev.Model, rev.BudgetMicros, rev.Stage, first.Stage)
	}
	if rev.MinOutputTokens != 0 {
		t.Errorf("min_output_tokens = %d for an entry that does not set one", rev.MinOutputTokens)
	}
}

// workspaceKeyFactory bills every call to the workspace's own key, as the key resolver of P4-00b will for a BYOK workspace.
type workspaceKeyFactory struct{ fakeFactory }

func (f workspaceKeyFactory) For(context.Context, ids.WorkspaceID, string) (llm.Client, llm.Billing, error) {
	return f.client, llm.BilledToWorkspaceKey, nil
}

// A BYOK plan (studio) is priced as if the creator's key paid for the model. Until a workspace's own key can be used, the factory bills
// ours, and the run is refused: no run, no hold. With the creator's key it starts.
func TestAByokRunOnOurKeyIsRefused(t *testing.T) {
	e := newEnv(t, newScripted(), withBilledAgent)
	e.svc.Credits = creditstest.Service(t, e.st)
	creditstest.Fund(t, e.st, e.ws.ID, plans.Studio, 50)
	project := e.project()
	if _, err := e.startEdit(project, Budgets{}, false); !errors.Is(err, ErrBYOKKeyRequired) {
		t.Fatalf("a studio run billed to us: %v", err)
	}
	if runs, holds := e.counts(); runs != 0 || holds != 0 {
		t.Fatalf("%d runs and %d holds after the refusal", runs, holds)
	}
	// With the workspace's own key it starts, under the plan's run budget.
	e.svc.Factory = workspaceKeyFactory{fakeFactory{client: newScripted()}}
	run, err := e.startEdit(project, Budgets{}, false)
	if err != nil || run.BilledTo != string(llm.BilledToWorkspaceKey) || run.BudgetMicros != 2_000_000 {
		t.Fatalf("a studio run on the creator's key: %+v, %v", run, err)
	}
	// A managed plan on the platform's (here test) key is not affected, and a free development run on a studio workspace is not either.
	other := newEnv(t, newScripted(), withBilledAgent)
	other.svc.Credits = creditstest.Service(t, other.st)
	creditstest.Fund(t, other.st, other.ws.ID, plans.Creator, 50)
	if _, err := other.startEdit(other.project(), Budgets{}, false); err != nil {
		t.Fatalf("a creator run: %v", err)
	}
	if _, err := e.startEdit(e.project(), Budgets{Micros: 100_000}, true); err != nil {
		t.Fatalf("a free run on a studio workspace: %v", err)
	}
}

// The credits package and the llm layer name the creator's own key the same way: a BYOK run is refused unless the factory says this.
func TestRequiredBillingIsTheLlmWorkspaceKey(t *testing.T) {
	if credits.RequiredBilling(credits.BYOK) != string(llm.BilledToWorkspaceKey) || credits.BilledToWorkspaceKey != string(llm.BilledToWorkspaceKey) {
		t.Fatalf("credits says %q, llm says %q", credits.RequiredBilling(credits.BYOK), llm.BilledToWorkspaceKey)
	}
}

// The failure reasons that count toward the cap on runs that end without a charge are the runtime's own names, and none of them is one of
// the failures that are ours.
func TestUnchargedFailureReasonsAreRuntimeReasons(t *testing.T) {
	known := map[string]bool{
		FailRefusal: true, FailContextWindow: true, FailBadInput: true, FailContext: true,
	}
	for _, r := range credits.UnchargedFailureReasons {
		if !known[r] {
			t.Errorf("%q is not the failure reason of a creator's own failure", r)
		}
		delete(known, r)
	}
	for r := range known {
		t.Errorf("%q should count and does not", r)
	}
	ours := []string{FailBudget, FailModelError, FailModelAccess, FailModelOutcomeUnknown, FailMaxTokens, FailEventTooLarge, FailInternal, FailLedger, FailCorruptLog,
		FailUnknownAgent, FailTooManyResumes, FailStopReason, FailRequestCap, FailReplayMiss, FailDailyCap, FailPlatformSpendPaused, FailSpendCapBusy}
	for _, r := range ours {
		for _, c := range credits.UnchargedFailureReasons {
			if r == c {
				t.Errorf("%q is our failure and counts toward the cap", r)
			}
		}
	}
}
