package editor

import (
	"context"
	"errors"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
)

// ContactSheetRef is the object key (ws/{workspace}/..., a JPEG or PNG) of a preview's contact sheet: a grid of frames from
// the whole video, labelled with their times. It goes into an image block as a reference; the bytes never enter an event.
type ContactSheetRef string

// DurationMs is a length in whole milliseconds.
type DurationMs int64

// PreviewRenderer renders a fast preview of one version of a project's timeline and waits for it. The render jobs of P3-06
// implement it.
//
// TODO(director): wire P3-06's renderjobs.RenderAndWait here once it has merged, in cmd/worker/agents.go (agentset.Options.Preview)
// and cmd/cfctl/agent.go. The implementation must (1) render the version with the preview profile for exactly this workspace
// and project, (2) return the key of the contact sheet, a JPEG or PNG under ws/{workspace}/ (the editor refuses any other key:
// CheckImageRef), (3) wait for a bounded time and return ctx's error when the context ends first, (4) return ErrPreviewUnavailable
// when rendering is not configured, and (5) be idempotent in (project, version): the editor may ask twice for the same version
// after a resume. Until then the editor is built with UnavailablePreviews{}.
type PreviewRenderer interface {
	RenderAndWait(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (ContactSheetRef, DurationMs, error)
}

// ErrPreviewUnavailable is what a PreviewRenderer returns when it cannot render at all (not configured, not built yet). The
// render_preview tool tells the model, which then checks its work with get_timeline.
var ErrPreviewUnavailable = errors.New("editor: previews are not available")

// UnavailablePreviews is the PreviewRenderer until the render jobs are wired: it always answers ErrPreviewUnavailable.
type UnavailablePreviews struct{}

// RenderAndWait implements PreviewRenderer.
func (UnavailablePreviews) RenderAndWait(context.Context, ids.WorkspaceID, ids.ProjectID, int) (ContactSheetRef, DurationMs, error) {
	return "", 0, ErrPreviewUnavailable
}

// PreviewWait is how long render_preview waits for a render. A render that takes longer is reported to the model as too slow;
// the render itself is not stopped.
const PreviewWait = 90 * time.Second

// previewWait is PreviewWait, a variable so that a test can shorten it.
var previewWait = PreviewWait
