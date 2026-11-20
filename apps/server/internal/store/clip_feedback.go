package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// ClipFeedback is a row of clip_feedback: what the creator said about a shot of a proof edit, stored against the clip and the range of
// its source, never the shot's number (docs/design/proof-edit-v1.md).
type ClipFeedback = db.ClipFeedback

// The kinds the clip_feedback check constraint allows.
const (
	// ClipNeverUse: the range is never used again.
	ClipNeverUse = "never_use"
	// ClipStartLater: the take starts later; the range is the part before the new start.
	ClipStartLater = "start_later"
)

// MaxClipFeedbackNoteChars is the longest note (clip_feedback_note_check).
const MaxClipFeedbackNoteChars = 500

const maxClipFeedbackRowLimit = 1000

// NewClipFeedback is the input of SaveClipFeedback. The caller (package clipfeedback) has validated it.
type NewClipFeedback struct {
	Asset      ids.AssetID
	Kind       string
	StartMs    int64
	EndMs      int64
	Project    ids.ProjectID
	Version    int
	ShotNumber int
	ItemID     string
	Note       string
	// CreatedByKind is AuthorUser or AuthorAgent, CreatedByID the user's TypeID or the run id.
	CreatedByKind string
	CreatedByID   string
}

// SaveClipFeedback stores feedback on a range of a clip. The same kind on the same range again is the same row: its note and where it
// was given are updated. ErrNotFound when the clip or the project is not in this workspace.
func (s *Store) SaveClipFeedback(ctx context.Context, ws ids.WorkspaceID, in NewClipFeedback) (ClipFeedback, error) {
	if in.Version < 1 || in.Version > 1<<30 || in.ShotNumber < 1 || in.ShotNumber > 1<<20 {
		return ClipFeedback{}, fmt.Errorf("%w: version %d, shot %d", ErrInvalid, in.Version, in.ShotNumber)
	}
	r, err := s.q.UpsertClipFeedback(ctx, db.UpsertClipFeedbackParams{
		WorkspaceID: ws, AssetID: in.Asset, Kind: in.Kind, StartMs: in.StartMs, EndMs: in.EndMs, ProjectID: in.Project,
		TimelineVersion: int32(in.Version), ShotNumber: int32(in.ShotNumber), ItemID: in.ItemID, Note: in.Note, //nolint:gosec // range checked above
		CreatedByKind: in.CreatedByKind, CreatedByID: in.CreatedByID,
	})
	return r, mapErr(err)
}

// ListClipFeedback returns up to limit rows of the workspace's feedback, newest first: on the given clips, or on every clip when assets
// is empty.
func (s *Store) ListClipFeedback(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID, limit int) ([]ClipFeedback, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	list := make([]uuid.UUID, len(assets))
	for i, a := range assets {
		list[i] = a.UUID
	}
	rows, err := s.q.ListClipFeedback(ctx, db.ListClipFeedbackParams{WorkspaceID: ws, AssetIds: list, RowLimit: int32(min(limit, maxClipFeedbackRowLimit))}) //nolint:gosec // capped
	return rows, mapErr(err)
}

// maxClipFeedbackRows bounds ListAllClipFeedback: far more than a creator ever gives, a guard against a runaway loop only.
const maxClipFeedbackRows = 100_000

// ListAllClipFeedback returns every row of the workspace's feedback on the given clips (on every clip when assets is empty), newest
// first, page by page: what the editor and its checks apply must not miss an old row. More than maxClipFeedbackRows is an error.
func (s *Store) ListAllClipFeedback(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]ClipFeedback, error) {
	list := make([]uuid.UUID, len(assets))
	for i, a := range assets {
		list[i] = a.UUID
	}
	var out []ClipFeedback
	p := db.ListClipFeedbackParams{WorkspaceID: ws, AssetIds: list, RowLimit: maxClipFeedbackRowLimit}
	for {
		rows, err := s.q.ListClipFeedback(ctx, p)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, rows...)
		if len(rows) < maxClipFeedbackRowLimit {
			return out, nil
		}
		if len(out) >= maxClipFeedbackRows {
			return nil, fmt.Errorf("store: over %d clip feedback rows", maxClipFeedbackRows)
		}
		p.Before = pgtype.UUID{Bytes: rows[len(rows)-1].ID.UUID, Valid: true}
	}
}

// GetClipFeedback returns one row of the workspace's feedback, or ErrNotFound.
func (s *Store) GetClipFeedback(ctx context.Context, ws ids.WorkspaceID, id ids.ClipFeedbackID) (ClipFeedback, error) {
	r, err := s.q.GetClipFeedback(ctx, db.GetClipFeedbackParams{WorkspaceID: ws, ID: id})
	return r, mapErr(err)
}

// DeleteClipFeedback removes one row of the workspace's feedback (the creator took it back). ErrNotFound when there is no such row in
// this workspace.
func (s *Store) DeleteClipFeedback(ctx context.Context, ws ids.WorkspaceID, id ids.ClipFeedbackID) error {
	_, err := s.q.DeleteClipFeedback(ctx, db.DeleteClipFeedbackParams{WorkspaceID: ws, ID: id})
	return mapErr(err)
}
