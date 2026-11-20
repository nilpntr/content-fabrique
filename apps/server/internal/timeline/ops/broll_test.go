package ops

import (
	"slices"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// No op makes or changes a cutaway yet (docs/design/broll-cutaways.md, slice 2), but a spec with the b-roll track goes through Apply: the
// cutaway's id is taken, and an op that names the cutaway is refused (the wrong kind of item, or not one it knows), before validation.
func TestOpsKnowTheCutawayIDs(t *testing.T) {
	base := baseSpec(t)
	base.Tracks = append(base.Tracks, &timeline.BrollTrack{ID: timeline.BrollTrackID, Items: []timeline.BrollItem{
		{ID: "b1", AssetID: vid30, Source: timeline.Source{InMs: 10000, OutMs: 11000}, AtMs: 1000},
	}})
	for js, code := range map[string]string{
		`[{"op":"add_text","id":"b1","at_ms":0,"duration_ms":1000,"text":"x","style":"title_bold","anchor":"center"}]`: "id_taken",
		`[{"op":"remove_item","id":"b1"}]`:                      "wrong_item_kind",
		`[{"op":"trim_item","id":"b1","in_ms":0,"out_ms":500}]`: "unknown_item",
	} {
		out, res := apply(t, base, js)
		if res.OK() || !slices.Contains(errorCodes(res), code) || res.Errors()[0].OpIndex != 0 {
			t.Errorf("%s: want %s on op 0, got %+v", js, code, res.Issues)
		}
		if !sameSpec(t, out, base) {
			t.Errorf("%s: the spec changed", js)
		}
	}
	// An op on the rest of the spec leaves the cutaway alone.
	out := mustApply(t, base, `[{"op":"update_text","id":"t1","text":"Hi"}]`)
	if got := timeline.Cutaways(&out); len(got) != 1 || got[0].ID != "b1" || got[0].AtMs != 1000 {
		t.Fatalf("cutaways after an op: %+v", got)
	}
}
