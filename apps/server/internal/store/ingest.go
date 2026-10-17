package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Derivative is a row of asset_derivatives: one generated file of an asset.
type Derivative = db.AssetDerivative

// The derivative kinds the asset_derivatives check constraint allows.
const (
	DerivativeProxy720     = "proxy_720"
	DerivativeProxy360     = "proxy_360"
	DerivativePoster       = "poster"
	DerivativeSprite       = "sprite"
	DerivativeSpriteVTT    = "sprite_vtt"
	DerivativeWaveform     = "waveform"
	DerivativeAudio16k     = "audio_16k"
	DerivativeKeyframe     = "keyframe"
	DerivativeHoverPreview = "hover_preview"
)

// The reasons an asset fails during ingest (assets.failure_reason): codes, safe to show a user. The API documents them.
const (
	FailureUnsupportedFormat = "unsupported_format"
	FailureCorruptMedia      = "corrupt_media"
	FailureNoVideo           = "no_video_stream"
	FailureNoAudio           = "no_audio_stream"
	FailureUndecodableVideo  = "undecodable_video"
	FailureNoDuration        = "invalid_duration"
	FailureTooLong           = "too_long"
	FailureResolution        = "unsupported_resolution"
	FailureKindMismatch      = "kind_mismatch"
	FailureProcessing        = "processing_failed"
	FailureTimeout           = "processing_timeout"     // the last attempt ran out of time
	FailureInterrupted       = "processing_interrupted" // the last attempt was cut short (the worker was stopped)
)

// IngestStages are the values of assets.ingest_stage while an asset is processing, in order.
const (
	StageStarting    = "starting"
	StageDownloading = "downloading"
	StageProbing     = "probing"
	StageProxy       = "proxy"
	StageDerivatives = "derivatives"
	StageUploading   = "uploading"
)

// NewDerivative is a derivative the ingest wrote to object storage.
type NewDerivative struct {
	Kind        string
	Key         string
	ContentType string
	SizeBytes   int64
	Meta        json.RawMessage // a JSON object; nil means {}
}

// IngestResult is what a finished ingest records on the asset.
type IngestResult struct {
	DurationMs int64
	Width      int
	Height     int
	FPS        float64
	Rotation   int // degrees clockwise: 0, 90, 180 or 270
	VideoCodec string
	AudioCodec string
	HasAudio   bool
	HDR        bool
	Probe      json.RawMessage // the normalised probe, a JSON object

	// Derivatives replace the asset's rows of the kinds they name; rows of other kinds are removed.
	Derivatives []NewDerivative
}

// ClaimAssetForIngest moves the asset to `processing` and resets its progress. It accepts an uploaded asset, one already
// processing (a retry) and a ready one (a re-run). ErrNotFound when the asset is not in this workspace, is deleted or has
// failed: the job then has nothing to do.
func (s *Store) ClaimAssetForIngest(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Asset, error) {
	a, err := s.q.ClaimAssetForIngest(ctx, db.ClaimAssetForIngestParams{WorkspaceID: ws, ID: id})
	return a, mapErr(err)
}

// SetIngestProgress records how far the ingest is (0..100) and what it is doing. ErrNotFound when the asset is not
// processing any more (deleted, failed, finished): the job stops.
func (s *Store) SetIngestProgress(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, progress int, stage string) error {
	_, err := s.q.SetIngestProgress(ctx, db.SetIngestProgressParams{
		WorkspaceID: ws, ID: id, Progress: int16(min(max(progress, 0), 100)), Stage: stage,
	})
	return mapErr(err)
}

// FinishAssetIngest marks the asset ready with what the probe found and replaces its derivatives, in one transaction.
// ErrNotFound when the asset is not processing any more (it was deleted while the job worked): nothing is written then.
func (s *Store) FinishAssetIngest(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, r IngestResult) (Asset, error) {
	var a Asset
	err := s.inTx(ctx, func(tx *Store) error {
		probe := r.Probe
		if len(probe) == 0 {
			probe = json.RawMessage(`{}`)
		}
		var err error
		a, err = tx.q.FinishAssetIngest(ctx, db.FinishAssetIngestParams{
			WorkspaceID: ws, ID: id, DurationMs: r.DurationMs, Width: int32(r.Width), Height: int32(r.Height), //nolint:gosec // validated by the ingest
			Fps: r.FPS, Rotation: int32(r.Rotation), //nolint:gosec // 0, 90, 180 or 270
			VideoCodec: nilIfEmpty(r.VideoCodec), AudioCodec: nilIfEmpty(r.AudioCodec),
			HasAudio: r.HasAudio, Hdr: r.HDR, Probe: probe,
		})
		if err != nil {
			return mapErr(err)
		}
		keep := make([]string, 0, len(r.Derivatives))
		for _, d := range r.Derivatives {
			meta := d.Meta
			if len(meta) == 0 {
				meta = json.RawMessage(`{}`)
			}
			if _, err := tx.q.UpsertAssetDerivative(ctx, db.UpsertAssetDerivativeParams{
				WorkspaceID: ws, AssetID: id, Kind: d.Kind, R2Key: d.Key, ContentType: d.ContentType, SizeBytes: d.SizeBytes, Meta: meta,
			}); err != nil {
				return fmt.Errorf("derivative %s: %w", d.Kind, mapErr(err))
			}
			keep = append(keep, d.Kind)
		}
		if err := mapErr(tx.q.DeleteStaleAssetDerivatives(ctx, db.DeleteStaleAssetDerivativesParams{WorkspaceID: ws, AssetID: id, Keep: keep})); err != nil {
			return err
		}
		if a, err = tx.quickLookDue(ctx, a); err != nil {
			return err
		}
		if a, err = tx.transcribeDue(ctx, a, keep); err != nil {
			return err
		}
		return tx.analysisDue(ctx, a)
	})
	if err != nil {
		return Asset{}, err
	}
	return a, nil
}

// FailAssetIngest marks the asset failed with a reason (one of the Failure constants) and removes its derivative rows,
// in one transaction: a failed asset has no derivatives, whether it failed on its first ingest or on a re-run (the caller
// removes the objects). ErrNotFound when it is not uploaded or processing any more; nothing changes then.
func (s *Store) FailAssetIngest(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string) (Asset, error) {
	var a Asset
	err := s.inTx(ctx, func(tx *Store) error {
		var err error
		if a, err = tx.q.FailAssetIngest(ctx, db.FailAssetIngestParams{WorkspaceID: ws, ID: id, FailureReason: reason}); err != nil {
			return mapErr(err)
		}
		return mapErr(tx.q.DeleteAssetDerivatives(ctx, db.DeleteAssetDerivativesParams{WorkspaceID: ws, AssetID: id}))
	})
	if err != nil {
		return Asset{}, err
	}
	return a, nil
}

// ListAssetDerivatives returns the derivatives of the given assets of the workspace, ordered by asset and kind.
func (s *Store) ListAssetDerivatives(ctx context.Context, ws ids.WorkspaceID, assetIDs []ids.AssetID) ([]Derivative, error) {
	if len(assetIDs) == 0 {
		return nil, nil
	}
	raw := make([]uuid.UUID, len(assetIDs))
	for i, id := range assetIDs {
		raw[i] = id.UUID
	}
	rows, err := s.q.ListAssetDerivatives(ctx, db.ListAssetDerivativesParams{WorkspaceID: ws, AssetIds: slices.Clone(raw)})
	return rows, mapErr(err)
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
