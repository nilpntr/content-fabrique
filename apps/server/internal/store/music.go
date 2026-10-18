package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// The music analysis of an asset (P6-06): a creator's song, analysed once by the analyze job (package music), read by the editor. The
// columns and their meaning are in the asset_music migration.

type (
	// AssetMusicRow is the music analysis of one asset.
	AssetMusicRow = db.ListAssetMusicRow
	// AssetMusicMeta says which analysis an asset has (version and the ingest it was made from).
	AssetMusicMeta = db.GetAssetMusicMetaRow
)

// SaveAssetMusic stores the music analysis of a ready asset, replacing the one it had. The asset row is locked first, so the analysis is
// written only when the asset is still ready and still the ingest it was made from (ErrStaleAnalysis otherwise; ErrNotFound when the
// asset is not in this workspace). analysis is a JSON object.
func (s *Store) SaveAssetMusic(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, version int, sourceIngestedAt *time.Time, analysis json.RawMessage) error {
	if version < 1 || len(analysis) == 0 || !json.Valid(analysis) {
		return fmt.Errorf("%w: a music analysis needs a version and a JSON document", ErrInvalid)
	}
	return s.inTx(ctx, func(tx *Store) error {
		a, err := tx.q.GetAssetForAnalysisSave(ctx, db.GetAssetForAnalysisSaveParams{WorkspaceID: ws, ID: id})
		if err != nil {
			return mapErr(err)
		}
		if a.Status != AssetReady || !sameInstant(a.IngestedAt, sourceIngestedAt) {
			return ErrStaleAnalysis
		}
		_, err = tx.q.UpsertAssetMusic(ctx, db.UpsertAssetMusicParams{
			WorkspaceID: ws, AssetID: id, Version: int32(version), SourceIngestedAt: sourceIngestedAt, Analysis: analysis, //nolint:gosec // a small version number
		})
		return mapErr(err)
	})
}

// GetAssetMusicMeta says which music analysis the asset has; ErrNotFound when it has none.
func (s *Store) GetAssetMusicMeta(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (AssetMusicMeta, error) {
	r, err := s.q.GetAssetMusicMeta(ctx, db.GetAssetMusicMetaParams{WorkspaceID: ws, AssetID: id})
	return r, mapErr(err)
}

// ListAssetMusic returns the music analyses of the assets that have one.
func (s *Store) ListAssetMusic(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]AssetMusicRow, error) {
	if len(assets) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListAssetMusic(ctx, db.ListAssetMusicParams{WorkspaceID: ws, AssetIds: assetUUIDs(assets)})
	return rows, mapErr(err)
}

// DeleteAssetMusic removes the music analysis of an asset (a video whose sound is no longer read as music). Not an error when there is
// none.
func (s *Store) DeleteAssetMusic(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) error {
	return mapErr(s.q.DeleteAssetMusic(ctx, db.DeleteAssetMusicParams{WorkspaceID: ws, AssetID: id}))
}
