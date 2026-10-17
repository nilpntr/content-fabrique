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

// LockAssetsForProject locks the (not deleted) assets of this workspace that the ids name until the end of the transaction
// and returns them. An id that is unknown, deleted or belongs to another workspace is simply missing from the result: the
// caller compares, and answers the same for all three. Use it inside InTx.
func (s *Store) LockAssetsForProject(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]Asset, error) {
	rows, err := s.q.LockAssetsForProject(ctx, db.LockAssetsForProjectParams{WorkspaceID: ws, AssetIds: uuids(assets)})
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
}

// ErrVersionConflict is returned by AddTimelineVersion when the version is not the project's next one: another writer was
// first, or the caller built on a version that is not the current one.
var ErrVersionConflict = errors.New("store: timeline version conflict")

// AddTimelineVersion stores the next version of a project's timeline and makes it the project's current one, in one
// transaction. Versions are immutable and numbered 1, 2, 3 ...: one that is not current+1 (or whose parent is not the
// current version) is ErrVersionConflict, and so is a number another writer took. It locks the project row, so two writers
// queue; the unique key (project, version) is the backstop. ErrNotFound when the project is not in this workspace or was
// deleted.
func (s *Store) AddTimelineVersion(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, in NewTimelineVersion) (TimelineVersion, error) {
	var v TimelineVersion
	err := s.inTx(ctx, func(tx *Store) error {
		p, err := tx.q.GetProjectForUpdate(ctx, db.GetProjectForUpdateParams{WorkspaceID: ws, ID: project})
		if err != nil {
			return mapErr(err)
		}
		current := 0
		if p.CurrentTimelineVersion != nil {
			current = int(*p.CurrentTimelineVersion)
		}
		if in.Version != current+1 || in.Parent != current {
			return ErrVersionConflict
		}
		var parent *int32
		if in.Parent > 0 {
			n := int32(in.Parent) //nolint:gosec // equals the current version, which the table keeps small
			parent = &n
		}
		summary := in.Summary
		if summary == nil {
			summary = []string{}
		}
		v, err = tx.q.InsertTimelineVersion(ctx, db.InsertTimelineVersionParams{
			WorkspaceID: ws, ProjectID: project, Version: int32(in.Version), //nolint:gosec // see above
			ParentVersion: parent, Spec: in.Spec, Ops: orEmptyArray(in.Ops), Diff: orEmptyArray(in.Diff), Summary: summary,
			AuthorKind: in.AuthorKind, AuthorID: in.AuthorID,
		})
		if err != nil {
			if errors.Is(mapErr(err), ErrConflict) {
				return ErrVersionConflict
			}
			return mapErr(err)
		}
		_, err = tx.q.SetCurrentTimelineVersion(ctx, db.SetCurrentTimelineVersionParams{WorkspaceID: ws, ID: project, Version: &v.Version})
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
