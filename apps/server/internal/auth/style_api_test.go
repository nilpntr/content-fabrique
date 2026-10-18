package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
)

func ptrOf[T any](v T) *T { return &v }

// The style starts empty with the defaults; the settings follow the role rules of every workspace setting; one workspace never sees or
// changes another's.
func TestStyleSettingsRolesAndTenancy(t *testing.T) {
	e := newAssetsEnv(t)
	var got api.CreatorStyle
	e.call(http.MethodGet, "/v1/style", "ada", nil, http.StatusOK, &got)
	if got.Status != api.CreatorStyleStatusEmpty || got.Summary != nil || got.Measures != nil || len(got.References) != 0 {
		t.Fatalf("an empty style: %+v", got)
	}
	if got.Effective.Vibe != api.EditVibeLively || got.Effective.Transitions != api.StyleLevelSubtle || got.Effective.Sources.Vibe != api.StyleSettingSourceDefault {
		t.Errorf("the defaults: %+v", got.Effective)
	}
	if got.Limits.MaxReferences != 10 || got.Limits.MaxSizeBytes != 1<<30 || got.Limits.MaxDurationMs != 180_000 {
		t.Errorf("limits %+v", got.Limits)
	}

	body := api.UpdateStyleRequest{Settings: api.StyleSettings{Vibe: ptrOf(api.EditVibeHype), Transitions: ptrOf(api.StyleLevelOff), TitleColour: ptrOf("#f2e9a0")}}
	e.fail(http.MethodPut, "/v1/style", "viewer", body, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodPut, "/v1/style", "", body, http.StatusUnauthorized, api.ErrorCodeUnauthenticated)
	e.call(http.MethodPut, "/v1/style", "editor", body, http.StatusOK, &got)
	if got.Settings.Vibe == nil || *got.Settings.Vibe != api.EditVibeHype || got.Settings.TitleColour == nil || *got.Settings.TitleColour != "#F2E9A0" {
		t.Errorf("settings %+v", got.Settings)
	}
	if got.Effective.Transitions != api.StyleLevelOff || got.Effective.Sources.Transitions != api.StyleSettingSourceYou || got.Effective.CameraMotion != api.StyleLevelLots {
		t.Errorf("effective %+v", got.Effective)
	}
	// A viewer reads it.
	e.call(http.MethodGet, "/v1/style", "viewer", nil, http.StatusOK, &got)
	if got.Settings.Vibe == nil {
		t.Error("the viewer does not see the settings")
	}
	// The structure settings (P6-05c), a title form among them; a form that is not one is refused.
	var structured api.CreatorStyle
	e.call(http.MethodPut, "/v1/style", "ada", `{"settings":{"chapter_titles":"off","title_template":"<N> MOVES FOR <GOAL>","ending_hold":"short","cut_timing":"mid_rep","angle_switches":"on"}}`, http.StatusOK, &structured)
	if structured.Effective.ChapterTitles != api.StyleToggleOff || structured.Effective.TitleTemplate == nil || *structured.Effective.TitleTemplate != "<N> MOVES FOR <GOAL>" ||
		structured.Effective.Sources.TitleTemplate != api.StyleSettingSourceYou || structured.Effective.CutTiming != api.StyleCutTimingMidRep {
		t.Errorf("structure settings: %+v", structured.Effective)
	}
	e.fail(http.MethodPut, "/v1/style", "ada", `{"settings":{"title_template":"<script>x</script>"}}`, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.call(http.MethodPut, "/v1/style", "editor", body, http.StatusOK, nil)
	// Bad values are refused.
	e.fail(http.MethodPut, "/v1/style", "ada", `{"settings":{"title_colour":"red"}}`, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodPut, "/v1/style", "ada", `{"settings":{"vibe":"frantic"}}`, http.StatusBadRequest, api.ErrorCodeInvalidRequest)

	// Bob's workspace has nothing of Ada's, and his changes stay his.
	var bobs api.CreatorStyle
	e.call(http.MethodGet, "/v1/style", "bob", nil, http.StatusOK, &bobs)
	if bobs.Settings.Vibe != nil || bobs.Effective.Vibe != api.EditVibeLively {
		t.Errorf("bob sees ada's settings: %+v", bobs.Settings)
	}
	e.call(http.MethodPut, "/v1/style", "bob", api.UpdateStyleRequest{Settings: api.StyleSettings{Vibe: ptrOf(api.EditVibeCalm)}}, http.StatusOK, nil)
	var adas api.CreatorStyle
	e.call(http.MethodGet, "/v1/style", "ada", nil, http.StatusOK, &adas)
	if adas.Settings.Vibe == nil || *adas.Settings.Vibe != api.EditVibeHype {
		t.Errorf("bob changed ada's vibe: %+v", adas.Settings)
	}

	// Reset: owners and editors only, and only their own.
	e.fail(http.MethodDelete, "/v1/style", "viewer", nil, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	if rec := e.do(http.MethodDelete, "/v1/style", "ada", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	var reset api.CreatorStyle
	e.call(http.MethodGet, "/v1/style", "ada", nil, http.StatusOK, &reset)
	if reset.Settings.Vibe != nil || reset.Status != api.CreatorStyleStatusEmpty {
		t.Errorf("after the reset: %+v", reset)
	}
	var bobAfter api.CreatorStyle
	e.call(http.MethodGet, "/v1/style", "bob", nil, http.StatusOK, &bobAfter)
	if bobAfter.Settings.Vibe == nil || *bobAfter.Settings.Vibe != api.EditVibeCalm {
		t.Errorf("ada's reset reached bob: %+v", bobAfter.Settings)
	}
}

func referenceRequest(name string, size int64, contentType string) api.CreateAssetRequest {
	p := api.AssetPurposeStyleReference
	return api.CreateAssetRequest{Filename: name, SizeBytes: size, ContentType: contentType, Purpose: &p}
}

// Reference videos go through the upload path with their own limits; they are not footage (not in the library, not usable in a
// project); removing one is the owner workspace's alone.
func TestStyleReferencesQuotaLibraryAndTenancy(t *testing.T) {
	e := newAssetsEnv(t)
	ctx := context.Background()
	e.fail(http.MethodPost, "/v1/assets", "ada", referenceRequest("pose.jpg", 1000, "image/jpeg"), http.StatusUnsupportedMediaType, api.ErrorCodeUnsupportedContentType)
	e.fail(http.MethodPost, "/v1/assets", "ada", referenceRequest("huge.mp4", 1<<30+1, "video/mp4"), http.StatusRequestEntityTooLarge, api.ErrorCodeFileTooLarge)
	e.fail(http.MethodPost, "/v1/assets", "viewer", referenceRequest("a.mp4", 1000, "video/mp4"), http.StatusForbidden, api.ErrorCodeInsufficientRole)

	var first api.CreateAssetResponse
	for i := range 10 {
		var out api.CreateAssetResponse
		e.call(http.MethodPost, "/v1/assets", "ada", referenceRequest("ref.mp4", 1000, "video/mp4"), http.StatusCreated, &out)
		if out.Asset.Purpose == nil || *out.Asset.Purpose != api.AssetPurposeStyleReference {
			t.Fatalf("purpose %+v", out.Asset.Purpose)
		}
		if i == 0 {
			first = out
		}
	}
	e.fail(http.MethodPost, "/v1/assets", "ada", referenceRequest("ref.mp4", 1000, "video/mp4"), http.StatusConflict, api.ErrorCodeTooManyReferences)
	// Bob has his own ten, and footage is not limited by them.
	e.call(http.MethodPost, "/v1/assets", "bob", referenceRequest("ref.mp4", 1000, "video/mp4"), http.StatusCreated, nil)
	footage := e.create("ada", "clip.mp4", 1000)

	// The library lists the footage only; the references have their own list and the style lists them.
	var lib api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets", "ada", nil, http.StatusOK, &lib)
	if len(lib.Items) != 1 || lib.Items[0].Id != footage.Asset.Id {
		t.Fatalf("the library lists %d assets", len(lib.Items))
	}
	e.call(http.MethodGet, "/v1/assets?purpose=style_reference", "ada", nil, http.StatusOK, &lib)
	if len(lib.Items) != 10 {
		t.Fatalf("%d references listed", len(lib.Items))
	}
	var st api.CreatorStyle
	e.call(http.MethodGet, "/v1/style", "ada", nil, http.StatusOK, &st)
	if len(st.References) != 10 || st.References[0].Status != api.StyleReferenceStatusUploading || st.Status != api.CreatorStyleStatusLearning || st.Progress.Total != 10 {
		t.Fatalf("style %+v", st)
	}

	// A reference is never footage: a project that names one (ready, so only its purpose stands in the way) is refused.
	refID, err := ids.ParseAssetID(first.Asset.Id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE assets SET status = 'ready', duration_ms = 20000 WHERE id = $1`, refID.UUID); err != nil {
		t.Fatal(err)
	}
	e.fail(http.MethodPost, "/v1/projects", "ada", api.CreateProjectRequest{Format: "9:16", AssetIds: []string{first.Asset.Id}}, http.StatusUnprocessableEntity, api.ErrorCodeAssetsNotUsable)

	// Removing: another workspace's reference is not found, a viewer may not, footage is not a reference, the owner may.
	path := "/v1/style/references/" + first.Asset.Id
	e.fail(http.MethodDelete, path, "bob", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodDelete, path, "viewer", nil, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodDelete, "/v1/style/references/"+footage.Asset.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodDelete, "/v1/style/references/nonsense", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	if rec := e.do(http.MethodDelete, path, "ada", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if _, err := e.st.GetAsset(ctx, e.ws["ada"], refID); err == nil {
		t.Error("the removed reference is still an asset")
	}
	e.fail(http.MethodDelete, path, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	var jobsN int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'style_profile' AND args->>'workspace_id' = $1`, e.ws["ada"].String()).Scan(&jobsN); err != nil || jobsN != 1 {
		t.Errorf("%d style_profile jobs after a removal (%v)", jobsN, err)
	}
	// A place is free again.
	e.call(http.MethodPost, "/v1/assets", "ada", referenceRequest("ref.mp4", 1000, "video/mp4"), http.StatusCreated, nil)

	// A reference is not deleted as an asset: its measures would stay in the style.
	second := lib.Items[1].Id
	e.fail(http.MethodDelete, "/v1/assets/"+second, "ada", nil, http.StatusConflict, api.ErrorCodeStyleReference)
	if _, err := e.st.GetAsset(ctx, e.ws["ada"], mustAsset(t, second)); err != nil {
		t.Errorf("the refused delete removed the reference: %v", err)
	}

	// Learning again is queued for the workspace alone, once at a time: while a reference is measured, another pass is refused.
	if _, err := e.pool.Exec(ctx, `UPDATE assets SET status = 'ready', duration_ms = 20000 WHERE id = $1`, mustAsset(t, second).UUID); err != nil {
		t.Fatal(err)
	}
	e.fail(http.MethodPost, "/v1/style/relearn", "viewer", nil, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	if rec := e.do(http.MethodPost, "/v1/style/relearn", "ada", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("relearn: %d %s", rec.Code, rec.Body)
	}
	countJobs := func(kind string) int {
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'workspace_id' = $2`, kind, e.ws["ada"].String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countJobs("style_reference"); n != 1 {
		t.Errorf("%d style_reference jobs after one pass", n)
	}
	e.fail(http.MethodPost, "/v1/style/relearn", "ada", nil, http.StatusConflict, api.ErrorCodeStyleLearningInProgress)
	if n := countJobs("style_reference"); n != 1 {
		t.Errorf("a refused pass queued work: %d style_reference jobs", n)
	}
	// When it is measured (here: failed) the next pass may start, up to the day's cap (3 without a price list); Bob's are his own.
	finish := func() {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE style_references SET status = 'failed' WHERE workspace_id = $1`, e.ws["ada"].UUID); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		finish()
		if rec := e.do(http.MethodPost, "/v1/style/relearn", "ada", nil); rec.Code != http.StatusAccepted {
			t.Fatalf("relearn: %d %s", rec.Code, rec.Body)
		}
	}
	finish()
	e.fail(http.MethodPost, "/v1/style/relearn", "ada", nil, http.StatusTooManyRequests, api.ErrorCodeTooManyRelearns)
	if rec := e.do(http.MethodPost, "/v1/style/relearn", "bob", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("bob's relearn: %d %s", rec.Code, rec.Body)
	}

	// Resetting deletes every reference of the workspace and nothing of Bob's.
	if rec := e.do(http.MethodDelete, "/v1/style", "ada", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	if n, err := e.st.CountStyleReferenceAssets(ctx, e.ws["ada"]); err != nil || n != 0 {
		t.Errorf("%d references after the reset (%v)", n, err)
	}
	if n, err := e.st.CountStyleReferenceAssets(ctx, e.ws["bob"]); err != nil || n != 1 {
		t.Errorf("bob has %d references after ada's reset (%v)", n, err)
	}
	if _, err := e.st.GetAsset(ctx, e.ws["ada"], mustAsset(t, footage.Asset.Id)); err != nil {
		t.Errorf("the reset took the footage: %v", err)
	}
}

// A run's vibe is checked and kept in its input.
func TestStartEditTakesAVibe(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true })
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	path := editPath(p)
	e.fail(http.MethodPost, path, "ada", `{"vibe":"frantic"}`, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	var run api.Run
	e.call(http.MethodPost, path, "ada", api.StartEditRequest{Vibe: ptrOf(api.EditVibeHype)}, http.StatusAccepted, &run)
	id, err := ids.Decode(ids.PrefixRun, run.Id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.st.GetAgentRun(context.Background(), e.ws["ada"], id)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Vibe string `json:"vibe"`
	}
	if err := json.Unmarshal(r.Input, &in); err != nil || in.Vibe != "hype" {
		t.Errorf("input %s", r.Input)
	}
}
