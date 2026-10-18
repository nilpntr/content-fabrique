package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Quality is chosen per edit (POST /v1/projects/{id}/edit, field quality): standard by default, max for the extra careful edit. The hold
// and the run's budget are those of the action chosen, the run's credits say which action it was, and GET /v1/me has both prices.
func TestStartEditWithQuality(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true; o.credits = 100 })
	ctx := context.Background()
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	holdOf := func(run api.Run) (action string, credits int) {
		t.Helper()
		id, _ := ids.Decode(ids.PrefixRun, run.Id)
		if err := e.pool.QueryRow(ctx, `SELECT action, credits FROM credit_holds WHERE action_id = $1`, id).Scan(&action, &credits); err != nil {
			t.Fatal(err)
		}
		return action, credits
	}
	avail := func() int { return e.me("ada").Credits.Available }

	// Max: its own action, credits and budget; the run is routed on the max entry of the profile.
	first := e.startEdit("ada", p, map[string]any{"quality": "max", "profile": "v1-start"})
	if action, c := holdOf(first); action != "edit.first.max" || c != 16 || first.BudgetMicros != 2_000_000 || first.Effort != "max" {
		t.Fatalf("max first edit: hold %s %d, budget %d, effort %s", action, c, first.BudgetMicros, first.Effort)
	}
	if avail() != 84 {
		t.Fatalf("available while a max first edit is held = %d, want 84", avail())
	}
	e.endRun("ada", first, store.AgentSucceeded)
	var detail api.Run
	e.call(http.MethodGet, runPath(first, ""), "ada", nil, http.StatusOK, &detail)
	if c := detail.Credits; c == nil || c.Action != api.RunCreditsActionEditFirstMax || c.Credits != 16 || c.State != api.RunCreditsStateCharged {
		t.Fatalf("the run's credits = %+v", c)
	}

	// No quality is standard, and the project's next edit is a revision at the standard price.
	rev := e.startEdit("ada", p, nil)
	if action, c := holdOf(rev); action != "edit.revision" || c != 2 || rev.BudgetMicros != 320_000 {
		t.Fatalf("standard revision: hold %s %d, budget %d", action, c, rev.BudgetMicros)
	}
	e.endRun("ada", rev, store.AgentSucceeded)
	// An explicit standard is the same; a max revision has its own price.
	if r := e.startEdit("ada", p, map[string]any{"quality": "max"}); true {
		if action, c := holdOf(r); action != "edit.revision.max" || c != 7 || r.BudgetMicros != 800_000 {
			t.Fatalf("max revision: hold %s %d, budget %d", action, c, r.BudgetMicros)
		}
		e.endRun("ada", r, store.AgentFailed)
	}
	if r := e.startEdit("ada", p, map[string]any{"quality": "standard"}); true {
		if action, c := holdOf(r); action != "edit.revision" || c != 2 {
			t.Fatalf("explicit standard: hold %s %d", action, c)
		}
	}
}

func TestStartEditRefusesAnUnknownQuality(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true })
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	wantError(t, e.do(http.MethodPost, editPath(p), "ada", map[string]any{"quality": "ultra"}), http.StatusBadRequest, string(api.ErrorCodeInvalidRequest))
	var runs, holds int
	if err := e.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM agent_runs), (SELECT count(*) FROM credit_holds)`).Scan(&runs, &holds); err != nil || runs != 0 || holds != 0 {
		t.Fatalf("%d runs and %d holds after a refused start (%v)", runs, holds, err)
	}
}

// The trial does not include the extra careful edits: 402 quality_not_in_plan, a message that says so, and no run, no hold, no job. A
// standard edit on the same workspace starts.
func TestTheTrialGetsQualityNotInPlan(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true })
	ctx := context.Background()
	if _, err := e.st.GrantPlan(ctx, store.GrantInput{WorkspaceID: e.ws["bob"], Plan: "trial", GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	bp := e.newProjectAs("bob", "9:16")
	rec := e.do(http.MethodPost, editPath(bp), "bob", map[string]any{"quality": "max"})
	wantError(t, rec, http.StatusPaymentRequired, string(api.ErrorCodeQualityNotInPlan))
	var runs, holds int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_runs WHERE workspace_id = $1), (SELECT count(*) FROM credit_holds WHERE workspace_id = $1)`, e.ws["bob"]).Scan(&runs, &holds); err != nil || runs != 0 || holds != 0 {
		t.Fatalf("%d runs and %d holds after the refusal (%v)", runs, holds, err)
	}
	if n := e.agentRunJobsTotal(); n != 0 {
		t.Fatalf("%d agent_run jobs after a refused start", n)
	}
	e.startEdit("bob", bp, nil)
}

// GET /v1/me says what an edit costs at each quality, from the price list: standard 6 and 2, max 16 and 7, 10 credits to a standard video;
// the trial has no max price, and the web app shows exactly these.
func TestMeHasTheEditCostsPerQuality(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true })
	c := e.me("ada").Credits
	if c == nil || c.CreditsPerVideo != 10 || c.EditCosts.Standard != (api.EditCost{FirstEdit: 6, Revision: 2}) || c.EditCosts.Max == nil || *c.EditCosts.Max != (api.EditCost{FirstEdit: 16, Revision: 7}) {
		t.Fatalf("ada's edit costs = %+v", c)
	}
	if _, err := e.st.GrantPlan(context.Background(), store.GrantInput{WorkspaceID: e.ws["bob"], Plan: "trial", GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	b := e.me("bob").Credits
	if b == nil || b.EditCosts.Standard != (api.EditCost{FirstEdit: 6, Revision: 2}) || b.EditCosts.Max != nil {
		t.Fatalf("the trial's edit costs = %+v, want standard only", b)
	}
}
