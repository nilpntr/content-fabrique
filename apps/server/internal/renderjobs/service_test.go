package renderjobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// fixture is a workspace with two ready assets, a project and a version 1 of a short edit. Rendering is not what these tests are
// about, so the edit is two clips of 1.5 s.
type fixture struct {
	e       *env
	w       store.Workspace
	a, b    store.Asset
	project store.Project
	spec    int // the version of the short edit
}

func newFixture(t *testing.T, org string) *fixture {
	t.Helper()
	e := newEnv(t)
	return e.fixture(t, org)
}

func (e *env) fixture(t *testing.T, org string) *fixture {
	t.Helper()
	clips := testClips(t)
	w := e.newWorkspace(t, org)
	a, b := e.readyAsset(t, w, "a", clips[0]), e.readyAsset(t, w, "b", clips[1])
	p := e.newProject(t, w, a, b)
	return &fixture{e: e, w: w, a: a, b: b, project: p, spec: e.addVersion(t, w, p, shortSpec(a, b))}
}

func (f *fixture) renderKeys(t *testing.T, r Render) []string {
	t.Helper()
	var out []string
	for _, n := range []string{storage.RenderMP4Name, storage.RenderContactSheetName, storage.RenderTimeMapName} {
		k, err := storage.RenderKey(f.w.ID, f.project.ID, r.ID, n)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

func (e *env) jobsFor(t *testing.T, r Render) int {
	t.Helper()
	var n int
	err := e.super.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'render' AND args->>'render_id' = $1`, r.ID.String()).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) renderJobs(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.super.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'render'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// An identical render that is waiting, running or ready is reused; a failed one is not; each queued render has exactly one job.
func TestRequestReusesAnIdenticalRender(t *testing.T) {
	f := newFixture(t, "org_dedupe")
	e, ctx := f.e, context.Background()

	r1, created := e.request(t, f.w, f.project, f.spec, "preview")
	if !created || r1.Status != store.RenderQueued || e.jobsFor(t, r1) != 1 {
		t.Fatalf("first request: created=%v %+v, jobs %d", created, r1, e.jobsFor(t, r1))
	}
	r2, created := e.request(t, f.w, f.project, f.spec, "preview")
	if created || r2.ID != r1.ID || e.jobsFor(t, r1) != 1 {
		t.Fatalf("a second request while it is queued: created=%v id %s (first %s), jobs %d", created, r2.ID, r1.ID, e.jobsFor(t, r1))
	}
	// Running is in flight too.
	if _, err := e.st.ClaimRender(ctx, f.w.ID, r1.ID); err != nil {
		t.Fatal(err)
	}
	if r3, created := e.request(t, f.w, f.project, f.spec, "preview"); created || r3.ID != r1.ID {
		t.Fatalf("while it runs: created=%v id %s", created, r3.ID)
	}
	// Another profile is another render.
	fin, created := e.request(t, f.w, f.project, f.spec, "final")
	if !created || fin.ID == r1.ID || fin.Profile != "final" || e.renderJobs(t) != 2 {
		t.Fatalf("final: created=%v %+v, %d render jobs", created, fin, e.renderJobs(t))
	}
	// Ready is reused.
	_, out := e.runRender(t, f.w, r1)
	if out.Status != store.RenderReady {
		t.Fatalf("outcome %+v", out)
	}
	if r4, created := e.request(t, f.w, f.project, f.spec, "preview"); created || r4.ID != r1.ID || r4.Status != store.RenderReady {
		t.Fatalf("a ready render: created=%v %+v", created, r4)
	}
	// A failed one is not: asking again starts a new render, and the failed one stays as history.
	if _, err := e.st.FailRender(ctx, f.w.ID, fin.ID, ReasonRenderFailed, -1, -1); err != nil {
		t.Fatal(err)
	}
	r5, created := e.request(t, f.w, f.project, f.spec, "final")
	if !created || r5.ID == fin.ID || r5.Status != store.RenderQueued || e.jobsFor(t, r5) != 1 {
		t.Fatalf("after a failure: created=%v %+v", created, r5)
	}
	list, _, lerr := e.svc.List(ctx, f.w.ID, f.project.ID, ListFilter{Profile: "final"}, 0, "")
	if lerr != nil || len(list) != 2 || list[0].ID != r5.ID || list[1].ID != fin.ID || list[1].Status != store.RenderFailed {
		t.Fatalf("list: %+v, %v", list, lerr)
	}
}

// Concurrent requests for one version and profile make one render and one job.
func TestConcurrentRequestsMakeOneRender(t *testing.T) {
	f := newFixture(t, "org_concurrent")
	e := f.e
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		idsSeen = map[ids.RenderID]bool{}
		created int
	)
	for range 10 {
		wg.Go(func() {
			r, c, err := e.svc.Request(context.Background(), e.caller(f.w), f.project.ID, f.spec, "preview")
			if err != nil {
				t.Errorf("Request: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			idsSeen[r.ID] = true
			if c {
				created++
			}
		})
	}
	wg.Wait()
	if len(idsSeen) != 1 || created != 1 || e.renderJobs(t) != 1 {
		t.Fatalf("%d renders, %d of them new, %d jobs; want 1, 1, 1", len(idsSeen), created, e.renderJobs(t))
	}
}

// What Request refuses, and that a refusal leaves neither a row nor a job.
func TestRequestRefusals(t *testing.T) {
	f := newFixture(t, "org_refusals")
	e, ctx := f.e, context.Background()
	other := e.newWorkspace(t, "org_refusals_other")
	foreign := e.readyAsset(t, other, "foreign", testClips(t)[0])
	missingProject := f.project.ID
	missingProject.UUID[15] ^= 0xff

	expect := func(name string, who Caller, project ids.ProjectID, version int, profile string, status int, code api.ErrorCode) {
		t.Helper()
		_, _, err := e.svc.Request(ctx, who, project, version, profile)
		if err == nil || err.Status != status || err.Code != code {
			t.Errorf("%s: %v, want %d %s", name, err, status, code)
		}
	}
	expect("an unknown profile", e.caller(f.w), f.project.ID, f.spec, "ultra", 400, api.ErrorCodeInvalidRequest)
	expect("version 0", e.caller(f.w), f.project.ID, 0, "preview", 400, api.ErrorCodeInvalidRequest)
	expect("a version that does not exist", e.caller(f.w), f.project.ID, 99, "preview", 404, api.ErrorCodeNotFound)
	expect("a project that does not exist", e.caller(f.w), missingProject, f.spec, "preview", 404, api.ErrorCodeNotFound)
	expect("another workspace's project", e.caller(other), f.project.ID, f.spec, "preview", 404, api.ErrorCodeNotFound)

	// A version that names an asset of another workspace: the project's catalog does not hold it.
	spec := shortSpec(f.a, foreign)
	v := e.addVersion(t, f.w, f.project, spec)
	_, _, err := e.svc.Request(ctx, e.caller(f.w), f.project.ID, v, "preview")
	if err == nil || err.Status != 422 || err.Code != api.ErrorCodeTimelineInvalid || !strings.Contains(err.Message, "unknown_asset") {
		t.Errorf("an asset of another workspace: %v", err)
	}
	// It is the answer for an asset that does not exist at all: nothing says the asset is real, or whose it is.
	nothing := foreign
	nothing.ID = foreign.ID
	nothing.ID.UUID[15] ^= 0xff
	v0 := e.addVersion(t, f.w, f.project, shortSpec(f.a, nothing))
	_, _, err0 := e.svc.Request(ctx, e.caller(f.w), f.project.ID, v0, "preview")
	if err0 == nil || strings.ReplaceAll(err0.Message, nothing.ID.String(), "X") != strings.ReplaceAll(err.Message, foreign.ID.String(), "X") {
		t.Errorf("a foreign asset and a missing one are told apart:\n %v\n %v", err, err0)
	}

	// An asset deleted since the edit.
	v2 := e.addVersion(t, f.w, f.project, shortSpec(f.a, f.b))
	if _, err := e.st.SoftDeleteAsset(ctx, f.w.ID, f.b.ID); err != nil {
		t.Fatal(err)
	}
	expect("a deleted asset", e.caller(f.w), f.project.ID, v2, "preview", 422, api.ErrorCodeTimelineInvalid)

	// A deleted project.
	if err := e.st.SoftDeleteProject(ctx, f.w.ID, f.project.ID); err != nil {
		t.Fatal(err)
	}
	expect("a deleted project", e.caller(f.w), f.project.ID, f.spec, "preview", 404, api.ErrorCodeNotFound)

	if n := e.renderJobs(t); n != 0 {
		t.Errorf("%d render jobs after refusals", n)
	}
	var rows int
	if err := e.super.QueryRow(ctx, `SELECT count(*) FROM renders`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("%d render rows after refusals (%v)", rows, err)
	}
}

// A workspace may have MaxInFlight renders waiting or running; asking for one that is in flight already still works.
func TestRequestCapsRendersInFlight(t *testing.T) {
	f := newFixture(t, "org_cap")
	e, ctx := f.e, context.Background()
	for range 2 { // versions 2 and 3 of the same edit
		e.addVersion(t, f.w, f.project, shortSpec(f.a, f.b))
	}
	var first Render
	n := 0
	for v := 1; v <= 3; v++ {
		for _, prof := range []string{"preview", "final"} {
			r, _, err := e.svc.Request(ctx, e.caller(f.w), f.project.ID, v, prof)
			n++
			if n <= MaxInFlight {
				if err != nil {
					t.Fatalf("request %d: %v", n, err)
				}
				if n == 1 {
					first = r
				}
				continue
			}
			if err == nil || err.Status != 429 || err.Code != api.ErrorCodeTooManyRenders {
				t.Fatalf("request %d = %v, want 429 too_many_renders", n, err)
			}
		}
	}
	if again, created, err := e.svc.Request(ctx, e.caller(f.w), f.project.ID, 1, "preview"); err != nil || created || again.ID != first.ID {
		t.Errorf("a render that is in flight is returned at the cap: %v %v %v", again.ID, created, err)
	}
	// Another workspace has its own allowance.
	o := e.fixture(t, "org_cap_other")
	if _, _, err := e.svc.Request(ctx, e.caller(o.w), o.project.ID, o.spec, "preview"); err != nil {
		t.Errorf("another workspace: %v", err)
	}
	// Finishing one frees a place.
	if _, err := e.st.FailRender(ctx, f.w.ID, first.ID, ReasonRenderFailed, -1, -1); err != nil {
		t.Fatal(err)
	}
	if _, created, err := e.svc.Request(ctx, e.caller(f.w), f.project.ID, 3, "final"); err != nil || !created {
		t.Errorf("after one failed: %v %v", created, err)
	}
}

// Workspace B can neither see, list, request, run nor fetch anything of workspace A: each is a 404, as if it did not exist.
func TestRendersAreTenantScoped(t *testing.T) {
	f := newFixture(t, "org_tenant_a")
	e, ctx := f.e, context.Background()
	other := e.newWorkspace(t, "org_tenant_b")
	oa := e.readyAsset(t, other, "b-asset", testClips(t)[1])
	op := e.newProject(t, other, oa)
	ov := e.addVersion(t, other, op, shortSpec(oa, oa))

	r, _ := e.request(t, f.w, f.project, f.spec, "preview")
	done, out := e.runRender(t, f.w, r)
	if out.Status != store.RenderReady {
		t.Fatalf("outcome %+v", out)
	}

	if _, err := e.svc.Get(ctx, other.ID, r.ID); err == nil || err.Status != 404 {
		t.Errorf("Get from B: %v", err)
	}
	if _, _, err := e.svc.List(ctx, other.ID, f.project.ID, ListFilter{}, 0, ""); err == nil || err.Status != 404 {
		t.Errorf("List from B: %v", err)
	}
	if rows, _, err := e.svc.List(ctx, other.ID, op.ID, ListFilter{}, 0, ""); err != nil || len(rows) != 0 {
		t.Errorf("B's own list holds %d renders (%v)", len(rows), err)
	}
	if _, _, err := e.svc.Request(ctx, e.caller(other), f.project.ID, f.spec, "preview"); err == nil || err.Status != 404 {
		t.Errorf("Request from B for A's project: %v", err)
	}
	// B asks for its own version 1: not A's render of A's version 1.
	if rb, created, err := e.svc.Request(ctx, e.caller(other), op.ID, ov, "preview"); err != nil || !created || rb.ID == r.ID || rb.WorkspaceID != other.ID {
		t.Errorf("B's own request: %+v %v %v", rb, created, err)
	}
	// A job that names A's render under B's workspace does nothing: not a failure of A's render either.
	r2, _ := e.request(t, f.w, f.project, f.spec, "final")
	if _, err := e.pipe.Run(ctx, other.ID, r2.ID); !errors.Is(err, errGone) {
		t.Errorf("Run for the wrong workspace = %v, want ErrRenderGone", err)
	}
	if cur, err := e.svc.Get(ctx, f.w.ID, r2.ID); err != nil || cur.Status != store.RenderQueued {
		t.Errorf("A's render after B's job: %+v %v", cur, err)
	}
	if err := e.pipe.Fail(ctx, other.ID, r2.ID, errors.New("x")); err != nil {
		t.Errorf("Fail for the wrong workspace: %v", err)
	}
	if cur, _ := e.svc.Get(ctx, f.w.ID, r2.ID); cur.Status != store.RenderQueued {
		t.Errorf("B failed A's render: %+v", cur)
	}

	// The handler of B's caller says the same.
	h := NewAPI(e.svc, callerOf(other.ID, e.user, store.RoleOwner), quiet)
	if resp, err := h.GetRender(ctx, api.GetRenderRequestObject{Id: r.ID.String()}); err != nil {
		t.Fatal(err)
	} else if _, ok := resp.(api.GetRender404JSONResponse); !ok {
		t.Errorf("GetRender from B = %T, want 404", resp)
	}
	if resp, err := h.ListRenders(ctx, api.ListRendersRequestObject{Id: f.project.ID.String()}); err != nil {
		t.Fatal(err)
	} else if _, ok := resp.(api.ListRenders404JSONResponse); !ok {
		t.Errorf("ListRenders from B = %T, want 404", resp)
	}
	if resp, err := h.CreateRender(ctx, api.CreateRenderRequestObject{Id: f.project.ID.String(), Body: &api.CreateRenderRequest{Version: f.spec, Profile: api.RenderProfilePreview}}); err != nil {
		t.Fatal(err)
	} else if _, ok := resp.(api.CreateRender404JSONResponse); !ok {
		t.Errorf("CreateRender from B = %T, want 404", resp)
	}

	// Everything A's render wrote is under A's prefix, and nothing is under B's.
	for _, k := range f.renderKeys(t, done) {
		if !strings.HasPrefix(k, "ws/"+f.w.ID.String()+"/projects/"+f.project.ID.String()+"/renders/"+r.ID.String()+"/") || !e.exists(t, k) {
			t.Errorf("key %s", k)
		}
	}
	if n, err := e.objs.DeleteRenderObjects(ctx, other.ID, f.project.ID, r.ID); err != nil || n != 0 {
		t.Errorf("under B's prefix: %d objects (%v)", n, err)
	}
	if !e.exists(t, f.renderKeys(t, done)[0]) {
		t.Error("deleting under B's prefix removed A's render")
	}

	// A row whose key was tampered with is never presigned.
	if _, err := e.super.Exec(ctx, `UPDATE renders SET mp4_key = $2 WHERE id = $1`, r.ID, "ws/"+other.ID.String()+"/assets/x/original"); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.svc.Get(ctx, f.w.ID, r.ID)
	if _, err := e.svc.Files(ctx, cur); err == nil || err.Status != 500 {
		t.Errorf("Files with a foreign key: %v", err)
	}
	if _, err := e.svc.ContactSheetJPEG(ctx, cur); err != nil {
		t.Errorf("the contact sheet key was not tampered with: %v", err)
	}
}

func TestRenderRowsRejectWhatTheServiceShould(t *testing.T) {
	f := newFixture(t, "org_rows")
	e, ctx := f.e, context.Background()
	r, _ := e.request(t, f.w, f.project, f.spec, "preview")
	for name, q := range map[string]string{
		"ready without files":       `UPDATE renders SET status = 'ready', progress = 100 WHERE id = $1`,
		"failed without a reason":   `UPDATE renders SET status = 'failed' WHERE id = $1`,
		"an unknown status":         `UPDATE renders SET status = 'done' WHERE id = $1`,
		"an unknown profile":        `UPDATE renders SET profile = 'draft' WHERE id = $1`,
		"progress over 100":         `UPDATE renders SET progress = 101 WHERE id = $1`,
		"a version that is not one": `UPDATE renders SET timeline_version = 99 WHERE id = $1`,
		"another project's version": `UPDATE renders SET project_id = gen_random_uuid() WHERE id = $1`,
	} {
		if _, err := e.super.Exec(ctx, q, r.ID); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := e.st.FailRender(ctx, f.w.ID, r.ID, strings.Repeat("x", 65), -1, -1); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("a reason of 65 characters: %v", err)
	}
	if got, _ := json.Marshal(r.SoundCues); string(got) != "[]" {
		t.Errorf("sound_cues default %s", got)
	}
}
