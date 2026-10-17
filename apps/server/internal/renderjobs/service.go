package renderjobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Render is a row of the renders table.
type Render = store.Render

// Limits of the API.
const (
	DefaultPageSize = 50
	MaxPageSize     = 100
	// URLExpiry is how long the presigned GET URLs of a render's files work. They are made fresh by every read of the render.
	URLExpiry = 30 * time.Minute
	// maxSheetBytes bounds the contact sheet read back for an agent: the sheet is made to be about 300 KB.
	maxSheetBytes = 4 << 20
)

// Error is a failure with the answer to give for it. The message never contains request data.
type Error struct {
	Status  int
	Code    api.ErrorCode
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

func invalid(msg string) *Error {
	return &Error{Status: 400, Code: api.ErrorCodeInvalidRequest, Message: msg}
}

var (
	errProjectNotFound = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such project"}
	errVersionNotFound = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such timeline version"}
	errRenderNotFound  = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such render"}
	errTooMany         = &Error{Status: 429, Code: api.ErrorCodeTooManyRenders, Message: fmt.Sprintf("this workspace already has %d renders waiting or running; wait for one to finish", MaxInFlight)}
	errQueueMigration  = &Error{Status: 503, Code: api.ErrorCodeQueueNotMigrated, Message: "the job queue is not ready; try again later"}
)

// Objects is the object storage the service needs. *storage.Client is the real one.
type Objects interface {
	PresignGet(ctx context.Context, key string, expires time.Duration) (string, error)
	Get(ctx context.Context, key string) (storage.Object, error)
}

// Enqueuer inserts a job in the caller's transaction. *jobs.Enqueuer is the real one.
type Enqueuer interface {
	InsertInStore(ctx context.Context, tx *store.Store, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Caller is who asks for a render: the verified workspace and the requester recorded on the render, a user (a usr_ TypeID) or
// an agent run (its run id).
type Caller struct {
	Workspace ids.WorkspaceID
	// Kind is store.RenderRequestedByUser or store.RenderRequestedByAgent.
	Kind string
	ID   string
}

// UserCaller is the Caller of a user's request.
func UserCaller(ws ids.WorkspaceID, user ids.UserID) Caller {
	return Caller{Workspace: ws, Kind: store.RenderRequestedByUser, ID: user.String()}
}

// AgentCaller is the Caller of an editor run's tool call.
func AgentCaller(ws ids.WorkspaceID, runID string) Caller {
	return Caller{Workspace: ws, Kind: store.RenderRequestedByAgent, ID: runID}
}

// Service is the renders API without HTTP.
type Service struct {
	store   *store.Store
	objects Objects
	enq     Enqueuer
	logger  *slog.Logger
	now     func() time.Time
}

// NewService returns a Service. The store, object storage and the enqueuer are required.
func NewService(st *store.Store, objects Objects, enq Enqueuer, logger *slog.Logger) (*Service, error) {
	if st == nil || objects == nil || enq == nil {
		return nil, errors.New("renderjobs: a store, object storage and a job enqueuer are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, objects: objects, enq: enq, logger: logger, now: time.Now}, nil
}

// Request asks for a render of the version of the project with the profile ("preview" or "final"). It returns the render and
// whether it is a new one: a render of the same version and profile that is waiting, running or ready is returned as it is and
// nothing is queued (created is false). The version must exist and validate against the project's footage as it is now, and the
// workspace may have MaxInFlight renders in flight.
//
// It is serialised per workspace (an advisory lock, as the cap on uploads is), so the cap on renders in flight is exact however
// many requests race, and per project (the project row is locked, as the edit of a timeline does), so two requests for the same
// version cannot both queue a render; the partial unique index of the table is the backstop. The job is enqueued in the same
// transaction as the row: the row exists exactly when the job does.
func (s *Service) Request(ctx context.Context, who Caller, project ids.ProjectID, version int, profile string) (r Render, created bool, e *Error) {
	if _, err := render.ParseProfile(profile); err != nil {
		return Render{}, false, invalid("profile must be preview or final")
	}
	if version < 1 || version > 1<<30 {
		return Render{}, false, invalid("version must be a timeline version number")
	}
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		// The requests of one workspace take turns, so the count of renders in flight below is exact; then the project, so the
		// requests for one version take turns too. Always in this order.
		if err := tx.LockWorkspaceRenders(ctx, who.Workspace); err != nil {
			return err
		}
		if _, err := tx.GetProjectForUpdate(ctx, who.Workspace, project); err != nil {
			return err
		}
		v, err := tx.GetTimelineVersion(ctx, who.Workspace, project, version)
		if errors.Is(err, store.ErrNotFound) {
			return errVersionNotFound
		}
		if err != nil {
			return err
		}
		switch existing, err := tx.GetActiveRender(ctx, who.Workspace, project, version, profile); {
		case err == nil:
			r = existing
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		n, err := tx.CountRendersInFlight(ctx, who.Workspace)
		if err != nil {
			return err
		}
		if n >= MaxInFlight {
			return errTooMany
		}
		rows, err := tx.ListProjectCatalogAssets(ctx, who.Workspace, project)
		if err != nil {
			return err
		}
		if res := timeline.Validate(v.Spec, projects.CatalogOf(rows)); !res.OK() {
			return &Error{Status: 422, Code: api.ErrorCodeTimelineInvalid, Message: invalidMessage(res.Errors())}
		}
		if r, err = tx.CreateRender(ctx, who.Workspace, store.NewRender{
			Project: project, Version: version, Profile: profile, RequestedByKind: who.Kind, RequestedByID: who.ID,
		}); err != nil {
			return err
		}
		if _, err := s.enq.InsertInStore(ctx, tx, jobs.RenderArgs{WorkspaceID: who.Workspace, RenderID: r.ID}, &river.InsertOpts{Priority: jobs.RenderPriority(profile)}); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		var pe *Error
		switch {
		case errors.As(err, &pe):
			return Render{}, false, pe
		case errors.Is(err, store.ErrNotFound):
			return Render{}, false, errProjectNotFound
		case errors.Is(err, store.ErrQueueNotMigrated):
			return Render{}, false, errQueueMigration
		}
		return Render{}, false, s.internal(ctx, "request render", err)
	}
	return r, created, nil
}

// invalidMessage says why a version cannot be rendered: the first problems the validator found.
func invalidMessage(errs []timeline.Issue) string {
	const shown = 3
	parts := make([]string, 0, shown)
	for _, is := range errs[:min(len(errs), shown)] {
		parts = append(parts, fmt.Sprintf("%s: %s", is.Code, is.Message))
	}
	msg := "this timeline version cannot be rendered now: " + strings.Join(parts, "; ")
	if len(errs) > shown {
		msg += fmt.Sprintf(" (and %d more)", len(errs)-shown)
	}
	return msg
}

// Get returns one render of the workspace. A render of another workspace, or of a deleted project, is not found.
func (s *Service) Get(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID) (Render, *Error) {
	r, err := s.store.GetRender(ctx, ws, id)
	if errors.Is(err, store.ErrNotFound) {
		return Render{}, errRenderNotFound
	}
	if err != nil {
		return Render{}, s.internal(ctx, "get render", err)
	}
	return r, nil
}

// ListFilter narrows List.
type ListFilter struct {
	Version int
	Profile string
}

// List returns a page of a project's renders, newest first, and the cursor of the next page ("" on the last one). The cursor is
// the id of the last render of the page.
func (s *Service) List(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, f ListFilter, limit int, cursor string) ([]Render, string, *Error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, "", invalid("limit must be between 1 and 100")
	}
	flt := store.RenderFilter{Version: f.Version}
	if f.Version < 0 {
		return nil, "", invalid("version must be a timeline version number")
	}
	if f.Profile != "" {
		if _, err := render.ParseProfile(f.Profile); err != nil {
			return nil, "", invalid("profile must be preview or final")
		}
		flt.Profile = f.Profile
	}
	if cursor != "" {
		id, err := ids.ParseRenderID(cursor)
		if err != nil {
			return nil, "", invalid("cursor is not valid")
		}
		flt.Before = &id
	}
	if _, err := s.store.GetProject(ctx, ws, project); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, "", errProjectNotFound
		}
		return nil, "", s.internal(ctx, "get project", err)
	}
	rows, err := s.store.ListRenders(ctx, ws, project, flt, limit+1)
	if err != nil {
		return nil, "", s.internal(ctx, "list renders", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID.String()
	}
	return rows, next, nil
}

// File is one output of a render with a presigned GET URL.
type File struct {
	URL         string
	ContentType string
	SizeBytes   int64
}

// Files are the three outputs of a ready render.
type Files struct {
	MP4, ContactSheet, TimeMap File
	Expires                    time.Time
}

// Files presigns the outputs of a ready render, or returns nil for one that is not ready. The keys come from the row, and are
// checked against the render's own prefix before anything is signed.
func (s *Service) Files(ctx context.Context, r Render) (*Files, *Error) {
	if r.Status != store.RenderReady || r.Mp4Key == nil || r.ContactSheetKey == nil || r.TimeMapKey == nil ||
		r.SizeBytes == nil || r.ContactSheetBytes == nil || r.TimeMapBytes == nil {
		return nil, nil
	}
	out := &Files{Expires: s.now().Add(URLExpiry)}
	for _, f := range []struct {
		dst         *File
		name        string
		key         *string
		contentType string
		size        int64
	}{
		{&out.MP4, storage.RenderMP4Name, r.Mp4Key, "video/mp4", *r.SizeBytes},
		{&out.ContactSheet, storage.RenderContactSheetName, r.ContactSheetKey, "image/jpeg", *r.ContactSheetBytes},
		{&out.TimeMap, storage.RenderTimeMapName, r.TimeMapKey, "application/json", *r.TimeMapBytes},
	} {
		want, err := storage.RenderKey(r.WorkspaceID, r.ProjectID, r.ID, f.name)
		if err != nil || *f.key != want {
			return nil, s.internal(ctx, "render keys", fmt.Errorf("render %s has a key that is not its own", r.ID))
		}
		url, err := s.objects.PresignGet(ctx, want, URLExpiry)
		if err != nil {
			return nil, s.internal(ctx, "presign render file", err)
		}
		*f.dst = File{URL: url, ContentType: f.contentType, SizeBytes: f.size}
	}
	return out, nil
}

// ContactSheetJPEG reads the contact sheet of a ready render, for the agent's render_preview tool, which hands the image to the
// model. It is read through the server's own storage credentials, so no URL is involved.
func (s *Service) ContactSheetJPEG(ctx context.Context, r Render) ([]byte, error) {
	if r.Status != store.RenderReady || r.ContactSheetKey == nil {
		return nil, errors.New("renderjobs: the render has no contact sheet yet")
	}
	want, err := storage.RenderKey(r.WorkspaceID, r.ProjectID, r.ID, storage.RenderContactSheetName)
	if err != nil || *r.ContactSheetKey != want {
		return nil, errors.New("renderjobs: the contact sheet key is not the render's own")
	}
	obj, err := s.objects.Get(ctx, want)
	if err != nil {
		return nil, fmt.Errorf("get contact sheet: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(obj.Body, maxSheetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read contact sheet: %w", err)
	}
	if len(b) > maxSheetBytes {
		return nil, errors.New("renderjobs: the stored contact sheet is too large")
	}
	return b, nil
}

// internal logs an unexpected failure and returns the 500 that says nothing about it.
func (s *Service) internal(ctx context.Context, what string, err error) *Error {
	s.logger.ErrorContext(ctx, "renderjobs: "+what+" failed", "error", err)
	return &Error{Status: 500, Code: api.ErrorCodeInternalError, Message: "something went wrong"}
}
