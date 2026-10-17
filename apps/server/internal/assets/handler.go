package assets

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Caller and CallerFunc are the shared ones of package httpapi: the workspace, user and role the verified token resolved to.
type (
	Caller     = httpapi.Caller
	CallerFunc = httpapi.CallerFunc
)

// Handler implements the assets operations of the generated api.StrictServerInterface. The workspace of every call comes
// from the Caller and nowhere else: no request field names a workspace.
type Handler struct {
	svc    *Service // nil when object storage is not configured
	caller httpapi.CallerFunc
	logger *slog.Logger
}

// NewHandler returns the handler. svc may be nil (object storage is not configured): every operation then answers 503
// storage_not_configured, after the checks of authentication and role.
func NewHandler(svc *Service, caller CallerFunc, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, caller: caller, logger: logger}
}

// begin authorises a call. Reads are open to every role, changes need an owner or an editor (a viewer only looks).
func (h *Handler) begin(ctx context.Context, write bool) (Actor, *Error) {
	c, r := h.caller.Authorize(ctx, write, "assets")
	if r != nil {
		return Actor{}, &Error{Status: r.Status, Code: r.Code, Message: r.Message}
	}
	if h.svc == nil {
		return Actor{}, &Error{Status: http.StatusServiceUnavailable, Code: api.ErrorCodeStorageNotConfigured, Message: "object storage is not configured on this server"}
	}
	return Actor{Workspace: c.Workspace, User: c.User}, nil
}

// parseID reads the asset id of the path. A malformed one is a bad request; a well-formed one that belongs to another
// workspace is a 404 later, the same as one that never existed.
func parseID(raw string) (ids.AssetID, *Error) {
	id, err := ids.ParseAssetID(raw)
	if err != nil {
		return ids.AssetID{}, invalid("the asset id is not valid")
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

func assetOf(a store.Asset) api.Asset {
	tags := a.Tags
	if tags == nil {
		tags = []string{}
	}
	out := api.Asset{
		Id: a.ID.String(), Kind: api.AssetKind(a.Kind), Status: api.AssetStatus(a.Status), Filename: a.Filename,
		ContentType: a.ContentType, SizeBytes: a.SizeBytes, Sha256: a.Sha256, Title: a.Title, Description: a.Description,
		Tags: tags, FailureReason: a.FailureReason, IngestProgress: int(a.IngestProgress), IngestStage: a.IngestStage,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
	if a.Status == store.AssetReady && a.DurationMs != nil {
		out.Media = &api.MediaInfo{
			DurationMs: *a.DurationMs, Width: intOf(a.Width), Height: intOf(a.Height), Fps: a.Fps,
			Rotation: api.MediaInfoRotation(deref(a.Rotation)), VideoCodec: a.VideoCodec, AudioCodec: a.AudioCodec,
			HasAudio: deref(a.HasAudio), Hdr: deref(a.Hdr),
		}
	}
	return out
}

// viewOf is assetOf plus the derivative URLs of a ready asset.
func viewOf(v View) api.Asset {
	out := assetOf(v.Asset)
	if len(v.Files) == 0 {
		return out
	}
	d := &api.AssetDerivatives{}
	for _, f := range v.Files {
		switch f.Kind {
		case store.DerivativeProxy720:
			d.Proxy720 = f.file()
		case store.DerivativePoster:
			d.Poster = f.file()
		case store.DerivativeHoverPreview:
			d.HoverPreview = f.file()
		case store.DerivativeSprite:
			d.Sprite = f.file()
		case store.DerivativeSpriteVTT:
			d.SpriteVtt = f.file()
		case store.DerivativeWaveform:
			d.Waveform = f.file()
		}
	}
	out.Derivatives = d
	exp := v.URLsExpires
	out.DerivativeUrlsExpireAt = &exp
	return out
}

func intOf(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (h *Handler) uploadOf(u store.Upload) api.UploadInfo {
	return api.UploadInfo{
		Id: u.ID.String(), AssetId: u.AssetID.String(), Status: h.svc.StatusOf(u), PartSize: u.PartSize,
		PartCount: int(u.PartCount), ExpiresAt: u.ExpiresAt,
	}
}

// ---- Operations ------------------------------------------------------------------------------------------------

// CreateAsset is POST /v1/assets.
func (h *Handler) CreateAsset(ctx context.Context, req api.CreateAssetRequestObject) (api.CreateAssetResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil && req.Body == nil {
		e = invalid("a request body is required")
	}
	var out Created
	if e == nil {
		out, e = h.svc.Create(ctx, who, *req.Body)
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.CreateAsset400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.CreateAsset401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.CreateAsset403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 413:
			return api.CreateAsset413JSONResponse(body(e)), nil
		case 415:
			return api.CreateAsset415JSONResponse(body(e)), nil
		case 429:
			return api.CreateAsset429JSONResponse(body(e)), nil
		case 500:
			return api.CreateAsset500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.CreateAsset503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("createAsset", e)
	}
	return api.CreateAsset201JSONResponse{Asset: assetOf(out.Asset), Upload: h.uploadOf(out.Upload), Parts: out.Parts, UrlsExpireAt: out.URLsExpires}, nil
}

// ListAssets is GET /v1/assets.
func (h *Handler) ListAssets(ctx context.Context, req api.ListAssetsRequestObject) (api.ListAssetsResponseObject, error) {
	who, e := h.begin(ctx, false)
	var (
		rows []View
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
			return api.ListAssets400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.ListAssets401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.ListAssets403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 500:
			return api.ListAssets500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.ListAssets503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("listAssets", e)
	}
	items := make([]api.Asset, len(rows))
	for i, v := range rows {
		items[i] = viewOf(v)
	}
	res := api.ListAssets200JSONResponse{Items: items}
	if next != "" {
		res.NextCursor = &next
	}
	return res, nil
}

// GetAsset is GET /v1/assets/{id}.
func (h *Handler) GetAsset(ctx context.Context, req api.GetAssetRequestObject) (api.GetAssetResponseObject, error) {
	who, e := h.begin(ctx, false)
	var v View
	if e == nil {
		var id ids.AssetID
		if id, e = parseID(req.Id); e == nil {
			v, e = h.svc.Get(ctx, who, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.GetAsset400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.GetAsset401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.GetAsset403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.GetAsset404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.GetAsset500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.GetAsset503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("getAsset", e)
	}
	return api.GetAsset200JSONResponse(viewOf(v)), nil
}

// GetAssetSpriteVtt is GET /v1/assets/{id}/sprite.vtt.
func (h *Handler) GetAssetSpriteVtt(ctx context.Context, req api.GetAssetSpriteVttRequestObject) (api.GetAssetSpriteVttResponseObject, error) {
	who, e := h.begin(ctx, false)
	var vtt string
	if e == nil {
		var id ids.AssetID
		if id, e = parseID(req.Id); e == nil {
			vtt, e = h.svc.SpriteVTT(ctx, who, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.GetAssetSpriteVtt400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.GetAssetSpriteVtt401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.GetAssetSpriteVtt403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.GetAssetSpriteVtt404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.GetAssetSpriteVtt500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.GetAssetSpriteVtt503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("getAssetSpriteVtt", e)
	}
	return api.GetAssetSpriteVtt200TextvttResponse{Body: strings.NewReader(vtt), ContentLength: int64(len(vtt))}, nil
}

// DeleteAsset is DELETE /v1/assets/{id}.
func (h *Handler) DeleteAsset(ctx context.Context, req api.DeleteAssetRequestObject) (api.DeleteAssetResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil {
		var id ids.AssetID
		if id, e = parseID(req.Id); e == nil {
			e = h.svc.Delete(ctx, who, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.DeleteAsset400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.DeleteAsset401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.DeleteAsset403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.DeleteAsset404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.DeleteAsset500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.DeleteAsset503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("deleteAsset", e)
	}
	return api.DeleteAsset204Response{}, nil
}

// GetAssetUpload is GET /v1/assets/{id}/upload.
func (h *Handler) GetAssetUpload(ctx context.Context, req api.GetAssetUploadRequestObject) (api.GetAssetUploadResponseObject, error) {
	who, e := h.begin(ctx, false)
	var st UploadState
	if e == nil {
		var id ids.AssetID
		if id, e = parseID(req.Id); e == nil {
			st, e = h.svc.State(ctx, who, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.GetAssetUpload400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.GetAssetUpload401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.GetAssetUpload403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.GetAssetUpload404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.GetAssetUpload500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.GetAssetUpload503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("getAssetUpload", e)
	}
	info := h.uploadOf(st.Upload)
	info.Status = st.Status
	parts := make([]api.UploadedPart, len(st.Parts))
	for i, p := range st.Parts {
		parts[i] = api.UploadedPart{PartNumber: p.Number, Etag: p.ETag, SizeBytes: p.Size}
	}
	return api.GetAssetUpload200JSONResponse{Upload: info, CompletedParts: parts}, nil
}

// AbortAssetUpload is DELETE /v1/assets/{id}/upload.
func (h *Handler) AbortAssetUpload(ctx context.Context, req api.AbortAssetUploadRequestObject) (api.AbortAssetUploadResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil {
		var id ids.AssetID
		if id, e = parseID(req.Id); e == nil {
			e = h.svc.Abort(ctx, who, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.AbortAssetUpload400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.AbortAssetUpload401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.AbortAssetUpload403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.AbortAssetUpload404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 409:
			return api.AbortAssetUpload409JSONResponse{UploadConflictJSONResponse: api.UploadConflictJSONResponse(body(e))}, nil
		case 500:
			return api.AbortAssetUpload500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.AbortAssetUpload503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("abortAssetUpload", e)
	}
	return api.AbortAssetUpload204Response{}, nil
}

// PresignAssetParts is POST /v1/assets/{id}/parts.
func (h *Handler) PresignAssetParts(ctx context.Context, req api.PresignAssetPartsRequestObject) (api.PresignAssetPartsResponseObject, error) {
	who, e := h.begin(ctx, true)
	var (
		urls    []api.PartUrl
		expires time.Time
	)
	if e == nil {
		var id ids.AssetID
		switch id, e = parseID(req.Id); {
		case e != nil:
		case req.Body == nil:
			e = invalid("a request body is required")
		default:
			urls, expires, e = h.svc.PresignParts(ctx, who, id, req.Body.PartNumbers)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.PresignAssetParts400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.PresignAssetParts401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.PresignAssetParts403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.PresignAssetParts404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 409:
			return api.PresignAssetParts409JSONResponse{UploadConflictJSONResponse: api.UploadConflictJSONResponse(body(e))}, nil
		case 500:
			return api.PresignAssetParts500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.PresignAssetParts503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("presignAssetParts", e)
	}
	return api.PresignAssetParts200JSONResponse{Parts: urls, UrlsExpireAt: expires}, nil
}

// CompleteAssetUpload is POST /v1/assets/{id}/complete.
func (h *Handler) CompleteAssetUpload(ctx context.Context, req api.CompleteAssetUploadRequestObject) (api.CompleteAssetUploadResponseObject, error) {
	who, e := h.begin(ctx, true)
	var a store.Asset
	if e == nil {
		var id ids.AssetID
		switch id, e = parseID(req.Id); {
		case e != nil:
		case req.Body == nil:
			e = invalid("a request body is required")
		default:
			a, e = h.svc.Complete(ctx, who, id, req.Body.Parts)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.CompleteAssetUpload400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.CompleteAssetUpload401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.CompleteAssetUpload403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.CompleteAssetUpload404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 409:
			return api.CompleteAssetUpload409JSONResponse{UploadConflictJSONResponse: api.UploadConflictJSONResponse(body(e))}, nil
		case 422:
			return api.CompleteAssetUpload422JSONResponse(body(e)), nil
		case 500:
			return api.CompleteAssetUpload500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.CompleteAssetUpload503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("completeAssetUpload", e)
	}
	return api.CompleteAssetUpload200JSONResponse(assetOf(a)), nil
}
