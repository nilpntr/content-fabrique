package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// CreatorStyle is a row of creator_style_profiles: the creator's style profile (P6-05) with the measures of the references it was made
// from. Profile and Measures are JSON documents the styleprofile package reads and writes; the store keeps them as they are.
type CreatorStyle = db.CreatorStyleProfile

// NewCreatorStyle is a profile to store.
type NewCreatorStyle struct {
	// Version is the shape version of Profile (styleprofile.ProfileVersion).
	Version int
	Profile json.RawMessage
	// Measures is a JSON array, one entry per reference.
	Measures        json.RawMessage
	ReferencesCount int
	// Summary is the AI-written summary (styleprofile.Summary), nil for none. ReferenceIDs are the style_references the profile was
	// made from (none for a profile stored without them, as cfctl's older path did).
	Summary      json.RawMessage
	ReferenceIDs []uuid.UUID
}

// GetCreatorStyle returns the workspace's style profile, or ErrNotFound when it has none.
func (s *Store) GetCreatorStyle(ctx context.Context, ws ids.WorkspaceID) (CreatorStyle, error) {
	r, err := s.q.GetCreatorStyle(ctx, ws)
	return r, mapErr(err)
}

// SaveCreatorStyle stores the workspace's style profile, replacing the one it had.
func (s *Store) SaveCreatorStyle(ctx context.Context, ws ids.WorkspaceID, in NewCreatorStyle) (CreatorStyle, error) {
	if in.Version < 1 || len(in.Profile) == 0 || !json.Valid(in.Profile) {
		return CreatorStyle{}, fmt.Errorf("%w: a style profile needs a version and a JSON profile", ErrInvalid)
	}
	if len(in.Measures) == 0 {
		in.Measures = json.RawMessage(`[]`)
	}
	if len(in.Summary) > 0 && !json.Valid(in.Summary) {
		return CreatorStyle{}, fmt.Errorf("%w: the style summary is not JSON", ErrInvalid)
	}
	refs := in.ReferenceIDs
	if refs == nil {
		refs = []uuid.UUID{}
	}
	r, err := s.q.UpsertCreatorStyle(ctx, db.UpsertCreatorStyleParams{
		WorkspaceID: ws, Version: int32(in.Version), Profile: in.Profile, Measures: in.Measures, ReferencesCount: int32(in.ReferencesCount), //nolint:gosec // small
		Summary: nullJSON(in.Summary), ReferenceIds: refs,
	})
	return r, mapErr(err)
}

// DeleteCreatorStyle removes the workspace's style profile (and so everything derived from the references). It is not an error to
// delete a profile that is not there.
func (s *Store) DeleteCreatorStyle(ctx context.Context, ws ids.WorkspaceID) error {
	return mapErr(s.q.DeleteCreatorStyle(ctx, ws))
}

func nullJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// ---- reference videos (P6-05b) ---------------------------------------------------------------------------------------------------

// StyleReference is a row of style_references: a reference video that was or is being measured.
type StyleReference = db.StyleReference

// The statuses of a style reference (style_references.status).
const (
	StyleRefQueued    = "queued"
	StyleRefAnalysing = "analysing"
	StyleRefReady     = "ready"
	StyleRefFailed    = "failed"
)

// StyleReferenceAsset is the asset of a reference, or false for a file measured by cfctl.
func StyleReferenceAsset(r StyleReference) (ids.AssetID, bool) {
	if !r.AssetID.Valid {
		return ids.AssetID{}, false
	}
	return ids.AssetID{UUID: r.AssetID.Bytes}, true
}

func assetUUID(id ids.AssetID) pgtype.UUID { return pgtype.UUID{Bytes: id.UUID, Valid: true} }

// SetStyleReferenceHook registers fn to run, in the transaction that marks a style reference ready (FinishAssetIngest), after its row
// is queued: fn enqueues the analysis of its style in the same transaction, so the job exists exactly when the ready reference does. An
// error from fn rolls the ingest's write back (the ingest job then runs again). Call it during setup; nil removes it.
func (s *Store) SetStyleReferenceHook(fn func(ctx context.Context, tx *Store, a Asset) error) {
	s.hooks.styleRefDue = fn
}

// styleReferenceDue queues the analysis of a style reference whose ingest just finished.
func (s *Store) styleReferenceDue(ctx context.Context, a Asset) error {
	if _, err := s.QueueStyleReference(ctx, a.WorkspaceID, a.ID, a.Filename); err != nil {
		return err
	}
	if s.hooks != nil && s.hooks.styleRefDue != nil {
		if err := s.hooks.styleRefDue(ctx, s, a); err != nil {
			return fmt.Errorf("style reference hook: %w", err)
		}
	}
	return nil
}

// CountStyleReferenceAssets counts the workspace's style reference assets that hold a place: not deleted, not failed.
func (s *Store) CountStyleReferenceAssets(ctx context.Context, ws ids.WorkspaceID) (int, error) {
	n, err := s.q.CountStyleReferenceAssets(ctx, ws)
	return int(n), mapErr(err)
}

// ListStyleReferenceAssets lists the workspace's style reference assets that are not deleted, oldest first.
func (s *Store) ListStyleReferenceAssets(ctx context.Context, ws ids.WorkspaceID) ([]Asset, error) {
	rows, err := s.q.ListStyleReferenceAssets(ctx, ws)
	return rows, mapErr(err)
}

// ListStyleReferences lists every reference row of the workspace, oldest first.
func (s *Store) ListStyleReferences(ctx context.Context, ws ids.WorkspaceID) ([]StyleReference, error) {
	rows, err := s.q.ListStyleReferences(ctx, ws)
	return rows, mapErr(err)
}

// GetStyleReferenceByAsset returns the reference row of an asset, ErrNotFound when it has none (yet).
func (s *Store) GetStyleReferenceByAsset(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID) (StyleReference, error) {
	r, err := s.q.GetStyleReferenceByAsset(ctx, db.GetStyleReferenceByAssetParams{WorkspaceID: ws, AssetID: assetUUID(asset)})
	return r, mapErr(err)
}

// QueueStyleReference queues a reference asset for its analysis (a new one, or one measured again). The composite foreign key refuses
// an asset of another workspace.
func (s *Store) QueueStyleReference(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, name string) (StyleReference, error) {
	r, err := s.q.QueueStyleReference(ctx, db.QueueStyleReferenceParams{WorkspaceID: ws, AssetID: assetUUID(asset), Name: name})
	return r, mapErr(err)
}

// RequeueStyleReferences puts every asset reference of the workspace back in the queue (learning the style again), and returns them.
func (s *Store) RequeueStyleReferences(ctx context.Context, ws ids.WorkspaceID) ([]StyleReference, error) {
	rows, err := s.q.RequeueStyleReferences(ctx, ws)
	return rows, mapErr(err)
}

// StartStyleReference marks the analysis of a reference started; ErrNotFound when the reference was removed.
func (s *Store) StartStyleReference(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID) (StyleReference, error) {
	r, err := s.q.StartStyleReference(ctx, db.StartStyleReferenceParams{WorkspaceID: ws, AssetID: assetUUID(asset)})
	return r, mapErr(err)
}

// FinishStyleReference stores the measures of a reference; ErrNotFound when it was removed meanwhile.
func (s *Store) FinishStyleReference(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, measures, detail json.RawMessage) (StyleReference, error) {
	if !json.Valid(measures) {
		return StyleReference{}, fmt.Errorf("%w: the measures are not JSON", ErrInvalid)
	}
	r, err := s.q.FinishStyleReference(ctx, db.FinishStyleReferenceParams{WorkspaceID: ws, AssetID: assetUUID(asset), Measures: measures, Detail: objectOr(detail)})
	return r, mapErr(err)
}

// FailStyleReference marks a reference failed with a code that is safe to show; ErrNotFound when it was removed meanwhile.
func (s *Store) FailStyleReference(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, reason string, detail json.RawMessage) (StyleReference, error) {
	r, err := s.q.FailStyleReference(ctx, db.FailStyleReferenceParams{WorkspaceID: ws, AssetID: assetUUID(asset), FailureReason: &reason, Detail: objectOr(detail)})
	return r, mapErr(err)
}

// InsertFileStyleReference stores the measures of a file measured by cfctl (no asset).
func (s *Store) InsertFileStyleReference(ctx context.Context, ws ids.WorkspaceID, name string, measures, detail json.RawMessage) (StyleReference, error) {
	if !json.Valid(measures) {
		return StyleReference{}, fmt.Errorf("%w: the measures are not JSON", ErrInvalid)
	}
	r, err := s.q.InsertFileStyleReference(ctx, db.InsertFileStyleReferenceParams{WorkspaceID: ws, Name: name, Measures: measures, Detail: objectOr(detail)})
	return r, mapErr(err)
}

// DeleteFileStyleReferences removes the references measured by cfctl from files (the asset references stay).
func (s *Store) DeleteFileStyleReferences(ctx context.Context, ws ids.WorkspaceID) error {
	return mapErr(s.q.DeleteFileStyleReferences(ctx, ws))
}

// DeleteStyleReferenceByAsset removes the reference row of an asset (not an error when there is none).
func (s *Store) DeleteStyleReferenceByAsset(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID) error {
	return mapErr(s.q.DeleteStyleReferenceByAsset(ctx, db.DeleteStyleReferenceByAssetParams{WorkspaceID: ws, AssetID: assetUUID(asset)}))
}

// DeleteStyleReference removes one reference row (not an error when it is not there).
func (s *Store) DeleteStyleReference(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID) error {
	return mapErr(s.q.DeleteStyleReference(ctx, db.DeleteStyleReferenceParams{WorkspaceID: ws, ID: id}))
}

// DeleteStyleReferences removes every reference row of the workspace.
func (s *Store) DeleteStyleReferences(ctx context.Context, ws ids.WorkspaceID) error {
	return mapErr(s.q.DeleteStyleReferences(ctx, ws))
}

// SoftDeleteStyleReferenceAssets soft-deletes every reference asset of the workspace and returns them (their objects are to be removed).
func (s *Store) SoftDeleteStyleReferenceAssets(ctx context.Context, ws ids.WorkspaceID) ([]Asset, error) {
	rows, err := s.q.SoftDeleteStyleReferenceAssets(ctx, ws)
	return rows, mapErr(err)
}

func objectOr(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// ---- the creator's settings (P6-05b) -----------------------------------------------------------------------------------------------

// CreatorStyleSettings is a row of creator_style_settings: the settings the creator chose (a styleprofile.Settings document).
type CreatorStyleSettings = db.CreatorStyleSetting

// GetCreatorStyleSettings returns the workspace's settings, ErrNotFound when it never chose any.
func (s *Store) GetCreatorStyleSettings(ctx context.Context, ws ids.WorkspaceID) (CreatorStyleSettings, error) {
	r, err := s.q.GetCreatorStyleSettings(ctx, ws)
	return r, mapErr(err)
}

// SaveCreatorStyleSettings stores the workspace's settings, replacing what it had. by is who changed them (a zero id for none).
func (s *Store) SaveCreatorStyleSettings(ctx context.Context, ws ids.WorkspaceID, settings json.RawMessage, by ids.UserID) (CreatorStyleSettings, error) {
	if len(settings) == 0 || !json.Valid(settings) {
		return CreatorStyleSettings{}, fmt.Errorf("%w: the style settings are not JSON", ErrInvalid)
	}
	var who pgtype.UUID
	if by.UUID != uuid.Nil {
		who = pgtype.UUID{Bytes: by.UUID, Valid: true}
	}
	r, err := s.q.UpsertCreatorStyleSettings(ctx, db.UpsertCreatorStyleSettingsParams{WorkspaceID: ws, Settings: settings, UpdatedBy: who})
	return r, mapErr(err)
}

// DeleteCreatorStyleSettings removes the workspace's settings (not an error when there are none).
func (s *Store) DeleteCreatorStyleSettings(ctx context.Context, ws ids.WorkspaceID) error {
	return mapErr(s.q.DeleteCreatorStyleSettings(ctx, ws))
}
