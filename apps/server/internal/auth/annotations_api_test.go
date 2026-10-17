package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// annotationFixture is a workspace's content the annotations tests comment on: a clip, a project with two versions and a ready render
// of the second.
type annotationFixture struct {
	asset   ids.AssetID
	project ids.ProjectID
	v1, v2  ids.TimelineID
	render  ids.RenderID
}

// annotationContent makes the content directly in the store (no object storage is involved in comments).
func (e *assetsEnv) annotationContent(who string) annotationFixture {
	e.t.Helper()
	ctx := context.Background()
	ws := e.ws[who]
	user, err := e.st.UpsertUserByClerkID(ctx, "user_"+strings.TrimPrefix(who, ""), who+"@example.com")
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := e.st.NewAssetID(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	a, _, err := e.st.CreateAssetWithUpload(ctx, ws, store.NewAsset{
		ID: id, Kind: store.AssetKindVideo, Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1 << 20, Title: "a",
		R2Key: "ws/" + ws.String() + "/assets/" + id.String() + "/original", CreatedBy: user.ID,
		R2UploadID: "r2-" + id.String(), PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
	}, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE assets SET duration_ms = 60000 WHERE id = $1`, a.ID.UUID); err != nil {
		e.t.Fatal(err)
	}
	var p store.Project
	err = e.st.InTx(ctx, func(tx *store.Store) error {
		var err error
		if p, err = tx.CreateProject(ctx, ws, store.NewProject{Title: "Back day", Format: store.Format9x16, CreatedBy: user.ID}); err != nil {
			return err
		}
		return tx.AddProjectAssets(ctx, ws, p.ID, []ids.AssetID{a.ID})
	})
	if err != nil {
		e.t.Fatal(err)
	}
	var vs [2]store.TimelineVersion
	for v := 1; v <= 2; v++ {
		if vs[v-1], err = e.st.AddTimelineVersion(ctx, ws, p.ID, store.NewTimelineVersion{
			Version: v, Parent: v - 1, Spec: json.RawMessage(`{"schema":"x"}`), AuthorKind: store.AuthorUser, AuthorID: "usr_x",
		}); err != nil {
			e.t.Fatal(err)
		}
	}
	r, err := e.st.CreateRender(ctx, ws, store.NewRender{Project: p.ID, Version: 2, Profile: store.ProfilePreview, RequestedByKind: store.RenderRequestedByUser, RequestedByID: user.ID.String()})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.ClaimRender(ctx, ws, r.ID); err != nil {
		e.t.Fatal(err)
	}
	prefix := "ws/" + ws.String() + "/projects/" + p.ID.String() + "/renders/" + r.ID.String() + "/"
	if _, err := e.st.FinishRender(ctx, ws, r.ID, store.RenderResult{
		MP4Key: prefix + "render.mp4", ContactSheetKey: prefix + "contact_sheet.jpg", TimeMapKey: prefix + "time_map.json",
		DurationMs: 30000, Width: 540, Height: 960, SizeBytes: 1 << 20, ContactSheetBytes: 1 << 10, TimeMapBytes: 1 << 10,
		ContactSheet: json.RawMessage(`{"columns":4,"tile_width":216,"tile_height":384,"tiles":[]}`), SoundCues: json.RawMessage(`[]`),
	}); err != nil {
		e.t.Fatal(err)
	}
	return annotationFixture{asset: a.ID, project: p.ID, v1: vs[0].ID, v2: vs[1].ID, render: r.ID}
}

func TestAnnotationsAPI(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.noStorage = true })
	fa := e.annotationContent("ada")

	// An editor comments on the render with a pin and a range.
	var c api.Annotation
	e.call(http.MethodPost, "/v1/annotations", "editor", map[string]any{
		"target_type": "render", "target_id": fa.render.String(), "start_ms": 4000, "end_ms": 6000, "body": "  make this text bigger  ",
		"pin": map[string]any{"x": 0.5, "y": 0.25},
	}, http.StatusCreated, &c)
	if !strings.HasPrefix(c.Id, "ann_") || c.TargetType != api.AnnotationTargetTypeRender || c.TargetId != fa.render.String() || c.ProjectId == nil || *c.ProjectId != fa.project.String() ||
		c.TimelineVersion == nil || *c.TimelineVersion != 2 || c.StartMs != 4000 || c.EndMs != 6000 || c.Kind != api.AnnotationKindComment || c.Body != "make this text bigger" ||
		c.Status != api.AnnotationStatusOpen || c.ParentId != nil || c.Author.Kind != api.AuthorKindUser || !strings.HasPrefix(c.Author.Id, "usr_") || c.Pin == nil || c.Pin.X != 0.5 || c.Pin.Y != 0.25 ||
		c.AppliedInRunId != nil || c.AddressedInVersion != nil || c.CreatedAt.IsZero() {
		t.Fatalf("created: %+v", c)
	}
	// The null fields are in the JSON (the contract has them required), not left out.
	raw := e.do(http.MethodGet, "/v1/annotations?project_id="+fa.project.String(), "viewer", nil).Body.String()
	for _, key := range []string{`"parent_id":null`, `"applied_in_run_id":null`, `"addressed_in_version":null`} {
		if !strings.Contains(raw, key) {
			t.Errorf("list JSON lacks %s: %s", key, raw)
		}
	}

	// On the version and on the clip, by the owner.
	var onVersion, onClip api.Annotation
	e.call(http.MethodPost, "/v1/annotations", "ada", map[string]any{"target_type": "timeline_version", "target_id": fa.v1.String(), "start_ms": 100, "body": "intro is slow", "kind": "instruction"}, http.StatusCreated, &onVersion)
	e.call(http.MethodPost, "/v1/annotations", "ada", map[string]any{"target_type": "asset", "target_id": fa.asset.String(), "start_ms": 1000, "end_ms": 5000, "kind": "include"}, http.StatusCreated, &onClip)
	if onVersion.TimelineVersion == nil || *onVersion.TimelineVersion != 1 || onVersion.EndMs != 100 || onVersion.Kind != api.AnnotationKindInstruction ||
		onClip.ProjectId != nil || onClip.TimelineVersion != nil || onClip.Kind != api.AnnotationKindInclude || onClip.Body != "" {
		t.Fatalf("version: %+v\nclip: %+v", onVersion, onClip)
	}

	// A reply.
	var reply api.Annotation
	e.call(http.MethodPost, "/v1/annotations", "ada", map[string]any{"parent_id": c.Id, "body": "will do"}, http.StatusCreated, &reply)
	if reply.ParentId == nil || *reply.ParentId != c.Id || reply.StartMs != 4000 || reply.EndMs != 6000 || reply.TargetId != c.TargetId || reply.Pin != nil {
		t.Fatalf("reply: %+v", reply)
	}

	// Lists: by target, by project (replies included, oldest first), a viewer reads.
	var list api.ListAnnotationsResponse
	e.call(http.MethodGet, "/v1/annotations?target_type=render&target_id="+fa.render.String(), "viewer", nil, http.StatusOK, &list)
	if len(list.Items) != 2 || list.Items[0].Id != c.Id || list.Items[1].Id != reply.Id || list.NextCursor != nil {
		t.Fatalf("by render: %+v", list)
	}
	e.call(http.MethodGet, "/v1/annotations?project_id="+fa.project.String(), "viewer", nil, http.StatusOK, &list)
	if len(list.Items) != 3 || list.Items[0].Id != c.Id || list.Items[1].Id != onVersion.Id || list.Items[2].Id != reply.Id {
		t.Fatalf("by project: %+v", list)
	}
	e.call(http.MethodGet, "/v1/annotations?target_type=asset&target_id="+fa.asset.String(), "viewer", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].Id != onClip.Id {
		t.Fatalf("by clip: %+v", list)
	}
	e.call(http.MethodGet, "/v1/annotations?project_id="+fa.project.String()+"&limit=2", "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 2 || list.NextCursor == nil || *list.NextCursor != onVersion.Id {
		t.Fatalf("page 1: %+v", list)
	}
	e.call(http.MethodGet, "/v1/annotations?project_id="+fa.project.String()+"&limit=2&cursor="+*list.NextCursor, "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.NextCursor != nil {
		t.Fatalf("page 2: %+v", list)
	}

	// Edit by the author; resolve by another member; the author deletes.
	var got api.Annotation
	e.call(http.MethodPatch, "/v1/annotations/"+c.Id, "editor", map[string]any{"body": "bigger, and bolder", "remove_pin": true, "start_ms": 4500}, http.StatusOK, &got)
	if got.Body != "bigger, and bolder" || got.Pin != nil || got.StartMs != 4500 || got.EndMs != 6000 {
		t.Fatalf("edited: %+v", got)
	}
	e.call(http.MethodPatch, "/v1/annotations/"+c.Id, "ada", map[string]any{"status": "resolved"}, http.StatusOK, &got)
	if got.Status != api.AnnotationStatusResolved {
		t.Fatalf("resolved: %+v", got)
	}
	e.fail(http.MethodPatch, "/v1/annotations/"+c.Id, "ada", map[string]any{"body": "mine"}, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodDelete, "/v1/annotations/"+c.Id, "ada", nil, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.call(http.MethodDelete, "/v1/annotations/"+c.Id, "editor", nil, http.StatusNoContent, nil)
	e.fail(http.MethodDelete, "/v1/annotations/"+c.Id, "editor", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.call(http.MethodGet, "/v1/annotations?project_id="+fa.project.String(), "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].Id != onVersion.Id {
		t.Fatalf("after the delete: %+v", list)
	}
}

func TestAnnotationsAPIRolesAndTenancy(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.noStorage = true })
	fa := e.annotationContent("ada")
	fb := e.annotationContent("bob")

	var mine, theirs api.Annotation
	e.call(http.MethodPost, "/v1/annotations", "ada", map[string]any{"target_type": "render", "target_id": fa.render.String(), "start_ms": 1000, "body": "mine"}, http.StatusCreated, &mine)
	e.call(http.MethodPost, "/v1/annotations", "bob", map[string]any{"target_type": "render", "target_id": fb.render.String(), "start_ms": 1000, "body": "theirs"}, http.StatusCreated, &theirs)

	// A viewer reads and changes nothing.
	e.fail(http.MethodPost, "/v1/annotations", "viewer", map[string]any{"target_type": "render", "target_id": fa.render.String(), "start_ms": 1, "body": "x"}, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodPatch, "/v1/annotations/"+mine.Id, "viewer", map[string]any{"status": "resolved"}, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodDelete, "/v1/annotations/"+mine.Id, "viewer", nil, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.call(http.MethodGet, "/v1/annotations?project_id="+fa.project.String(), "viewer", nil, http.StatusOK, nil)

	// No token: 401.
	e.fail(http.MethodGet, "/v1/annotations?project_id="+fa.project.String(), "", nil, http.StatusUnauthorized, api.ErrorCodeUnauthenticated)
	e.fail(http.MethodPost, "/v1/annotations", "", "{}", http.StatusUnauthorized, api.ErrorCodeUnauthenticated)

	// Every operation, with the other workspace's ids, is a 404, and the same one as for an id that does not exist.
	ghostRender, ghostComment := ids.Encode(ids.PrefixRender, uuid.Must(uuid.NewV7())), ids.Encode(ids.PrefixAnnotation, uuid.Must(uuid.NewV7()))
	ghostProject := ids.Encode(ids.PrefixProject, uuid.Must(uuid.NewV7()))
	type call struct {
		method, path string
		body         any
	}
	pairs := [][2]call{
		{{http.MethodPost, "/v1/annotations", map[string]any{"target_type": "render", "target_id": fb.render.String(), "start_ms": 1, "body": "x"}},
			{http.MethodPost, "/v1/annotations", map[string]any{"target_type": "render", "target_id": ghostRender, "start_ms": 1, "body": "x"}}},
		{{http.MethodPost, "/v1/annotations", map[string]any{"target_type": "asset", "target_id": fb.asset.String(), "start_ms": 1, "body": "x"}},
			{http.MethodPost, "/v1/annotations", map[string]any{"target_type": "asset", "target_id": ids.Encode(ids.PrefixAsset, uuid.Must(uuid.NewV7())), "start_ms": 1, "body": "x"}}},
		{{http.MethodPost, "/v1/annotations", map[string]any{"target_type": "timeline_version", "target_id": fb.v1.String(), "start_ms": 1, "body": "x"}},
			{http.MethodPost, "/v1/annotations", map[string]any{"target_type": "timeline_version", "target_id": ids.Encode(ids.PrefixTimeline, uuid.Must(uuid.NewV7())), "start_ms": 1, "body": "x"}}},
		{{http.MethodPost, "/v1/annotations", map[string]any{"parent_id": theirs.Id, "body": "x"}},
			{http.MethodPost, "/v1/annotations", map[string]any{"parent_id": ghostComment, "body": "x"}}},
		{{http.MethodGet, "/v1/annotations?target_type=render&target_id=" + fb.render.String(), nil},
			{http.MethodGet, "/v1/annotations?target_type=render&target_id=" + ghostRender, nil}},
		{{http.MethodGet, "/v1/annotations?project_id=" + fb.project.String(), nil},
			{http.MethodGet, "/v1/annotations?project_id=" + ghostProject, nil}},
		{{http.MethodPatch, "/v1/annotations/" + theirs.Id, map[string]any{"status": "resolved"}},
			{http.MethodPatch, "/v1/annotations/" + ghostComment, map[string]any{"status": "resolved"}}},
		{{http.MethodDelete, "/v1/annotations/" + theirs.Id, nil},
			{http.MethodDelete, "/v1/annotations/" + ghostComment, nil}},
	}
	for _, p := range pairs {
		foreign := e.do(p[0].method, p[0].path, "ada", p[0].body)
		ghost := e.do(p[1].method, p[1].path, "ada", p[1].body)
		wantError(t, foreign, http.StatusNotFound, "not_found")
		if foreign.Body.String() != ghost.Body.String() && !strings.Contains(p[0].path, "annotations/") {
			// The messages say what kind of thing is missing, which is the same for both of a pair.
			t.Errorf("%s %s: a foreign id answers %s, a missing one %s", p[0].method, p[0].path, foreign.Body, ghost.Body)
		}
	}
	// A project of A's own, named with B's render, is not found either; and B's comment is untouched by all of it.
	e.fail(http.MethodPost, "/v1/annotations", "ada", map[string]any{"target_type": "render", "target_id": fa.render.String(), "project_id": fb.project.String(), "start_ms": 1, "body": "x"}, http.StatusNotFound, api.ErrorCodeNotFound)
	var still api.ListAnnotationsResponse
	e.call(http.MethodGet, "/v1/annotations?project_id="+fb.project.String(), "bob", nil, http.StatusOK, &still)
	if len(still.Items) != 1 || still.Items[0].Id != theirs.Id || still.Items[0].Status != api.AnnotationStatusOpen || still.Items[0].Body != "theirs" {
		t.Fatalf("B's comments after A's attempts: %+v", still)
	}
	// And A's list never has B's.
	var mineList api.ListAnnotationsResponse
	e.call(http.MethodGet, "/v1/annotations?project_id="+fa.project.String(), "ada", nil, http.StatusOK, &mineList)
	if len(mineList.Items) != 1 || mineList.Items[0].Id != mine.Id {
		t.Fatalf("A's comments: %+v", mineList)
	}

	// Validation answers 400 invalid_request.
	for name, body := range map[string]any{
		"no body at all":             "",
		"an end before the start":    map[string]any{"target_type": "render", "target_id": fa.render.String(), "start_ms": 5, "end_ms": 4, "body": "x"},
		"a pin off the frame":        map[string]any{"target_type": "render", "target_id": fa.render.String(), "start_ms": 1, "body": "x", "pin": map[string]any{"x": 1.5, "y": 0}},
		"a long body":                map[string]any{"target_type": "render", "target_id": fa.render.String(), "start_ms": 1, "body": strings.Repeat("a", 2001)},
		"an unknown kind":            map[string]any{"target_type": "render", "target_id": fa.render.String(), "start_ms": 1, "body": "x", "kind": "love"},
		"a target of the wrong kind": map[string]any{"target_type": "render", "target_id": fa.asset.String(), "start_ms": 1, "body": "x"},
	} {
		rec := e.do(http.MethodPost, "/v1/annotations", "ada", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	e.fail(http.MethodGet, "/v1/annotations", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodGet, "/v1/annotations?project_id=nope", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodGet, "/v1/annotations?project_id="+fa.project.String()+"&limit=0", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodPatch, "/v1/annotations/nope", "ada", map[string]any{"status": "open"}, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodPatch, "/v1/annotations/"+mine.Id, "ada", map[string]any{"status": "applied"}, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodPatch, "/v1/annotations/"+mine.Id, "ada", map[string]any{}, http.StatusBadRequest, api.ErrorCodeInvalidRequest)

	// No plan: 402 on every operation, before anything else about the request is looked at.
	if _, err := e.st.RevokePlan(context.Background(), store.RevokeInput{WorkspaceID: e.ws["ada"], GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []call{
		{http.MethodGet, "/v1/annotations?project_id=" + fa.project.String(), nil}, {http.MethodPost, "/v1/annotations", "{}"},
		{http.MethodPatch, "/v1/annotations/" + mine.Id, "{}"}, {http.MethodDelete, "/v1/annotations/" + mine.Id, nil},
	} {
		e.fail(c.method, c.path, "ada", c.body, http.StatusPaymentRequired, api.ErrorCodePlanRequired)
	}
}
