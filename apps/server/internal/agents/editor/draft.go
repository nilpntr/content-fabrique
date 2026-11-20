package editor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

// Draft first. The live test of 2026-11-20 (section 4.2) ran two extra careful (effort max) edits that spent their whole budget, $2.00 and
// $0.80, and made nothing: Opus thought 13,000 to 18,000 tokens a call (about $0.40 each), kept viewing clips and finding moments, and the
// last call was cut off where the budget ran out, before the first edit_timeline. Nothing was wrong with the budget arithmetic or the loop;
// the run simply never drafted. So once a run that has made no version has spent half of what it had after its first call, the tools that
// study the footage (view_clip, find_moments, get_transcript) are refused with a message to draft now. The first call is left out of the
// count because it writes the whole prompt to the cache: on a revision on Sonnet it is about half the budget by itself. The cheap reads
// (the brief, the timeline, the comments, the clip feedback, the formats, the music) stay open, because a revision needs them to edit at
// all. A version made late is still a result: a run that reaches its limit keeps it (onLimit), and one that made none says plainly that
// nothing changed (noVersionStop).

// draftGate returns the refusal for a tool that studies the footage when the run has made no version and has spent half of what it had
// after its first call, or "" when it may go on.
func (d Deps) draftGate(ctx context.Context, rc runtime.RunContext) string {
	if !rc.Left.Known || rc.RunID == uuid.Nil {
		return ""
	}
	pid, ws, err := project(rc)
	if err != nil {
		return ""
	}
	run, err := d.Store.GetAgentRun(ctx, ws, rc.RunID)
	if err != nil {
		slog.WarnContext(ctx, "editor: the run row could not be read; the draft gate is off", "run_id", rc.RunID.String(), "error", err)
		return ""
	}
	spent, left := run.CostMicros, max(rc.Left.Micros, 0)
	if spent*2 < spent+left {
		return "" // under half of the whole budget: the first call cannot matter yet
	}
	var first int64
	if rows, err := d.Store.UsageForRun(ctx, ws, rc.RunID); err == nil {
		for _, r := range rows {
			if r.Kind == store.KindLLM {
				first = r.ListCostMicros
				break
			}
		}
	}
	if after := spent - first; after*2 < after+left {
		return ""
	}
	if _, err := d.Store.GetTimelineVersionByAuthor(ctx, ws, pid, string(ops.AuthorAgent), ids.Encode(ids.PrefixRun, rc.RunID)); !errors.Is(err, store.ErrNotFound) {
		return "" // a version made (or a read that failed: never block the run on it)
	}
	return fmt.Sprintf("refused: you have spent %s of this edit's %s and have not made a version yet, so there is no room left to keep studying the footage. Draft now from what you already know: call plan_edit if you have not, then edit_timeline with the whole first cut, then render_preview. Refine it afterwards if money is left; a draft is worth more than more reading.",
		spend.USD(spent), spend.USD(spent+left))
}

// draftGated wraps the run of a tool that studies the footage with the draft gate.
func draftGated[In any](d Deps, run func(context.Context, runtime.RunContext, In) (runtime.Result, error)) func(context.Context, runtime.RunContext, In) (runtime.Result, error) {
	return func(ctx context.Context, rc runtime.RunContext, in In) (runtime.Result, error) {
		if msg := d.draftGate(ctx, rc); msg != "" {
			return runtime.Errorf("%s", msg), nil
		}
		return run(ctx, rc, in)
	}
}
