package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Clip analysis (P2-02, P2-03, P2-09): the rows an `analyze` job writes and the editor's find_moments reads. The columns and their
// meaning are in the clip analysis migration.

// Rows of the analysis tables.
type (
	// Moment is a row of moments.
	Moment = db.Moment
	// Shot is a row of shots.
	Shot = db.Shot
	// AssetSignals is a row of asset_signals (with the whole series).
	AssetSignals = db.AssetSignal
	// AnalysisSummaryRow is the summary of one asset's analysis.
	AnalysisSummaryRow = db.ListAssetAnalysisSummariesRow
	// SignalsMeta says which analysis an asset has (version and the proxy it was made from), without the series.
	SignalsMeta = db.GetAssetSignalsMetaRow
	// ExposureSeriesRow is the luma and 90th percentile luma series of one asset's analysis.
	ExposureSeriesRow = db.ListAssetExposureSeriesRow
	// AssetSpanRow is a rep, hold or dead stretch of an asset's analysis.
	AssetSpanRow = db.ListAssetSpansRow
	// DeadSpan is one of the longest dead spans of an asset, with the number of dead spans it has in all.
	DeadSpan = db.ListDeadSpansRow
)

// The moment kinds (moments.kind).
const (
	MomentRep = "rep"
	// MomentRepCandidate is movement next to a set that looks like its reps but does not repeat clearly enough to cut reps from
	// (slow reps at the end of a tired set): not dead time, and ranked below the reps.
	MomentRepCandidate = "rep_candidate"
	MomentHold         = "hold"
	MomentDead         = "dead"
	MomentShot         = "shot"
)

// SetAnalysisHook registers fn to run, in the transaction that marks a video ready (FinishAssetIngest), for that video: fn
// enqueues the analyze job in the same transaction, so the job exists exactly when the ready asset does. An error from fn rolls
// the ingest's write back (the ingest job then runs again). Call it during setup, before the Store is used from several
// goroutines; passing nil removes the hook. Without a hook (the api, the tests of other packages) nothing is enqueued, and
// `cfctl assets analyze` can run it.
func (s *Store) SetAnalysisHook(fn func(ctx context.Context, tx *Store, a Asset) error) {
	s.hooks.analysisDue = fn
}

// analysisDue runs the analysis hook for a just-finished video or audio asset (an audio asset is a creator's song: its music analysis,
// P6-06).
func (s *Store) analysisDue(ctx context.Context, a Asset) error {
	if (a.Kind != AssetKindVideo && a.Kind != AssetKindAudio) || s.hooks == nil || s.hooks.analysisDue == nil {
		return nil
	}
	if err := s.hooks.analysisDue(ctx, s, a); err != nil {
		return fmt.Errorf("analysis hook: %w", err)
	}
	return nil
}

// NewShot is a shot to store.
type NewShot struct {
	Idx     int
	StartMs int
	EndMs   int
	Signals json.RawMessage
}

// NewMoment is a moment to store (source analysis).
type NewMoment struct {
	Kind        string
	StartMs     int
	EndMs       int
	Score       float64
	Labels      []string
	Attrs       json.RawMessage
	Description string
}

// PackedSeries are the per-sample series of an analysis as stored: one byte string per series (int16 little-endian for motion,
// sharp and scene, uint8 for luma, contrast and high) and the JSON document of their scales (package analysis packs them).
type PackedSeries struct {
	Scales                                     json.RawMessage
	Motion, Luma, Contrast, High, Sharp, Scene []byte
}

// AnalysisResult is what the analysis of one asset produced.
type AnalysisResult struct {
	Version int
	// SourceIngestedAt is the assets.ingested_at of the proxy the analysis was made from: an asset that is ingested again has a
	// new one, and its analysis is out of date.
	SourceIngestedAt *time.Time
	StepMs           int
	DurationMs       int
	Series           PackedSeries
	// Summary is a JSON document.
	Summary json.RawMessage
	Shots   []NewShot
	Moments []NewMoment
}

// ErrStaleAnalysis is returned by SaveAnalysis when the analysis was made from a proxy that is no longer the asset's (it was ingested
// again meanwhile) or the asset is not ready: nothing is written, and the job treats it as a clean no-op (the newer ingest has its
// own analysis job).
var ErrStaleAnalysis = errors.New("store: the analysis is out of date")

// SaveAnalysis stores the analysis of a ready asset and replaces the shots and the analysis moments an earlier run left, all in
// one transaction that starts by locking the asset row, so two analyses of one asset save one after the other, and the tags the
// moments get are the ones the row holds when they are written (an ApplyQuickLook cannot commit in between).
// ErrNotFound when the asset is not in this workspace (or is deleted); ErrStaleAnalysis when it is not ready or its proxy is not the
// one the analysis came from (r.SourceIngestedAt). Nothing is written then.
func (s *Store) SaveAnalysis(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, r AnalysisResult) error {
	return s.inTx(ctx, func(tx *Store) error {
		a, err := tx.q.GetAssetForAnalysisSave(ctx, db.GetAssetForAnalysisSaveParams{WorkspaceID: ws, ID: id})
		if err != nil {
			return mapErr(err)
		}
		if a.Status != AssetReady || !sameInstant(a.IngestedAt, r.SourceIngestedAt) {
			return ErrStaleAnalysis
		}
		if _, err := tx.q.UpsertAssetSignals(ctx, db.UpsertAssetSignalsParams{
			WorkspaceID: ws, AssetID: id, Version: int32(r.Version), SourceIngestedAt: r.SourceIngestedAt, StepMs: int32(r.StepMs), DurationMs: int32(r.DurationMs), //nolint:gosec // bounded by the analysis
			Scales: jsonOrEmpty(r.Series.Scales), Motion: nonNil(r.Series.Motion), Luma: nonNil(r.Series.Luma), Contrast: nonNil(r.Series.Contrast),
			High: nonNil(r.Series.High), Sharp: nonNil(r.Series.Sharp), Scene: nonNil(r.Series.Scene), Summary: jsonOrEmpty(r.Summary),
		}); err != nil {
			return mapErr(err)
		}
		if err := mapErr(tx.q.DeleteAssetShots(ctx, db.DeleteAssetShotsParams{WorkspaceID: ws, AssetID: id})); err != nil {
			return err
		}
		if err := mapErr(tx.q.DeleteAssetAnalysisMoments(ctx, db.DeleteAssetAnalysisMomentsParams{WorkspaceID: ws, AssetID: id})); err != nil {
			return err
		}
		for _, sh := range r.Shots {
			if err := mapErr(tx.q.InsertShot(ctx, db.InsertShotParams{
				WorkspaceID: ws, AssetID: id, Idx: int32(sh.Idx), StartMs: int32(sh.StartMs), EndMs: int32(sh.EndMs), Signals: jsonOrEmpty(sh.Signals), //nolint:gosec // bounded by the analysis
			})); err != nil {
				return err
			}
		}
		for _, m := range r.Moments {
			labels := m.Labels
			if labels == nil {
				labels = a.Tags // what the quick look has said so far; ApplyQuickLook labels them again when it lands
			}
			if labels == nil {
				labels = []string{}
			}
			if err := mapErr(tx.q.InsertMoment(ctx, db.InsertMomentParams{
				WorkspaceID: ws, AssetID: id, Kind: m.Kind, StartMs: int32(m.StartMs), EndMs: int32(m.EndMs), Score: float32(m.Score), //nolint:gosec // bounded by the analysis
				Labels: labels, Attrs: jsonOrEmpty(m.Attrs), Description: m.Description,
			})); err != nil {
				return err
			}
		}
		return nil
	})
}

// sameInstant reports whether two instants are the same to the microsecond (both unset counts).
func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Truncate(time.Microsecond).Equal(b.Truncate(time.Microsecond))
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func jsonOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// ListAssetAnalysisSummaries returns the summaries of the assets that have been analysed, in asset order.
func (s *Store) ListAssetAnalysisSummaries(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]AnalysisSummaryRow, error) {
	if len(assets) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListAssetAnalysisSummaries(ctx, db.ListAssetAnalysisSummariesParams{WorkspaceID: ws, AssetIds: assetUUIDs(assets)})
	return rows, mapErr(err)
}

// ListAssetExposureSeries returns the per-sample mean luma and 90th percentile luma of the analysed assets (packed as stored: one
// uint8 per sample), for the percentile exposure. An asset that was not analysed is not in the result.
func (s *Store) ListAssetExposureSeries(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]ExposureSeriesRow, error) {
	if len(assets) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListAssetExposureSeries(ctx, db.ListAssetExposureSeriesParams{WorkspaceID: ws, AssetIds: assetUUIDs(assets)})
	return rows, mapErr(err)
}

// ListAssetSpans returns the reps, holds and dead moments of the assets (whatever made them) in time order (kind MomentRep, MomentHold or
// MomentDead), for all of them in one read.
func (s *Store) ListAssetSpans(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]AssetSpanRow, error) {
	if len(assets) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListAssetSpans(ctx, db.ListAssetSpansParams{WorkspaceID: ws, AssetIds: assetUUIDs(assets)})
	return rows, mapErr(err)
}

// GetAssetSignalsMeta returns which analysis an asset has (its version and the proxy it was made from) without reading the series,
// or ErrNotFound when it was not analysed.
func (s *Store) GetAssetSignalsMeta(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (SignalsMeta, error) {
	r, err := s.q.GetAssetSignalsMeta(ctx, db.GetAssetSignalsMetaParams{WorkspaceID: ws, AssetID: id})
	return r, mapErr(err)
}

// ListDeadSpans returns, for each asset, its perAsset longest dead spans in time order, each with the asset's total number of them.
func (s *Store) ListDeadSpans(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID, perAsset int) ([]DeadSpan, error) {
	if len(assets) == 0 || perAsset < 1 {
		return nil, nil
	}
	rows, err := s.q.ListDeadSpans(ctx, db.ListDeadSpansParams{WorkspaceID: ws, AssetIds: assetUUIDs(assets), PerAsset: int32(perAsset)}) //nolint:gosec // small
	return rows, mapErr(err)
}

// GetAssetSignals returns the stored series and summary of an asset, or ErrNotFound when it was not analysed.
func (s *Store) GetAssetSignals(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (AssetSignals, error) {
	r, err := s.q.GetAssetSignals(ctx, db.GetAssetSignalsParams{WorkspaceID: ws, AssetID: id})
	return r, mapErr(err)
}

// ListAssetShots returns the shots of an asset in order.
func (s *Store) ListAssetShots(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) ([]Shot, error) {
	rows, err := s.q.ListAssetShots(ctx, db.ListAssetShotsParams{WorkspaceID: ws, AssetID: id})
	return rows, mapErr(err)
}

// ListAssetMoments returns every moment of an asset in time order.
func (s *Store) ListAssetMoments(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) ([]Moment, error) {
	rows, err := s.q.ListAssetMoments(ctx, db.ListAssetMomentsParams{WorkspaceID: ws, AssetID: id})
	return rows, mapErr(err)
}

// MomentFilter says which moments ListMoments returns.
type MomentFilter struct {
	// Assets are the assets to look in; the caller has checked that they belong to the project asking.
	Assets []ids.AssetID
	// Kinds are the kinds wanted (MomentRep and so on); empty means rep, rep candidate and hold, the moments worth cutting.
	Kinds    []string
	MinScore float64
	Limit    int
}

// ListMoments returns the best moments of the filter's assets, highest score first. Limit is 1 to 200.
func (s *Store) ListMoments(ctx context.Context, ws ids.WorkspaceID, f MomentFilter) ([]Moment, error) {
	if f.Limit < 1 || f.Limit > 200 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, f.Limit)
	}
	if len(f.Assets) == 0 {
		return nil, nil
	}
	kinds := f.Kinds
	if len(kinds) == 0 {
		kinds = []string{MomentRep, MomentHold, MomentRepCandidate}
	}
	rows, err := s.q.ListMoments(ctx, db.ListMomentsParams{
		WorkspaceID: ws, AssetIds: assetUUIDs(f.Assets), Kinds: kinds, MinScore: float32(f.MinScore), RowLimit: int32(f.Limit), //nolint:gosec // checked
	})
	return rows, mapErr(err)
}

// ListAssetsForAnalysis returns up to limit ready videos of the workspace, oldest first: those without an analysis of the
// given version or newer, or every one with includeDone.
func (s *Store) ListAssetsForAnalysis(ctx context.Context, ws ids.WorkspaceID, version int, includeDone bool, limit int) ([]Asset, error) {
	if limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	rows, err := s.q.ListAssetsForAnalysis(ctx, db.ListAssetsForAnalysisParams{WorkspaceID: ws, IncludeDone: includeDone, Version: int32(version), RowLimit: int32(limit)}) //nolint:gosec // checked
	return rows, mapErr(err)
}

func assetUUIDs(assets []ids.AssetID) []uuid.UUID {
	out := make([]uuid.UUID, len(assets))
	for i, a := range assets {
		out[i] = a.UUID
	}
	return out
}
