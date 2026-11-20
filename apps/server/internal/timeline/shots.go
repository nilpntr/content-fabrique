package timeline

import (
	"cmp"
	"slices"
)

// Shot is one clip of the main track as a proof edit numbers it (docs/design/proof-edit-v1.md): the shots are numbered 1, 2, 3 ... in
// the order they play, which is the order the renderer lays the clips out in (sorted by at_ms, a stable sort), so the number burned
// into a proof render and the number of the shot list are the same. A number belongs to one version: the next version numbers its own
// shots, which is why feedback on a shot is stored by its clip and source range, never by its number.
type Shot struct {
	Number  int    `json:"number"`
	ItemID  string `json:"item_id"`
	AssetID string `json:"asset_id"`
	// AtMs and EndMs are where the shot is on screen: from its start to where the next shot starts (the last one: to its own end), so
	// the overlap of a transition belongs to the shot it leads into.
	AtMs  int64 `json:"at_ms"`
	EndMs int64 `json:"end_ms"`
	// Source is the range of the clip the shot plays, in the clip's own time.
	Source Source `json:"source"`
}

// Shots numbers the playable clips of the main track of spec in the order they play. A spec without a main track has none.
func Shots(spec *Spec) []Shot {
	main, _, ok := MainTrack(spec)
	if !ok {
		return nil
	}
	items := make([]VideoItem, 0, len(main.Items))
	for _, it := range main.Items {
		if Playable(it) {
			items = append(items, it)
		}
	}
	slices.SortStableFunc(items, func(a, b VideoItem) int { return cmp.Compare(a.AtMs, b.AtMs) })
	out := make([]Shot, len(items))
	for i, it := range items {
		end := EndMs(it)
		if i+1 < len(items) {
			end = min(end, items[i+1].AtMs)
		}
		out[i] = Shot{Number: i + 1, ItemID: it.ID, AssetID: it.AssetID, AtMs: it.AtMs, EndMs: max(end, it.AtMs), Source: it.Source}
	}
	return out
}

// ShotByNumber returns the shot with the given number, if the spec has it.
func ShotByNumber(spec *Spec, n int) (Shot, bool) {
	for _, s := range Shots(spec) {
		if s.Number == n {
			return s, true
		}
	}
	return Shot{}, false
}
