package creatorstyle

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// Handler implements the style operations of the generated api.StrictServerInterface. The workspace of every call comes from the
// caller and nowhere else. Reads are open to every role; changes need an owner or an editor, like every other workspace setting.
type Handler struct {
	svc    *Service // nil when the style is not set up on this server
	caller httpapi.CallerFunc
	logger *slog.Logger
}

// NewHandler returns the handler. svc may be nil: every operation then answers 503, after the checks of authentication and role.
func NewHandler(svc *Service, caller httpapi.CallerFunc, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, caller: caller, logger: logger}
}

func (h *Handler) begin(ctx context.Context, write bool) (Actor, *Error) {
	c, r := h.caller.Authorize(ctx, write, "the style")
	if r != nil {
		return Actor{}, &Error{Status: r.Status, Code: r.Code, Message: r.Message}
	}
	if h.svc == nil {
		return Actor{}, &Error{Status: http.StatusServiceUnavailable, Code: api.ErrorCodeQueueNotMigrated, Message: "the style is not available on this server"}
	}
	return Actor{Workspace: c.Workspace, User: c.User}, nil
}

func body(e *Error) api.Error { return httpapi.ErrorBody(e.Code, e.Message) }

func undocumented(op string, e *Error) error {
	return fmt.Errorf("%s failed with undocumented status %d (%s)", op, e.Status, e.Code)
}

// ---- conversions ---------------------------------------------------------------------------------------------------------------

func ptr[T any](v T) *T { return &v }

// SettingsOf converts the API's settings.
func SettingsOf(in api.StyleSettings) styleprofile.Settings {
	var s styleprofile.Settings
	if in.Vibe != nil {
		s.Vibe = string(*in.Vibe)
	}
	if in.Transitions != nil {
		s.Transitions = string(*in.Transitions)
	}
	if in.TextAnimation != nil {
		s.TextAnimation = string(*in.TextAnimation)
	}
	if in.CameraMotion != nil {
		s.CameraMotion = string(*in.CameraMotion)
	}
	if in.TitleColour != nil {
		s.TitleColour = *in.TitleColour
	}
	if in.TitlePosition != nil {
		s.TitlePosition = string(*in.TitlePosition)
	}
	if in.TitleCase != nil {
		s.TitleCase = string(*in.TitleCase)
	}
	if in.Music != nil {
		s.Music = string(*in.Music)
	}
	if in.Pace != nil {
		s.Pace = string(*in.Pace)
	}
	if in.ChapterTitles != nil {
		s.ChapterTitles = string(*in.ChapterTitles)
	}
	if in.TitleTemplate != nil {
		s.TitleTemplate = *in.TitleTemplate
	}
	if in.EndingHold != nil {
		s.EndingHold = string(*in.EndingHold)
	}
	if in.CutTiming != nil {
		s.CutTiming = string(*in.CutTiming)
	}
	if in.AngleSwitches != nil {
		s.AngleSwitches = string(*in.AngleSwitches)
	}
	if in.TextPattern != nil {
		s.TextPattern = string(*in.TextPattern)
	}
	if in.TextPosition != nil {
		s.TextPosition = string(*in.TextPosition)
	}
	return s
}

func apiSettings(s styleprofile.Settings) api.StyleSettings {
	var out api.StyleSettings
	if s.Vibe != "" {
		out.Vibe = ptr(api.EditVibe(s.Vibe))
	}
	if s.Transitions != "" {
		out.Transitions = ptr(api.StyleLevel(s.Transitions))
	}
	if s.TextAnimation != "" {
		out.TextAnimation = ptr(api.StyleToggle(s.TextAnimation))
	}
	if s.CameraMotion != "" {
		out.CameraMotion = ptr(api.StyleLevel(s.CameraMotion))
	}
	if s.TitleColour != "" {
		out.TitleColour = ptr(s.TitleColour)
	}
	if s.TitlePosition != "" {
		out.TitlePosition = ptr(api.StyleTitlePosition(s.TitlePosition))
	}
	if s.TitleCase != "" {
		out.TitleCase = ptr(api.StyleTitleCase(s.TitleCase))
	}
	if s.Music != "" {
		out.Music = ptr(api.StyleMusic(s.Music))
	}
	if s.Pace != "" {
		out.Pace = ptr(api.StylePace(s.Pace))
	}
	if s.ChapterTitles != "" {
		out.ChapterTitles = ptr(api.StyleToggle(s.ChapterTitles))
	}
	if s.TitleTemplate != "" {
		out.TitleTemplate = ptr(s.TitleTemplate)
	}
	if s.EndingHold != "" {
		out.EndingHold = ptr(api.StyleEndingHold(s.EndingHold))
	}
	if s.CutTiming != "" {
		out.CutTiming = ptr(api.StyleCutTiming(s.CutTiming))
	}
	if s.AngleSwitches != "" {
		out.AngleSwitches = ptr(api.StyleToggle(s.AngleSwitches))
	}
	if s.TextPattern != "" {
		out.TextPattern = ptr(api.StyleTextPattern(s.TextPattern))
	}
	if s.TextPosition != "" {
		out.TextPosition = ptr(api.StyleTextPosition(s.TextPosition))
	}
	return out
}

func source(s string) api.StyleSettingSource { return api.StyleSettingSource(s) }

// StyleOf is the API's answer for a view.
func StyleOf(v View) api.CreatorStyle {
	st := v.Style
	r := st.Resolved
	eff := api.EffectiveStyleSettings{
		Vibe: api.EditVibe(r.Vibe), Transitions: api.StyleLevel(r.Transitions), TextAnimation: api.StyleToggle(r.TextAnimation),
		CameraMotion: api.StyleLevel(r.CameraMotion), TitlePosition: api.StyleTitlePosition(r.TitlePosition), TitleCase: api.StyleTitleCase(r.TitleCase),
		Music: api.StyleMusic(r.Music), Pace: api.StylePace(r.Pace),
		ChapterTitles: api.StyleToggle(r.ChapterTitles), EndingHold: api.StyleEndingHold(r.EndingHold), CutTiming: api.StyleCutTiming(r.CutTiming),
		AngleSwitches: api.StyleToggle(r.AngleSwitches), TextPattern: api.StyleTextPattern(r.TextPattern), TextPosition: api.StyleTextPosition(r.TextPosition),
		Sources: api.StyleSettingSources{
			Vibe: source(r.Sources.Vibe), Transitions: source(r.Sources.Transitions), TextAnimation: source(r.Sources.TextAnimation),
			CameraMotion: source(r.Sources.CameraMotion), TitleColour: source(r.Sources.TitleColour), TitlePosition: source(r.Sources.TitlePosition),
			TitleCase: source(r.Sources.TitleCase), Music: source(r.Sources.Music), Pace: source(r.Sources.Pace),
			ChapterTitles: source(r.Sources.ChapterTitles), TitleTemplate: source(r.Sources.TitleTemplate), EndingHold: source(r.Sources.EndingHold),
			CutTiming: source(r.Sources.CutTiming), AngleSwitches: source(r.Sources.AngleSwitches),
			TextPattern: source(r.Sources.TextPattern), TextPosition: source(r.Sources.TextPosition),
		},
	}
	if r.TitleColour != "" {
		eff.TitleColour = ptr(r.TitleColour)
	} else {
		eff.Sources.TitleColour = api.StyleSettingSourceDefault
	}
	if r.TitleTemplate != "" {
		eff.TitleTemplate = ptr(r.TitleTemplate)
	} else {
		eff.Sources.TitleTemplate = api.StyleSettingSourceDefault
	}
	out := api.CreatorStyle{
		Status: api.CreatorStyleStatus(v.Status), Progress: api.StyleProgress{Done: v.Done, Total: v.Total},
		Settings: apiSettings(st.Mine), Effective: eff, References: []api.StyleReference{},
		Limits: api.StyleLimits{
			MaxReferences: styleprofile.MaxReferences, MaxSizeBytes: styleprofile.MaxReferenceBytes,
			MaxDurationMs: int(styleprofile.MaxReferenceDuration.Milliseconds()),
		},
		LearnedAt: v.LearnedAt,
	}
	if s := st.Summary; s != nil && st.Profile != nil {
		out.Summary = &api.StyleSummary{Text: s.Text, Suggested: apiSettings(s.Suggested), References: s.References, PromptVersion: s.PromptVersion, WrittenAt: s.WrittenAt}
	}
	if p := st.Profile; p != nil {
		lines := p.PlainLines()
		m := &api.StyleMeasures{References: p.References, Confidence: float32(p.Confidence), Firm: p.Firm(), Lines: make([]api.StyleMeasure, len(lines))}
		for i, l := range lines {
			m.Lines[i] = api.StyleMeasure{Id: l.ID, Label: l.Label, Value: l.Value}
		}
		out.Measures = m
	}
	for _, ref := range v.References {
		item := api.StyleReference{Name: ref.Name, Status: api.StyleReferenceStatus(ref.Status), Progress: ref.Progress, CreatedAt: ref.CreatedAt}
		if ref.Asset != nil {
			item.AssetId = ptr(ref.Asset.String())
		}
		if ref.FailureReason != "" {
			item.FailureReason = ptr(ref.FailureReason)
		}
		if ref.DurationMs != nil {
			item.DurationMs = ptr(int(*ref.DurationMs))
		}
		if ref.PosterURL != "" {
			item.PosterUrl = ptr(ref.PosterURL)
		}
		out.References = append(out.References, item)
	}
	if !v.PostersExpire.IsZero() {
		out.PosterUrlsExpireAt = ptr(v.PostersExpire)
	}
	return out
}

// ---- operations ----------------------------------------------------------------------------------------------------------------

// GetStyle is GET /v1/style.
func (h *Handler) GetStyle(ctx context.Context, _ api.GetStyleRequestObject) (api.GetStyleResponseObject, error) {
	who, e := h.begin(ctx, false)
	var v View
	if e == nil {
		v, e = h.svc.Get(ctx, who)
	}
	if e != nil {
		switch e.Status {
		case 401:
			return api.GetStyle401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.GetStyle403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 500:
			return api.GetStyle500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.GetStyle503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("getStyle", e)
	}
	return api.GetStyle200JSONResponse(StyleOf(v)), nil
}

// UpdateStyle is PUT /v1/style.
func (h *Handler) UpdateStyle(ctx context.Context, req api.UpdateStyleRequestObject) (api.UpdateStyleResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil && req.Body == nil {
		e = invalid("a request body is required")
	}
	var v View
	if e == nil {
		v, e = h.svc.PutSettings(ctx, who, SettingsOf(req.Body.Settings))
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.UpdateStyle400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.UpdateStyle401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.UpdateStyle403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 500:
			return api.UpdateStyle500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.UpdateStyle503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("updateStyle", e)
	}
	return api.UpdateStyle200JSONResponse(StyleOf(v)), nil
}

// PatchStyle is PATCH /v1/style: only the settings given change, the ones named in clear go back to the layers below.
func (h *Handler) PatchStyle(ctx context.Context, req api.PatchStyleRequestObject) (api.PatchStyleResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil && req.Body == nil {
		e = invalid("a request body is required")
	}
	var v View
	if e == nil {
		var change styleprofile.Settings
		if req.Body.Settings != nil {
			change = SettingsOf(*req.Body.Settings)
		}
		var unset []string
		if req.Body.Clear != nil {
			unset = *req.Body.Clear
		}
		v, e = h.svc.PatchSettings(ctx, who, change, unset)
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.PatchStyle400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.PatchStyle401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.PatchStyle403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 500:
			return api.PatchStyle500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.PatchStyle503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("patchStyle", e)
	}
	return api.PatchStyle200JSONResponse(StyleOf(v)), nil
}

// ResetStyle is DELETE /v1/style.
func (h *Handler) ResetStyle(ctx context.Context, _ api.ResetStyleRequestObject) (api.ResetStyleResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil {
		e = h.svc.Reset(ctx, who)
	}
	if e != nil {
		switch e.Status {
		case 401:
			return api.ResetStyle401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.ResetStyle403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 500:
			return api.ResetStyle500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.ResetStyle503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("resetStyle", e)
	}
	return api.ResetStyle204Response{}, nil
}

// RelearnStyle is POST /v1/style/relearn.
func (h *Handler) RelearnStyle(ctx context.Context, _ api.RelearnStyleRequestObject) (api.RelearnStyleResponseObject, error) {
	who, e := h.begin(ctx, true)
	var v View
	if e == nil {
		v, e = h.svc.Relearn(ctx, who)
	}
	if e != nil {
		switch e.Status {
		case 401:
			return api.RelearnStyle401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.RelearnStyle403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 409:
			return api.RelearnStyle409JSONResponse(body(e)), nil
		case 429:
			return api.RelearnStyle429JSONResponse(body(e)), nil
		case 500:
			return api.RelearnStyle500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.RelearnStyle503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("relearnStyle", e)
	}
	return api.RelearnStyle202JSONResponse(StyleOf(v)), nil
}

// RemoveStyleReference is DELETE /v1/style/references/{id}.
func (h *Handler) RemoveStyleReference(ctx context.Context, req api.RemoveStyleReferenceRequestObject) (api.RemoveStyleReferenceResponseObject, error) {
	who, e := h.begin(ctx, true)
	if e == nil {
		id, err := ids.ParseAssetID(req.Id)
		if err != nil {
			e = invalid("the asset id is not valid")
		} else {
			e = h.svc.Remove(ctx, who, id)
		}
	}
	if e != nil {
		switch e.Status {
		case 400:
			return api.RemoveStyleReference400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(e))}, nil
		case 401:
			return api.RemoveStyleReference401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(e))}, nil
		case 403:
			return api.RemoveStyleReference403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(e))}, nil
		case 404:
			return api.RemoveStyleReference404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(e))}, nil
		case 500:
			return api.RemoveStyleReference500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(e))}, nil
		case 503:
			return api.RemoveStyleReference503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(e))}, nil
		}
		return nil, undocumented("removeStyleReference", e)
	}
	return api.RemoveStyleReference204Response{}, nil
}
