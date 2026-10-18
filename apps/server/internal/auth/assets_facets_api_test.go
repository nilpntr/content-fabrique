package auth

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/taxonomy"
)

// labelled is a quick look result with labels from the vocabulary (and their search words, as the quick look writes them).
func labelled(title, exercise string, facets ...string) *store.QuickLookResult {
	tax := taxonomy.Default()
	return &store.QuickLookResult{Title: title, Description: "A clip of " + strings.ToLower(title) + ".", Exercise: exercise, Facets: facets,
		FacetWords: tax.SearchText(tax.Normalize(facets))}
}

func (e *assetsEnv) list(who, query string) []api.Asset {
	e.t.Helper()
	var out api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets?"+query, who, nil, http.StatusOK, &out)
	return out.Items
}

func (e *assetsEnv) facetCounts(who, query string) map[string]string {
	e.t.Helper()
	var out api.ListAssetFacetsResponse
	e.call(http.MethodGet, "/v1/assets/facets?"+query, who, nil, http.StatusOK, &out)
	got := map[string]string{}
	for _, f := range out.Facets {
		var parts []string
		for _, v := range f.Values {
			parts = append(parts, v.Value+"="+strconv.Itoa(v.Count))
		}
		got[f.Facet] = f.Label + ": " + strings.Join(parts, " ")
	}
	return got
}

func TestAssetsFacetFiltersAndCounts(t *testing.T) {
	e := newAssetsEnv(t)
	row := e.quickLooked("ada", "a.mp4", labelled("Seated cable row", "seated cable row", "type:lift", "body:back", "movement:row", "angle:side", "equipment:cable"))
	pull := e.quickLooked("ada", "b.mp4", labelled("Lat pulldown", "lat pulldown", "type:lift", "body:back", "movement:pull", "angle:back", "equipment:cable"))
	pose := e.quickLooked("ada", "c.mp4", labelled("Back double biceps", "back double biceps", "type:pose", "body:back", "angle:back"))
	curl := e.quickLooked("ada", "d.mp4", labelled("Hammer curl", "hammer curl", "type:lift", "type:slow_motion", "body:arms", "movement:curl", "angle:front", "equipment:dumbbell"))
	raw := e.quickLooked("ada", "e.mp4", nil)
	e.quickLooked("bob", "f.mp4", labelled("Squat", "back squat", "type:lift", "body:legs"))

	// The asset carries its labels, with the creator's words, and its exercise; never the old tags.
	if row.Exercise != "seated cable row" || len(row.Facets) != 5 || row.Facets[0] != (api.AssetFacet{Facet: "type", Value: "lift", Label: "Lifting"}) ||
		row.Facets[3] != (api.AssetFacet{Facet: "angle", Value: "side", Label: "Side view"}) {
		t.Errorf("asset labels = %q %+v", row.Exercise, row.Facets)
	}
	if raw.Facets == nil || len(raw.Facets) != 0 || raw.Exercise != "" {
		t.Errorf("an unlabelled asset = %q %+v (an empty list, not null)", raw.Exercise, raw.Facets)
	}

	ids := func(items []api.Asset) []string {
		var out []string
		for _, a := range items {
			out = append(out, a.Id)
		}
		slices.Sort(out)
		return out
	}
	sorted := func(s ...string) []string { slices.Sort(s); return s }
	for q, want := range map[string][]string{
		"body=back":                       sorted(row.Id, pull.Id, pose.Id),
		"body=back&type=lift":             sorted(row.Id, pull.Id),
		"body=back&angle=back":            sorted(pull.Id, pose.Id),
		"type=slow_motion":                sorted(curl.Id),
		"type=lift&angle=side":            sorted(row.Id),
		"equipment=cable&movement=pull":   sorted(pull.Id),
		"body=legs":                       nil, // bob's clip is bob's
		"q=back+double&type=pose":         sorted(pose.Id),
		"q=side+view":                     sorted(row.Id), // a label's words
		"q=posing":                        sorted(pose.Id),
		"q=seated+cable":                  sorted(row.Id), // the exercise
		"q=lifting&body=back&angle=front": nil,
	} {
		if got := ids(e.list("ada", q)); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", q, got, want)
		}
	}

	// A value or a facet the vocabulary does not have is a bad request, not an empty library.
	e.fail(http.MethodGet, "/v1/assets?body=elbow", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodGet, "/v1/assets?angle=rear+view", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodGet, "/v1/assets/facets?type=gym", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)

	// The counts: only the values the clips have, in the vocabulary's order.
	got := e.facetCounts("ada", "")
	for facet, want := range map[string]string{
		"type":      "Type: lift=3 pose=1 slow_motion=1",
		"body":      "Body part: back=3 arms=1",
		"movement":  "Movement: pull=1 row=1 curl=1",
		"angle":     "Angle: front=1 side=1 back=2",
		"equipment": "Equipment: cable=2 dumbbell=1",
	} {
		if got[facet] != want {
			t.Errorf("counts %s = %q, want %q", facet, got[facet], want)
		}
	}
	// A filter narrows the other facets' counts, never its own: with body=back the body counts still offer arms.
	got = e.facetCounts("ada", "body=back")
	if got["body"] != "Body part: back=3 arms=1" || got["type"] != "Type: lift=2 pose=1" || got["angle"] != "Angle: side=1 back=2" {
		t.Errorf("counts with body=back = %v", got)
	}
	// And the search words narrow them all.
	if got = e.facetCounts("ada", "q=curl"); got["body"] != "Body part: arms=1" || got["type"] != "Type: lift=1 slow_motion=1" {
		t.Errorf("counts with q=curl = %v", got)
	}
	// Bob sees his own.
	if got := e.facetCounts("bob", ""); got["body"] != "Body part: legs=1" || len(got) != 2 {
		t.Errorf("bob's counts = %v", got)
	}
	// Viewers read the counts; no token does not.
	e.call(http.MethodGet, "/v1/assets/facets", "viewer", nil, http.StatusOK, nil)
	e.fail(http.MethodGet, "/v1/assets/facets", "", nil, http.StatusUnauthorized, api.ErrorCodeUnauthenticated)
}
