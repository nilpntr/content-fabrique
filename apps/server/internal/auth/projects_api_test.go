package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
)

// readyAsset makes a ready video asset (87.135 s) in who's workspace, through the API and the ingest stand-in.
func (e *assetsEnv) readyAsset(who, name string) string {
	e.t.Helper()
	a := e.uploadedAsset(who, name)
	e.ingested(who, a)
	return a.Id
}

// newProject creates a project as who and returns it.
func (e *assetsEnv) newProject(who, format string, assetIDs ...string) api.Project {
	e.t.Helper()
	var p api.Project
	e.call(http.MethodPost, "/v1/projects", who, map[string]any{"format": format, "asset_ids": assetIDs}, http.StatusCreated, &p)
	return p
}

func addClip(asset string, in, out int) map[string]any {
	return map[string]any{"op": "add_clip", "asset_id": asset, "source": map[string]any{"in_ms": in, "out_ms": out}}
}

func randomAssetID() string { return ids.Encode(ids.PrefixAsset, uuid.Must(uuid.NewV7())) }

func projectPath(p api.Project, suffix string) string { return "/v1/projects/" + p.Id + suffix }

func TestProjectsCreateAndRead(t *testing.T) {
	e := newAssetsEnv(t)
	a1, a2 := e.readyAsset("ada", "one.mp4"), e.readyAsset("ada", "two.mp4")

	brief := strings.Repeat("é", 4000)
	var p api.Project
	e.call(http.MethodPost, "/v1/projects", "ada", map[string]any{
		"title": "  Back day  ", "format": "9:16", "asset_ids": []string{a2, a1}, "brief": brief,
	}, http.StatusCreated, &p)
	if !strings.HasPrefix(p.Id, "prj_") || p.Title != "Back day" || p.Format != api.ProjectFormatN916 || p.Brief != brief || p.Status != api.ProjectStatusDraft {
		t.Fatalf("project: %+v", p)
	}
	if p.CurrentTimelineVersion != nil || p.CurrentVersion != nil {
		t.Fatalf("a new project has no timeline: %+v", p)
	}
	if len(p.Assets) != 2 || p.Assets[0].AssetId != a2 || p.Assets[0].Position != 0 || p.Assets[1].AssetId != a1 || p.Assets[1].Position != 1 ||
		!p.Assets[0].Available || p.Assets[0].DurationMs == nil || *p.Assets[0].DurationMs != 87135 || p.Assets[0].Title != "two" {
		t.Fatalf("assets: %+v", p.Assets)
	}

	var got api.Project
	e.call(http.MethodGet, "/v1/projects/"+p.Id, "ada", nil, http.StatusOK, &got)
	if got.Id != p.Id || len(got.Assets) != 2 || got.Assets[0].AssetId != a2 {
		t.Fatalf("GET: %+v", got)
	}
	// A viewer reads too.
	e.call(http.MethodGet, "/v1/projects/"+p.Id, "viewer", nil, http.StatusOK, &got)

	// The default title.
	var untitled api.Project
	e.call(http.MethodPost, "/v1/projects", "ada", map[string]any{"format": "16:9", "asset_ids": []string{a1}}, http.StatusCreated, &untitled)
	if untitled.Title != "Untitled project" || untitled.Brief != "" {
		t.Fatalf("defaults: %+v", untitled)
	}

	var list api.ListProjectsResponse
	e.call(http.MethodGet, "/v1/projects", "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 2 || list.Items[0].Id != untitled.Id || list.Items[1].Id != p.Id || list.Items[1].AssetCount != 2 || list.NextCursor != nil {
		t.Fatalf("list: %+v", list)
	}
	e.call(http.MethodGet, "/v1/projects?limit=1", "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.NextCursor == nil || *list.NextCursor != untitled.Id {
		t.Fatalf("page 1: %+v", list)
	}
	e.call(http.MethodGet, "/v1/projects?limit=1&cursor="+*list.NextCursor, "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].Id != p.Id || list.NextCursor != nil {
		t.Fatalf("page 2: %+v", list)
	}
	e.fail(http.MethodGet, "/v1/projects?limit=0", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodGet, "/v1/projects?cursor=nope", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)

	// Soft delete.
	e.call(http.MethodDelete, "/v1/projects/"+untitled.Id, "ada", nil, http.StatusNoContent, nil)
	e.fail(http.MethodGet, "/v1/projects/"+untitled.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodDelete, "/v1/projects/"+untitled.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.call(http.MethodGet, "/v1/projects", "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].Id != p.Id {
		t.Fatalf("list after delete: %+v", list)
	}
	// The assets stay.
	e.call(http.MethodGet, "/v1/assets/"+a1, "ada", nil, http.StatusOK, nil)
}

func TestProjectsCreateValidation(t *testing.T) {
	e := newAssetsEnv(t)
	a1 := e.readyAsset("ada", "one.mp4")
	bobAsset := e.readyAsset("bob", "bobs.mp4")
	uploading := e.uploadedAsset("ada", "not-ready.mp4")
	image := e.readyAsset("ada", "image.mp4")
	if _, err := e.pool.Exec(context.Background(), `UPDATE assets SET kind = 'image' WHERE id = $1`, mustAsset(t, image).UUID); err != nil {
		t.Fatal(err)
	}

	many := make([]string, 51)
	for i := range many {
		many[i] = randomAssetID()
	}
	bad := func(name string, body map[string]any, status int, code api.ErrorCode) {
		t.Helper()
		t.Run(name, func(*testing.T) { e.fail(http.MethodPost, "/v1/projects", "ada", body, status, code) })
	}
	bad("no assets", map[string]any{"format": "9:16", "asset_ids": []string{}}, 400, api.ErrorCodeInvalidRequest)
	bad("more than 50 assets", map[string]any{"format": "9:16", "asset_ids": many}, 400, api.ErrorCodeInvalidRequest)
	bad("a duplicate", map[string]any{"format": "9:16", "asset_ids": []string{a1, a1}}, 400, api.ErrorCodeInvalidRequest)
	bad("not an asset id", map[string]any{"format": "9:16", "asset_ids": []string{"wks_" + a1[4:]}}, 400, api.ErrorCodeInvalidRequest)
	bad("unknown format", map[string]any{"format": "2:1", "asset_ids": []string{a1}}, 400, api.ErrorCodeInvalidRequest)
	bad("no format", map[string]any{"asset_ids": []string{a1}}, 400, api.ErrorCodeInvalidRequest)
	bad("brief over 4000 characters", map[string]any{"format": "9:16", "asset_ids": []string{a1}, "brief": strings.Repeat("a", 4001)}, 400, api.ErrorCodeInvalidRequest)
	bad("brief with a NUL", map[string]any{"format": "9:16", "asset_ids": []string{a1}, "brief": "a\x00b"}, 400, api.ErrorCodeInvalidRequest)
	bad("blank title", map[string]any{"format": "9:16", "asset_ids": []string{a1}, "title": "   "}, 400, api.ErrorCodeInvalidRequest)
	bad("title over 255 characters", map[string]any{"format": "9:16", "asset_ids": []string{a1}, "title": strings.Repeat("t", 256)}, 400, api.ErrorCodeInvalidRequest)
	bad("a title with a control character", map[string]any{"format": "9:16", "asset_ids": []string{a1}, "title": "a\u0007b"}, 400, api.ErrorCodeInvalidRequest)
	bad("not ready", map[string]any{"format": "9:16", "asset_ids": []string{a1, uploading.Id}}, 422, api.ErrorCodeAssetsNotUsable)
	bad("an image", map[string]any{"format": "9:16", "asset_ids": []string{image}}, 422, api.ErrorCodeAssetsNotUsable)
	bad("an unknown asset", map[string]any{"format": "9:16", "asset_ids": []string{a1, randomAssetID()}}, 404, api.ErrorCodeNotFound)

	// Another workspace's asset is exactly as unknown as one that never existed: the same status, code and message, and
	// it wins over a not-ready asset in the same request (nothing is said about assets of this workspace first).
	other := e.do(http.MethodPost, "/v1/projects", "ada", map[string]any{"format": "9:16", "asset_ids": []string{a1, bobAsset}})
	never := e.do(http.MethodPost, "/v1/projects", "ada", map[string]any{"format": "9:16", "asset_ids": []string{a1, randomAssetID()}})
	if other.Code != http.StatusNotFound || other.Body.String() != never.Body.String() {
		t.Fatalf("another workspace's asset: %d %s, an unknown one: %d %s", other.Code, other.Body, never.Code, never.Body)
	}
	if rec := e.do(http.MethodPost, "/v1/projects", "ada", map[string]any{"format": "9:16", "asset_ids": []string{uploading.Id, bobAsset}}); rec.Code != http.StatusNotFound {
		t.Fatalf("not-ready plus another workspace's asset = %d %s, want 404", rec.Code, rec.Body)
	}
	// The message of a not-ready answer never names the other workspace's asset.
	if rec := e.do(http.MethodPost, "/v1/projects", "ada", map[string]any{"format": "9:16", "asset_ids": []string{uploading.Id}}); !strings.Contains(rec.Body.String(), uploading.Id) {
		t.Fatalf("not-ready answer: %s", rec.Body)
	}

	// Nothing was stored by any of the failures.
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM projects) + (SELECT count(*) FROM project_assets)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left behind: %d, %v", n, err)
	}
}

func mustAsset(t *testing.T, s string) ids.AssetID {
	t.Helper()
	id, err := ids.ParseAssetID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestProjectsTenancyAndRoles(t *testing.T) {
	e := newAssetsEnv(t)
	a1 := e.readyAsset("ada", "one.mp4")
	p := e.newProject("ada", "9:16", a1)
	var v1 api.ApplyTimelineOpsResponse
	e.call(http.MethodPost, projectPath(p, "/timelines"), "ada",
		map[string]any{"base_version": 0, "ops": []map[string]any{addClip(a1, 0, 3000)}}, http.StatusCreated, &v1)

	missing := "/v1/projects/" + ids.Encode(ids.PrefixProject, uuid.Must(uuid.NewV7()))
	for _, c := range []struct {
		method, suffix string
		body           any
	}{
		{http.MethodGet, "", nil},
		{http.MethodDelete, "", nil},
		{http.MethodGet, "/timelines", nil},
		{http.MethodGet, "/timelines/1", nil},
		{http.MethodPost, "/timelines", map[string]any{"base_version": 1, "ops": []map[string]any{{"op": "set_look", "preset": "bw"}}}},
	} {
		t.Run("bob: "+c.method+" "+c.suffix, func(t *testing.T) {
			// Workspace B gets the answer an id that never existed gets: the same status and body.
			theirs := e.do(c.method, "/v1/projects/"+p.Id+c.suffix, "bob", c.body)
			never := e.do(c.method, missing+c.suffix, "bob", c.body)
			if theirs.Code != http.StatusNotFound || theirs.Body.String() != never.Body.String() {
				t.Fatalf("another workspace's project = %d %s; an unknown id = %d %s", theirs.Code, theirs.Body, never.Code, never.Body)
			}
			wantError(t, theirs, http.StatusNotFound, string(api.ErrorCodeNotFound))
		})
	}
	// Bob's list does not hold ada's project, and ada's project is untouched by all of that.
	var list api.ListProjectsResponse
	e.call(http.MethodGet, "/v1/projects", "bob", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Fatalf("bob sees %+v", list.Items)
	}
	var got api.Project
	e.call(http.MethodGet, "/v1/projects/"+p.Id, "ada", nil, http.StatusOK, &got)
	if got.CurrentTimelineVersion == nil || *got.CurrentTimelineVersion != 1 || got.CurrentVersion == nil || got.CurrentVersion.Version != 1 {
		t.Fatalf("ada's project: %+v", got)
	}

	// A viewer reads and cannot write.
	e.call(http.MethodGet, "/v1/projects", "viewer", nil, http.StatusOK, &list)
	e.call(http.MethodGet, projectPath(p, "/timelines"), "viewer", nil, http.StatusOK, nil)
	e.call(http.MethodGet, projectPath(p, "/timelines/1"), "viewer", nil, http.StatusOK, nil)
	e.fail(http.MethodPost, "/v1/projects", "viewer", map[string]any{"format": "9:16", "asset_ids": []string{a1}}, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodDelete, "/v1/projects/"+p.Id, "viewer", nil, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodPost, projectPath(p, "/timelines"), "viewer",
		map[string]any{"base_version": 1, "ops": []map[string]any{{"op": "set_look", "preset": "bw"}}}, http.StatusForbidden, api.ErrorCodeInsufficientRole)

	// No token: 401 everywhere.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/projects"}, {http.MethodGet, "/v1/projects"}, {http.MethodGet, "/v1/projects/" + p.Id},
		{http.MethodDelete, "/v1/projects/" + p.Id}, {http.MethodGet, projectPath(p, "/timelines")},
		{http.MethodPost, projectPath(p, "/timelines")}, {http.MethodGet, projectPath(p, "/timelines/1")},
	} {
		wantError(t, e.do(c.method, c.path, "", nil), http.StatusUnauthorized, string(api.ErrorCodeUnauthenticated))
	}

	// A malformed id is a bad request, not a 404.
	e.fail(http.MethodGet, "/v1/projects/not-an-id", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodGet, "/v1/projects/"+a1, "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest) // an asset id is not a project id
}

func TestTimelineVersions(t *testing.T) {
	e := newAssetsEnv(t)
	a1, a2 := e.readyAsset("ada", "one.mp4"), e.readyAsset("ada", "two.mp4")
	outside := e.readyAsset("ada", "not-in-the-project.mp4")
	bobAsset := e.readyAsset("bob", "bobs.mp4")
	p := e.newProject("ada", "1:1", a1, a2)

	// Version 1 comes from the first edit, on an empty timeline of the project's format.
	var v1 api.ApplyTimelineOpsResponse
	e.call(http.MethodPost, projectPath(p, "/timelines"), "ada",
		map[string]any{"base_version": 0, "ops": []map[string]any{addClip(a1, 1000, 4000), addClip(a2, 0, 2500)}}, http.StatusCreated, &v1)
	if v1.Version.Version != 1 || v1.Version.ParentVersion != nil || v1.Version.AuthorKind != api.AuthorKindUser || !strings.HasPrefix(v1.Version.AuthorId, "usr_") ||
		!strings.HasPrefix(v1.Version.Id, "tl_") || len(v1.Version.Summary) == 0 || len(v1.Created) != 2 || v1.Created[0].Id != "c1" || v1.Created[1].Id != "c2" || len(v1.Version.Issues) != 0 {
		t.Fatalf("version 1: %+v", v1)
	}
	var spec struct {
		Output struct {
			Format        string `json:"format"`
			Width, Height int
		} `json:"output"`
		Tracks []struct {
			ID    string `json:"id"`
			Items []struct {
				ID      string `json:"id"`
				AssetID string `json:"asset_id"`
				AtMs    int    `json:"at_ms"`
			} `json:"items"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(v1.Version.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Output.Format != "1:1" || spec.Tracks[0].ID != "main" || len(spec.Tracks[0].Items) != 2 || spec.Tracks[0].Items[1].AtMs != 3000 || spec.Tracks[0].Items[0].AssetID != a1 {
		t.Fatalf("spec: %s", v1.Version.Spec)
	}
	var ops1 []map[string]any
	if err := json.Unmarshal(v1.Version.Ops, &ops1); err != nil || len(ops1) != 2 {
		t.Fatalf("ops: %s %v", v1.Version.Ops, err)
	}

	// Version 2 builds on 1.
	var v2 api.ApplyTimelineOpsResponse
	e.call(http.MethodPost, projectPath(p, "/timelines"), "ada",
		map[string]any{"base_version": 1, "ops": []map[string]any{{"op": "trim_item", "id": "c1", "out_ms": 3000}}}, http.StatusCreated, &v2)
	if v2.Version.Version != 2 || v2.Version.ParentVersion == nil || *v2.Version.ParentVersion != 1 || len(v2.Version.Summary) == 0 {
		t.Fatalf("version 2: %+v", v2.Version)
	}
	var diff []map[string]any
	if err := json.Unmarshal(v2.Version.Diff, &diff); err != nil || len(diff) == 0 {
		t.Fatalf("diff: %s %v", v2.Version.Diff, err)
	}
	var cur api.Project
	e.call(http.MethodGet, "/v1/projects/"+p.Id, "ada", nil, http.StatusOK, &cur)
	if cur.CurrentTimelineVersion == nil || *cur.CurrentTimelineVersion != 2 || cur.CurrentVersion == nil || cur.CurrentVersion.Id != v2.Version.Id {
		t.Fatalf("project after two edits: %+v", cur)
	}

	t.Run("optimistic concurrency", func(t *testing.T) {
		batch := []map[string]any{{"op": "set_look", "preset": "bw"}}
		e.fail(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 1, "ops": batch}, http.StatusConflict, api.ErrorCodeVersionConflict)
		e.fail(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 0, "ops": batch}, http.StatusConflict, api.ErrorCodeVersionConflict)
		e.fail(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 3, "ops": batch}, http.StatusConflict, api.ErrorCodeVersionConflict)
		e.fail(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": -1, "ops": batch}, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
		var vs api.ListTimelineVersionsResponse
		e.call(http.MethodGet, projectPath(p, "/timelines"), "ada", nil, http.StatusOK, &vs)
		if len(vs.Items) != 2 {
			t.Fatalf("a refused edit stored a version: %+v", vs.Items)
		}
	})

	t.Run("ops can only use the project's own ready footage", func(t *testing.T) {
		for name, asset := range map[string]string{"same workspace, not in the project": outside, "another workspace": bobAsset, "unknown": randomAssetID()} {
			rec := e.do(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 2, "ops": []map[string]any{addClip(asset, 0, 2000)}})
			var rej api.OpsRejected
			if rec.Code != http.StatusUnprocessableEntity || json.Unmarshal(rec.Body.Bytes(), &rej) != nil {
				t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
			}
			found := false
			for _, is := range rej.Issues {
				found = found || (is.Code == "unknown_asset" && is.OpIndex == 0)
			}
			if rej.Error.Code != api.ErrorCodeInvalidOps || !found {
				t.Errorf("%s: %+v", name, rej)
			}
		}
	})

	t.Run("refused batches", func(t *testing.T) {
		var rej api.OpsRejected
		rec := e.do(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 2, "ops": []map[string]any{{"op": "trim_item", "id": "nope", "out_ms": 100}}})
		if rec.Code != 422 || json.Unmarshal(rec.Body.Bytes(), &rej) != nil || len(rej.Issues) == 0 || rej.Issues[0].Code != "unknown_item" || rej.Issues[0].OpIndex != 0 {
			t.Fatalf("unknown item: %d %s", rec.Code, rec.Body)
		}
		rec = e.do(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 2, "ops": []map[string]any{{"op": "frobnicate"}}})
		if rec.Code != 422 || json.Unmarshal(rec.Body.Bytes(), &rej) != nil || rej.Error.Code != api.ErrorCodeInvalidOps || len(rej.Issues) == 0 {
			t.Fatalf("unknown op: %d %s", rec.Code, rec.Body)
		}
		rec = e.do(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 2, "ops": []map[string]any{}})
		if rec.Code != 422 || json.Unmarshal(rec.Body.Bytes(), &rej) != nil || rej.Error.Code != api.ErrorCodeInvalidOps {
			t.Fatalf("empty batch: %d %s", rec.Code, rec.Body)
		}
		// The same trim again changes nothing.
		rec = e.do(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 2, "ops": []map[string]any{{"op": "trim_item", "id": "c1", "out_ms": 3000}}})
		if rec.Code != 422 || json.Unmarshal(rec.Body.Bytes(), &rej) != nil || rej.Error.Code != api.ErrorCodeNoChange {
			t.Fatalf("no change: %d %s", rec.Code, rec.Body)
		}
		// A batch is atomic: the second op fails, so the first is not stored either.
		rec = e.do(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 2, "ops": []map[string]any{
			{"op": "trim_item", "id": "c1", "out_ms": 2500}, {"op": "trim_item", "id": "c9", "out_ms": 100}}})
		if rec.Code != 422 {
			t.Fatalf("atomic: %d %s", rec.Code, rec.Body)
		}
		var vs api.ListTimelineVersionsResponse
		e.call(http.MethodGet, projectPath(p, "/timelines"), "ada", nil, http.StatusOK, &vs)
		if len(vs.Items) != 2 {
			t.Fatalf("versions after refused batches: %d", len(vs.Items))
		}
		// The body must be a JSON object with the two fields.
		e.fail(http.MethodPost, projectPath(p, "/timelines"), "ada", "not json", http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	})

	t.Run("list and read", func(t *testing.T) {
		var vs api.ListTimelineVersionsResponse
		e.call(http.MethodGet, projectPath(p, "/timelines"), "ada", nil, http.StatusOK, &vs)
		if len(vs.Items) != 2 || vs.Items[0].Version != 2 || vs.Items[1].Version != 1 || vs.NextBefore != nil || vs.Items[0].Id != v2.Version.Id {
			t.Fatalf("list: %+v", vs)
		}
		e.call(http.MethodGet, projectPath(p, "/timelines?limit=1"), "ada", nil, http.StatusOK, &vs)
		if len(vs.Items) != 1 || vs.NextBefore == nil || *vs.NextBefore != 2 {
			t.Fatalf("page 1: %+v", vs)
		}
		e.call(http.MethodGet, projectPath(p, fmt.Sprintf("/timelines?limit=1&before=%d", *vs.NextBefore)), "ada", nil, http.StatusOK, &vs)
		if len(vs.Items) != 1 || vs.Items[0].Version != 1 || vs.NextBefore != nil {
			t.Fatalf("page 2: %+v", vs)
		}
		var got api.TimelineVersion
		e.call(http.MethodGet, projectPath(p, "/timelines/1"), "ada", nil, http.StatusOK, &got)
		if string(got.Spec) != string(v1.Version.Spec) || len(got.Issues) != 0 || got.Version != 1 {
			t.Fatalf("version 1 read back: %s vs %s", got.Spec, v1.Version.Spec)
		}
		e.fail(http.MethodGet, projectPath(p, "/timelines/9"), "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
		e.fail(http.MethodGet, projectPath(p, "/timelines/0"), "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
		e.fail(http.MethodGet, projectPath(p, "/timelines?limit=0"), "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	})

	t.Run("a stored version never changes; validation reports what changed around it", func(t *testing.T) {
		// The database refuses to change a version, even for a role that could (the test connects as the superuser).
		_, err := e.pool.Exec(context.Background(), `UPDATE timeline_versions SET summary = '{x}' WHERE project_id = $1`, mustProject(t, p.Id).UUID)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("UPDATE of a version: %v", err)
		}
		// Delete an asset the timeline uses: the version is the same, the issues say the clip's asset is gone.
		e.call(http.MethodDelete, "/v1/assets/"+a2, "ada", nil, http.StatusNoContent, nil)
		var got api.TimelineVersion
		e.call(http.MethodGet, projectPath(p, "/timelines/2"), "ada", nil, http.StatusOK, &got)
		unknown := false
		for _, is := range got.Issues {
			unknown = unknown || is.Code == "unknown_asset"
		}
		if !unknown {
			t.Fatalf("issues after deleting an asset: %+v", got.Issues)
		}
		var again api.TimelineVersion
		e.call(http.MethodGet, projectPath(p, "/timelines/2"), "ada", nil, http.StatusOK, &again)
		if string(again.Spec) != string(v2.Version.Spec) {
			t.Error("the stored spec changed")
		}
		// And the project page shows the asset as gone.
		var pg api.Project
		e.call(http.MethodGet, "/v1/projects/"+p.Id, "ada", nil, http.StatusOK, &pg)
		if len(pg.Assets) != 2 || pg.Assets[1].Available {
			t.Fatalf("assets: %+v", pg.Assets)
		}
		// A new edit that keeps using the deleted asset is refused (unknown_asset); removing the clip is fine.
		rec := e.do(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 2, "ops": []map[string]any{{"op": "set_look", "id": "c1", "preset": "bw"}}})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "unknown_asset") {
			t.Fatalf("edit with a deleted asset in the timeline: %d %s", rec.Code, rec.Body)
		}
		e.call(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 2, "ops": []map[string]any{{"op": "remove_item", "id": "c2"}}}, http.StatusCreated, nil)
	})
}

func mustProject(t *testing.T, s string) ids.ProjectID {
	t.Helper()
	id, err := ids.ParseProjectID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Two edits made for the same version: one wins, the other is told to fetch again, and the chain has no gap or fork.
func TestTimelineEditsRace(t *testing.T) {
	e := newAssetsEnv(t)
	a1 := e.readyAsset("ada", "one.mp4")
	p := e.newProject("ada", "9:16", a1)
	e.call(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 0, "ops": []map[string]any{addClip(a1, 0, 5000)}}, http.StatusCreated, nil)

	const n = 6
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := e.do(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": 1, "ops": []map[string]any{
				{"op": "trim_item", "id": "c1", "out_ms": 4000 - i*100}}})
			codes[i] = rec.Code
		}()
	}
	wg.Wait()
	created, conflicts := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("status codes %v: want one 201 and %d 409", codes, n-1)
	}
	var vs api.ListTimelineVersionsResponse
	e.call(http.MethodGet, projectPath(p, "/timelines"), "ada", nil, http.StatusOK, &vs)
	if len(vs.Items) != 2 || vs.Items[0].Version != 2 || vs.Items[0].ParentVersion == nil || *vs.Items[0].ParentVersion != 1 {
		t.Fatalf("versions: %+v", vs.Items)
	}
}
