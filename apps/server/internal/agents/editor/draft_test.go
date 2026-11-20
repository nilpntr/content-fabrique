package editor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
)

// spent sets what the run has spent so far, as the runtime's events would have.
func (r *dbRun) spent(t *testing.T, micros int64) {
	t.Helper()
	if _, err := r.pool.Exec(context.Background(), `UPDATE agent_runs SET cost_micros = $1 WHERE id = $2`, micros, r.run.ID); err != nil {
		t.Fatal(err)
	}
}

// withLeft is the next call with the dollars left known.
func (r *dbRun) withLeft(micros int64) runtime.RunContext {
	rc := r.next()
	rc.Left = runtime.BudgetLeft{Known: true, ToolCalls: 40, Wall: 10 * time.Minute, Micros: micros}
	return rc
}

// The live test of 2026-11-20 (section 4.2): two extra careful edits spent $2.00 and $0.80 looking at clips and made nothing. Once a run
// with no version has spent half its budget, the tools that only look are refused with a message to draft; plan_edit and edit_timeline
// stay open, and once a version exists the run may look again.
func TestARunWithNoVersionIsToldToDraftAtHalfItsBudget(t *testing.T) {
	r := newDBRun(t)
	moments := `{"asset_id":"` + r.clip.String() + `"}`
	// $0.60 of $2.00 spent: it may still look.
	r.spent(t, 600_000)
	r.ok(r.withLeft(1_400_000), ToolFindMoments, moments)
	// $1.20 of $2.00: refused, with the money in the message.
	r.spent(t, 1_200_000)
	msg := r.toolErr(r.withLeft(800_000), ToolFindMoments, moments)
	for _, want := range []string{"$1.20 of this edit's $2.00", "have not made a version yet", "edit_timeline"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal lacks %q: %s", want, msg)
		}
	}
	r.toolErr(r.withLeft(800_000), ToolViewClip, moments)
	// Drafting is open: the plan and the edit.
	r.ok(r.withLeft(800_000), ToolPlanEdit, planCall(validPlanArgs(r.clip.String())))
	r.ok(r.withLeft(700_000), ToolEditTimeline, r.opsArgs(r.prj, addClipOp(r.clip, 6000, 9000), addClipOp(r.clip, 9000, 12000)))
	// With a version made, looking is allowed again.
	r.ok(r.withLeft(600_000), ToolFindMoments, moments)
	// A budget that is not known never blocks.
	r2 := newDBRun(t)
	r2.spent(t, 4_900_000)
	r2.ok(r2.next(), ToolFindMoments, `{"asset_id":"`+r2.clip.String()+`"}`)
}

// A run that reaches its limit with no version (or with one it never looked at) still fails, but says why in plain words and what the
// project holds: never only "the budget was used up".
func TestALimitWithNoVersionSaysWhatHappened(t *testing.T) {
	r := newDBRun(t)
	ag, err := NewAgent(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := ag.OnLimit(context.Background(), r.rc, runtime.LimitDollars)
	if ok {
		t.Fatalf("a run with no version ended gracefully: %s", out)
	}
	var o struct {
		Summary string `json:"summary"`
		Made    bool   `json:"made_version"`
	}
	if err := json.Unmarshal(out, &o); err != nil || o.Made || !strings.Contains(o.Summary, "ran out of budget") || !strings.Contains(o.Summary, "before I made a version, so nothing changed") || !strings.Contains(o.Summary, "not charged") {
		t.Fatalf("the stop note: %v %s", err, out)
	}
	// A version made but never previewed: still a failure, and the note names it.
	r.ok(r.next(), ToolPlanEdit, planCall(validPlanArgs(r.clip.String())))
	r.ok(r.next(), ToolEditTimeline, r.opsArgs(r.prj, addClipOp(r.clip, 6000, 9000), addClipOp(r.clip, 9000, 12000)))
	out, ok = ag.OnLimit(context.Background(), r.rc, runtime.LimitWallClock)
	if ok || !strings.Contains(string(out), "right after I made version 1, before I could look at it") || !strings.Contains(string(out), `"unpreviewed_version":1`) {
		t.Fatalf("%v %s", ok, out)
	}
}
