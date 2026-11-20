package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// proofVersion adds version 3 to the annotation fixture: a real spec with two shots of the fixture's clip (12 to 15.5 s, then 30 to 33 s).
func (e *assetsEnv) proofVersion(who string, fa annotationFixture) {
	e.t.Helper()
	spec := timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"},
		Tracks: []timeline.Track{&timeline.VideoTrack{ID: timeline.MainTrackID, Items: []timeline.VideoItem{
			{ID: "c1", AssetID: fa.asset.String(), AtMs: 0, Source: timeline.Source{InMs: 12_000, OutMs: 15_500}},
			{ID: "c2", AssetID: fa.asset.String(), AtMs: 3_500, Source: timeline.Source{InMs: 30_000, OutMs: 33_000}},
		}}},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		e.t.Fatal(err)
	}
	// The clip is ready (the fixture leaves it uploading), so it is in the project's catalog.
	if _, err := e.pool.Exec(context.Background(), `UPDATE assets SET status = 'ready' WHERE id = $1`, fa.asset.UUID); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.AddTimelineVersion(context.Background(), e.ws[who], fa.project, store.NewTimelineVersion{
		Version: 3, Parent: 2, Spec: raw, AuthorKind: store.AuthorUser, AuthorID: "usr_x",
	}); err != nil {
		e.t.Fatal(err)
	}
}

func TestClipFeedbackAPI(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.noStorage = true })
	fa := e.annotationContent("ada")
	e.proofVersion("ada", fa)
	fb := e.annotationContent("bob")
	shots := "/v1/projects/" + fa.project.String() + "/timelines/3/shots"

	var list api.ShotList
	e.call(http.MethodGet, shots, "viewer", nil, http.StatusOK, &list)
	if len(list.Shots) != 2 || list.Shots[0].Number != 1 || list.Shots[1].SourceInMs != 30_000 || list.Shots[0].AssetTitle == nil || len(list.Shots[0].Feedback) != 0 {
		t.Fatalf("shot list: %+v", list)
	}

	// "Never use shot 1 again" and "shot 2 two seconds later", by an editor.
	var never, later api.ClipFeedback
	e.call(http.MethodPost, "/v1/clip-feedback", "editor", map[string]any{
		"project_id": fa.project.String(), "timeline_version": 3, "shot_number": 1, "kind": "never_use", "note": " blurry ",
	}, http.StatusOK, &never)
	if never.AssetId != fa.asset.String() || never.StartMs != 12_000 || never.EndMs != 15_500 || never.Note != "blurry" || never.GivenOn.ShotNumber != 1 || never.GivenOn.ItemId != "c1" {
		t.Fatalf("never use: %+v", never)
	}
	e.call(http.MethodPost, "/v1/clip-feedback", "ada", map[string]any{
		"project_id": fa.project.String(), "timeline_version": 3, "shot_number": 2, "kind": "start_later", "offset_ms": 2000,
	}, http.StatusOK, &later)
	if later.Kind != api.ClipFeedbackKindStartLater || later.StartMs != 30_000 || later.EndMs != 32_000 {
		t.Fatalf("start later: %+v", later)
	}
	e.call(http.MethodGet, shots, "viewer", nil, http.StatusOK, &list)
	if len(list.Shots[0].Feedback) != 1 || list.Shots[0].Feedback[0].Id != never.Id || len(list.Shots[1].Feedback) != 1 {
		t.Errorf("the shot list does not show the feedback: %+v", list.Shots)
	}
	var all api.ListClipFeedbackResponse
	e.call(http.MethodGet, "/v1/clip-feedback?asset_id="+fa.asset.String(), "viewer", nil, http.StatusOK, &all)
	if len(all.Items) != 2 {
		t.Errorf("list: %+v", all)
	}

	// What is refused.
	give := func(body map[string]any) map[string]any {
		out := map[string]any{"project_id": fa.project.String(), "timeline_version": 3, "shot_number": 1, "kind": "never_use"}
		for k, v := range body {
			out[k] = v
		}
		return out
	}
	e.fail(http.MethodPost, "/v1/clip-feedback", "viewer", give(nil), http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodPost, "/v1/clip-feedback", "ada", give(map[string]any{"kind": "start_later"}), http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodPost, "/v1/clip-feedback", "ada", give(map[string]any{"offset_ms": 2000}), http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodPost, "/v1/clip-feedback", "ada", give(map[string]any{"shot_number": 9}), http.StatusNotFound, api.ErrorCodeNotFound)
	// Another workspace's project, version or feedback is a 404, the same as one that does not exist.
	e.fail(http.MethodPost, "/v1/clip-feedback", "bob", give(nil), http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodGet, shots, "bob", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodGet, "/v1/projects/"+fb.project.String()+"/timelines/9/shots", "bob", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodDelete, "/v1/clip-feedback/"+never.Id, "bob", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.call(http.MethodGet, "/v1/clip-feedback", "bob", nil, http.StatusOK, &all)
	if len(all.Items) != 0 {
		t.Errorf("bob sees ada's feedback: %+v", all)
	}

	// Taken back.
	e.fail(http.MethodDelete, "/v1/clip-feedback/"+never.Id, "viewer", nil, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.call(http.MethodDelete, "/v1/clip-feedback/"+never.Id, "editor", nil, http.StatusNoContent, nil)
	e.fail(http.MethodDelete, "/v1/clip-feedback/"+ids.Encode(ids.PrefixClipFeedback, uuid.Must(uuid.NewV7())), "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.call(http.MethodGet, "/v1/clip-feedback?limit="+strconv.Itoa(10), "ada", nil, http.StatusOK, &all)
	if len(all.Items) != 1 || all.Items[0].Id != later.Id {
		t.Errorf("after the delete: %+v", all)
	}

	// Proof edits are a style setting, off unless the creator turns them on.
	var st api.CreatorStyle
	e.call(http.MethodPatch, "/v1/style", "ada", api.PatchStyleRequest{Settings: &api.StyleSettings{ProofEdits: ptrOf(api.StyleToggleOn)}}, http.StatusOK, &st)
	if st.Effective.ProofEdits != api.StyleToggleOn || st.Effective.Sources.ProofEdits != api.StyleSettingSourceYou {
		t.Errorf("proof edits on: %+v", st.Effective)
	}
	e.call(http.MethodGet, "/v1/style", "bob", nil, http.StatusOK, &st)
	if st.Effective.ProofEdits != api.StyleToggleOff || st.Effective.Sources.ProofEdits != api.StyleSettingSourceDefault {
		t.Errorf("proof edits for another creator: %+v", st.Effective)
	}
}
