package renderjobs

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func newV7(t *testing.T) uuid.UUID {
	t.Helper()
	u, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// An API wired without a service (a server without object storage) answers 503 after the checks of authentication and role, and
// never panics; a viewer may look but not render; anonymous callers get 401.
func TestRenderAPIChecksAuthenticationAndRoleFirst(t *testing.T) {
	ctx := context.Background()
	project := ids.Encode(ids.PrefixProject, newV7(t))
	render := ids.Encode(ids.PrefixRender, newV7(t))
	body := &api.CreateRenderRequest{Version: 1, Profile: api.RenderProfilePreview}
	as := func(role store.Role) *RenderAPI {
		return NewAPI(nil, func(context.Context) (httpapi.Caller, bool) { return httpapi.Caller{Role: role}, true }, nil)
	}

	// Signed in with a service that is not there: 503 for everyone who may ask.
	for _, role := range []store.Role{store.RoleOwner, store.RoleEditor} {
		if r, err := as(role).CreateRender(ctx, api.CreateRenderRequestObject{Id: project, Body: body}); err != nil {
			t.Fatal(err)
		} else if _, ok := r.(api.CreateRender503JSONResponse); !ok {
			t.Errorf("%s CreateRender = %T, want 503", role, r)
		}
	}
	for _, role := range []store.Role{store.RoleOwner, store.RoleEditor, store.RoleViewer} {
		if r, _ := as(role).GetRender(ctx, api.GetRenderRequestObject{Id: render}); r == nil {
			t.Error("no answer")
		} else if _, ok := r.(api.GetRender503JSONResponse); !ok {
			t.Errorf("%s GetRender = %T, want 503", role, r)
		}
		if r, _ := as(role).ListRenders(ctx, api.ListRendersRequestObject{Id: project}); r == nil {
			t.Error("no answer")
		} else if _, ok := r.(api.ListRenders503JSONResponse); !ok {
			t.Errorf("%s ListRenders = %T, want 503", role, r)
		}
	}
	// A viewer cannot start a render (it costs CPU): 403 before anything else.
	if r, _ := as(store.RoleViewer).CreateRender(ctx, api.CreateRenderRequestObject{Id: project, Body: body}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.CreateRender403JSONResponse); !ok {
		t.Errorf("viewer CreateRender = %T, want 403", r)
	}
	anon := NewAPI(nil, func(context.Context) (httpapi.Caller, bool) { return httpapi.Caller{}, false }, nil)
	if r, _ := anon.CreateRender(ctx, api.CreateRenderRequestObject{Id: project, Body: body}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.CreateRender401JSONResponse); !ok {
		t.Errorf("anonymous CreateRender = %T, want 401", r)
	}
	if r, _ := anon.GetRender(ctx, api.GetRenderRequestObject{Id: render}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.GetRender401JSONResponse); !ok {
		t.Errorf("anonymous GetRender = %T, want 401", r)
	}
	if r, _ := anon.ListRenders(ctx, api.ListRendersRequestObject{Id: project}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.ListRenders401JSONResponse); !ok {
		t.Errorf("anonymous ListRenders = %T, want 401", r)
	}
}

// The handler over a real service: ids and parameters are checked, a new render is 202 and a reused one 200, and the list pages.
func TestRenderAPIOverARealService(t *testing.T) {
	f := newFixture(t, "org_api")
	e, ctx := f.e, context.Background()
	h := NewAPI(e.svc, callerOf(f.w.ID, e.user, store.RoleEditor), quiet)
	project := f.project.ID.String()

	// Bad input is 400, never a 404 or a 500.
	if r, err := h.CreateRender(ctx, api.CreateRenderRequestObject{Id: "prj_nope", Body: &api.CreateRenderRequest{Version: 1, Profile: api.RenderProfilePreview}}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.CreateRender400JSONResponse); !ok {
		t.Errorf("a bad project id = %T, want 400", r)
	}
	if r, _ := h.CreateRender(ctx, api.CreateRenderRequestObject{Id: project}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.CreateRender400JSONResponse); !ok {
		t.Errorf("no body = %T, want 400", r)
	}
	if r, _ := h.CreateRender(ctx, api.CreateRenderRequestObject{Id: project, Body: &api.CreateRenderRequest{Version: f.spec, Profile: "ultra"}}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.CreateRender400JSONResponse); !ok {
		t.Errorf("a bad profile = %T, want 400", r)
	}
	if r, _ := h.CreateRender(ctx, api.CreateRenderRequestObject{Id: project, Body: &api.CreateRenderRequest{Version: 99, Profile: api.RenderProfilePreview}}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.CreateRender404JSONResponse); !ok {
		t.Errorf("a missing version = %T, want 404", r)
	}
	if r, _ := h.GetRender(ctx, api.GetRenderRequestObject{Id: "ast_01j3er3kwcgrasdgd4aznmxxe0"}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.GetRender400JSONResponse); !ok {
		t.Errorf("an id of another kind = %T, want 400", r)
	}
	if r, _ := h.GetRender(ctx, api.GetRenderRequestObject{Id: ids.Encode(ids.PrefixRender, newV7(t))}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.GetRender404JSONResponse); !ok {
		t.Errorf("an unknown render = %T, want 404", r)
	}
	zero, big := 0, 101
	for name, p := range map[string]api.ListRendersParams{
		"limit 0": {Limit: &zero}, "limit 101": {Limit: &big}, "a bad cursor": {Cursor: ptr("nope")}, "version 0": {Version: &zero},
	} {
		if r, _ := h.ListRenders(ctx, api.ListRendersRequestObject{Id: project, Params: p}); r == nil {
			t.Error("no answer")
		} else if _, ok := r.(api.ListRenders400JSONResponse); !ok {
			t.Errorf("%s = %T, want 400", name, r)
		}
	}

	// 202 for a new render, 200 for the same one asked for again.
	req := func(version int, profile api.RenderProfile) api.CreateRenderResponseObject {
		t.Helper()
		r, err := h.CreateRender(ctx, api.CreateRenderRequestObject{Id: project, Body: &api.CreateRenderRequest{Version: version, Profile: profile}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	created, ok := req(f.spec, api.RenderProfilePreview).(api.CreateRender202JSONResponse)
	if !ok || created.Status != api.RenderStatusQueued || created.Progress != 0 || created.Files != nil || created.ContactSheet != nil || created.UrlsExpiresAt != nil ||
		created.SoundCues == nil || created.Stage != nil || created.FailureReason != nil || created.Profile != api.RenderProfilePreview || created.TimelineVersion != f.spec {
		t.Fatalf("a new render: %T %+v", created, created)
	}
	if again, ok := req(f.spec, api.RenderProfilePreview).(api.CreateRender200JSONResponse); !ok || again.Id != created.Id {
		t.Errorf("the same request again: %+v", again)
	}
	// A second version and the final profile, to page and filter.
	v2 := e.addVersion(t, f.w, f.project, shortSpec(f.a, f.b))
	for _, c := range []struct {
		v int
		p api.RenderProfile
	}{{v2, api.RenderProfilePreview}, {f.spec, api.RenderProfileFinal}} {
		if _, ok := req(c.v, c.p).(api.CreateRender202JSONResponse); !ok {
			t.Fatalf("render of %d %s was not queued", c.v, c.p)
		}
	}
	list := func(p api.ListRendersParams) api.ListRenders200JSONResponse {
		t.Helper()
		r, err := h.ListRenders(ctx, api.ListRendersRequestObject{Id: project, Params: p})
		if err != nil {
			t.Fatal(err)
		}
		out, ok := r.(api.ListRenders200JSONResponse)
		if !ok {
			t.Fatalf("ListRenders = %T", r)
		}
		return out
	}
	two := 2
	page1 := list(api.ListRendersParams{Limit: &two})
	if len(page1.Items) != 2 || page1.NextCursor == nil {
		t.Fatalf("page 1: %d items, cursor %v", len(page1.Items), page1.NextCursor)
	}
	page2 := list(api.ListRendersParams{Limit: &two, Cursor: page1.NextCursor})
	if len(page2.Items) != 1 || page2.NextCursor != nil || page2.Items[0].Id != created.Id {
		t.Fatalf("page 2: %+v", page2)
	}
	for i := 1; i < len(page1.Items); i++ {
		if page1.Items[i-1].Id < page1.Items[i].Id {
			t.Errorf("the list is not newest first: %v", []string{page1.Items[0].Id, page1.Items[1].Id})
		}
	}
	prof := api.RenderProfileFinal
	if got := list(api.ListRendersParams{Profile: &prof}); len(got.Items) != 1 || got.Items[0].Profile != api.RenderProfileFinal {
		t.Errorf("profile filter: %+v", got)
	}
	if got := list(api.ListRendersParams{Version: &v2}); len(got.Items) != 1 || got.Items[0].TimelineVersion != v2 {
		t.Errorf("version filter: %+v", got)
	}

	// A viewer reads them and cannot render.
	viewer := NewAPI(e.svc, callerOf(f.w.ID, e.user, store.RoleViewer), quiet)
	if r, _ := viewer.GetRender(ctx, api.GetRenderRequestObject{Id: created.Id}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.GetRender200JSONResponse); !ok {
		t.Errorf("viewer GetRender = %T", r)
	}
	if r, _ := viewer.CreateRender(ctx, api.CreateRenderRequestObject{Id: project, Body: &api.CreateRenderRequest{Version: v2, Profile: api.RenderProfileFinal}}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.CreateRender403JSONResponse); !ok {
		t.Errorf("viewer CreateRender = %T, want 403", r)
	}
}
