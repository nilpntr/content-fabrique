package annotations

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// CommentAPI implements the annotations operations of the generated api.StrictServerInterface. The workspace of every call comes from the
// Caller and nowhere else: no request field names a workspace.
type CommentAPI struct {
	svc    *Service
	caller httpapi.CallerFunc
	logger *slog.Logger
}

// NewAPI returns the handler. With a nil service every operation answers 500 after the checks of authentication and role (the
// service needs only the store, so a server without one is a programming error, not a configuration).
func NewAPI(svc *Service, caller httpapi.CallerFunc, logger *slog.Logger) *CommentAPI {
	if logger == nil {
		logger = slog.Default()
	}
	return &CommentAPI{svc: svc, caller: caller, logger: logger}
}

// begin authorises a call. Reading is open to every role; writing a comment needs an owner or an editor.
func (h *CommentAPI) begin(ctx context.Context, write bool) (Actor, *Error) {
	c, r := h.caller.Authorize(ctx, write, "comments")
	if r != nil {
		return Actor{}, &Error{Status: r.Status, Code: r.Code, Message: r.Message}
	}
	if h.svc == nil {
		return Actor{}, &Error{Status: http.StatusInternalServerError, Code: api.ErrorCodeInternalError, Message: "something went wrong"}
	}
	return Actor{Workspace: c.Workspace, User: c.User, Role: c.Role}, nil
}

func body(e *Error) api.Error { return httpapi.ErrorBody(e.Code, e.Message) }

// undocumented is what an operation returns for a status its spec entry does not list: the generated error handler logs it and
// answers 500, so a new kind of failure is never reported as another one.
func undocumented(op string, e *Error) error {
	return fmt.Errorf("%s failed with undocumented status %d (%s)", op, e.Status, e.Code)
}

// ---- Conversions -----------------------------------------------------------------------------------------------

func targetPrefix(targetType string) string {
	switch targetType {
	case store.AnnotationOnAsset:
		return ids.PrefixAsset
	case store.AnnotationOnRender:
		return ids.PrefixRender
	}
	return ids.PrefixTimeline
}

// Of converts a stored comment to its API form.
func Of(a Annotation) (api.Annotation, error) {
	out := api.Annotation{
		Id: a.ID.String(), TargetType: api.AnnotationTargetType(a.TargetType), TargetId: ids.Encode(targetPrefix(a.TargetType), a.TargetID),
		StartMs: a.StartMs, EndMs: a.EndMs, Kind: api.AnnotationKind(a.Kind), Body: a.Body, Status: api.AnnotationStatus(a.Status),
		Author:    api.AnnotationAuthor{Kind: api.AuthorKind(a.AuthorKind), Id: a.AuthorID},
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
	if !a.ProjectID.IsZero() {
		p := a.ProjectID.String()
		out.ProjectId = &p
	}
	if a.TimelineVersion != nil {
		v := int(*a.TimelineVersion)
		out.TimelineVersion = &v
	}
	if !a.ParentID.IsZero() {
		p := a.ParentID.String()
		out.ParentId = &p
	}
	if a.AppliedInRunID.Valid {
		r := ids.Encode(ids.PrefixRun, uuid.UUID(a.AppliedInRunID.Bytes))
		out.AppliedInRunId = &r
	}
	if a.AddressedInVersion != nil {
		v := int(*a.AddressedInVersion)
		out.AddressedInVersion = &v
	}
	pin, err := ParsePin(a.Pin)
	if err != nil {
		return api.Annotation{}, err
	}
	if pin != nil {
		out.Pin = &api.AnnotationPin{X: pin.X, Y: pin.Y, W: pin.W, H: pin.H}
	}
	return out, nil
}

func pinOf(p *api.AnnotationPin) *Pin {
	if p == nil {
		return nil
	}
	return &Pin{X: p.X, Y: p.Y, W: p.W, H: p.H}
}

func (h *CommentAPI) convert(ctx context.Context, a Annotation) (api.Annotation, *Error) {
	out, err := Of(a)
	if err != nil {
		h.logger.ErrorContext(ctx, "annotations: converting a stored comment failed", "error", err)
		return api.Annotation{}, &Error{Status: 500, Code: api.ErrorCodeInternalError, Message: "something went wrong"}
	}
	return out, nil
}

// ---- Operations ------------------------------------------------------------------------------------------------

// CreateAnnotation is POST /v1/annotations.
func (h *CommentAPI) CreateAnnotation(ctx context.Context, req api.CreateAnnotationRequestObject) (api.CreateAnnotationResponseObject, error) {
	who, e := h.begin(ctx, true)
	var out api.Annotation
	if e == nil {
		e = h.create(ctx, who, req.Body, &out)
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.CreateAnnotation400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.CreateAnnotation401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.CreateAnnotation403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.CreateAnnotation404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.CreateAnnotation500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.CreateAnnotation503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("createAnnotation", e)
	}
	return api.CreateAnnotation201JSONResponse(out), nil
}

func (h *CommentAPI) create(ctx context.Context, who Actor, req *api.CreateAnnotationRequest, out *api.Annotation) *Error {
	if req == nil {
		return invalid("a request body is required")
	}
	in := CreateInput{
		TargetType: string(deref(req.TargetType)), TargetID: deref(req.TargetId), StartMs: req.StartMs, EndMs: req.EndMs,
		Pin: pinOf(req.Pin), Body: deref(req.Body),
	}
	if req.Kind != nil {
		in.Kind = string(*req.Kind)
	}
	if req.ProjectId != nil {
		p, err := ids.ParseProjectID(*req.ProjectId)
		if err != nil {
			return invalid("project_id is not a valid project id")
		}
		in.Project = &p
	}
	if req.ParentId != nil {
		p, err := ids.ParseAnnotationID(*req.ParentId)
		if err != nil {
			return invalid("parent_id is not a valid comment id")
		}
		in.Parent = &p
	}
	a, e := h.svc.Create(ctx, who, in)
	if e != nil {
		return e
	}
	var ce *Error
	*out, ce = h.convert(ctx, a)
	return ce
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// ListAnnotations is GET /v1/annotations.
func (h *CommentAPI) ListAnnotations(ctx context.Context, req api.ListAnnotationsRequestObject) (api.ListAnnotationsResponseObject, error) {
	who, e := h.begin(ctx, false)
	var (
		rows []Annotation
		next string
	)
	if e == nil {
		q := ListQuery{TargetType: string(deref(req.Params.TargetType)), TargetID: deref(req.Params.TargetId), Cursor: deref(req.Params.Cursor)}
		if req.Params.Limit != nil {
			if q.Limit = *req.Params.Limit; q.Limit == 0 {
				q.Limit = -1 // an explicit 0 is out of range, not "the default"
			}
		}
		if req.Params.ProjectId != nil {
			p, err := ids.ParseProjectID(*req.Params.ProjectId)
			if err != nil {
				e = invalid("project_id is not a valid project id")
			}
			q.Project = &p
		}
		if e == nil {
			rows, next, e = h.svc.List(ctx, who, q)
		}
	}
	items := make([]api.Annotation, 0, len(rows))
	for _, r := range rows {
		if e != nil {
			break
		}
		var item api.Annotation
		if item, e = h.convert(ctx, r); e == nil {
			items = append(items, item)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.ListAnnotations400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.ListAnnotations401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.ListAnnotations403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.ListAnnotations404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.ListAnnotations500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.ListAnnotations503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("listAnnotations", e)
	}
	res := api.ListAnnotations200JSONResponse{Items: items}
	if next != "" {
		res.NextCursor = &next
	}
	return res, nil
}

// UpdateAnnotation is PATCH /v1/annotations/{id}.
func (h *CommentAPI) UpdateAnnotation(ctx context.Context, req api.UpdateAnnotationRequestObject) (api.UpdateAnnotationResponseObject, error) {
	who, e := h.begin(ctx, true)
	var out api.Annotation
	if e == nil {
		e = h.update(ctx, who, req, &out)
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.UpdateAnnotation400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.UpdateAnnotation401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.UpdateAnnotation403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.UpdateAnnotation404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.UpdateAnnotation500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.UpdateAnnotation503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("updateAnnotation", e)
	}
	return api.UpdateAnnotation200JSONResponse(out), nil
}

func (h *CommentAPI) update(ctx context.Context, who Actor, req api.UpdateAnnotationRequestObject, out *api.Annotation) *Error {
	id, err := ids.ParseAnnotationID(req.Id)
	if err != nil {
		return invalid("the comment id is not valid")
	}
	if req.Body == nil {
		return invalid("a request body is required")
	}
	b := req.Body
	in := UpdateInput{Body: b.Body, StartMs: b.StartMs, EndMs: b.EndMs, Pin: pinOf(b.Pin), RemovePin: deref(b.RemovePin)}
	if b.Kind != nil {
		k := string(*b.Kind)
		in.Kind = &k
	}
	if b.Status != nil {
		s := string(*b.Status)
		in.Status = &s
	}
	a, e := h.svc.Update(ctx, who, id, in)
	if e != nil {
		return e
	}
	var ce *Error
	*out, ce = h.convert(ctx, a)
	return ce
}

// DeleteAnnotation is DELETE /v1/annotations/{id}.
func (h *CommentAPI) DeleteAnnotation(ctx context.Context, req api.DeleteAnnotationRequestObject) (api.DeleteAnnotationResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil {
		if id, err := ids.ParseAnnotationID(req.Id); err != nil {
			e = invalid("the comment id is not valid")
		} else {
			e = h.svc.Delete(ctx, who, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.DeleteAnnotation400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.DeleteAnnotation401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.DeleteAnnotation403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.DeleteAnnotation404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.DeleteAnnotation500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.DeleteAnnotation503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("deleteAnnotation", e)
	}
	return api.DeleteAnnotation204Response{}, nil
}
