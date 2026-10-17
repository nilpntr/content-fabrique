package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Render is a row of renders: one run of a timeline version through a profile (P3-06).
type Render = db.Render

// RenderInput is one asset of a project that a render may read, with the keys of its original and its proxy.
type RenderInput = db.ListRenderInputsRow

// The statuses and profiles the renders check constraints allow.
const (
	RenderQueued    = "queued"
	RenderRendering = "rendering"
	RenderReady     = "ready"
	RenderFailed    = "failed"

	ProfilePreview = "preview"
	ProfileFinal   = "final"
)

// The stages a running render reports (renders.stage).
const (
	RenderStageStaging      = "staging"
	RenderStageMeasuring    = "measuring"
	RenderStageRendering    = "rendering"
	RenderStageContactSheet = "contact_sheet"
	RenderStageUploading    = "uploading"
)

// The two kinds of requester of a render (renders.requested_by_kind): a user with a usr_ TypeID, or an agent run.
const (
	RenderRequestedByUser  = AuthorUser
	RenderRequestedByAgent = AuthorAgent
)

const (
	maxRenderRowLimit        = 1000
	renderFailureReasonLimit = 64
)

// NewRender is the input of CreateRender. The caller has validated it (package renderjobs).
type NewRender struct {
	Project ids.ProjectID
	Version int
	Profile string
	// RequestedByKind is RenderRequestedByUser or RenderRequestedByAgent; RequestedByID the user's TypeID or the run id.
	RequestedByKind string
	RequestedByID   string
}

// CreateRender inserts a queued render. A second active render of the same version and profile is ErrConflict (the partial
// unique index): callers that want to reuse the first look it up with GetActiveRender under the project's lock instead.
// ErrNotFound when the project or the version is not in this workspace.
func (s *Store) CreateRender(ctx context.Context, ws ids.WorkspaceID, in NewRender) (Render, error) {
	if in.Version < 1 || in.Version > 1<<30 {
		return Render{}, fmt.Errorf("%w: version %d", ErrInvalid, in.Version)
	}
	r, err := s.q.CreateRender(ctx, db.CreateRenderParams{
		WorkspaceID: ws, ProjectID: in.Project, TimelineVersion: int32(in.Version), //nolint:gosec // range checked above
		Profile: in.Profile, RequestedByKind: in.RequestedByKind, RequestedByID: in.RequestedByID,
	})
	return r, mapErr(err)
}

// GetActiveRender returns the waiting, running or ready render of the version and profile, or ErrNotFound.
func (s *Store) GetActiveRender(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int, profile string) (Render, error) {
	if version < 1 || version > 1<<30 {
		return Render{}, ErrNotFound
	}
	r, err := s.q.GetActiveRender(ctx, db.GetActiveRenderParams{WorkspaceID: ws, ProjectID: project, TimelineVersion: int32(version), Profile: profile}) //nolint:gosec // range checked
	return r, mapErr(err)
}

// GetRender returns a render of a (not deleted) project of this workspace. A render of another workspace is ErrNotFound.
func (s *Store) GetRender(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID) (Render, error) {
	r, err := s.q.GetRender(ctx, db.GetRenderParams{WorkspaceID: ws, ID: id})
	return r, mapErr(err)
}

// RenderFilter narrows ListRenders. The zero value lists everything.
type RenderFilter struct {
	Version int    // 0: any
	Profile string // "": any
	Before  *ids.RenderID
}

// ListRenders returns up to limit renders of a project, newest first.
func (s *Store) ListRenders(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, f RenderFilter, limit int) ([]Render, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	p := db.ListRendersParams{WorkspaceID: ws, ProjectID: project, RowLimit: int32(min(limit, maxRenderRowLimit))} //nolint:gosec // capped
	if f.Before != nil {
		p.Before = pgtype.UUID{Bytes: f.Before.UUID, Valid: true}
	}
	if f.Version > 0 {
		v := int32(min(f.Version, 1<<30)) //nolint:gosec // capped
		p.TimelineVersion = &v
	}
	if f.Profile != "" {
		p.Profile = &f.Profile
	}
	rows, err := s.q.ListRenders(ctx, p)
	return rows, mapErr(err)
}

// CountRendersInFlight is how many renders of the workspace are waiting or running.
func (s *Store) CountRendersInFlight(ctx context.Context, ws ids.WorkspaceID) (int, error) {
	n, err := s.q.CountRendersInFlight(ctx, ws)
	return int(n), mapErr(err)
}

// ClaimRender takes a render for the job: queued -> rendering (a retry finds it rendering and starts over). ErrNotFound when
// it is finished, failed, deleted with its project, or not in this workspace.
func (s *Store) ClaimRender(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID) (Render, error) {
	r, err := s.q.ClaimRender(ctx, db.ClaimRenderParams{WorkspaceID: ws, ID: id})
	return r, mapErr(err)
}

// SetRenderProgress records how far a running render is. ErrNotFound when it is not running any more.
func (s *Store) SetRenderProgress(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID, progress int, stage string) error {
	_, err := s.q.SetRenderProgress(ctx, db.SetRenderProgressParams{WorkspaceID: ws, ID: id, Progress: int16(min(max(progress, 0), 100)), Stage: stage}) //nolint:gosec // clamped
	return mapErr(err)
}

// RenderResult is what a finished render stored: the keys of its three files and what is known about the video.
type RenderResult struct {
	MP4Key, ContactSheetKey, TimeMapKey string
	DurationMs                          int64
	Width, Height                       int
	SizeBytes                           int64
	ContactSheetBytes, TimeMapBytes     int64
	// ContactSheet is the layout of the sheet (JSON object); SoundCues the plan's cue_only sounds (JSON array).
	ContactSheet  json.RawMessage
	SoundCues     json.RawMessage
	CPUMs, WallMs int64
}

// FinishRender moves a running render to ready with its files. ErrNotFound when it is not running any more (it failed
// meanwhile) or its project was deleted while the job worked: the caller then removes what it uploaded.
func (s *Store) FinishRender(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID, r RenderResult) (Render, error) {
	cues := r.SoundCues
	if len(cues) == 0 {
		cues = json.RawMessage("[]")
	}
	w, h := int32(r.Width), int32(r.Height) //nolint:gosec // a video size
	out, err := s.q.FinishRender(ctx, db.FinishRenderParams{
		WorkspaceID: ws, ID: id, Mp4Key: &r.MP4Key, ContactSheetKey: &r.ContactSheetKey, TimeMapKey: &r.TimeMapKey,
		DurationMs: &r.DurationMs, Width: &w, Height: &h, SizeBytes: &r.SizeBytes, ContactSheetBytes: &r.ContactSheetBytes, TimeMapBytes: &r.TimeMapBytes, ContactSheet: r.ContactSheet, SoundCues: cues,
		CpuMs: &r.CPUMs, WallMs: &r.WallMs,
	})
	return out, mapErr(err)
}

// FailRender moves a waiting or running render to failed with a reason that is safe to show. ErrNotFound when it already
// finished or failed. wallMs and cpuMs are what the attempt used (negative: unknown).
func (s *Store) FailRender(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID, reason string, wallMs, cpuMs int64) (Render, error) {
	if reason == "" || len(reason) > renderFailureReasonLimit {
		return Render{}, fmt.Errorf("%w: failure reason", ErrInvalid)
	}
	p := db.FailRenderParams{WorkspaceID: ws, ID: id, FailureReason: reason}
	if wallMs >= 0 && cpuMs >= 0 {
		p.WallMs, p.CpuMs = &wallMs, &cpuMs
	}
	r, err := s.q.FailRender(ctx, p)
	return r, mapErr(err)
}

// ListRenderInputs returns the footage of a project that a render may read: ready, not deleted, with the keys of the original
// and the proxy.
func (s *Store) ListRenderInputs(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID) ([]RenderInput, error) {
	rows, err := s.q.ListRenderInputs(ctx, db.ListRenderInputsParams{WorkspaceID: ws, ProjectID: project})
	return rows, mapErr(err)
}

// ListRendersToPurge returns up to limit finished renders of deleted projects whose objects are still in storage, across
// workspaces: it serves the clean-up job, which acts for no workspace in particular.
func (s *Store) ListRendersToPurge(ctx context.Context, limit int) ([]Render, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	rows, err := s.q.ListRendersToPurge(ctx, int32(min(limit, maxRenderRowLimit))) //nolint:gosec // capped
	return rows, mapErr(err)
}

// MarkRenderObjectsPurged records that the objects of a render are gone.
func (s *Store) MarkRenderObjectsPurged(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID) error {
	return mapErr(s.q.MarkRenderObjectsPurged(ctx, db.MarkRenderObjectsPurgedParams{WorkspaceID: ws, ID: id}))
}
