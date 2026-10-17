package projects

import (
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Catalog is the timeline.AssetCatalog of one project: the assets the editor may use, read from the assets table for the
// project's workspace, and nothing else. It holds only the project's own footage that is ready (probed and ingested) and
// not deleted, so a timeline validated against it cannot name an asset of another project, of another workspace, one
// that is still uploading or one that is gone: each is an unknown_asset issue.
//
// It is read once per call (Validate looks each asset up once per item, and the interface has no context), so a lookup
// never touches the database.
type Catalog map[string]timeline.Asset

// Lookup implements timeline.AssetCatalog.
func (c Catalog) Lookup(assetID string) (timeline.Asset, bool) {
	a, ok := c[assetID]
	return a, ok
}

// CatalogOf builds the catalog from the rows ListProjectCatalogAssets returned, for the other packages that validate a stored
// version against the project's footage as it is now (the render jobs).
func CatalogOf(rows []store.ProjectCatalogRow) Catalog { return catalogOf(rows) }

// catalogOf builds the catalog from the rows ListProjectCatalogAssets returned.
//
// Rights: the rights field applies to audio (D-03). A creator's own upload is not licensed, and the attestation that
// would make it "attested" (the creator says they own it) has no record in the database yet, so every asset is "none"
// here: a creator_audio item on it is an unlicensed_audio error, which is the safe answer until that record exists
// (the timeline note, section 3). Image assets are not part of the catalog; the timeline has no image items.
func catalogOf(rows []store.ProjectCatalogRow) Catalog {
	c := make(Catalog, len(rows))
	for _, r := range rows {
		var kind timeline.AssetKind
		switch r.Kind {
		case store.AssetKindVideo:
			kind = timeline.AssetVideo
		case store.AssetKindAudio:
			kind = timeline.AssetAudio
		default:
			continue
		}
		if r.DurationMs == nil || *r.DurationMs <= 0 {
			continue
		}
		c[r.AssetID.String()] = timeline.Asset{DurationMs: *r.DurationMs, Kind: kind, Rights: timeline.RightsNone}
	}
	return c
}

// EmptySpec is the timeline an edit of a project without versions starts from: the project's format and size, 30 fps, a
// black background, an empty main track. It is not a valid timeline (a timeline needs at least one clip); it is only ever
// the parent of version 1, never stored.
func EmptySpec(format string) (timeline.Spec, bool) {
	f := timeline.Format(format)
	w, h, ok := f.Size()
	if !ok {
		return timeline.Spec{}, false
	}
	return timeline.Spec{
		Schema:  timeline.SchemaVersion,
		Output:  timeline.Output{Format: f, Width: w, Height: h, FPS: 30, Background: "#000000"},
		Tracks:  []timeline.Track{&timeline.VideoTrack{ID: timeline.MainTrackID, Items: []timeline.VideoItem{}}},
		Markers: []timeline.Marker{},
	}, true
}
