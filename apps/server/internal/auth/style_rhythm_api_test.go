package auth

import (
	"net/http"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
)

// The personal rhythm rules (cut_grid, shot_reuse) are stored by PUT and come back from GET, and a PUT that sends back what the client
// read keeps them (the handler once dropped fields it did not know, and PUT replaces the stored settings).
func TestStyleSettingsKeepTheRhythmRules(t *testing.T) {
	e := newAssetsEnv(t)
	var got api.CreatorStyle
	e.call(http.MethodPut, "/v1/style", "ada", `{"settings":{"cut_grid":"fixed_2","shot_reuse":"once"}}`, http.StatusOK, &got)
	if got.Settings.CutGrid == nil || *got.Settings.CutGrid != api.StyleCutGridFixed2 || got.Settings.ShotReuse == nil || *got.Settings.ShotReuse != api.StyleShotReuseOnce {
		t.Fatalf("the rules after PUT: %+v", got.Settings)
	}
	var again api.CreatorStyle
	e.call(http.MethodGet, "/v1/style", "ada", nil, http.StatusOK, &again)
	if again.Settings.CutGrid == nil || *again.Settings.CutGrid != api.StyleCutGridFixed2 || again.Settings.ShotReuse == nil || *again.Settings.ShotReuse != api.StyleShotReuseOnce {
		t.Fatalf("the rules after GET: %+v", again.Settings)
	}
	// The client sends back what it read, with one change (as the web app does): the rules stay.
	again.Settings.Vibe = ptrOf(api.EditVibeCalm)
	var kept api.CreatorStyle
	e.call(http.MethodPut, "/v1/style", "ada", api.UpdateStyleRequest{Settings: again.Settings}, http.StatusOK, &kept)
	if kept.Settings.CutGrid == nil || *kept.Settings.CutGrid != api.StyleCutGridFixed2 || kept.Settings.ShotReuse == nil || *kept.Settings.ShotReuse != api.StyleShotReuseOnce {
		t.Errorf("the rules after a PUT that names them: %+v", kept.Settings)
	}
	e.fail(http.MethodPut, "/v1/style", "ada", `{"settings":{"cut_grid":"fixed_3"}}`, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
}
