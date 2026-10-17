package renderjobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// PreviewRenderer is the editor agent's render_preview (editor.PreviewRenderer) over RenderAndWait: it renders the version with the
// preview profile for the run, which is recorded as the requester (the run's TypeID), waits, and hands back the key of the contact
// sheet and the video's length. A render of that version that exists is reused, so the editor asking twice after a resume costs
// nothing.
type PreviewRenderer struct {
	svc     Requester
	timeout time.Duration
}

var _ editor.PreviewRenderer = (*PreviewRenderer)(nil)

// DefaultPreviewTimeout is how long a PreviewRenderer waits unless told otherwise: a little longer than the editor's own wait
// (editor.PreviewWait), which is the one that normally ends the call, so the editor tells the model "too slow" and the render goes
// on to be reused by the next ask.
const DefaultPreviewTimeout = editor.PreviewWait + 15*time.Second

// NewPreviewRenderer returns a PreviewRenderer over the service. A zero timeout means DefaultPreviewTimeout.
func NewPreviewRenderer(svc Requester, timeout time.Duration) *PreviewRenderer {
	if timeout <= 0 {
		timeout = DefaultPreviewTimeout
	}
	return &PreviewRenderer{svc: svc, timeout: timeout}
}

// NewPreviewRendererFor builds the PreviewRenderer a process that hosts the editor agent uses (the worker, cfctl agent run): the
// renders service over the store and object storage, with an insert-only River client over the store's pool for the render job
// (a worker that has the render pipeline works it). A nil logger means slog.Default.
func NewPreviewRendererFor(st *store.Store, objects Objects, logger *slog.Logger) (*PreviewRenderer, error) {
	river, err := jobs.NewInsertClient(st.Pool(), logger)
	if err != nil {
		return nil, err
	}
	svc, err := NewService(st, objects, jobs.NewEnqueuer(river, logger), logger)
	if err != nil {
		return nil, err
	}
	return NewPreviewRenderer(svc, 0), nil
}

// RenderAndWait implements editor.PreviewRenderer.
func (p *PreviewRenderer) RenderAndWait(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int, run uuid.UUID) (editor.ContactSheetRef, editor.DurationMs, error) {
	if p == nil || p.svc == nil {
		return "", 0, editor.ErrPreviewUnavailable
	}
	r, err := RenderAndWait(ctx, p.svc, AgentCaller(ws, ids.Encode(ids.PrefixRun, run)), project, version, store.ProfilePreview, p.timeout)
	var (
		failed  *FailedError
		refused *Error
	)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return "", 0, ctx.Err()
	case errors.As(err, &failed) && failed.Reason == ReasonGraphTooLong:
		return "", 0, runtime.UserErrorf("the preview was not rendered: the timeline's filter graph is too long, which means too many sound effects (or clips). Remove sound effects with remove_sound (a few per video are plenty), then render the new version")
	case errors.As(err, &failed) && failed.Reason == ReasonInvalidTimeline:
		return "", 0, runtime.UserErrorf("the preview render failed (%s): the timeline could not be compiled. Read the issues of get_timeline; if it lists none, remove the sound effects you added last, then render again", failed.Reason)
	case errors.As(err, &failed):
		return "", 0, runtime.UserErrorf("the preview render failed (%s). Check your work with get_timeline instead, and carry on", failed.Reason)
	case errors.As(err, &refused) && (refused.Status == 422 || refused.Status == 429):
		// The model can act on these: the timeline does not validate, or too many renders are running.
		return "", 0, runtime.UserErrorf("the preview was not started: %s", refused.Message)
	case errors.Is(err, ErrWaitTimeout):
		return "", 0, fmt.Errorf("render_preview: %w", context.DeadlineExceeded)
	default:
		return "", 0, err
	}
	if r.ContactSheetKey == nil || r.DurationMs == nil {
		return "", 0, errors.New("renderjobs: a ready render without a contact sheet")
	}
	return editor.ContactSheetRef(*r.ContactSheetKey), editor.DurationMs(*r.DurationMs), nil
}
