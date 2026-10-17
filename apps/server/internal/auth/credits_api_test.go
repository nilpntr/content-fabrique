package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/credits/creditstest"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Credits through the whole stack (P7-01a): the balance in GET /v1/me, the 402 credits_exhausted of POST /v1/projects/{id}/edit, the
// hold that starts with a run and is charged or released when it ends.

func (e *assetsEnv) me(who string) api.MeResponse {
	e.t.Helper()
	var me api.MeResponse
	e.call(http.MethodGet, "/v1/me", who, nil, http.StatusOK, &me)
	return me
}

// endRun finishes a run the way the worker would: claim it and end it with status.
func (e *assetsEnv) endRun(who string, run api.Run, status string) {
	e.t.Helper()
	ctx := context.Background()
	id, err := ids.Decode(ids.PrefixRun, run.Id)
	if err != nil {
		e.t.Fatal(err)
	}
	ws := e.ws[who]
	c, err := e.st.ClaimAgentRun(ctx, ws, id, time.Minute)
	if err != nil {
		e.t.Fatal(err)
	}
	end := store.RunEnd{Status: status, Event: json.RawMessage(`{"state":"` + status + `"}`)}
	if status == store.AgentFailed {
		end.FailureReason = "budget"
	}
	if _, err := e.st.EndAgentRun(ctx, ws, id, c.Epoch, end); err != nil {
		e.t.Fatal(err)
	}
}

func TestMeCarriesTheCredits(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true })
	ctx := context.Background()
	svc := creditstest.Service(t, e.st)
	if res, err := svc.SyncAllowance(ctx, e.ws["ada"]); err != nil || res.Granted != 150 {
		t.Fatalf("allowance: %+v, %v", res, err)
	}
	me := e.me("ada")
	c := me.Credits
	if c == nil || c.Available != 250 || c.Allowance != 150 || c.Low || c.PeriodEnd == nil || len(c.TopUps) != 1 || c.TopUps[0].Credits != 100 || !c.TopUps[0].ExpiresAt.After(time.Now().AddDate(0, 11, 0)) {
		t.Fatalf("ada's credits = %+v", c)
	}
	// Each workspace has its own: bob's allowance was never granted.
	if c := e.me("bob").Credits; c == nil || c.Available != 100 || c.Allowance != 0 || c.PeriodEnd != nil || c.Low || len(c.TopUps) != 1 {
		t.Fatalf("bob's credits = %+v", c)
	}
	// Members of ada's workspace see ada's.
	if c := e.me("viewer").Credits; c == nil || c.Available != 250 {
		t.Fatalf("the viewer's credits = %+v", c)
	}

	// A run in flight is already taken out; low is "at least 80 % of the allowance used".
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	run := e.startEdit("ada", p, nil)
	if c := e.me("ada").Credits; c.Available != 244 {
		t.Fatalf("with a run under way: %+v", c)
	}
	e.call(http.MethodPost, runPath(run, "/cancel"), "ada", nil, http.StatusOK, nil)
	if c := e.me("ada").Credits; c.Available != 250 {
		t.Fatalf("after the cancel (no preview, so released): %+v", c)
	}
	adjust := func(n int) {
		t.Helper()
		if _, err := e.st.AdjustCredits(ctx, store.AdjustCreditsParams{Workspace: e.ws["ada"], Credits: -n, Actor: "test", Reason: "test", Now: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	adjust(250 - 31) // the allowance goes first, then the top-up: 31 available, 150 allowance
	if c := e.me("ada").Credits; c.Available != 31 || c.Low {
		t.Fatalf("31 of 150: %+v", c)
	}
	adjust(1)
	if c := e.me("ada").Credits; c.Available != 30 || !c.Low {
		t.Fatalf("30 of 150 is 80 percent used: %+v", c)
	}
	// A workspace with no credit account at all gets zeros, not a missing field.
	if c := e.me("bob").Credits; c == nil || c.TopUps == nil {
		t.Fatalf("bob: %+v", c)
	}
}

// With fewer credits than the action costs, the start of a run is refused with the stable code, and nothing is created.
func TestStartEditWithoutEnoughCredits(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true; o.credits = 5 }) // a first edit costs 6
	ctx := context.Background()
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	rec := e.do(http.MethodPost, editPath(p), "ada", nil)
	wantError(t, rec, http.StatusPaymentRequired, string(api.ErrorCodeCreditsExhausted))
	var runs, holds int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_runs), (SELECT count(*) FROM credit_holds)`).Scan(&runs, &holds); err != nil || runs != 0 || holds != 0 {
		t.Fatalf("%d runs and %d holds after a refused start (%v)", runs, holds, err)
	}
	if n := e.agentRunJobsTotal(); n != 0 {
		t.Fatalf("%d agent_run jobs after a refused start", n)
	}
	// The message names no numbers and nothing of the request.
	var body api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != api.ErrorCodeCreditsExhausted || body.Error.Message == "" {
		t.Fatalf("body = %s", rec.Body)
	}
	// One more credit and it starts.
	creditstest.Fund(t, e.st, e.ws["ada"], "creator", 1)
	e.startEdit("ada", p, nil)

	// A workspace without a plan is stopped by the paywall before credits come into it.
	bp := e.newProjectAs("bob", "9:16")
	if _, err := e.st.RevokePlan(ctx, store.RevokeInput{WorkspaceID: e.ws["bob"], GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do(http.MethodPost, editPath(bp), "bob", nil), http.StatusPaymentRequired, string(CodePlanRequired))
}

func (e *assetsEnv) agentRunJobsTotal() int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'agent_run'`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// newProjectAs makes a project for a workspace owner with a ready clip.
func (e *assetsEnv) newProjectAs(who, format string) api.Project {
	e.t.Helper()
	return e.newProject(who, format, e.readyAsset(who, "clip.mp4"))
}

// The first edit costs 6 and a revision 2, decided by the server from what the project has had charged; the run's budget is the plan's
// for that action; a run that fails costs nothing.
func TestStartEditChargesFirstThenRevision(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true; o.credits = 20 })
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	other := e.newProject("ada", "9:16", e.readyAsset("ada", "other.mp4"))
	avail := func() int { return e.me("ada").Credits.Available }

	first := e.startEdit("ada", p, nil)
	if first.BudgetMicros != 960_000 || avail() != 14 {
		t.Fatalf("first edit: budget %d, available %d", first.BudgetMicros, avail())
	}
	// The hold is attributed to the project, the member who started it and the plan and list that priced it.
	runID, _ := ids.Decode(ids.PrefixRun, first.Id)
	var holdProject, holdMember *string
	var holdCredits int
	var holdAction, holdPlan, holdVersion string
	if err := e.pool.QueryRow(context.Background(), `SELECT credits, action, project_id::text, member_user_id::text, plan, plan_list_version FROM credit_holds WHERE action_id = $1`, runID).
		Scan(&holdCredits, &holdAction, &holdProject, &holdMember, &holdPlan, &holdVersion); err != nil {
		t.Fatal(err)
	}
	pid, _ := ids.ParseProjectID(p.Id)
	if holdCredits != 6 || holdAction != "edit.first" || holdProject == nil || *holdProject != pid.UUID.String() || holdMember == nil || holdPlan != "creator" || holdVersion != "2026-10" {
		t.Fatalf("hold = %d %s project %v member %v plan %s list %s", holdCredits, holdAction, holdProject, holdMember, holdPlan, holdVersion)
	}
	e.endRun("ada", first, store.AgentFailed)
	if avail() != 20 {
		t.Fatalf("a failed first edit cost %d", 20-avail())
	}
	again := e.startEdit("ada", p, nil) // still the first edit: nothing was charged
	if again.BudgetMicros != 960_000 {
		t.Fatalf("after a failed first edit the next run has budget %d", again.BudgetMicros)
	}
	e.endRun("ada", again, store.AgentSucceeded)
	if avail() != 14 {
		t.Fatalf("a successful first edit left %d, want 14", avail())
	}

	rev := e.startEdit("ada", p, map[string]any{"budget_usd": 4})
	if rev.BudgetMicros != 320_000 || avail() != 12 {
		t.Fatalf("revision: budget %d, available %d", rev.BudgetMicros, avail())
	}
	e.endRun("ada", rev, store.AgentSucceeded)
	if avail() != 12 {
		t.Fatalf("after the revision: %d", avail())
	}
	// Another project is its own first edit.
	if o := e.startEdit("ada", other, nil); o.BudgetMicros != 960_000 || avail() != 6 {
		t.Fatalf("other project: budget %d, available %d", o.BudgetMicros, avail())
	}

	// bob's credits were never touched.
	if c := e.me("bob").Credits; c.Available != 20 {
		t.Fatalf("bob's credits = %+v", c)
	}
}

// A workspace on the BYOK plan (studio) cannot start a model run until its own key can pay for it: 402 byok_key_required, and no run, no
// hold. A managed workspace is not affected.
func TestStartEditOnAByokPlanNeedsTheWorkspacesOwnKey(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true })
	ctx := context.Background()
	if _, err := e.st.GrantPlan(ctx, store.GrantInput{WorkspaceID: e.ws["bob"], Plan: "studio", GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	bp := e.newProjectAs("bob", "9:16")
	rec := e.do(http.MethodPost, editPath(bp), "bob", nil)
	wantError(t, rec, http.StatusPaymentRequired, string(api.ErrorCodeByokKeyRequired))
	var body api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Message != "this plan uses your own Anthropic key; add it in settings" {
		t.Fatalf("body = %s", rec.Body)
	}
	var runs, holds int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_runs WHERE workspace_id = $1), (SELECT count(*) FROM credit_holds WHERE workspace_id = $1)`, e.ws["bob"]).Scan(&runs, &holds); err != nil || runs != 0 || holds != 0 {
		t.Fatalf("%d runs and %d holds after the refusal (%v)", runs, holds, err)
	}
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	e.startEdit("ada", p, nil)
}

// A workspace that keeps starting runs and cancelling them before they make anything burns model money for nothing: after the day's cap
// the next start is refused with 429 too_many_uncharged_runs. Runs that cost nothing do not count.
func TestStartEditIsRefusedAfterTheDaysUnchargedRuns(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true; o.credits = 200 })
	ctx := context.Background()
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	spendAndCancel := func(cost int64) {
		t.Helper()
		run := e.startEdit("ada", p, nil)
		id, _ := ids.Decode(ids.PrefixRun, run.Id)
		if _, err := e.pool.Exec(ctx, `UPDATE agent_runs SET cost_micros = $2 WHERE id = $1`, id, cost); err != nil {
			t.Fatal(err)
		}
		e.call(http.MethodPost, runPath(run, "/cancel"), "ada", nil, http.StatusOK, nil)
	}
	for range 3 { // runs cancelled in the queue cost nothing and do not count
		spendAndCancel(0)
	}
	for range 5 {
		spendAndCancel(25_000)
	}
	rec := e.do(http.MethodPost, editPath(p), "ada", nil)
	wantError(t, rec, http.StatusTooManyRequests, string(api.ErrorCodeTooManyUnchargedRuns))
	var body api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !strings.Contains(body.Error.Message, "5 editor runs today") {
		t.Fatalf("body = %s", rec.Body)
	}
	var runs int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE status = 'queued'`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("%d queued runs after the refusal (%v)", runs, err)
	}
	// Another workspace is not affected.
	bp := e.newProjectAs("bob", "9:16")
	e.startEdit("bob", bp, nil)
}
