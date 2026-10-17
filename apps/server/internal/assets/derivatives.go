package assets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
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

// maxVTTBytes bounds the track read from object storage: the largest legitimate one is 100 cues of about 70 bytes.
const maxVTTBytes = 1 << 20

// cueRE matches the cue payload lines the ingest writes: the sheet's relative name and a media fragment.
var cueRE = regexp.MustCompile(`^sprite\.jpg(#xywh=\d+,\d+,\d+,\d+)$`)

var errNoSprite = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "this asset has no sprite track"}

// SpriteVTT returns the thumbnails track of a ready asset with each cue's URL replaced by a fresh presigned URL of the
// sprite sheet, so the track works on its own. The stored track names the sheet relatively; the sheet's URL is signed and
// expires, so it cannot be stored in the file.
func (s *Service) SpriteVTT(ctx context.Context, who Actor, id ids.AssetID) (string, *Error) {
	a, err := s.store.GetAsset(ctx, who.Workspace, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", errNotFound
	}
	if err != nil {
		return "", s.internal(ctx, "get asset", err)
	}
	if a.Status != store.AssetReady {
		return "", errNoSprite
	}
	derivs, err := s.store.ListAssetDerivatives(ctx, who.Workspace, []ids.AssetID{a.ID})
	if err != nil {
		return "", s.internal(ctx, "list derivatives", err)
	}
	var vtt, sprite *store.Derivative
	for i := range derivs {
		switch derivs[i].Kind {
		case store.DerivativeSpriteVTT:
			vtt = &derivs[i]
		case store.DerivativeSprite:
			sprite = &derivs[i]
		}
	}
	if vtt == nil || sprite == nil {
		return "", errNoSprite
	}
	url, err := s.objects.PresignGet(ctx, sprite.R2Key, DerivativeURLExpiry)
	if err != nil {
		return "", s.internal(ctx, "presign the sprite sheet", err)
	}
	obj, err := s.objects.Get(ctx, vtt.R2Key)
	if err != nil {
		return "", s.storageErr(ctx, "get the sprite track", err)
	}
	defer func() { _ = obj.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(obj.Body, maxVTTBytes+1))
	if err != nil {
		return "", s.storageErr(ctx, "read the sprite track", err)
	}
	if len(raw) > maxVTTBytes {
		return "", s.internal(ctx, "sprite track", errors.New("the stored track is too large"))
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if m := cueRE.FindStringSubmatch(line); m != nil {
			lines[i] = url + m[1]
		}
	}
	return strings.Join(lines, "\n"), nil
}
