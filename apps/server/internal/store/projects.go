package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Rows as the repository returns them.
type (
	Project              = db.Project
	ProjectListRow       = db.ListProjectsRow
	ProjectAssetRow      = db.ListProjectAssetsRow
	ProjectCatalogRow    = db.ListProjectCatalogAssetsRow
	ProjectFootageRow    = db.ListProjectFootageRow
	TimelineVersion      = db.TimelineVersion
	TimelineVersionBrief = db.ListTimelineVersionsRow
)

// The formats and statuses the projects check constraints allow.
const (
	Format9x16 = "9:16"
	Format1x1  = "1:1"
	Format4x5  = "4:5"
	Format16x9 = "16:9"

	ProjectDraft    = "draft"
	ProjectEditing  = "editing"
	ProjectReview   = "review"
	ProjectApproved = "approved"
	ProjectExported = "exported"
)

// MaxProjectAssets is the most assets one project can hold (project_assets.position is checked to 0..49 as well).
const MaxProjectAssets = 50

// The authors a timeline version can have (timeline_versions.author_kind).
const (
	AuthorUser  = "user"
	AuthorAgent = "agent"
)

// NewProject is the input of CreateProject. The caller has validated it (package projects); the database checks the rest.
type NewProject struct {
	Title     string
	Format    string
	Brief     string
	CreatedBy ids.UserID
}

// CreateProject inserts a project of this workspace (status draft, no timeline version yet). Run it inside InTx together
// with AddProjectAssets so that a project never exists without its footage.
func (s *Store) CreateProject(ctx context.Context, ws ids.WorkspaceID, in NewProject) (Project, error) {
	p, err := s.q.CreateProject(ctx, db.CreateProjectParams{WorkspaceID: ws, Title: in.Title, Format: in.Format, Brief: in.Brief, CreatedBy: in.CreatedBy})
	return p, mapErr(err)
}

// AddProjectAssets stores the footage of a project in the order given (position 0, 1, ...). The assets must be those of the
// project's workspace: the composite foreign keys refuse any other (ErrNotFound).
func (s *Store) AddProjectAssets(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, assets []ids.AssetID) error {
	if len(assets) < 1 || len(assets) > MaxProjectAssets {
		return fmt.Errorf("%w: a project holds 1 to %d assets", ErrInvalid, MaxProjectAssets)
	}
	err := s.q.AddProjectAssets(ctx, db.AddProjectAssetsParams{WorkspaceID: ws.UUID, ProjectID: project.UUID, AssetIds: uuids(assets)})
	return mapErr(err)
}

// ListAssetsForProject returns the (not deleted) assets of this workspace that the ids name. An id that is unknown, deleted
// or belongs to another workspace is simply missing from the result: the caller compares, and answers the same for all three.
func (s *Store) ListAssetsForProject(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]Asset, error) {
	rows, err := s.q.ListAssetsForProject(ctx, db.ListAssetsForProjectParams{WorkspaceID: ws, AssetIds: uuids(assets)})
	return rows, mapErr(err)
}

// LockReadyAssetsForProject locks the ready (not deleted) assets of this workspace that the ids name until the end of the
// transaction and returns them. Only ready assets are locked, so an ingest that is still updating an asset never waits for a
// project. Use it inside InTx.
func (s *Store) LockReadyAssetsForProject(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]Asset, error) {
	rows, err := s.q.LockReadyAssetsForProject(ctx, db.LockReadyAssetsForProjectParams{WorkspaceID: ws, AssetIds: uuids(assets)})
	return rows, mapErr(err)
}

func uuids(in []ids.AssetID) []uuid.UUID {
	out := make([]uuid.UUID, len(in))
	for i, id := range in {
		out[i] = id.UUID
	}
	return out
}

// GetProject returns the (not deleted) project with this id in this workspace. A project of another workspace is ErrNotFound.
func (s *Store) GetProject(ctx context.Context, ws ids.WorkspaceID, id ids.ProjectID) (Project, error) {
	p, err := s.q.GetProject(ctx, db.GetProjectParams{WorkspaceID: ws, ID: id})
	return p, mapErr(err)
}

// GetProjectForUpdate is GetProject with a row lock until the end of the transaction (use it inside InTx): the writers of
// one project's timeline queue on it, so two edits cannot both build on the same version.
func (s *Store) GetProjectForUpdate(ctx context.Context, ws ids.WorkspaceID, id ids.ProjectID) (Project, error) {
	p, err := s.q.GetProjectForUpdate(ctx, db.GetProjectForUpdateParams{WorkspaceID: ws, ID: id})
	return p, mapErr(err)
}

// ListProjects returns up to limit projects of the workspace, newest first, those older than before when it is not nil.
func (s *Store) ListProjects(ctx context.Context, ws ids.WorkspaceID, before *ids.ProjectID, limit int) ([]ProjectListRow, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	var cursor pgtype.UUID
	if before != nil {
		cursor = pgtype.UUID{Bytes: before.UUID, Valid: true}
	}
	rows, err := s.q.ListProjects(ctx, db.ListProjectsParams{WorkspaceID: ws, Before: cursor, RowLimit: int32(min(limit, 1000))}) //nolint:gosec // capped
	return rows, mapErr(err)
}

// SoftDeleteProject sets deleted_at. ErrNotFound when the project is not in this workspace or is deleted already.
func (s *Store) SoftDeleteProject(ctx context.Context, ws ids.WorkspaceID, id ids.ProjectID) error {
	_, err := s.q.SoftDeleteProject(ctx, db.SoftDeleteProjectParams{WorkspaceID: ws, ID: id})
	return mapErr(err)
}

// ListProjectAssets returns the footage of a project in order, with what the library knows about each asset. A deleted
// asset is included (Deleted is true).
func (s *Store) ListProjectAssets(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID) ([]ProjectAssetRow, error) {
	rows, err := s.q.ListProjectAssets(ctx, db.ListProjectAssetsParams{WorkspaceID: ws, ProjectID: project})
	return rows, mapErr(err)
}

// ListProjectCatalogAssets returns the footage of a project that an edit may use: ready, not deleted, with a duration.
func (s *Store) ListProjectCatalogAssets(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID) ([]ProjectCatalogRow, error) {
	rows, err := s.q.ListProjectCatalogAssets(ctx, db.ListProjectCatalogAssetsParams{WorkspaceID: ws, ProjectID: project})
	return rows, mapErr(err)
}

// NewTimelineVersion is the input of AddTimelineVersion: one version as internal/timeline/ops built it. Parent is 0 for the
// first version.
type NewTimelineVersion struct {
	Version    int
	Parent     int
	Spec       json.RawMessage
	Ops        json.RawMessage
	Diff       json.RawMessage
	Summary    []string
	AuthorKind string
	AuthorID   string
	// IdempotencyKey and Result are set for a version an agent's tool call made: the call's key (unique per project), and the
	// JSON object the repeat of the call answers with. Empty for a person's edit.
	IdempotencyKey string
	Result         json.RawMessage
}

// ErrVersionConflict is returned by AddTimelineVersion when the project is no longer at the version the new one was made
// from: another writer was first.
var ErrVersionConflict = errors.New("store: timeline version conflict")

// AddTimelineVersion stores a version of a project's timeline and makes it the project's current one, in one transaction.
// Versions are immutable and numbered 1, 2, 3 ...; in.Parent is the version this one was made from (0 for the first), and
// the project moves to the new version only if it is still at the parent. That guarded update is the one check, and the
// unique key (project, version) backs it: a writer that lost the race gets ErrVersionConflict and nothing is stored.
// ErrNotFound when the project is not in this workspace or was deleted. Callers that read the current version first lock the
// project (GetProjectForUpdate) so that they do not do the work of an edit that is bound to lose.
func (s *Store) AddTimelineVersion(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, in NewTimelineVersion) (TimelineVersion, error) {
	if in.Version < 1 || in.Version > 1<<30 || in.Parent != in.Version-1 {
		return TimelineVersion{}, fmt.Errorf("%w: version %d with parent %d", ErrInvalid, in.Version, in.Parent)
	}
	var v TimelineVersion
	err := s.inTx(ctx, func(tx *Store) error {
		var parent *int32
		if in.Parent > 0 {
			n := int32(in.Parent) //nolint:gosec // range checked above
			parent = &n
		}
		summary := in.Summary
		if summary == nil {
			summary = []string{}
		}
		var err error
		v, err = tx.q.InsertTimelineVersion(ctx, db.InsertTimelineVersionParams{
			WorkspaceID: ws, ProjectID: project, Version: int32(in.Version), //nolint:gosec // range checked above
			ParentVersion: parent, Spec: in.Spec, Ops: orEmptyArray(in.Ops), Diff: orEmptyArray(in.Diff), Summary: summary,
			AuthorKind: in.AuthorKind, AuthorID: in.AuthorID, IdempotencyKey: nilIfEmpty(in.IdempotencyKey), Result: nilIfEmptyJSON(in.Result),
		})
		if err != nil {
			if errors.Is(mapErr(err), ErrConflict) {
				return ErrVersionConflict
			}
			return mapErr(err)
		}
		_, err = tx.q.SetCurrentTimelineVersion(ctx, db.SetCurrentTimelineVersionParams{WorkspaceID: ws, ID: project, Version: &v.Version, Parent: parent})
		if errors.Is(mapErr(err), ErrNotFound) {
			// Either the project is gone (deleted) or another writer moved it on.
			if _, perr := tx.q.GetProject(ctx, db.GetProjectParams{WorkspaceID: ws, ID: project}); perr != nil {
				return mapErr(perr)
			}
			return ErrVersionConflict
		}
		return mapErr(err)
	})
	if err != nil {
		return TimelineVersion{}, err
	}
	return v, nil
}

func orEmptyArray(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
}

// GetTimelineVersion returns one version of a project's timeline. ErrNotFound when the project is not in this workspace or
// has no such version. (Versions of a deleted project are still readable here, but the API reads the project first.)
func (s *Store) GetTimelineVersion(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (TimelineVersion, error) {
	if version < 1 || version > 1<<30 {
		return TimelineVersion{}, ErrNotFound
	}
	v, err := s.q.GetTimelineVersion(ctx, db.GetTimelineVersionParams{WorkspaceID: ws, ProjectID: project, Version: int32(version)}) //nolint:gosec // range checked
	return v, mapErr(err)
}

// ListTimelineVersions returns up to limit versions of a project's timeline, newest first, those below before when it is
// above zero, without the spec, ops and diff.
func (s *Store) ListTimelineVersions(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, before, limit int) ([]TimelineVersionBrief, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	var cursor *int32
	if before > 0 {
		n := int32(min(before, 1<<30)) //nolint:gosec // capped
		cursor = &n
	}
	rows, err := s.q.ListTimelineVersions(ctx, db.ListTimelineVersionsParams{WorkspaceID: ws, ProjectID: project, Before: cursor, RowLimit: int32(min(limit, 1000))}) //nolint:gosec // capped
	return rows, mapErr(err)
}

// ListProjectFootage returns the footage of a project as the editor agent sees it, in the creator's order: the ready, not
// deleted assets with their library title and description, probed size and rotation, and whether they have sound.
func (s *Store) ListProjectFootage(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID) ([]ProjectFootageRow, error) {
	rows, err := s.q.ListProjectFootage(ctx, db.ListProjectFootageParams{WorkspaceID: ws, ProjectID: project})
	return rows, mapErr(err)
}

// GetTimelineVersionByIdempotencyKey returns the version a tool call made, or ErrNotFound. This is how a retried call finds
// the version it made the first time.
func (s *Store) GetTimelineVersionByIdempotencyKey(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, key string) (TimelineVersion, error) {
	v, err := s.q.GetTimelineVersionByIdempotencyKey(ctx, db.GetTimelineVersionByIdempotencyKeyParams{WorkspaceID: ws, ProjectID: project, IdempotencyKey: &key})
	return v, mapErr(err)
}

// GetTimelineVersionByAuthor returns the latest version of the project that the author made, or ErrNotFound. The editor
// agent's author id is its run's TypeID, so this says whether a run has made a version.
func (s *Store) GetTimelineVersionByAuthor(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, authorKind, authorID string) (TimelineVersion, error) {
	v, err := s.q.GetTimelineVersionByAuthor(ctx, db.GetTimelineVersionByAuthorParams{WorkspaceID: ws, ProjectID: project, AuthorKind: authorKind, AuthorID: authorID})
	return v, mapErr(err)
}

func nilIfEmptyJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
