package assets

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// DerivativeURLExpiry is how long the presigned GET URLs of an asset's derivatives work. They are made fresh by every read
// of the asset, so a client that keeps a page open refetches the asset when they run out.
const DerivativeURLExpiry = 30 * time.Minute

// exposed lists the derivative kinds the API hands out, and the field of api.AssetDerivatives each one fills. audio_16k is
// input for transcription and stays internal.
var exposed = map[string]bool{
	store.DerivativeProxy720: true, store.DerivativePoster: true, store.DerivativeHoverPreview: true,
	store.DerivativeSprite: true, store.DerivativeSpriteVTT: true, store.DerivativeWaveform: true,
}

// File is one derivative with a presigned GET URL.
type File struct {
	Kind        string
	URL         string
	ContentType string
	SizeBytes   int64
	Width       *int
	Height      *int
	DurationMs  *int64
}

// View is an asset with its derivative URLs, for the read endpoints. Files is empty until the asset is ready.
type View struct {
	Asset       store.Asset
	Files       []File
	URLsExpires time.Time // zero without files
}

// derivativeMeta is the part of a derivative's meta the API shows.
type derivativeMeta struct {
	Width      *int   `json:"width"`
	Height     *int   `json:"height"`
	DurationMs *int64 `json:"duration_ms"`
}

// views attaches the derivative URLs to the (ready) assets of one workspace with a single query. The keys come from rows of
// this workspace, selected by workspace and asset id, so a URL can only point at an object of the asset it is shown with.
func (s *Service) views(ctx context.Context, ws ids.WorkspaceID, rows []store.Asset) ([]View, *Error) {
	out := make([]View, len(rows))
	var readyIDs []ids.AssetID
	for i, a := range rows {
		out[i].Asset = a
		if a.Status == store.AssetReady {
			readyIDs = append(readyIDs, a.ID)
		}
	}
	if len(readyIDs) == 0 {
		return out, nil
	}
	derivs, err := s.store.ListAssetDerivatives(ctx, ws, readyIDs)
	if err != nil {
		return nil, s.internal(ctx, "list derivatives", err)
	}
	byAsset := make(map[ids.AssetID][]store.Derivative, len(readyIDs))
	for _, d := range derivs {
		if exposed[d.Kind] {
			byAsset[d.AssetID] = append(byAsset[d.AssetID], d)
		}
	}
	expires := s.now().Add(DerivativeURLExpiry)
	for i := range out {
		for _, d := range byAsset[out[i].Asset.ID] {
			url, err := s.objects.PresignGet(ctx, d.R2Key, DerivativeURLExpiry)
			if err != nil {
				// Presigning computes a signature locally; a failure is a bug or a bad configuration. Leave the file out
				// rather than fail the whole page.
				s.logger.ErrorContext(ctx, "assets: could not presign a derivative", "asset_id", d.AssetID.String(), "kind", d.Kind, "error", err)
				continue
			}
			f := File{Kind: d.Kind, URL: url, ContentType: d.ContentType, SizeBytes: d.SizeBytes}
			var m derivativeMeta
			if json.Unmarshal(d.Meta, &m) == nil {
				f.Width, f.Height, f.DurationMs = m.Width, m.Height, m.DurationMs
			}
			out[i].Files = append(out[i].Files, f)
		}
		if len(out[i].Files) > 0 {
			out[i].URLsExpires = expires
		}
	}
	return out, nil
}

// file converts one File to its API form.
func (f File) file() *api.DerivativeFile {
	return &api.DerivativeFile{Url: f.URL, ContentType: f.ContentType, SizeBytes: f.SizeBytes, Width: f.Width, Height: f.Height, DurationMs: f.DurationMs}
}
