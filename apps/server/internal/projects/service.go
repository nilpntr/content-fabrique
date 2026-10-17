// Package projects is the projects API of P4-04b and the timeline versions of P3-02: creating a project (its format, its
// footage and its brief), reading and deleting it, and the versions of its timeline, written by edit ops.
//
// Tenancy: every method takes the workspace of the verified token (the Handler reads it from the Principal, never from the
// request) and only reads or changes rows of that workspace. A project, a version or an asset of another workspace is
// exactly as unknown as one that was never issued: 404 not_found. An edit is checked against a catalog that holds only the
// project's own ready footage (Catalog), so an op cannot reach any other asset.
//
// A project starts without a timeline: a timeline needs at least one clip, so version 1 is made by the first edit, from an
// empty timeline of the project's format (base_version 0). Versions are immutable (the table allows INSERT and SELECT
// only) and numbered from 1; an edit names the version it was made for, and is refused (409) when the project moved on.
package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

// Limits of the API.
const (
	MaxTitleChars = 255
	MaxBriefChars = 4000
	// DefaultTitle is the title of a project created without one.
	DefaultTitle = "Untitled project"

	// MaxSpecBytes is the largest timeline spec a version may hold, and MaxItemsPerTrack the most items one track may hold. The
	// editor never needs more (a 180 s timeline), and a version is stored for good.
	MaxSpecBytes     = 1 << 20
	MaxItemsPerTrack = 500

	DefaultPageSize = 50
	MaxPageSize     = 100
	// DefaultVersionPage and MaxVersionPage bound a page of timeline versions.
	DefaultVersionPage = 100
	MaxVersionPage     = 200
)

// Error is a failure with the answer to give for it. The message never contains request data. Issues is set for the
// failures of an edit batch (invalid_ops, no_change).
type Error struct {
	Status  int
	Code    api.ErrorCode
	Message string
	Issues  []ops.Issue
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

func invalid(msg string) *Error {
	return &Error{Status: 400, Code: api.ErrorCodeInvalidRequest, Message: msg}
}

var (
	errNotFound      = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such project"}
	errAssetNotFound = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "an asset in asset_ids does not exist in this workspace"}
	errNoVersion     = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such timeline version"}
)

// Service is the projects API without HTTP.
type Service struct {
	store  *store.Store
	logger *slog.Logger
}

// NewService returns a Service. The store is required.
func NewService(st *store.Store, logger *slog.Logger) (*Service, error) {
	if st == nil {
		return nil, errors.New("projects: a store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, logger: logger}, nil
}

// Actor is who acts: the verified workspace and user of the request.
type Actor struct {
	Workspace ids.WorkspaceID
	User      ids.UserID
}

// Detail is a project with its footage and its latest timeline version.
type Detail struct {
	Project store.Project
	Assets  []store.ProjectAssetRow
	// AssetCount is the number of assets that are not deleted (the same count the project list shows).
	AssetCount int
	// Current is the latest version (without its spec), nil before the first edit.
	Current *store.TimelineVersionBrief
}

// ---- Create ----------------------------------------------------------------------------------------------------

// Create makes a project from the new-project flow. The project starts in status draft with no timeline version.
func (s *Service) Create(ctx context.Context, who Actor, req api.CreateProjectRequest) (Detail, *Error) {
	title, brief, format, assetIDs, e := validateCreate(req)
	if e != nil {
		return Detail{}, e
	}
	var created store.Project
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		rows, err := tx.ListAssetsForProject(ctx, who.Workspace, assetIDs)
		if err != nil {
			return err
		}
		byID := make(map[ids.AssetID]store.Asset, len(rows))
		for _, a := range rows {
			byID[a.ID] = a
		}
		// An id that is not in this workspace (or deleted, or unknown) is one answer, before anything is said about the
		// others: what is reported next only concerns assets of this workspace.
		var notUsable []string
		for _, id := range assetIDs {
			a, ok := byID[id]
			if !ok {
				return errAssetNotFound
			}
			if !usable(a) {
				notUsable = append(notUsable, id.String())
			}
		}
		if len(notUsable) > 0 {
			shown := notUsable[:min(len(notUsable), 5)]
			msg := "these assets are not ready or are images, so they cannot be edited: " + strings.Join(shown, ", ")
			if len(notUsable) > len(shown) {
				msg += fmt.Sprintf(" and %d more", len(notUsable)-len(shown))
			}
			return &Error{Status: 422, Code: api.ErrorCodeAssetsNotUsable, Message: msg}
		}
		// All of them are ready: lock them (FOR SHARE) until the project is stored, so none is deleted in between. Only ready
		// assets are locked, so a worker that is still ingesting another asset is never held up. One that changed since the
		// first look is a race the caller can simply repeat.
		locked, err := tx.LockReadyAssetsForProject(ctx, who.Workspace, assetIDs)
		if err != nil {
			return err
		}
		if len(locked) != len(assetIDs) {
			return &Error{Status: 422, Code: api.ErrorCodeAssetsNotUsable, Message: "an asset changed while the project was being created; try again"}
		}
		if created, err = tx.CreateProject(ctx, who.Workspace, store.NewProject{Title: title, Format: format, Brief: brief, CreatedBy: who.User}); err != nil {
			return err
		}
		return tx.AddProjectAssets(ctx, who.Workspace, created.ID, assetIDs)
	})
	if err != nil {
		var pe *Error
		if errors.As(err, &pe) {
			return Detail{}, pe
		}
		if errors.Is(err, store.ErrInvalid) {
			return Detail{}, invalid("the title, format or brief is not valid")
		}
		return Detail{}, s.internal(ctx, "create project", err)
	}
	return s.detail(ctx, who.Workspace, created)
}

// usable says whether an asset can be part of a project: probed and ingested (ready), and something the timeline has an
// item for (a video or an audio file; an image is not).
func usable(a store.Asset) bool {
	return a.Status == store.AssetReady && a.DurationMs != nil && (a.Kind == store.AssetKindVideo || a.Kind == store.AssetKindAudio)
}

func validateCreate(req api.CreateProjectRequest) (title, brief, format string, assetIDs []ids.AssetID, e *Error) {
	if _, _, ok := timeline.Format(req.Format).Size(); !ok {
		return "", "", "", nil, invalid("format must be one of 9:16, 1:1, 4:5, 16:9")
	}
	format = string(req.Format)

	title = DefaultTitle
	if req.Title != nil {
		var ok bool
		if title, ok = cleanText(*req.Title, false); !ok || title == "" || utf8.RuneCountInString(title) > MaxTitleChars {
			return "", "", "", nil, invalid("title must be 1 to 255 characters of text")
		}
	}
	if req.Brief != nil {
		var ok bool
		if brief, ok = cleanText(*req.Brief, true); !ok || utf8.RuneCountInString(brief) > MaxBriefChars {
			return "", "", "", nil, invalid("brief must be text of at most 4,000 characters")
		}
	}
	if n := len(req.AssetIds); n < 1 || n > store.MaxProjectAssets {
		return "", "", "", nil, invalid("asset_ids must hold between 1 and 50 assets")
	}
	seen := make(map[ids.AssetID]bool, len(req.AssetIds))
	for _, raw := range req.AssetIds {
		id, err := ids.ParseAssetID(raw)
		if err != nil {
			return "", "", "", nil, invalid("asset_ids holds an id that is not valid")
		}
		if seen[id] {
			return "", "", "", nil, invalid("asset_ids lists an asset twice")
		}
		seen[id] = true
		assetIDs = append(assetIDs, id)
	}
	return title, brief, format, assetIDs, nil
}

// cleanText makes a title or a brief safe to store, show and hand to a model: valid UTF-8 in NFC, without the Unicode format
// characters (the bidirectional overrides and isolates that make text read differently from what it says, zero-width spaces
// and joiners, the byte order mark), which are removed as sanitizeFilename of package assets does, and with other spaces
// turned into plain ones. A control character, the line and paragraph separators, or a NUL byte makes the text invalid (ok is
// false); a brief (multiline) may keep line breaks and tabs. A title is trimmed and may end up empty: the caller refuses that.
func cleanText(raw string, multiline bool) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	var b strings.Builder
	for _, r := range norm.NFC.String(raw) {
		switch {
		case multiline && (r == '\n' || r == '\r' || r == '\t'):
			b.WriteRune(r)
		case unicode.IsControl(r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r), r == utf8.RuneError:
			return "", false
		case unicode.Is(unicode.Cf, r):
			// dropped
		case unicode.Is(unicode.Zs, r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String()), true
}

// ---- Read ------------------------------------------------------------------------------------------------------

// Get returns one project of the workspace.
func (s *Service) Get(ctx context.Context, who Actor, id ids.ProjectID) (Detail, *Error) {
	p, err := s.store.GetProject(ctx, who.Workspace, id)
	if errors.Is(err, store.ErrNotFound) {
		return Detail{}, errNotFound
	}
	if err != nil {
		return Detail{}, s.internal(ctx, "get project", err)
	}
	return s.detail(ctx, who.Workspace, p)
}

func (s *Service) detail(ctx context.Context, ws ids.WorkspaceID, p store.Project) (Detail, *Error) {
	assets, err := s.store.ListProjectAssets(ctx, ws, p.ID)
	if err != nil {
		return Detail{}, s.internal(ctx, "list project assets", err)
	}
	d := Detail{Project: p, Assets: assets}
	for _, a := range assets {
		if !a.Deleted {
			d.AssetCount++
		}
	}
	if p.CurrentTimelineVersion != nil {
		latest, err := s.store.ListTimelineVersions(ctx, ws, p.ID, 0, 1)
		if err != nil {
			return Detail{}, s.internal(ctx, "latest timeline version", err)
		}
		if len(latest) == 1 {
			d.Current = &latest[0]
		}
	}
	return d, nil
}

// List returns a page of the workspace's projects, newest first, and the cursor of the next page ("" on the last one). The
// cursor is the id of the last project of the page.
func (s *Service) List(ctx context.Context, who Actor, limit int, cursor string) ([]store.ProjectListRow, string, *Error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, "", invalid("limit must be between 1 and 100")
	}
	var before *ids.ProjectID
	if cursor != "" {
		id, err := ids.ParseProjectID(cursor)
		if err != nil {
			return nil, "", invalid("cursor is not valid")
		}
		before = &id
	}
	rows, err := s.store.ListProjects(ctx, who.Workspace, before, limit+1)
	if err != nil {
		return nil, "", s.internal(ctx, "list projects", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID.String()
	}
	return rows, next, nil
}

// Delete soft-deletes a project.
func (s *Service) Delete(ctx context.Context, who Actor, id ids.ProjectID) *Error {
	err := s.store.SoftDeleteProject(ctx, who.Workspace, id)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return errNotFound
	}
	return s.internal(ctx, "delete project", err)
}

// ---- Timeline versions -----------------------------------------------------------------------------------------

// Versions returns a page of the project's timeline versions, newest first, and the number to pass as before for the next
// page (0 on the last one).
func (s *Service) Versions(ctx context.Context, who Actor, id ids.ProjectID, limit, before int) ([]store.TimelineVersionBrief, int, *Error) {
	if limit == 0 {
		limit = DefaultVersionPage
	}
	if limit < 1 || limit > MaxVersionPage {
		return nil, 0, invalid("limit must be between 1 and 200")
	}
	if before < 0 {
		return nil, 0, invalid("before must be a version number")
	}
	if _, err := s.store.GetProject(ctx, who.Workspace, id); err != nil {
		return nil, 0, s.projectErr(ctx, err)
	}
	rows, err := s.store.ListTimelineVersions(ctx, who.Workspace, id, before, limit+1)
	if err != nil {
		return nil, 0, s.internal(ctx, "list timeline versions", err)
	}
	next := 0
	if len(rows) > limit {
		rows = rows[:limit]
		next = int(rows[limit-1].Version)
	}
	return rows, next, nil
}

// VersionDetail is one stored version and what validating its spec finds now.
type VersionDetail struct {
	Version store.TimelineVersion
	Issues  []timeline.Issue
}

// Version returns one version with the issues of validating its spec against the project's footage as it is now.
func (s *Service) Version(ctx context.Context, who Actor, id ids.ProjectID, version int) (VersionDetail, *Error) {
	if version < 1 {
		return VersionDetail{}, invalid("the version must be a number from 1")
	}
	if _, err := s.store.GetProject(ctx, who.Workspace, id); err != nil {
		return VersionDetail{}, s.projectErr(ctx, err)
	}
	v, err := s.store.GetTimelineVersion(ctx, who.Workspace, id, version)
	if errors.Is(err, store.ErrNotFound) {
		return VersionDetail{}, errNoVersion
	}
	if err != nil {
		return VersionDetail{}, s.internal(ctx, "get timeline version", err)
	}
	rows, err := s.store.ListProjectCatalogAssets(ctx, who.Workspace, id)
	if err != nil {
		return VersionDetail{}, s.internal(ctx, "project catalog", err)
	}
	res := timeline.Validate(v.Spec, catalogOf(rows))
	issues := res.Issues
	if issues == nil {
		issues = []timeline.Issue{}
	}
	return VersionDetail{Version: v, Issues: issues}, nil
}

// ---- Edit ------------------------------------------------------------------------------------------------------

// Edit is what a successful ApplyOps made.
type Edit struct {
	Version store.TimelineVersion
	Created []ops.Created
	// Warnings are the non-blocking issues of the batch.
	Warnings []ops.Issue
	// Replayed is set by ApplyOpsOnce when the author had made this version already: nothing was applied now, Created and
	// Warnings are empty, and Version is the one stored earlier.
	Replayed bool
}

// ApplyOps applies the batch of ops (a JSON array, see ops.ParseBatch) to version baseVersion of the project's timeline and
// stores the result as the next version, written by author. baseVersion is 0 for a project without versions: the batch then
// builds version 1 from an empty timeline of the project's format.
//
// It is atomic and serialised per project: the project row is locked, the base version must be the current one (409
// version_conflict otherwise), the ops are applied against the project's own footage (Catalog), and the version, its diff and
// its summary are stored together with the project's new current version. A batch the engine refuses, or one that leaves the
// timeline invalid, is 422 invalid_ops with the issues, and one that changes nothing is 422 no_change; nothing is stored then.
//
// The HTTP handler calls it with the user as author; the editor agent's edit_timeline tool calls it with the run as author.
func (s *Service) ApplyOps(ctx context.Context, ws ids.WorkspaceID, id ids.ProjectID, baseVersion int, rawOps json.RawMessage, author ops.Author) (Edit, *Error) {
	return s.applyOps(ctx, ws, id, baseVersion, rawOps, author, false)
}

// ApplyOpsOnce is ApplyOps for a caller that may repeat a call it cannot tell was made: the editor agent's tool call, which a
// resumed run runs again when its worker died after the version was stored and before the result was. author.ID is the
// idempotency key (run id and tool-use id, at most 128 characters) and must be unique to the call. Under the project's lock,
// before anything else, it looks for a version that this author made: when there is one it is returned with Replayed set and
// nothing is applied, whatever base_version the repeat names (the project has moved on since). The key lives in the version
// itself (author_id), so the two cannot disagree.
func (s *Service) ApplyOpsOnce(ctx context.Context, ws ids.WorkspaceID, id ids.ProjectID, baseVersion int, rawOps json.RawMessage, author ops.Author) (Edit, *Error) {
	return s.applyOps(ctx, ws, id, baseVersion, rawOps, author, true)
}

func (s *Service) applyOps(ctx context.Context, ws ids.WorkspaceID, id ids.ProjectID, baseVersion int, rawOps json.RawMessage, author ops.Author, once bool) (Edit, *Error) {
	if baseVersion < 0 || baseVersion > 1<<30 {
		return Edit{}, invalid("base_version must be 0 or a version number")
	}
	// The batch is parsed (strictly, and size-checked) before the transaction opens, so a malformed one never holds the row lock.
	batch, pres := ops.ParseBatch(rawOps)
	if !pres.OK() {
		return Edit{}, rejected(api.ErrorCodeInvalidOps, "the ops are not valid", pres.Issues)
	}
	var out Edit
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		p, err := tx.GetProjectForUpdate(ctx, ws, id)
		if err != nil {
			return err
		}
		if once {
			earlier, err := tx.GetTimelineVersionByAuthor(ctx, ws, id, string(author.Kind), author.ID)
			switch {
			case err == nil:
				out.Version, out.Replayed = earlier, true
				return nil
			case !errors.Is(err, store.ErrNotFound):
				return err
			}
		}
		current := 0
		if p.CurrentTimelineVersion != nil {
			current = int(*p.CurrentTimelineVersion)
		}
		if baseVersion != current {
			return &Error{Status: 409, Code: api.ErrorCodeVersionConflict,
				Message: fmt.Sprintf("the timeline is at version %d, not %d; fetch it again before editing", current, baseVersion)}
		}
		parent, err := s.parentVersion(ctx, tx, ws, p, current)
		if err != nil {
			return err
		}
		rows, err := tx.ListProjectCatalogAssets(ctx, ws, id)
		if err != nil {
			return err
		}
		next, res := ops.NewVersion(parent, batch, author, catalogOf(rows))
		if next == nil {
			if !res.OK() {
				return rejected(api.ErrorCodeInvalidOps, "the ops could not be applied; nothing was stored", res.Issues)
			}
			return rejected(api.ErrorCodeNoChange, "the ops left the timeline as it was, so there is no new version", res.Issues)
		}
		if is := tooBig(next.Spec); is != nil {
			return rejected(api.ErrorCodeInvalidOps, "the timeline would be too large; nothing was stored", []ops.Issue{*is})
		}
		in, err := storable(next)
		if err != nil {
			return err
		}
		if len(in.Spec) > MaxSpecBytes {
			return rejected(api.ErrorCodeInvalidOps, "the timeline would be too large; nothing was stored",
				[]ops.Issue{limitIssue(fmt.Sprintf("the timeline is %d bytes, at most %d are allowed", len(in.Spec), MaxSpecBytes))})
		}
		if out.Version, err = tx.AddTimelineVersion(ctx, ws, id, in); err != nil {
			return err
		}
		out.Created, out.Warnings = res.Created, res.Issues
		return nil
	})
	if err != nil {
		var pe *Error
		switch {
		case errors.As(err, &pe):
			return Edit{}, pe
		case errors.Is(err, store.ErrNotFound):
			return Edit{}, errNotFound
		case errors.Is(err, store.ErrVersionConflict):
			return Edit{}, &Error{Status: 409, Code: api.ErrorCodeVersionConflict, Message: "the timeline changed while the ops were applied; fetch it again before editing"}
		case errors.Is(err, store.ErrInvalid):
			// The database refused what the engine accepted (a NUL character in a text, say).
			return Edit{}, rejected(api.ErrorCodeInvalidOps, "the timeline cannot be stored; do the texts hold a NUL character?", nil)
		}
		return Edit{}, s.internal(ctx, "apply ops", err)
	}
	return out, nil
}

// limitIssue is the issue of a timeline over the size limits.
func limitIssue(msg string) ops.Issue {
	return ops.Issue{OpIndex: ops.NoOp, Code: "timeline_too_large", Message: msg, Severity: timeline.SeverityError}
}

// tooBig reports a track with more than MaxItemsPerTrack items, or nil.
func tooBig(spec timeline.Spec) *ops.Issue {
	for _, t := range spec.Tracks {
		n := 0
		switch t := t.(type) {
		case *timeline.VideoTrack:
			n = len(t.Items)
		case *timeline.TextTrack:
			n = len(t.Items)
		case *timeline.AudioTrack:
			n = len(t.Items)
		}
		if n > MaxItemsPerTrack {
			is := limitIssue(fmt.Sprintf("track %q has %d items, at most %d are allowed", t.TrackID(), n, MaxItemsPerTrack))
			return &is
		}
	}
	return nil
}

// rejected is the *Error of a refused batch.
func rejected(code api.ErrorCode, msg string, issues []ops.Issue) *Error {
	return &Error{Status: 422, Code: code, Message: msg, Issues: issues}
}

// parentVersion is the version the batch is applied to: the stored current version, or for a project without one an empty
// timeline of the project's format (number 0).
func (s *Service) parentVersion(ctx context.Context, tx *store.Store, ws ids.WorkspaceID, p store.Project, current int) (ops.Version, error) {
	if current == 0 {
		spec, ok := EmptySpec(p.Format)
		if !ok {
			return ops.Version{}, fmt.Errorf("project %s has format %q", p.ID, p.Format)
		}
		return ops.Version{Number: 0, Spec: spec}, nil
	}
	row, err := tx.GetTimelineVersion(ctx, ws, p.ID, current)
	if err != nil {
		return ops.Version{}, fmt.Errorf("current timeline version %d: %w", current, err)
	}
	spec, err := timeline.Parse(row.Spec)
	if err != nil {
		return ops.Version{}, fmt.Errorf("stored spec of version %d: %w", current, err)
	}
	return ops.Version{Number: current, Spec: *spec}, nil
}

// storable encodes a version for the table.
func storable(v *ops.Version) (store.NewTimelineVersion, error) {
	spec, err := json.Marshal(v.Spec)
	if err != nil {
		return store.NewTimelineVersion{}, fmt.Errorf("encode spec: %w", err)
	}
	batch, err := ops.MarshalBatch(v.Ops)
	if err != nil {
		return store.NewTimelineVersion{}, fmt.Errorf("encode ops: %w", err)
	}
	patch, err := json.Marshal(v.Diff)
	if err != nil {
		return store.NewTimelineVersion{}, fmt.Errorf("encode diff: %w", err)
	}
	return store.NewTimelineVersion{
		Version: v.Number, Parent: v.Parent, Spec: spec, Ops: batch, Diff: patch, Summary: slices.Clone(v.Summary),
		AuthorKind: string(v.AuthorKind), AuthorID: v.AuthorID,
	}, nil
}

// ---- Failures --------------------------------------------------------------------------------------------------

func (s *Service) projectErr(ctx context.Context, err error) *Error {
	if errors.Is(err, store.ErrNotFound) {
		return errNotFound
	}
	return s.internal(ctx, "get project", err)
}

// internal logs an unexpected failure and returns the 500 that says nothing about it.
func (s *Service) internal(ctx context.Context, what string, err error) *Error {
	s.logger.ErrorContext(ctx, "projects: "+what+" failed", "error", err)
	return &Error{Status: 500, Code: api.ErrorCodeInternalError, Message: "something went wrong"}
}
