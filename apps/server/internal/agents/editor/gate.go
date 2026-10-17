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

// The gates. A run that edited the timeline must have looked at the result before it ends: when previews are wired, finish is
// refused until render_preview has succeeded for the newest version the run made, and a turn that ends in plain text counts as
// finishing only if that is true (otherwise the model is asked once to preview and call finish). Since editor v2 finish also needs a
// check_edit of that version (checkGate), and the first edit of a run needs a plan (planGate).
//
// The record of which versions a run previewed lives in the process: a resumed run in another worker starts with none, and is
// asked to preview again, which the renderer allows (it is idempotent in (project, version)).

// maxPreviewFailures is how many times in a row render_preview may fail for a version before finish no longer needs a preview of
// it: a renderer that is down must not trap the model in a loop it cannot leave. It applies to that version only; the next version
// the run makes needs a preview again, and a success clears the count.
const maxPreviewFailures = 2

// maxTrackedRuns bounds the record: past it, the run that was used least recently is forgotten. (Runs are also forgotten when they
// end, see Agent.OnEnd; the bound is for a worker that never hears of a run ending, such as one that lost it.)
const maxTrackedRuns = 1024

type previewState struct {
	version  int    // the newest version previewed, 0 for none
	failures int    // previews that failed in a row since the last success
	failedAt int    // the version the latest failure was for
	used     uint64 // when the run was last used, for the bound

	plan       *Plan // the run's latest creative plan
	checked    int   // the newest version check_edit ran on, 0 for none
	checkCalls int   // how many times check_edit ran in this run
}

type previewLog struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*previewState
	tick uint64
}

func newPreviewLog() *previewLog { return &previewLog{runs: map[uuid.UUID]*previewState{}} }

// state returns the run's record, making it (and making room for it) if there is none. The caller holds mu.
func (l *previewLog) state(run uuid.UUID) *previewState {
	l.tick++
	s := l.runs[run]
	if s == nil {
		if len(l.runs) >= maxTrackedRuns {
			var oldest uuid.UUID
			least := ^uint64(0)
			for id, st := range l.runs {
				if st.used < least {
					oldest, least = id, st.used
				}
			}
			delete(l.runs, oldest)
		}
		s = &previewState{}
		l.runs[run] = s
	}
	s.used = l.tick
	return s
}

// previewed records a successful preview of version, which also ends a streak of failures.
func (l *previewLog) previewed(run uuid.UUID, version int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state(run)
	s.version = max(s.version, version)
	s.failures, s.failedAt = 0, 0
}

// failed records n failed previews of version.
func (l *previewLog) failed(run uuid.UUID, version, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state(run)
	s.failures += n
	s.failedAt = version
}

// ok says the run has previewed version (or a later one), or has been let off for it because previews of it kept failing.
func (l *previewLog) ok(run uuid.UUID, version int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.runs[run]
	if s == nil {
		return false
	}
	l.tick++
	s.used = l.tick
	return s.version >= version || (s.failures >= maxPreviewFailures && s.failedAt >= version)
}

// setPlan keeps the run's latest plan.
func (l *previewLog) setPlan(run uuid.UUID, p Plan) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.state(run).plan = &p
}

// plan returns the run's latest plan, if this worker has seen one.
func (l *previewLog) plan(run uuid.UUID) (Plan, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.runs[run]
	if s == nil || s.plan == nil {
		return Plan{}, false
	}
	return *s.plan, true
}

// checkRun records a check_edit of version, the n-th of the run.
func (l *previewLog) checkRun(run uuid.UUID, version, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state(run)
	s.checked = max(s.checked, version)
	s.checkCalls = max(s.checkCalls, n)
}

// checkState returns the newest version check_edit ran on and how many times it ran, in this worker's record.
func (l *previewLog) checkState(run uuid.UUID) (version, calls int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.runs[run]; s != nil {
		return s.checked, s.checkCalls
	}
	return 0, 0
}

// forget drops the run's record: the run has ended.
func (l *previewLog) forget(run uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.runs, run)
}

func (l *previewLog) tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.runs)
}

// withState returns the Deps with the record of previews set (kept if there is one). NewAgent is its one production caller: the
// agent and its tools share the record of that one agent instance, and Deps.check refuses a Deps without it.
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
	if !d.previewsOn() || d.previews.ok(run, version) {
		return ""
	}
	return fmt.Sprintf("you cannot finish yet: you have not looked at version %d. Call render_preview and look at the contact sheet (black frames, a cropped-off subject, text over a face or outside the safe area, a weak hook), then call check_edit. If something is wrong, fix it with edit_timeline and preview again. Then call finish.", version)
}

const (
	nudgeNoPreview = "You ended your turn without calling finish, and you have not looked at your latest version, version %d, yet. Call render_preview now and look at the contact sheet. Fix what is wrong, then call finish with your message for the creator."
	nudgeNoFinish  = "You ended your turn without calling finish. Call finish now with your message for the creator."
	nudgeNoEdit    = "You ended your turn without calling finish. If you edited the timeline, call finish with your message for the creator (after render_preview, which shows the result). If the timeline already does what the creator asked and nothing needs to change, call finish with no_edit_needed set to true."
)

// endTurn decides what a turn that ends in text, with no tool call, means. The prompt says a run ends with finish, so the model is
// asked once to call it (and, with previews wired, to look at its latest version first). A turn that follows the nudge, or one
// that already previewed the newest version, is accepted as the finish, with the output finish would have written.
func (d Deps) endTurn(ctx context.Context, rc runtime.RunContext, text string, nudges int) (runtime.EndTurn, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.EndTurn{}, nil //nolint:nilerr // a run without a project ends as before
	}
	made, err := d.Store.GetTimelineVersionByAuthor(ctx, ws, pid, string(ops.AuthorAgent), ids.Encode(ids.PrefixRun, rc.RunID))
	switch {
	case errors.Is(err, store.ErrNotFound):
		if nudges == 0 {
			return runtime.EndTurn{Nudge: nudgeNoEdit}, nil
		}
		return runtime.EndTurn{}, nil
	case err != nil:
		return runtime.EndTurn{}, fmt.Errorf("look for the run's versions: %w", err)
	}
	v := int(made.Version)
	previewed := !d.previewsOn() || d.previews.ok(rc.RunID, v)
	switch {
	case nudges > 0:
		// Asked, and it did not: end the run rather than fail it.
	case !previewed:
		return runtime.EndTurn{Nudge: fmt.Sprintf(nudgeNoPreview, v)}, nil
	case !d.noCheckGate && !d.checked(ctx, rc, v):
		return runtime.EndTurn{Nudge: fmt.Sprintf(nudgeNoChk, v)}, nil
	case !d.previewsOn():
		return runtime.EndTurn{Nudge: nudgeNoFinish}, nil
	}
	// Looked at and checked: the text is the message.
	return runtime.EndTurn{Output: implicitFinish(text, v)}, nil
}

// implicitFinish is the run's output for a turn that ended in text after an edit: the same shape finish writes.
func implicitFinish(text string, version int) json.RawMessage {
	summary := cutRunes(strings.TrimSpace(strings.ReplaceAll(text, "\x00", "")), maxSummaryChars)
	if summary == "" {
		summary = "I finished the edit; open the new version to see it."
	}
	out, _ := json.Marshal(map[string]any{"summary": summary, "timeline_version": version, "implicit_finish": true})
	return out
}
