package projects

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

// API implements the projects operations of the generated api.StrictServerInterface. The workspace of every call comes from
// the Caller and nowhere else: no request field names a workspace.
type API struct {
	svc    *Service
	caller httpapi.CallerFunc
	logger *slog.Logger
}

// NewAPI returns the handler. With a nil service (a server wired without one) every operation answers 503 after the checks of
// authentication and role, as the assets handler does without object storage.
func NewAPI(svc *Service, caller httpapi.CallerFunc, logger *slog.Logger) *API {
	if logger == nil {
		logger = slog.Default()
	}
	return &API{svc: svc, caller: caller, logger: logger}
}

// begin authorises a call. Reads are open to every role, changes need an owner or an editor (a viewer only looks).
func (h *API) begin(ctx context.Context, write bool) (Actor, *Error) {
	c, r := h.caller.Authorize(ctx, write, "projects")
	if r != nil {
		return Actor{}, &Error{Status: r.Status, Code: r.Code, Message: r.Message}
	}
	if h.svc == nil {
		return Actor{}, &Error{Status: http.StatusServiceUnavailable, Code: api.ErrorCodeInternalError, Message: "the projects API is not available on this server"}
	}
	return Actor{Workspace: c.Workspace, User: c.User}, nil
}

// parseID reads the project id of the path. A malformed one is a bad request; a well-formed one that belongs to another
// workspace is a 404 later, the same as one that never existed.
func parseID(raw string) (ids.ProjectID, *Error) {
	id, err := ids.ParseProjectID(raw)
	if err != nil {
		return ids.ProjectID{}, invalid("the project id is not valid")
	}
	return id, nil
}

func body(e *Error) api.Error { return httpapi.ErrorBody(e.Code, e.Message) }

// undocumented is what an operation returns for a status its spec entry does not list: the generated error handler logs
// it and answers 500, so a new kind of failure is never reported as another one.
func undocumented(op string, e *Error) error {
	return fmt.Errorf("%s failed with undocumented status %d (%s)", op, e.Status, e.Code)
}

// ---- Conversions -----------------------------------------------------------------------------------------------

func intPtr(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

func summaryOf(id ids.TimelineID, version int32, parent *int32, authorKind, authorID string, summary []string, createdAt time.Time) api.TimelineVersionSummary {
	if summary == nil {
		summary = []string{}
	}
	return api.TimelineVersionSummary{
		Id: id.String(), Version: int(version), ParentVersion: intPtr(parent), AuthorKind: api.AuthorKind(authorKind),
		AuthorId: authorID, Summary: summary, CreatedAt: createdAt,
	}
}

func briefOf(v store.TimelineVersionBrief) api.TimelineVersionSummary {
	return summaryOf(v.ID, v.Version, v.ParentVersion, v.AuthorKind, v.AuthorID, v.Summary, v.CreatedAt)
}

func fullSummaryOf(v store.TimelineVersion) api.TimelineVersionSummary {
	return summaryOf(v.ID, v.Version, v.ParentVersion, v.AuthorKind, v.AuthorID, v.Summary, v.CreatedAt)
}

func projectOf(d Detail) api.Project {
	p := d.Project
	assets := make([]api.ProjectAsset, len(d.Assets))
	for i, a := range d.Assets {
		assets[i] = api.ProjectAsset{
			AssetId: a.AssetID.String(), Position: int(a.Position), Title: a.Title, Kind: api.AssetKind(a.Kind),
			Status: api.AssetStatus(a.Status), DurationMs: a.DurationMs, Available: !a.Deleted && a.Status == store.AssetReady,
		}
	}
	out := api.Project{
		Id: p.ID.String(), Title: p.Title, Format: api.ProjectFormat(p.Format), Brief: p.Brief, Status: api.ProjectStatus(p.Status),
		AssetCount: d.AssetCount, CurrentTimelineVersion: intPtr(p.CurrentTimelineVersion), Assets: assets, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	if d.Current != nil {
		cur := briefOf(*d.Current)
		out.CurrentVersion = &cur
	}
	return out
}

func timelineIssuesOf(in []timeline.Issue) []api.TimelineIssue {
	out := make([]api.TimelineIssue, len(in))
	for i, is := range in {
		out[i] = api.TimelineIssue{Code: string(is.Code), Path: is.Path, Message: is.Message, Severity: api.TimelineIssueSeverity(is.Severity)}
	}
	return out
}

func opIssuesOf(in []ops.Issue) []api.OpIssue {
	out := make([]api.OpIssue, len(in))
	for i, is := range in {
		o := api.OpIssue{OpIndex: is.OpIndex, Code: string(is.Code), Message: is.Message, Severity: api.OpIssueSeverity(is.Severity)}
		if is.Op != "" {
			op := is.Op
			o.Op = &op
		}
		if is.Field != "" {
			f := is.Field
			o.Field = &f
		}
		if is.Path != "" {
			p := is.Path
			o.Path = &p
		}
		out[i] = o
	}
	return out
}

// ---- Operations ------------------------------------------------------------------------------------------------

// CreateProject is POST /v1/projects.
func (h *API) CreateProject(ctx context.Context, req api.CreateProjectRequestObject) (api.CreateProjectResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil && req.Body == nil {
		e = invalid("a request body is required")
	}
	var d Detail
	if e == nil {
		d, e = h.svc.Create(ctx, who, *req.Body)
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.CreateProject400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.CreateProject401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.CreateProject403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.CreateProject404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 422:
			return api.CreateProject422JSONResponse(body(e)), nil
		case 500:
			return api.CreateProject500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.CreateProject503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("createProject", e)
	}
	return api.CreateProject201JSONResponse(projectOf(d)), nil
}

// ListProjects is GET /v1/projects.
func (h *API) ListProjects(ctx context.Context, req api.ListProjectsRequestObject) (api.ListProjectsResponseObject, error) {
	who, e := h.begin(ctx, false)
	var (
		rows []store.ProjectListRow
		next string
	)
	if e == nil {
		limit, cursor := 0, ""
		if req.Params.Limit != nil {
			limit = *req.Params.Limit
			if limit == 0 {
				limit = -1 // an explicit 0 is out of range, not "the default"
			}
		}
		if req.Params.Cursor != nil {
			cursor = *req.Params.Cursor
		}
		rows, next, e = h.svc.List(ctx, who, limit, cursor)
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.ListProjects400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.ListProjects401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.ListProjects403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 500:
			return api.ListProjects500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.ListProjects503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("listProjects", e)
	}
	items := make([]api.ProjectSummary, len(rows))
	for i, r := range rows {
		items[i] = api.ProjectSummary{
			Id: r.ID.String(), Title: r.Title, Format: api.ProjectFormat(r.Format), Status: api.ProjectStatus(r.Status),
			AssetCount: int(r.AssetCount), CurrentTimelineVersion: intPtr(r.CurrentTimelineVersion), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
	}
	res := api.ListProjects200JSONResponse{Items: items}
	if next != "" {
		res.NextCursor = &next
	}
	return res, nil
}

// GetProject is GET /v1/projects/{id}.
func (h *API) GetProject(ctx context.Context, req api.GetProjectRequestObject) (api.GetProjectResponseObject, error) {
	who, e := h.begin(ctx, false)
	var d Detail
	if e == nil {
		var id ids.ProjectID
		if id, e = parseID(req.Id); e == nil {
			d, e = h.svc.Get(ctx, who, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.GetProject400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.GetProject401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.GetProject403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.GetProject404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.GetProject500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.GetProject503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("getProject", e)
	}
	return api.GetProject200JSONResponse(projectOf(d)), nil
}

// DeleteProject is DELETE /v1/projects/{id}.
func (h *API) DeleteProject(ctx context.Context, req api.DeleteProjectRequestObject) (api.DeleteProjectResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil {
		var id ids.ProjectID
		if id, e = parseID(req.Id); e == nil {
			e = h.svc.Delete(ctx, who, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.DeleteProject400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.DeleteProject401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.DeleteProject403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.DeleteProject404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.DeleteProject500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.DeleteProject503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("deleteProject", e)
	}
	return api.DeleteProject204Response{}, nil
}

// ListTimelineVersions is GET /v1/projects/{id}/timelines.
func (h *API) ListTimelineVersions(ctx context.Context, req api.ListTimelineVersionsRequestObject) (api.ListTimelineVersionsResponseObject, error) {
	who, e := h.begin(ctx, false)
	var (
		rows []store.TimelineVersionBrief
		next int
	)
	if e == nil {
		var id ids.ProjectID
		if id, e = parseID(req.Id); e == nil {
			limit, before := 0, 0
			if req.Params.Limit != nil {
				limit = *req.Params.Limit
				if limit == 0 {
					limit = -1
				}
			}
			if req.Params.Before != nil {
				before = *req.Params.Before
				if before == 0 {
					before = -1
				}
			}
			rows, next, e = h.svc.Versions(ctx, who, id, limit, before)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.ListTimelineVersions400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.ListTimelineVersions401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.ListTimelineVersions403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.ListTimelineVersions404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.ListTimelineVersions500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.ListTimelineVersions503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("listTimelineVersions", e)
	}
	items := make([]api.TimelineVersionSummary, len(rows))
	for i, r := range rows {
		items[i] = briefOf(r)
	}
	res := api.ListTimelineVersions200JSONResponse{Items: items}
	if next > 0 {
		res.NextBefore = &next
	}
	return res, nil
}

// GetTimelineVersion is GET /v1/projects/{id}/timelines/{version}.
func (h *API) GetTimelineVersion(ctx context.Context, req api.GetTimelineVersionRequestObject) (api.GetTimelineVersionResponseObject, error) {
	who, e := h.begin(ctx, false)
	var vd VersionDetail
	if e == nil {
		var id ids.ProjectID
		if id, e = parseID(req.Id); e == nil {
			vd, e = h.svc.Version(ctx, who, id, req.Version)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.GetTimelineVersion400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.GetTimelineVersion401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.GetTimelineVersion403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.GetTimelineVersion404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.GetTimelineVersion500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.GetTimelineVersion503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("getTimelineVersion", e)
	}
	return api.GetTimelineVersion200JSONResponse(versionOf(vd.Version, vd.Issues)), nil
}

func versionOf(v store.TimelineVersion, issues []timeline.Issue) api.TimelineVersion {
	s := fullSummaryOf(v)
	return api.TimelineVersion{
		Id: s.Id, Version: s.Version, ParentVersion: s.ParentVersion, AuthorKind: s.AuthorKind, AuthorId: s.AuthorId,
		Summary: s.Summary, CreatedAt: s.CreatedAt, Spec: v.Spec, Ops: v.Ops, Diff: v.Diff, Issues: timelineIssuesOf(issues),
	}
}

// ApplyTimelineOps is POST /v1/projects/{id}/timelines.
func (h *API) ApplyTimelineOps(ctx context.Context, req api.ApplyTimelineOpsRequestObject) (api.ApplyTimelineOpsResponseObject, error) {
	who, e := h.begin(ctx, true)
	var edit Edit
	if e == nil {
		var id ids.ProjectID
		switch id, e = parseID(req.Id); {
		case e != nil:
		case req.Body == nil:
			e = invalid("a request body is required")
		default:
			edit, e = h.svc.ApplyOps(ctx, who.Workspace, id, req.Body.BaseVersion, req.Body.Ops,
				ops.Author{Kind: ops.AuthorUser, ID: who.User.String()})
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.ApplyTimelineOps400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.ApplyTimelineOps401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.ApplyTimelineOps403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.ApplyTimelineOps404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 409:
			return api.ApplyTimelineOps409JSONResponse(body(e)), nil
		case 422:
			return api.ApplyTimelineOps422JSONResponse(api.OpsRejected{Error: body(e).Error, Issues: opIssuesOf(e.Issues)}), nil
		case 500:
			return api.ApplyTimelineOps500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.ApplyTimelineOps503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("applyTimelineOps", e)
	}
	created := make([]api.CreatedItem, len(edit.Created))
	for i, c := range edit.Created {
		created[i] = api.CreatedItem{OpIndex: c.OpIndex, Kind: api.CreatedItemKind(c.Kind), Id: c.ID}
	}
	// The version was valid when it was made; what validating it finds now is what the batch's warnings said.
	return api.ApplyTimelineOps201JSONResponse{
		Version: versionOf(edit.Version, nil), Created: created, Warnings: opIssuesOf(edit.Warnings),
	}, nil
}
