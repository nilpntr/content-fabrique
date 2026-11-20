package editor

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
)

// The live test of 2026-11-20 (section 4.4): runs finished with beat_grid_fixed and shot_reused failing, because every check was advice.
// With the creator's settings asking for them, a failing rule keeps the run fixing: check_edit asks for another pass, finish (and a turn
// that ends in text) is refused while there is room; after maxRuleChecks checks the run may finish, and its output names what is not kept.
func TestTheCreatorsRulesKeepTheRunFixingThenAreNamed(t *testing.T) {
	g := newGateEnv(t)
	if _, err := g.st.SaveCreatorStyleSettings(context.Background(), g.wsA.ID, json.RawMessage(`{"shot_reuse":"once"}`), ids.UserID{}); err != nil {
		t.Fatal(err)
	}
	// The same moment twice (6 to 9 s of the clip): shot_reused fails.
	g.ok(g.nextCall(g.rc), ToolEditTimeline, g.opsArgs(g.prj, addClipOp(g.clip, 6000, 9000), addClipOp(g.clip, 6000, 9000)))
	g.ok(g.nextCall(g.rc), ToolRenderPreview, `{}`)
	text := textOf(g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`))
	if !strings.Contains(text, "FAIL shot_reused") || !strings.Contains(text, "the creator's own rule") {
		t.Fatalf("the first check:\n%s", text)
	}
	res, _ := g.call(g.nextCall(g.rc), ToolFinish, `{"summary":"Done."}`)
	if !res.IsError || res.Final || !strings.Contains(textOf(res), "shot_reused fails on version 1") {
		t.Fatalf("finish with a broken rule: %+v %s", res, textOf(res))
	}
	// A turn that ends in text is asked once more too.
	ag, err := NewAgent(g.deps)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := ag.EndTurn(context.Background(), g.rc, "Done.", 0); err != nil || !strings.Contains(d.Nudge, "shot_reused") {
		t.Fatalf("end of turn with a broken rule: %+v %v", d, err)
	}
	// The second check (the revision pass) still asks; the third is the last, and finish goes through with the rule named.
	if text := textOf(g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`)); !strings.Contains(text, "the creator's own rule") {
		t.Fatalf("the second check:\n%s", text)
	}
	if res, _ := g.call(g.nextCall(g.rc), ToolFinish, `{"summary":"Done."}`); !res.IsError {
		t.Fatal("finish went through after the second check with the rule still broken")
	}
	if text := textOf(g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`)); !strings.Contains(text, "no room left") {
		t.Fatalf("the third check:\n%s", text)
	}
	res = g.ok(g.nextCall(g.rc), ToolFinish, `{"summary":"Done."}`)
	if !res.Final {
		t.Fatalf("finish after the last check: %+v", res)
	}
	var out struct {
		Summary    string   `json:"summary"`
		Unresolved []string `json:"unresolved_checks"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil || !slices.Contains(out.Unresolved, CheckShotReused) || !strings.Contains(out.Summary, "Not kept yet from your settings: every shot used once.") {
		t.Fatalf("the output does not name the broken rule: %v %s", err, res.Output)
	}
}

func TestUnresolvedNoteKeepsTheMessageInBounds(t *testing.T) {
	long := strings.Repeat("a", maxSummaryChars)
	got := withUnresolved(long, []string{CheckBeatGridFixed, "hook_title"})
	if n := len([]rune(got)); n > maxSummaryChars {
		t.Errorf("%d runes, over %d", n, maxSummaryChars)
	}
	if !strings.HasSuffix(got, "Not kept yet from your settings: every cut on the 2-beat grid with a 4-beat ending.") {
		t.Errorf("the note: %q", got[len(got)-120:])
	}
	// Advice the model explains in its own words is not repeated to the creator.
	if withUnresolved("Done.", []string{"hook_title"}) != "Done." || withUnresolved("Done.", nil) != "Done." {
		t.Error("a note for checks that are not the creator's rules")
	}
}
