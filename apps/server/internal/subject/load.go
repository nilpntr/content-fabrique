package subject

import (
	"context"
	"fmt"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Index is the subject tracks of some assets, by the asset's TypeID string.
type Index map[string]Track

// Lookup is Index as a Lookup function.
func (ix Index) Lookup(assetID string) (Track, bool) {
	t, ok := ix[assetID]
	return t, ok
}

// Reader is the part of the store the index is loaded with. *store.Store is the real one.
type Reader interface {
	ListAssetSubjects(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]store.AssetSubjectsRow, error)
}

// Load reads and cleans the subject tracks of the assets; an asset whose stored boxes are not usable has none. The workspace scopes the
// read.
func Load(ctx context.Context, st Reader, ws ids.WorkspaceID, assets []ids.AssetID) (Index, error) {
	rows, err := st.ListAssetSubjects(ctx, ws, assets)
	if err != nil {
		return nil, fmt.Errorf("subjects: %w", err)
	}
	ix := make(Index, len(rows))
	for _, r := range rows {
		if t, ok := FromSubjects(r.Subjects); ok {
			t.ShowsOff = ShowsOffTheBody(r.Title, r.Facets) // "type:pose" says pose
			ix[r.AssetID.String()] = t
		}
	}
	return ix, nil
}

// LoadForSpec reads the subject tracks of the assets the main track of a spec plays.
func LoadForSpec(ctx context.Context, st Reader, ws ids.WorkspaceID, spec *timeline.Spec) (Index, error) {
	main, _, ok := timeline.MainTrack(spec)
	if !ok {
		return nil, nil
	}
	seen := map[string]bool{}
	var list []ids.AssetID
	for _, it := range main.Items {
		if aid, err := ids.ParseAssetID(it.AssetID); err == nil && !seen[it.AssetID] {
			seen[it.AssetID] = true
			list = append(list, aid)
		}
	}
	return Load(ctx, st, ws, list)
}
