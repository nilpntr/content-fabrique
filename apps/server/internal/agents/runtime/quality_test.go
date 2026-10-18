package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/credits/creditstest"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// An extra careful edit (quality max, D-31) runs on the profile's `editor_max` / `revision_max` entries (effort max, 32,000 and 16,000
// tokens on v1-start), with the budget and the credits of its own action; a standard edit is routed exactly as before (effort medium,
// 24,000 and 12,000 tokens). The trial cannot ask for it: nothing is created or held.
func TestAMaxEditRunsOnTheMaxEntries(t *testing.T) {
	e := newEnv(t, newScripted(), withBilledAgent)
	e.svc.Credits = creditstest.Service(t, e.st)
	creditstest.Fund(t, e.st, e.ws.ID, plans.Creator, 100)
	project := e.project()
	ctx := context.Background()
	start := func(quality credits.Quality) store.AgentRun {
		t.Helper()
		input, _ := json.Marshal(Input{Message: "edit"})
		run, err := e.svc.Start(ctx, StartParams{Agent: credits.EditorAgent, WorkspaceID: e.ws.ID, ProjectID: project.UUID, Input: input, Profile: "v1-start", Quality: quality})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	end := func(run store.AgentRun) {
		t.Helper()
		c, err := e.st.ClaimAgentRun(ctx, e.ws.ID, run.ID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.EndAgentRun(ctx, e.ws.ID, run.ID, c.Epoch, store.RunEnd{Status: store.AgentSucceeded, Event: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}

	first := start(credits.Max)
	if first.Effort != "max" || first.MaxTokens != 32_000 || first.Model != "claude-opus-5-5" || first.BudgetMicros != 2_000_000 || first.Stage != "editor" {
		t.Fatalf("max first edit: effort %s, max_tokens %d, model %s, budget %d, stage %s", first.Effort, first.MaxTokens, first.Model, first.BudgetMicros, first.Stage)
	}
	h, err := e.st.GetCreditHold(ctx, e.ws.ID, first.ID)
	if err != nil || h.Action != store.ActionEditFirstMax || h.Credits != 16 || h.ListVersion != "2026-10c" {
		t.Fatalf("hold = %+v, %v", h, err)
	}
	end(first)

	rev := start(credits.Max)
	if rev.Effort != "max" || rev.MaxTokens != 16_000 || rev.BudgetMicros != 800_000 || rev.Stage != "editor" {
		t.Fatalf("max revision: effort %s, max_tokens %d, budget %d, stage %s", rev.Effort, rev.MaxTokens, rev.BudgetMicros, rev.Stage)
	}
	if h, err := e.st.GetCreditHold(ctx, e.ws.ID, rev.ID); err != nil || h.Action != store.ActionEditRevisionMax || h.Credits != 7 {
		t.Fatalf("revision hold = %+v, %v", h, err)
	}
	end(rev)

	// A standard edit (the default, also when the quality is empty) is exactly what it was.
	std := start("")
	if std.Effort != "medium" || std.MaxTokens != 12_000 || std.BudgetMicros != 320_000 {
		t.Fatalf("standard revision: effort %s, max_tokens %d, budget %d", std.Effort, std.MaxTokens, std.BudgetMicros)
	}
	if h, err := e.st.GetCreditHold(ctx, e.ws.ID, std.ID); err != nil || h.Action != store.ActionEditRevision || h.Credits != 2 {
		t.Fatalf("standard hold = %+v, %v", h, err)
	}
	end(std)
	other := e.project()
	input, _ := json.Marshal(Input{Message: "edit"})
	stdFirst, err := e.svc.Start(ctx, StartParams{Agent: credits.EditorAgent, WorkspaceID: e.ws.ID, ProjectID: other.UUID, Input: input, Profile: "v1-start", Quality: credits.Standard})
	if err != nil || stdFirst.Effort != "medium" || stdFirst.MaxTokens != 24_000 || stdFirst.BudgetMicros != 960_000 {
		t.Fatalf("standard first edit: %+v, %v", stdFirst, err)
	}
}

func TestTheTrialCannotStartAMaxEdit(t *testing.T) {
	e := newEnv(t, newScripted(), withBilledAgent)
	e.svc.Credits = creditstest.Service(t, e.st)
	creditstest.Fund(t, e.st, e.ws.ID, plans.Trial, 100)
	input, _ := json.Marshal(Input{Message: "edit"})
	_, err := e.svc.Start(context.Background(), StartParams{Agent: credits.EditorAgent, WorkspaceID: e.ws.ID, ProjectID: e.project().UUID, Input: input, Quality: credits.Max})
	if !errors.Is(err, credits.ErrQualityNotInPlan) {
		t.Fatalf("a trial max edit: %v", err)
	}
	if runs, holds := e.counts(); runs != 0 || holds != 0 {
		t.Fatalf("%d runs and %d holds after the refusal", runs, holds)
	}
}

// A development profile has the max entries too, so a smoke test can take the path through the whole stack (and a profile that lacked one
// would refuse the edit, not run it as a standard one).
func TestEveryCheckedInProfileRoutesAMaxEdit(t *testing.T) {
	for _, profile := range []string{"dev-cheap", "dev-mid", "v1-start"} {
		e := newEnv(t, newScripted(), withBilledAgent)
		e.svc.Credits = creditstest.Service(t, e.st)
		creditstest.Fund(t, e.st, e.ws.ID, plans.Creator, 100)
		input, _ := json.Marshal(Input{Message: "edit"})
		run, err := e.svc.Start(context.Background(), StartParams{Agent: credits.EditorAgent, WorkspaceID: e.ws.ID, ProjectID: e.project().UUID, Input: input, Profile: profile, Quality: credits.Max})
		if err != nil || run.RoutingProfile != profile || run.BudgetMicros != 2_000_000 {
			t.Errorf("%s: %+v, %v", profile, run, err)
		}
	}
}
