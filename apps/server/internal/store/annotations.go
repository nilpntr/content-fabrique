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

// Annotation is a row of annotations: a comment on a clip, a render or a timeline version (P1-08). ProjectID and ParentID are the
// zero ID when the column is null.
type Annotation = db.Annotation

// What an annotation can be about (annotations.target_type).
const (
	AnnotationOnAsset   = "asset"
	AnnotationOnRender  = "render"
	AnnotationOnVersion = "timeline_version"
)

// The kinds and statuses the annotations check constraints allow.
const (
	AnnotationComment     = "comment"
	AnnotationInclude     = "include"
	AnnotationExclude     = "exclude"
	AnnotationPrefer      = "prefer"
	AnnotationInstruction = "instruction"

	AnnotationOpen     = "open"
	AnnotationApplied  = "applied"
	AnnotationResolved = "resolved"
)

// MaxAnnotationBodyChars is the longest body (annotations.body_check).
const MaxAnnotationBodyChars = 2000

const maxAnnotationRowLimit = 1000

// AnnotationTarget is a target found in a workspace: what a new annotation is stored against. Project and Version are the zero
// value for an asset.
type AnnotationTarget struct {
	Type string
	ID   uuid.UUID
	// Project and Version say which timeline version the target is (of a render: the version it was made from).
	Project ids.ProjectID
	Version int
	// DurationMs is the length of the clip or the render when it is known, else 0.
	DurationMs int64
}

// FindAnnotationTarget looks a target up in this workspace: an asset that is not deleted, a render or a timeline version of a
// project that is not deleted. ErrNotFound for anything else, whichever workspace it belongs to.
func (s *Store) FindAnnotationTarget(ctx context.Context, ws ids.WorkspaceID, targetType string, id uuid.UUID) (AnnotationTarget, error) {
	switch targetType {
	case AnnotationOnAsset:
		a, err := s.q.GetAsset(ctx, db.GetAssetParams{WorkspaceID: ws, ID: ids.AssetID{UUID: id}})
		if err != nil {
			return AnnotationTarget{}, mapErr(err)
		}
		return AnnotationTarget{Type: targetType, ID: a.ID.UUID, DurationMs: deref(a.DurationMs)}, nil
	case AnnotationOnRender:
		r, err := s.q.GetRender(ctx, db.GetRenderParams{WorkspaceID: ws, ID: ids.RenderID{UUID: id}})
		if err != nil {
			return AnnotationTarget{}, mapErr(err)
		}
		return AnnotationTarget{Type: targetType, ID: r.ID.UUID, Project: r.ProjectID, Version: int(r.TimelineVersion), DurationMs: deref(r.DurationMs)}, nil
	case AnnotationOnVersion:
		v, err := s.q.GetTimelineVersionRef(ctx, db.GetTimelineVersionRefParams{WorkspaceID: ws, ID: ids.TimelineID{UUID: id}})
		if err != nil {
			return AnnotationTarget{}, mapErr(err)
		}
		return AnnotationTarget{Type: targetType, ID: v.ID.UUID, Project: v.ProjectID, Version: int(v.Version)}, nil
	}
	return AnnotationTarget{}, fmt.Errorf("%w: target type %q", ErrInvalid, targetType)
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// NewAnnotation is the input of CreateAnnotation. The caller has validated it (package annotations).
type NewAnnotation struct {
	Target     AnnotationTarget
	StartMs    int64
	EndMs      int64
	Pin        json.RawMessage // nil: none
	Kind       string
	Body       string
	Parent     ids.AnnotationID // zero: not a reply
	AuthorKind string
	AuthorID   string
}

// CreateAnnotation inserts an annotation (status open). ErrNotFound when the project, version or parent is not in this workspace
// (the composite foreign keys), ErrInvalid for a value a check constraint refuses.
func (s *Store) CreateAnnotation(ctx context.Context, ws ids.WorkspaceID, in NewAnnotation) (Annotation, error) {
	var version *int32
	if in.Target.Version > 0 {
		v := int32(min(in.Target.Version, 1<<30)) //nolint:gosec // capped
		version = &v
	}
	var pin []byte
	if len(in.Pin) > 0 {
		pin = in.Pin
	}
	a, err := s.q.CreateAnnotation(ctx, db.CreateAnnotationParams{
		WorkspaceID: ws, TargetType: in.Target.Type, TargetID: in.Target.ID, ProjectID: in.Target.Project.UUID, TimelineVersion: version,
		StartMs: in.StartMs, EndMs: in.EndMs, Pin: pin, Kind: in.Kind, Body: in.Body, ParentID: in.Parent.UUID,
		AuthorKind: in.AuthorKind, AuthorID: in.AuthorID,
	})
	return a, mapErr(err)
}

// GetAnnotation returns a comment of this workspace that is not deleted, in a project or on an asset that is not deleted either.
func (s *Store) GetAnnotation(ctx context.Context, ws ids.WorkspaceID, id ids.AnnotationID) (Annotation, error) {
	a, err := s.q.GetAnnotation(ctx, db.GetAnnotationParams{WorkspaceID: ws, ID: id})
	return a, mapErr(err)
}

// GetAnnotationForUpdate is GetAnnotation locking the row until the end of the transaction.
func (s *Store) GetAnnotationForUpdate(ctx context.Context, ws ids.WorkspaceID, id ids.AnnotationID) (Annotation, error) {
	a, err := s.q.GetAnnotationForUpdate(ctx, db.GetAnnotationForUpdateParams{WorkspaceID: ws, ID: id})
	return a, mapErr(err)
}

// AnnotationFilter says which comments ListAnnotations returns: those on one target, or those on the renders and versions of one
// project. Exactly one of the two is set.
type AnnotationFilter struct {
	TargetType string
	TargetID   uuid.UUID
	Project    ids.ProjectID
	// After is the cursor: only comments with a later id.
	After ids.AnnotationID
}

// ListAnnotations returns up to limit comments and replies, oldest first. A project that is deleted, or in another workspace, has
// none.
func (s *Store) ListAnnotations(ctx context.Context, ws ids.WorkspaceID, f AnnotationFilter, limit int) ([]Annotation, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	rowLimit := int32(min(limit, maxAnnotationRowLimit)) //nolint:gosec // capped
	after := pgtype.UUID{Bytes: f.After.UUID, Valid: !f.After.IsZero()}
	if !f.Project.IsZero() {
		rows, err := s.q.ListAnnotationsByProject(ctx, db.ListAnnotationsByProjectParams{WorkspaceID: ws, ProjectID: f.Project, After: after, RowLimit: rowLimit})
		return rows, mapErr(err)
	}
	rows, err := s.q.ListAnnotationsByTarget(ctx, db.ListAnnotationsByTargetParams{WorkspaceID: ws, TargetType: f.TargetType, TargetID: f.TargetID, After: after, RowLimit: rowLimit})
	return rows, mapErr(err)
}

// AnnotationEdit is every field an edit may change: the caller reads the row (GetAnnotationForUpdate), changes what the request
// asked for and hands the whole set back.
type AnnotationEdit struct {
	StartMs, EndMs int64
	Pin            json.RawMessage // nil: none
	Kind           string
	Body           string
	Status         string
	// AppliedInRun and AddressedInVersion are set together with status applied, and cleared with any other status.
	AppliedInRun       uuid.UUID
	AddressedInVersion *int
}

// UpdateAnnotation writes an edit, and moves the replies to the new range. ErrNotFound when the comment is gone.
func (s *Store) UpdateAnnotation(ctx context.Context, ws ids.WorkspaceID, id ids.AnnotationID, e AnnotationEdit) (Annotation, error) {
	var pin []byte
	if len(e.Pin) > 0 {
		pin = e.Pin
	}
	var addressed *int32
	if e.AddressedInVersion != nil {
		v := int32(min(max(*e.AddressedInVersion, 0), 1<<30)) //nolint:gosec // clamped
		addressed = &v
	}
	a, err := s.q.UpdateAnnotation(ctx, db.UpdateAnnotationParams{
		WorkspaceID: ws, ID: id, StartMs: e.StartMs, EndMs: e.EndMs, Pin: pin, Kind: e.Kind, Body: e.Body, Status: e.Status,
		AppliedInRunID: pgUUID(e.AppliedInRun), AddressedInVersion: addressed,
	})
	if err != nil {
		return Annotation{}, mapErr(err)
	}
	if a.ParentID.IsZero() {
		if err := s.q.MoveAnnotationReplies(ctx, db.MoveAnnotationRepliesParams{WorkspaceID: ws, ParentID: id, StartMs: e.StartMs, EndMs: e.EndMs}); err != nil {
			return Annotation{}, mapErr(err)
		}
	}
	return a, nil
}

// SoftDeleteAnnotation deletes a comment and its replies. ErrNotFound when it is gone already.
func (s *Store) SoftDeleteAnnotation(ctx context.Context, ws ids.WorkspaceID, id ids.AnnotationID) error {
	n, err := s.q.SoftDeleteAnnotation(ctx, db.SoftDeleteAnnotationParams{WorkspaceID: ws, ID: id})
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListOpenAnnotationsForProject returns up to limit open comments (not replies) on the renders and versions of a project, oldest
// first: what an editor run is handed.
func (s *Store) ListOpenAnnotationsForProject(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, limit int) ([]Annotation, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	rows, err := s.q.ListOpenAnnotationsForProject(ctx, db.ListOpenAnnotationsForProjectParams{WorkspaceID: ws, ProjectID: project, RowLimit: int32(min(limit, maxAnnotationRowLimit))}) //nolint:gosec // capped
	return rows, mapErr(err)
}

// GetReadyRenderOfVersion returns the newest ready render of a version of a project (any profile), or ErrNotFound.
func (s *Store) GetReadyRenderOfVersion(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (Render, error) {
	if version < 1 || version > 1<<30 {
		return Render{}, ErrNotFound
	}
	r, err := s.q.GetReadyRenderOfVersion(ctx, db.GetReadyRenderOfVersionParams{WorkspaceID: ws, ProjectID: project, TimelineVersion: int32(version)}) //nolint:gosec // range checked
	return r, mapErr(err)
}

// ApplyAnnotations is the end of a successful editor run: the open comments and instructions of the project that existed when the run
// started become applied by the run, in the version it made (nil when it made none), and the editor answers each with reply, in
// the same transaction. Standing constraints (include, exclude, prefer) stay open. It returns the comments it marked.
func (s *Store) ApplyAnnotations(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, run uuid.UUID, version *int, reply string) ([]Annotation, error) {
	var marked []Annotation
	err := s.InTx(ctx, func(tx *Store) error {
		var v *int32
		if version != nil {
			n := int32(min(max(*version, 1), 1<<30)) //nolint:gosec // clamped
			v = &n
		}
		rows, err := tx.q.MarkAnnotationsApplied(ctx, db.MarkAnnotationsAppliedParams{WorkspaceID: ws, ProjectID: project, RunID: pgUUID(run), Version: v})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		parents := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			parents[i] = r.ID.UUID
		}
		if err := tx.q.CreateAgentReplies(ctx, db.CreateAgentRepliesParams{
			WorkspaceID: ws, Body: reply, AuthorID: ids.Encode(ids.PrefixRun, run), ParentIds: parents,
		}); err != nil {
			return err
		}
		marked = rows
		return nil
	})
	return marked, mapErr(err)
}
