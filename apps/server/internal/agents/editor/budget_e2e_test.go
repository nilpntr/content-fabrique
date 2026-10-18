package editor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The editor on the plan's real budgets (P7-01a), on the production profile (Opus 5.5), over the fake transport: a call's max_tokens is cut
// to what the run's budget can still pay for, the budget stays a hard cap, and a run that runs out of budget finishes on its newest
// previewed version instead of failing.

// budgetRun runs the scripted editor on v1-start with the plan's budget. scale makes the scripted turns write what a long max-effort Opus turn does (output tokens times scale).
func budgetRun(t *testing.T, revision bool, scale int) (*e2e, *fakeAnthropic) {
	t.Helper()
	api := &fakeAnthropic{scale: scale}
	vars := map[string]string{"LLM_MODE": "record", "LLM_CASSETTE_DIR": t.TempDir(), "ANTHROPIC_API_KEY": "sk-ant-api03-FAKE-not-a-real-key-0123456789"}
	x := newE2E(t, vars, api, func(c *e2eConfig) { c.profile, c.planBudgets, c.charged = "v1-start", true, revision })
	x.execute()
	return x, api
}

func (x *e2e) artifacts(kind string) int {
	x.t.Helper()
	evs, err := x.st.AgentArtifactEvents(context.Background(), x.run.WorkspaceID, x.run.ID, kind)
	if err != nil {
		x.t.Fatal(err)
	}
	return len(evs)
}

func maxTokensOf(api *fakeAnthropic) []int {
	var out []int
	for _, r := range api.requests {
		mt, _ := r["max_tokens"].(float64)
		out = append(out, int(mt))
	}
	return out
}

func outputOf(t *testing.T, r store.AgentRun) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Output, &m); err != nil {
		t.Fatalf("output %s: %v", r.Output, err)
	}
	return m
}

// A $2.00 first edit on Opus whose turns are long (each costs what an Opus turn does): the calls shrink as the money runs low, the run's
// cost never passes the budget, and the run ends succeeded, on its newest previewed version, not failed at the budget.
func TestFirstEditOnOpusShrinksCallsAndFinishesAtTheBudget(t *testing.T) {
	x, api := budgetRun(t, false, 165)
	r := x.get()
	if r.BudgetMicros != 2_000_000 || r.MaxTokens != 32_000 || r.Model != "claude-opus-5-5" {
		t.Fatalf("run: budget %d, max_tokens %d, model %s", r.BudgetMicros, r.MaxTokens, r.Model)
	}
	if r.Status != store.AgentSucceeded {
		t.Fatalf("the run %s (%v): %s; it must end gracefully at the budget, not fail", r.Status, r.FailureReason, r.Output)
	}
	if r.CostMicros <= 0 || r.CostMicros > r.BudgetMicros {
		t.Fatalf("cost %d against a budget of %d", r.CostMicros, r.BudgetMicros)
	}
	sent := maxTokensOf(api)
	if sent[0] != 32_000 {
		t.Errorf("the first call was cut to %d although the whole budget was there", sent[0])
	}
	var shortened int
	for i, mt := range sent {
		if mt < 32_000 {
			shortened++
			if mt < 2048 {
				t.Errorf("call %d was cut to %d, under the minimum", i+1, mt)
			}
		}
	}
	if shortened == 0 {
		t.Fatalf("no call was shortened: max_tokens sent %v, run cost %d of %d", sent, r.CostMicros, r.BudgetMicros)
	}
	// It reached the budget and finished on a previewed version.
	out := outputOf(t, r)
	if out["limit"] != "dollars" || out["implicit_finish"] != true {
		t.Errorf("the run did not end at the budget: %s", r.Output)
	}
	if x.artifacts(PreviewArtifact) == 0 || x.artifacts(PlanArtifact) == 0 {
		t.Errorf("previews %d, plans %d", x.artifacts(PreviewArtifact), x.artifacts(PlanArtifact))
	}
	t.Logf("max_tokens sent: %v; cost %d of %d; %d calls", sent, r.CostMicros, r.BudgetMicros, len(sent))
}

// A $0.80 revision on Opus (the `revision` entry: 16,000 tokens, half of what the editor entry reserves): it makes the plan,
// the edit and a check, the calls are cut to fit, and the cap holds.
func TestRevisionOnOpusCompletesAPlanAnEditAndACheck(t *testing.T) {
	x, api := budgetRun(t, true, 25)
	r := x.get()
	if r.BudgetMicros != 800_000 || r.MaxTokens != 16_000 || r.Model != "claude-opus-5-5" || r.Stage != "editor" {
		t.Fatalf("run: budget %d, max_tokens %d, model %s, stage %s; want the revision entry under the editor stage", r.BudgetMicros, r.MaxTokens, r.Model, r.Stage)
	}
	if r.Status != store.AgentSucceeded {
		t.Fatalf("the run %s (%v): %s", r.Status, r.FailureReason, r.Output)
	}
	if r.CostMicros > r.BudgetMicros {
		t.Fatalf("cost %d against a budget of %d", r.CostMicros, r.BudgetMicros)
	}
	sent := maxTokensOf(api)
	if sent[0] > 16_000 {
		t.Errorf("the first call asked for %d tokens, over the revision entry's 16,000", sent[0])
	}
	shortened := false
	for _, mt := range sent {
		shortened = shortened || mt < 16_000
	}
	if !shortened {
		t.Errorf("no call was shortened: %v", sent)
	}
	if x.artifacts(PlanArtifact) == 0 || x.artifacts(CheckArtifact) == 0 {
		t.Fatalf("plans %d, checks %d: the revision did not get to a plan and a check", x.artifacts(PlanArtifact), x.artifacts(CheckArtifact))
	}
	made, err := x.st.GetTimelineVersionByAuthor(context.Background(), x.run.WorkspaceID, x.prj, "agent", ids.Encode(ids.PrefixRun, x.run.ID))
	if err != nil || made.Version < 1 {
		t.Fatalf("the revision made no version: %v", err)
	}
	t.Logf("max_tokens sent: %v; cost %d of %d; %d calls", sent, r.CostMicros, r.BudgetMicros, len(sent))
}

// A budget that cannot hold the smallest call: the client refuses with the run-budget error, and the run ends at its budget gracefully
// on the version it had previewed, rather than failing. The scripted turns are so long that the money is gone after the first preview
// and before the revision pass is done; the test insists on that (a run that never reached a preview, or that finished all its ten
// turns, would pass the old checks without exercising the graceful finish at all).
func TestBudgetBelowTheMinimumEndsGracefully(t *testing.T) {
	x, api := budgetRun(t, false, 140)
	r := x.get()
	sent := maxTokensOf(api)
	t.Logf("max_tokens sent: %v; cost %d of %d; status %s", sent, r.CostMicros, r.BudgetMicros, r.Status)
	if x.artifacts(PreviewArtifact) == 0 {
		t.Fatalf("the run never previewed a version, so it cannot show the graceful finish: max_tokens sent %v", sent)
	}
	if len(sent) >= len(turnUsage) {
		t.Fatalf("the run made all %d scripted calls: the money did not run out (max_tokens sent %v)", len(sent), sent)
	}
	if r.Status != store.AgentSucceeded {
		t.Fatalf("the run %s (%v) although it had previewed a version: %s", r.Status, r.FailureReason, r.Output)
	}
	if r.FailureReason != nil {
		t.Fatalf("a graceful finish carries a failure reason %q", *r.FailureReason)
	}
	out := outputOf(t, r)
	if out["limit"] != "dollars" || out["implicit_finish"] != true {
		t.Fatalf("the run did not end at the budget on its previewed version: %s", r.Output)
	}
	if r.CostMicros > r.BudgetMicros {
		t.Fatalf("cost %d against a budget of %d", r.CostMicros, r.BudgetMicros)
	}
	for i, mt := range sent {
		if mt < 2048 {
			t.Errorf("call %d was sent with %d tokens, under the minimum that is refused instead", i+1, mt)
		}
	}
}
