package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

// The preview gate. A run that edited the timeline must have looked at the result before it ends: when previews are wired, finish
// is refused until render_preview has succeeded for the newest version the run made, and a turn that ends in plain text counts as
// finishing only if that is true (otherwise the model is asked once to preview and call finish).
//
// The record of which versions a run previewed lives in the process: a resumed run in another worker starts with none, and is
// asked to preview again, which the renderer allows (it is idempotent in (project, version)).

// maxPreviewFailures is how many times render_preview may fail for a run before the gate opens: a renderer that is down must not
// trap the model in a loop it cannot leave.
const maxPreviewFailures = 2

type previewState struct {
	version  int // the newest version previewed, 0 for none
	failures int
}

type previewLog struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*previewState
}

func newPreviewLog() *previewLog { return &previewLog{runs: map[uuid.UUID]*previewState{}} }

// maxTrackedRuns bounds the log: it forgets everything when it grows past this many runs (a few bytes each).
const maxTrackedRuns = 4096

func (l *previewLog) state(run uuid.UUID) *previewState {
	s := l.runs[run]
	if s == nil {
		if len(l.runs) >= maxTrackedRuns {
			clear(l.runs)
		}
		s = &previewState{}
		l.runs[run] = s
	}
	return s
}

func (l *previewLog) previewed(run uuid.UUID, version int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.state(run); version > s.version {
		s.version = version
	}
}

func (l *previewLog) failed(run uuid.UUID, times int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.state(run).failures += times
}

// ok says the run has previewed version (or a later one), or has been let off because previews keep failing.
func (l *previewLog) ok(run uuid.UUID, version int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state(run)
	return s.version >= version || s.failures >= maxPreviewFailures
}

// withState returns the Deps with the gate's record set. It is idempotent, and the agent and its tools must share one record, so
// NewAgent calls it before building the tools.
func (d Deps) withState() Deps {
	if d.previews == nil {
		d.previews = newPreviewLog()
	}
	return d
}

// previewsOn says a real renderer is wired, so the gate applies.
func (d Deps) previewsOn() bool {
	if d.Preview == nil {
		return false
	}
	_, none := d.Preview.(UnavailablePreviews)
	return !none
}

// previewGate returns the refusal for finish when the run's newest version has not been previewed, or "" when it may finish.
func (d Deps) previewGate(run uuid.UUID, version int) string {
	if !d.previewsOn() || d.previews == nil || d.previews.ok(run, version) {
		return ""
	}
	return fmt.Sprintf("you cannot finish yet: you have not looked at version %d. Call render_preview and look at the contact sheet (black frames, a cropped-off subject, text over a face or outside the safe area, a weak hook). If something is wrong, fix it with edit_timeline and preview again. Then call finish.", version)
}

const (
	nudgeNoPreview = "You ended your turn without calling finish, and you have not looked at your latest version, version %d, yet. Call render_preview now and look at the contact sheet. Fix what is wrong, then call finish with your message for the creator."
	nudgeNoEdit    = "You ended your turn without calling finish. If you edited the timeline, call finish with your message for the creator (after render_preview, which shows the result). If the timeline already does what the creator asked and nothing needs to change, call finish with no_edit_needed set to true."
)

// endTurn decides what a turn that ends in text, with no tool call, means. With previews wired it is an implicit finish if the
// run's newest version was previewed; otherwise the model is asked once to preview and finish, and the second time the text ends
// the run as it always did.
func (d Deps) endTurn(ctx context.Context, rc runtime.RunContext, text string, nudges int) (runtime.EndTurn, error) {
	if !d.previewsOn() || d.previews == nil {
		return runtime.EndTurn{}, nil
	}
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.EndTurn{}, nil //nolint:nilerr // a run without a project ends as before
	}
	made, err := d.Store.GetTimelineVersionByAuthor(ctx, ws, pid, string(ops.AuthorAgent), ids.Encode(ids.PrefixRun, rc.RunID))
	switch {
	case err == nil:
		v := int(made.Version)
		if d.previews.ok(rc.RunID, v) {
			return runtime.EndTurn{Output: implicitFinish(text, v)}, nil
		}
		if nudges == 0 {
			return runtime.EndTurn{Nudge: fmt.Sprintf(nudgeNoPreview, v)}, nil
		}
	case errors.Is(err, store.ErrNotFound):
		if nudges == 0 {
			return runtime.EndTurn{Nudge: nudgeNoEdit}, nil
		}
	default:
		return runtime.EndTurn{}, fmt.Errorf("look for the run's versions: %w", err)
	}
	return runtime.EndTurn{}, nil
}

// implicitFinish is the run's output for a turn that ended in text after a previewed edit: the same shape finish writes.
func implicitFinish(text string, version int) json.RawMessage {
	summary := cutRunes(strings.TrimSpace(strings.ReplaceAll(text, "\x00", "")), maxSummaryChars)
	if summary == "" {
		summary = "I finished the edit; open the new version to see it."
	}
	out, _ := json.Marshal(map[string]any{"summary": summary, "timeline_version": version, "implicit_finish": true})
	return out
}
