package renderjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/render"
)

// RenderAPI implements the renders operations of the generated api.StrictServerInterface. The workspace of every call comes from the
// Caller and nowhere else: no request field names a workspace.
type RenderAPI struct {
	svc    *Service
	caller httpapi.CallerFunc
	logger *slog.Logger
}

// NewAPI returns the handler. With a nil service (a server wired without object storage) every operation answers 503 after the
// checks of authentication and role, as the assets and projects handlers do.
func NewAPI(svc *Service, caller httpapi.CallerFunc, logger *slog.Logger) *RenderAPI {
	if logger == nil {
		logger = slog.Default()
	}
	return &RenderAPI{svc: svc, caller: caller, logger: logger}
}

// begin authorises a call. Reads are open to every role; a render costs CPU, so asking for one needs an owner or an editor.
func (h *RenderAPI) begin(ctx context.Context, write bool) (httpapi.Caller, *Error) {
	c, r := h.caller.Authorize(ctx, write, "renders")
	if r != nil {
		return httpapi.Caller{}, &Error{Status: r.Status, Code: r.Code, Message: r.Message}
	}
	if h.svc == nil {
		return httpapi.Caller{}, &Error{Status: http.StatusServiceUnavailable, Code: api.ErrorCodeStorageNotConfigured, Message: "renders are not available on this server"}
	}
	return c, nil
}

func body(e *Error) api.Error { return httpapi.ErrorBody(e.Code, e.Message) }

// undocumented is what an operation returns for a status its spec entry does not list: the generated error handler logs it
// and answers 500, so a new kind of failure is never reported as another one.
func undocumented(op string, e *Error) error {
	return fmt.Errorf("%s failed with undocumented status %d (%s)", op, e.Status, e.Code)
}

// ---- Conversions -----------------------------------------------------------------------------------------------

func ptrInt(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

func apiFile(f File) api.RenderFile {
	return api.RenderFile{Url: f.URL, ContentType: f.ContentType, SizeBytes: f.SizeBytes}
}

// renderOf converts a render to its API form. With files, a ready render carries presigned URLs, made now; a list leaves them out
// (files and urls_expires_at are null), because signing every row of a page is work nobody asked for: the reader fetches the render
// it wants.
func (h *RenderAPI) renderOf(ctx context.Context, r Render, withFiles bool) (api.Render, *Error) {
	out := api.Render{
		Id: r.ID.String(), ProjectId: r.ProjectID.String(), TimelineVersion: int(r.TimelineVersion),
		Profile: api.RenderProfile(r.Profile), Status: api.RenderStatus(r.Status), Progress: int(r.Progress), Stage: r.Stage,
		DurationMs: r.DurationMs, Width: ptrInt(r.Width), Height: ptrInt(r.Height), SizeBytes: r.SizeBytes,
		SoundCues: []api.SoundCue{}, FailureReason: r.FailureReason,
		RequestedBy: api.RequestedBy{Kind: api.AuthorKind(r.RequestedByKind), Id: r.RequestedByID},
		CreatedAt:   r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
	if len(r.SoundCues) > 0 {
		var cues []render.SoundCue
		if err := json.Unmarshal(r.SoundCues, &cues); err != nil {
			return api.Render{}, h.svc.internal(ctx, "decode sound cues", err)
		}
		for _, c := range cues {
			out.SoundCues = append(out.SoundCues, api.SoundCue{
				ItemId: c.ItemID, TrendId: c.TrendID, AtMs: c.AtMs, SourceOffsetMs: c.SourceOffsetMs, DurationMs: c.DurationMs, GainDb: float32(c.GainDB),
			})
		}
	}
	if len(r.ContactSheet) > 0 {
		var l SheetLayout
		if err := json.Unmarshal(r.ContactSheet, &l); err != nil {
			return api.Render{}, h.svc.internal(ctx, "decode contact sheet layout", err)
		}
		cs := api.ContactSheet{Columns: l.Columns, TileWidth: l.TileWidth, TileHeight: l.TileHeight, Tiles: make([]api.ContactSheetTile, len(l.Tiles))}
		for i, t := range l.Tiles {
			ct := api.ContactSheetTile{Index: t.Index, OutMs: t.OutMs, Frame: t.Frame, Kind: api.ContactSheetTileKind(t.Kind)}
			if t.ItemID != "" {
				id := t.ItemID
				ct.ItemId = &id
			}
			cs.Tiles[i] = ct
		}
		out.ContactSheet = &cs
	}
	if !withFiles {
		return out, nil
	}
	files, e := h.svc.Files(ctx, r)
	if e != nil {
		return api.Render{}, e
	}
	if files != nil {
		out.Files = &api.RenderFiles{Mp4: apiFile(files.MP4), ContactSheet: apiFile(files.ContactSheet), TimeMap: apiFile(files.TimeMap)}
		exp := files.Expires
		out.UrlsExpiresAt = &exp
	}
	return out, nil
}

// ---- Operations ------------------------------------------------------------------------------------------------

// CreateRender is POST /v1/projects/{id}/renders.
func (h *RenderAPI) CreateRender(ctx context.Context, req api.CreateRenderRequestObject) (api.CreateRenderResponseObject, error) {
	who, e := h.begin(ctx, true)
	var (
		r       Render
		created bool
	)
	if e == nil {
		project, err := ids.ParseProjectID(req.Id)
		switch {
		case err != nil:
			e = invalid("the project id is not valid")
		case req.Body == nil:
			e = invalid("a request body is required")
		default:
			r, created, e = h.svc.Request(ctx, UserCaller(who.Workspace, who.User), project, req.Body.Version, string(req.Body.Profile))
		}
	}
	var out api.Render
	if e == nil {
		out, e = h.renderOf(ctx, r, true)
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.CreateRender400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.CreateRender401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.CreateRender403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.CreateRender404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 422:
			return api.CreateRender422JSONResponse(body(e)), nil
		case 429:
			return api.CreateRender429JSONResponse(body(e)), nil
		case 500:
			return api.CreateRender500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.CreateRender503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("createRender", e)
	}
	if created {
		return api.CreateRender202JSONResponse(out), nil
	}
	return api.CreateRender200JSONResponse(out), nil
}

// ListRenders is GET /v1/projects/{id}/renders.
func (h *RenderAPI) ListRenders(ctx context.Context, req api.ListRendersRequestObject) (api.ListRendersResponseObject, error) {
	who, e := h.begin(ctx, false)
	var (
		rows []Render
		next string
	)
	if e == nil {
		project, err := ids.ParseProjectID(req.Id)
		if err != nil {
			e = invalid("the project id is not valid")
		} else {
			limit, cursor, f := 0, "", ListFilter{}
			if req.Params.Limit != nil {
				if limit = *req.Params.Limit; limit == 0 {
					limit = -1 // an explicit 0 is out of range, not "the default"
				}
			}
			if req.Params.Cursor != nil {
				cursor = *req.Params.Cursor
			}
			if req.Params.Version != nil {
				if f.Version = *req.Params.Version; f.Version == 0 {
					f.Version = -1
				}
			}
			if req.Params.Profile != nil {
				f.Profile = string(*req.Params.Profile)
			}
			rows, next, e = h.svc.List(ctx, who.Workspace, project, f, limit, cursor)
		}
	}
	items := make([]api.Render, 0, len(rows))
	for _, r := range rows {
		if e != nil {
			break
		}
		var item api.Render
		if item, e = h.renderOf(ctx, r, false); e == nil {
			items = append(items, item)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.ListRenders400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.ListRenders401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.ListRenders403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.ListRenders404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.ListRenders500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.ListRenders503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("listRenders", e)
	}
	res := api.ListRenders200JSONResponse{Items: items}
	if next != "" {
		res.NextCursor = &next
	}
	return res, nil
}

// GetRender is GET /v1/renders/{id}.
func (h *RenderAPI) GetRender(ctx context.Context, req api.GetRenderRequestObject) (api.GetRenderResponseObject, error) {
	who, e := h.begin(ctx, false)
	var out api.Render
	if e == nil {
		id, err := ids.ParseRenderID(req.Id)
		if err != nil {
			e = invalid("the render id is not valid")
		} else {
			var r Render
			if r, e = h.svc.Get(ctx, who.Workspace, id); e == nil {
				out, e = h.renderOf(ctx, r, true)
			}
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.GetRender400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.GetRender401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.GetRender403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.GetRender404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.GetRender500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.GetRender503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("getRender", e)
	}
	return api.GetRender200JSONResponse(out), nil
}
