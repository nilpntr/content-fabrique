package renderjobs

import (
	"context"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// export.final (P7-01a, spend control v1 sections 3 and 5): a final render costs no credits, but only an edit that was charged can be
// exported, and the export goes on the credit ledger. Previews have no such rule.

func (e *env) exportRows(t *testing.T, w store.Workspace) int {
	t.Helper()
	var n int
	err := e.super.QueryRow(context.Background(), `SELECT count(*) FROM credit_ledger WHERE workspace_id = $1 AND action = 'export.final' AND kind = 'charge' AND credits = 0`, w.ID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// uncharged is a workspace with a project that no editor run has been charged for, and a version of it.
func (e *env) uncharged(t *testing.T, org string) (store.Workspace, store.Project, int) {
	t.Helper()
	w := e.newWorkspace(t, org)
	p, v := e.unchargedIn(t, w)
	return w, p, v
}

func (e *env) unchargedIn(t *testing.T, w store.Workspace) (store.Project, int) {
	t.Helper()
	clips := testClips(t)
	a, b := e.readyAsset(t, w, "a", clips[0]), e.readyAsset(t, w, "b", clips[1])
	p := e.newProjectUncharged(t, w, a, b)
	return p, e.addVersion(t, w, p, shortSpec(a, b))
}

func TestFinalRenderNeedsAChargedEditAndIsRecorded(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w, p, v := e.uncharged(t, "org_export")

	// No charged edit: the final render is refused, nothing is queued and nothing is recorded; a preview is fine.
	_, _, rerr := e.svc.Request(ctx, e.caller(w), p.ID, v, "final")
	if rerr == nil || rerr.Status != 422 || rerr.Code != api.ErrorCodeEditNotCharged {
		t.Fatalf("a final render of an unedited project: %v", rerr)
	}
	if e.renderJobs(t) != 0 || e.exportRows(t, w) != 0 {
		t.Fatalf("a refused export left %d jobs and %d ledger rows", e.renderJobs(t), e.exportRows(t, w))
	}
	var renders int
	if err := e.super.QueryRow(ctx, `SELECT count(*) FROM renders WHERE workspace_id = $1`, w.ID).Scan(&renders); err != nil || renders != 0 {
		t.Fatalf("%d renders after a refused export (%v)", renders, err)
	}
	if r, created, err := e.svc.Request(ctx, e.caller(w), p.ID, v, "preview"); err != nil || !created || r.Profile != "preview" {
		t.Fatalf("a preview of an unedited project: %+v %v %v", r, created, err)
	}
	if e.exportRows(t, w) != 0 {
		t.Fatal("a preview was recorded as an export")
	}

	// An edit that was charged: the export goes through and is recorded once, at 0 credits.
	e.chargeEdit(t, w, p)
	fin, created := e.request(t, w, p, v, "final")
	if !created || e.exportRows(t, w) != 1 {
		t.Fatalf("export: created=%v, %d rows", created, e.exportRows(t, w))
	}
	// Asking again for the same render reuses it and records nothing more.
	if again, created := e.request(t, w, p, v, "final"); created || again.ID != fin.ID || e.exportRows(t, w) != 1 {
		t.Fatalf("again: created=%v id %s rows %d", created, again.ID, e.exportRows(t, w))
	}
	// A failed render is exported again as a new render, which is a new row.
	if _, err := e.st.FailRender(ctx, w.ID, fin.ID, ReasonRenderFailed, -1, -1); err != nil {
		t.Fatal(err)
	}
	if _, created := e.request(t, w, p, v, "final"); !created || e.exportRows(t, w) != 2 {
		t.Fatalf("after a failure: created=%v rows %d", created, e.exportRows(t, w))
	}
	// Exports cost nothing and attribute to the member and the project.
	var credits int
	var member, project *string
	if err := e.super.QueryRow(ctx, `SELECT coalesce(sum(credits), 0), min(member_user_id::text), min(project_id::text) FROM credit_ledger WHERE workspace_id = $1 AND action = 'export.final'`, w.ID).Scan(&credits, &member, &project); err != nil {
		t.Fatal(err)
	}
	if credits != 0 || member == nil || *member != e.user.UUID.String() || project == nil || *project != p.ID.UUID.String() {
		t.Fatalf("export attribution: credits %d member %v project %v", credits, member, project)
	}
}

// A project's charged edit belongs to its workspace and its project.
func TestChargedEditDoesNotCrossProjectsOrWorkspaces(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	wa, pa, va := e.uncharged(t, "org_export_a")
	wb, pb, vb := e.uncharged(t, "org_export_b")
	pa2, va2 := e.unchargedIn(t, wa)
	e.chargeEdit(t, wa, pa)
	if _, _, rerr := e.svc.Request(ctx, e.caller(wb), pb.ID, vb, "final"); rerr == nil || rerr.Code != api.ErrorCodeEditNotCharged {
		t.Fatalf("workspace B exported with A's charged edit: %v", rerr)
	}
	// Another project of A has none either.
	if _, _, rerr := e.svc.Request(ctx, e.caller(wa), pa2.ID, va2, "final"); rerr == nil || rerr.Code != api.ErrorCodeEditNotCharged {
		t.Fatalf("A's other project was exported with this project's charged edit: %v", rerr)
	}
	if _, created := e.request(t, wa, pa, va, "final"); !created {
		t.Fatal("A's own export was refused")
	}
	// B asking for A's project does not find it at all.
	if _, _, rerr := e.svc.Request(ctx, e.caller(wb), pa.ID, va, "final"); rerr == nil || rerr.Status != 404 {
		t.Fatalf("B exporting A's project: %v", rerr)
	}
}

// An edit that was released (a failed run) is not a charged edit, so it cannot be exported.
func TestAReleasedEditCannotBeExported(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w, p, v := e.uncharged(t, "org_export_released")
	action := newV7(t)
	hold := store.HoldCreditsParams{Workspace: w.ID, Action: store.ActionEditFirst, ActionID: action, Credits: 0, Project: p.ID, Now: time.Now(), TTL: time.Hour}
	if _, err := e.st.HoldCredits(ctx, hold); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.ReleaseCredits(ctx, w.ID, action, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, rerr := e.svc.Request(ctx, e.caller(w), p.ID, v, "final"); rerr == nil || rerr.Code != api.ErrorCodeEditNotCharged {
		t.Fatalf("export after a released edit: %v", rerr)
	}
	// An edit that was charged and then refunded is not an edit either.
	if _, err := e.st.HoldCredits(ctx, store.HoldCreditsParams{Workspace: w.ID, Action: store.ActionEditFirst, ActionID: action, Credits: 0, Project: p.ID, Now: time.Now(), TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SettleCredits(ctx, w.ID, action, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, rerr := e.svc.Request(ctx, e.caller(w), p.ID, v, "final"); rerr != nil {
		t.Fatalf("export after a charged edit: %v", rerr)
	}
}

// The service refuses final renders when it was wired without credits: a missing wiring is never a free export.
func TestFinalRenderWithoutTheCreditsServiceFailsClosed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w, p, v := e.uncharged(t, "org_export_unwired")
	e.chargeEdit(t, w, p)
	e.svc.Credits = nil
	if _, _, rerr := e.svc.Request(ctx, e.caller(w), p.ID, v, "final"); rerr == nil || rerr.Status != 500 {
		t.Fatalf("a final render without the credits service: %v", rerr)
	}
	if r, created, rerr := e.svc.Request(ctx, e.caller(w), p.ID, v, "preview"); rerr != nil || !created {
		t.Fatalf("a preview without the credits service: %+v %v %v", r, created, rerr)
	}
}

// Over the handler: 422 with the stable code.
func TestRenderAPIAnswersEditNotCharged(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w, p, v := e.uncharged(t, "org_export_api")
	h := NewAPI(e.svc, func(context.Context) (httpapi.Caller, bool) {
		return httpapi.Caller{Workspace: w.ID, User: e.user, Role: store.RoleOwner}, true
	}, quiet)
	req := api.CreateRenderRequestObject{Id: ids.Encode(ids.PrefixProject, p.ID.UUID), Body: &api.CreateRenderRequest{Version: v, Profile: api.RenderProfileFinal}}
	got, err := h.CreateRender(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	r422, ok := got.(api.CreateRender422JSONResponse)
	if !ok || r422.Error.Code != api.ErrorCodeEditNotCharged {
		t.Fatalf("response = %#v", got)
	}
	e.chargeEdit(t, w, p)
	if got, err := h.CreateRender(ctx, req); err != nil {
		t.Fatal(err)
	} else if _, ok := got.(api.CreateRender202JSONResponse); !ok {
		t.Fatalf("after a charged edit: %#v", got)
	}
}
