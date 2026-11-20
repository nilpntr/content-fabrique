package clipfeedback

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// FeedbackAPI implements the shot list and clip feedback operations of the generated api.StrictServerInterface. The workspace of every
// call comes from the Caller and nowhere else: no request field names a workspace.
type FeedbackAPI struct {
	svc    *Service
	caller httpapi.CallerFunc
	logger *slog.Logger
}

// NewAPI returns the handler. With a nil service every operation answers 503 after the checks of authentication and role.
func NewAPI(svc *Service, caller httpapi.CallerFunc, logger *slog.Logger) *FeedbackAPI {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackAPI{svc: svc, caller: caller, logger: logger}
}

// begin authorises a call: reads are open to every role, feedback is given and taken back by owners and editors.
func (h *FeedbackAPI) begin(ctx context.Context, write bool) (httpapi.Caller, *Error) {
	c, r := h.caller.Authorize(ctx, write, "clip feedback")
	if r != nil {
		return httpapi.Caller{}, &Error{Status: r.Status, Code: r.Code, Message: r.Message}
	}
	if h.svc == nil {
		return httpapi.Caller{}, &Error{Status: http.StatusServiceUnavailable, Code: api.ErrorCodeStorageNotConfigured, Message: "clip feedback is not available on this server"}
	}
	return c, nil
}

func body(e *Error) api.Error { return httpapi.ErrorBody(e.Code, e.Message) }

func undocumented(op string, e *Error) error {
	return fmt.Errorf("%s failed with undocumented status %d (%s)", op, e.Status, e.Code)
}

// FeedbackOf is the API form of a stored row.
func FeedbackOf(r store.ClipFeedback) api.ClipFeedback {
	return api.ClipFeedback{
		Id: r.ID.String(), AssetId: r.AssetID.String(), Kind: api.ClipFeedbackKind(r.Kind), StartMs: r.StartMs, EndMs: r.EndMs, Note: r.Note,
		GivenOn: api.ClipFeedbackOrigin{
			ProjectId: r.ProjectID.String(), TimelineVersion: int(r.TimelineVersion), ShotNumber: int(r.ShotNumber), ItemId: r.ItemID,
		},
		CreatedAt: r.CreatedAt,
	}
}

func feedbackList(rows []store.ClipFeedback) []api.ClipFeedback {
	out := make([]api.ClipFeedback, len(rows))
	for i, r := range rows {
		out[i] = FeedbackOf(r)
	}
	return out
}

// GetShotList is GET /v1/projects/{id}/timelines/{version}/shots.
func (h *FeedbackAPI) GetShotList(ctx context.Context, req api.GetShotListRequestObject) (api.GetShotListResponseObject, error) {
	who, e := h.begin(ctx, false)
	var list ShotList
	if e == nil {
		project, err := ids.ParseProjectID(req.Id)
		if err != nil {
			e = invalid("the project id is not valid")
		} else {
			list, e = h.svc.ShotList(ctx, who.Workspace, project, req.Version)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.GetShotList400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.GetShotList401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.GetShotList403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.GetShotList404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.GetShotList500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.GetShotList503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("getShotList", e)
	}
	out := api.ShotList{ProjectId: list.Project.String(), TimelineVersion: list.Version, Shots: make([]api.Shot, len(list.Shots))}
	for i, s := range list.Shots {
		shot := api.Shot{
			Number: s.Number, ItemId: s.ItemID, AssetId: s.AssetID, AtMs: s.AtMs, EndMs: s.EndMs,
			SourceInMs: s.Source.InMs, SourceOutMs: s.Source.OutMs, Feedback: feedbackList(s.Feedback),
		}
		if s.HasTitle {
			title := s.Title
			shot.AssetTitle = &title
		}
		out.Shots[i] = shot
	}
	return api.GetShotList200JSONResponse(out), nil
}

// giveInput reads the request body of GiveClipFeedback.
func giveInput(b *api.GiveClipFeedbackRequest) (Give, *Error) {
	if b == nil {
		return Give{}, invalid("a request body is required")
	}
	project, err := ids.ParseProjectID(b.ProjectId)
	if err != nil {
		return Give{}, invalid("the project id is not valid")
	}
	in := Give{Project: project, Version: b.TimelineVersion, ShotNumber: b.ShotNumber, Kind: string(b.Kind)}
	if b.OffsetMs != nil {
		in.OffsetMs = int64(*b.OffsetMs)
	} else if in.Kind == StartLater {
		return Give{}, invalid("offset_ms is required with start_later")
	}
	if b.WholeClip != nil {
		in.WholeClip = *b.WholeClip
	}
	if b.Note != nil {
		in.Note = *b.Note
	}
	return in, nil
}

// GiveClipFeedback is POST /v1/clip-feedback.
func (h *FeedbackAPI) GiveClipFeedback(ctx context.Context, req api.GiveClipFeedbackRequestObject) (api.GiveClipFeedbackResponseObject, error) {
	who, e := h.begin(ctx, true)
	var row store.ClipFeedback
	if e == nil {
		var in Give
		if in, e = giveInput(req.Body); e == nil {
			row, e = h.svc.Give(ctx, Giver{Workspace: who.Workspace, User: who.User}, in)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.GiveClipFeedback400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.GiveClipFeedback401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.GiveClipFeedback403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.GiveClipFeedback404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.GiveClipFeedback500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.GiveClipFeedback503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("giveClipFeedback", e)
	}
	return api.GiveClipFeedback200JSONResponse(FeedbackOf(row)), nil
}

// ListClipFeedback is GET /v1/clip-feedback.
func (h *FeedbackAPI) ListClipFeedback(ctx context.Context, req api.ListClipFeedbackRequestObject) (api.ListClipFeedbackResponseObject, error) {
	who, e := h.begin(ctx, false)
	var rows []store.ClipFeedback
	if e == nil {
		var asset *ids.AssetID
		if req.Params.AssetId != nil {
			if aid, err := ids.ParseAssetID(*req.Params.AssetId); err != nil {
				e = invalid("the asset id is not valid")
			} else {
				asset = &aid
			}
		}
		limit := 0
		if req.Params.Limit != nil {
			if limit = *req.Params.Limit; limit == 0 {
				limit = -1 // an explicit 0 is out of range, not "the default"
			}
		}
		if e == nil {
			rows, e = h.svc.List(ctx, who.Workspace, asset, limit)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.ListClipFeedback400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.ListClipFeedback401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.ListClipFeedback403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 500:
			return api.ListClipFeedback500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.ListClipFeedback503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("listClipFeedback", e)
	}
	return api.ListClipFeedback200JSONResponse{Items: feedbackList(rows)}, nil
}

// DeleteClipFeedback is DELETE /v1/clip-feedback/{id}.
func (h *FeedbackAPI) DeleteClipFeedback(ctx context.Context, req api.DeleteClipFeedbackRequestObject) (api.DeleteClipFeedbackResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil {
		if id, err := ids.ParseClipFeedbackID(req.Id); err != nil {
			e = invalid("the clip feedback id is not valid")
		} else {
			e = h.svc.Delete(ctx, who.Workspace, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.DeleteClipFeedback400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.DeleteClipFeedback401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.DeleteClipFeedback403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.DeleteClipFeedback404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.DeleteClipFeedback500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.DeleteClipFeedback503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("deleteClipFeedback", e)
	}
	return api.DeleteClipFeedback204Response{}, nil
}
