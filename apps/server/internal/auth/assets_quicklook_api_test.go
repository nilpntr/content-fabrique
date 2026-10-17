package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// quickLooked makes a ready asset of who with the quick look's result on it, the way the worker leaves it.
func (e *assetsEnv) quickLooked(who, filename string, r *store.QuickLookResult) api.Asset {
	e.t.Helper()
	a := e.uploadedAsset(who, filename)
	e.ingested(who, a)
	id, err := ids.ParseAssetID(a.Id)
	if err != nil {
		e.t.Fatal(err)
	}
	if r != nil {
		if _, err := e.st.ApplyQuickLook(context.Background(), e.ws[who], id, *r); err != nil {
			e.t.Fatal(err)
		}
	}
	var out api.Asset
	e.call(http.MethodGet, "/v1/assets/"+a.Id, who, nil, http.StatusOK, &out)
	return out
}

func (e *assetsEnv) search(who, q string) []api.Asset {
	e.t.Helper()
	var out api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets?q="+url.QueryEscape(q), who, nil, http.StatusOK, &out)
	return out.Items
}

func titles(items []api.Asset) string {
	var t []string
	for _, a := range items {
		t = append(t, a.Title)
	}
	return strings.Join(t, " | ")
}

func TestAssetsSearch(t *testing.T) {
	e := newAssetsEnv(t)
	rows := e.quickLooked("ada", "IMG_0001.mp4", &store.QuickLookResult{
		Title: "Barbell row", Description: "Bent-over barbell rows in a gym, filmed from the side.", Tags: []string{"barbell row", "back", "lats"},
	})
	posing := e.quickLooked("ada", "IMG_0002.mp4", &store.QuickLookResult{
		Title: "Front double biceps", Description: "A competitor hits front poses on stage.", Tags: []string{"posing", "stage"},
	})
	raw := e.quickLooked("ada", "Leg press session.mp4", nil) // no quick look: found by its file name
	theirs := e.quickLooked("bob", "Rows.mp4", &store.QuickLookResult{Title: "Seated cable row", Description: "Cable rows.", Tags: []string{"cable"}})

	// What the API says about the quick look.
	if deref(rows.TitleSource) != api.TitleSource(store.TitleFromQuickLook) || rows.QuickLookStatus == nil || *rows.QuickLookStatus != api.QuickLookStatusSucceeded ||
		rows.Description == "" || len(rows.Tags) != 3 {
		t.Errorf("a quick-looked asset = %+v", rows)
	}
	if deref(raw.TitleSource) != api.TitleSource(store.TitleFromFilename) || raw.Title != "Leg press session" || raw.QuickLookStatus == nil || *raw.QuickLookStatus != api.QuickLookStatusPending {
		t.Errorf("an asset whose quick look has not run = %+v (status %v)", raw, raw.QuickLookStatus)
	}

	for q, want := range map[string]string{
		"row":                    rows.Id,   // the criterion: a search for "row" finds the clip
		"ROWS":                   rows.Id,   // case and stemming
		"barb":                   rows.Id,   // a prefix, as typed
		"lats":                   rows.Id,   // a tag
		"filmed side":            rows.Id,   // words of the description, in any order
		"gym row":                rows.Id,   // every word must match
		"posing":                 posing.Id, // another clip's tag
		"stage":                  posing.Id,
		"leg press":              raw.Id,    // the title is the file name
		"IMG_0002":               posing.Id, // the original file name, which the quick look's title replaced
		"img":                    "",        // two clips share the prefix: checked below
		"row' OR 1=1 --":         "",        // text that means nothing but words
		"squat":                  "",
		"row squat":              "",
		"&|!:*()<>":              "",
		"\x00row":                "",
		"':* & ( | ! ) <-> 'row": "",
	} {
		got := e.search("ada", q)
		switch want {
		case "":
			// "img" finds the two IMG_ clips; the others must not find anything or must not crash.
			if q == "img" && len(got) != 2 {
				t.Errorf("q=%q found %d assets, want 2: %s", q, len(got), titles(got))
			}
			if q == "row' OR 1=1 --" || q == "':* & ( | ! ) <-> 'row" {
				if len(got) > 1 {
					t.Errorf("q=%q found %d assets: %s", q, len(got), titles(got))
				}
			}
			if q == "squat" || q == "row squat" || q == "&|!:*()<>" {
				if q == "&|!:*()<>" {
					if len(got) != 3 {
						t.Errorf("a query without any word lists everything, got %d", len(got))
					}
				} else if len(got) != 0 {
					t.Errorf("q=%q found %s", q, titles(got))
				}
			}
		default:
			if len(got) != 1 || got[0].Id != want {
				t.Errorf("q=%q found [%s], want %s", q, titles(got), want)
			}
		}
	}

	// Tenancy: bob's "row" is bob's, and ada's clips are invisible to him.
	if got := e.search("bob", "row"); len(got) != 1 || got[0].Id != theirs.Id {
		t.Errorf("bob's search for row = [%s]", titles(got))
	}
	if got := e.search("bob", "barbell"); len(got) != 0 {
		t.Errorf("bob found ada's clip: [%s]", titles(got))
	}
	if got := e.search("ada", "cable"); len(got) != 0 {
		t.Errorf("ada found bob's clip: [%s]", titles(got))
	}

	// The same cursor pagination under a search: newest first, one at a time.
	var page api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets?q=img&limit=1", "ada", nil, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].Id != posing.Id || page.NextCursor == nil {
		t.Fatalf("first page = %+v", page)
	}
	var page2 api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets?q=img&limit=1&cursor="+*page.NextCursor, "ada", nil, http.StatusOK, &page2)
	if len(page2.Items) != 1 || page2.Items[0].Id != rows.Id || page2.NextCursor != nil {
		t.Fatalf("second page = %+v", page2)
	}

	// A query that is too long is a bad request, not a slow search.
	e.fail(http.MethodGet, "/v1/assets?q="+strings.Repeat("a", 101), "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	if got := e.search("ada", strings.Repeat("a", 100)); len(got) != 0 {
		t.Errorf("a 100 character query found %d", len(got))
	}
	// Viewers search too; no token does not.
	e.call(http.MethodGet, "/v1/assets?q=row", "viewer", nil, http.StatusOK, nil)
	e.fail(http.MethodGet, "/v1/assets?q=row", "", nil, http.StatusUnauthorized, api.ErrorCodeUnauthenticated)
}

func TestAssetsRenameTitle(t *testing.T) {
	e := newAssetsEnv(t)
	a := e.quickLooked("ada", "IMG_0042.mp4", &store.QuickLookResult{Title: "Barbell row", Description: "Rows in a gym.", Tags: []string{"back"}})
	b := e.quickLooked("bob", "bobs.mp4", nil)
	path := "/v1/assets/" + a.Id

	var got api.Asset
	e.call(http.MethodPatch, path, "ada", api.UpdateAssetRequest{Title: "  Gino's   $100 <b>heavy</b>\t[rows] \xe2\x80\xae\x00 "}, http.StatusOK, &got)
	if got.Title != "Gino's $100 <b>heavy</b> [rows]" || deref(got.TitleSource) != api.TitleSource(store.TitleFromUser) || got.Id != a.Id || got.Description != "Rows in a gym." {
		t.Fatalf("after the edit: %+v", got)
	}
	// A title is the user's text: typed punctuation stays, invisible and control characters go.
	if strings.ContainsAny(got.Title, "\t\x00\u202e") {
		t.Errorf("control or format characters survived in %q", got.Title)
	}
	var fetched api.Asset
	e.call(http.MethodGet, path, "ada", nil, http.StatusOK, &fetched)
	if fetched.Title != got.Title || deref(fetched.TitleSource) != deref(got.TitleSource) {
		t.Errorf("GET after PATCH = %+v", fetched)
	}

	// The quick look run again does not take the title back, and the new title is searchable.
	id, _ := ids.ParseAssetID(a.Id)
	if _, err := e.st.ApplyQuickLook(context.Background(), e.ws["ada"], id, store.QuickLookResult{Title: "Overhead press", Description: "Pressing.", Tags: []string{"shoulders"}}); err != nil {
		t.Fatal(err)
	}
	e.call(http.MethodGet, path, "ada", nil, http.StatusOK, &fetched)
	if fetched.Title != got.Title || fetched.Description != "Pressing." {
		t.Errorf("a later quick look: %+v", fetched)
	}
	if found := e.search("ada", "gino"); len(found) != 1 || found[0].Id != a.Id {
		t.Errorf("search for the new title found [%s]", titles(found))
	}

	// Bad titles: nothing left after cleaning, too long, wrong shape.
	for name, body := range map[string]any{
		"blank":          api.UpdateAssetRequest{Title: "   "},
		"only invisible": api.UpdateAssetRequest{Title: "\u200b\x00 \t"},
		"too long":       api.UpdateAssetRequest{Title: strings.Repeat("t", 256)},
		"no body":        nil,
		"no title":       "{}",
		"wrong type":     `{"title": 5}`,
		"not JSON":       "title=x",
	} {
		rec := e.do(http.MethodPatch, path, "ada", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: PATCH = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	e.call(http.MethodGet, path, "ada", nil, http.StatusOK, &fetched)
	if fetched.Title != got.Title {
		t.Errorf("a rejected edit changed the title to %q", fetched.Title)
	}
	e.fail(http.MethodPatch, "/v1/assets/not-an-id", "ada", api.UpdateAssetRequest{Title: "x"}, http.StatusBadRequest, api.ErrorCodeInvalidRequest)

	// Tenancy and roles: another workspace sees a 404 and changes nothing; a viewer may look and not edit; no token is a 401.
	e.fail(http.MethodPatch, path, "bob", api.UpdateAssetRequest{Title: "stolen"}, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodPatch, "/v1/assets/"+b.Id, "ada", api.UpdateAssetRequest{Title: "stolen"}, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodPatch, path, "viewer", api.UpdateAssetRequest{Title: "viewed"}, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodPatch, path, "", api.UpdateAssetRequest{Title: "anon"}, http.StatusUnauthorized, api.ErrorCodeUnauthenticated)
	e.call(http.MethodGet, path, "ada", nil, http.StatusOK, &fetched)
	e.call(http.MethodGet, "/v1/assets/"+b.Id, "bob", nil, http.StatusOK, &got)
	if fetched.Title != "Gino's $100 <b>heavy</b> [rows]" || got.Title != "bobs" || deref(got.TitleSource) != api.TitleSource(store.TitleFromFilename) {
		t.Errorf("a refused edit changed something: ada's %q, bob's %q (%v)", fetched.Title, got.Title, got.TitleSource)
	}

	// A deleted asset is gone to PATCH as to everything else; without a plan the paywall answers first.
	e.call(http.MethodDelete, "/v1/assets/"+b.Id, "bob", nil, http.StatusNoContent, nil)
	e.fail(http.MethodPatch, "/v1/assets/"+b.Id, "bob", api.UpdateAssetRequest{Title: "ghost"}, http.StatusNotFound, api.ErrorCodeNotFound)
	if _, err := e.st.RevokePlan(context.Background(), store.RevokeInput{WorkspaceID: e.ws["ada"], GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	e.fail(http.MethodPatch, path, "ada", api.UpdateAssetRequest{Title: "paywalled"}, http.StatusPaymentRequired, api.ErrorCodePlanRequired)
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
