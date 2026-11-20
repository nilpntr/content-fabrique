package editor

import (
	"context"
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/clipfeedback"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Clip feedback (docs/design/proof-edit-v1.md): what the creator said about the shots of earlier proofs ("never use shot 5 again",
// "shot 6 a few seconds later"), stored by clip and source range in package clipfeedback. The editor reads it with get_clip_feedback,
// the run context says when there is some, and check_edit's clip_feedback check fails an edit that plays a range the creator ruled out.
// The creator's notes are untrusted data and reach the model in data blocks only; the check's lines give times, never the notes.

// ToolGetClipFeedback is the tool that lists the clip feedback on the project's clips.
const ToolGetClipFeedback = "get_clip_feedback"

// CheckClipFeedback fails an edit with a shot that plays a range of a clip the creator ruled out on an earlier proof.
const CheckClipFeedback = "clip_feedback"

// footageAssets is the ids of the clips of the footage rows.
func footageAssets(rows []store.ProjectFootageRow) []ids.AssetID {
	out := make([]ids.AssetID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.AssetID)
	}
	return out
}

// clipRules reads the rules on the project's clips, by clip (none without a store that keeps them).
func (d Deps) clipRules(ctx context.Context, ws ids.WorkspaceID, rows []store.ProjectFootageRow) (map[string][]clipfeedback.Rule, error) {
	return clipfeedback.LoadRules(ctx, d.Store, ws, footageAssets(rows))
}

// countRules is how many clips have rules and how many rules there are.
func countRules(rules map[string][]clipfeedback.Rule) (clips, total int) {
	for _, rs := range rules {
		if len(rs) > 0 {
			clips++
			total += len(rs)
		}
	}
	return clips, total
}

// clipFeedbackIntro is the run context's line about the clip feedback: none when there is none, so a workspace without feedback has the
// same context as before.
func clipFeedbackIntro(rules map[string][]clipfeedback.Rule) []llm.Block {
	clips, total := countRules(rules)
	if total == 0 {
		return nil
	}
	return []llm.Block{llm.TextBlock(fmt.Sprintf(
		"Clip feedback: on earlier proofs the creator ruled out parts of %d of these clips (%d %s, stored by clip and second, so they hold for this edit too). Call get_clip_feedback before you pick the moments, and keep every shot off those ranges: check_edit's clip_feedback check fails an edit that plays one.",
		clips, total, plural(total, "rule", "rules")))}
}

// getClipFeedback is the get_clip_feedback tool.
func (d Deps) getClipFeedback(ctx context.Context, rc runtime.RunContext, _ noArgs) (runtime.Result, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.Result{}, err
	}
	rows, err := d.Store.ListProjectFootage(ctx, ws, pid)
	if err != nil {
		return runtime.Result{}, fmt.Errorf("list footage: %w", err)
	}
	rules, err := d.clipRules(ctx, ws, rows)
	if err != nil {
		return runtime.Result{}, err
	}
	return runtime.Result{Content: clipFeedbackBlocks(rows, rules)}, nil
}

// clipFeedbackBlocks is the answer of get_clip_feedback: the rules of each clip in the footage's order, then the creator's notes in one
// data block.
func clipFeedbackBlocks(rows []store.ProjectFootageRow, rules map[string][]clipfeedback.Rule) []llm.Block {
	_, total := countRules(rules)
	if total == 0 {
		return []llm.Block{llm.TextBlock("No clip feedback: the creator has not ruled out any part of this project's clips. Use them as you see fit.")}
	}
	var b, notes strings.Builder
	fmt.Fprintf(&b, "Clip feedback on this project's clips (%d %s). Keep every shot off these ranges of the clip's own time (in_ms to out_ms); %d ms of overlap at an edge is tolerated:\n",
		total, plural(total, "rule", "rules"), clipfeedback.ToleranceMs)
	n := 0
	for _, r := range rows {
		rs := rules[r.AssetID.String()]
		if len(rs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- clip %s:\n", r.AssetID)
		for _, rule := range rs {
			n++
			fmt.Fprintf(&b, "  %d. %s (%d to %d ms); given on shot %d of version %d", n, rule.Plain(), rule.StartMs, rule.EndMs, rule.Shot, rule.Version)
			if rule.Note != "" {
				fmt.Fprintf(&b, "; the creator's note is note %d below", n)
				fmt.Fprintf(&notes, "note %d: %s\n", n, rule.Note)
			}
			b.WriteString("\n")
		}
	}
	blocks := []llm.Block{llm.TextBlock(strings.TrimRight(b.String(), "\n"))}
	if notes.Len() > 0 {
		blocks = append(blocks, llm.TextBlock("The creator's notes (their own words):"), runtime.UntrustedBlock("clip_feedback_notes", strings.TrimRight(notes.String(), "\n")))
	}
	return blocks
}

// hasClipFeedback says a clip of the edit has feedback.
func (c *checker) hasClipFeedback() bool {
	for _, it := range c.items {
		if len(c.in.ClipFeedback[it.AssetID]) > 0 {
			return true
		}
	}
	return false
}

// clipFeedbackCheck is the clip_feedback check: no shot plays more than clipfeedback.ToleranceMs of a range the creator ruled out. It is
// only in a report when the edit's clips have feedback.
func (c *checker) clipFeedbackCheck() Check {
	shots := timeline.Shots(c.in.Spec)
	vs := clipfeedback.Violations(shots, c.in.ClipFeedback)
	clips, total := 0, 0
	seen := map[string]bool{}
	for _, s := range shots {
		if n := len(c.in.ClipFeedback[s.AssetID]); n > 0 && !seen[s.AssetID] {
			seen[s.AssetID] = true
			clips++
			total += n
		}
	}
	if len(vs) == 0 {
		return c.pass(CheckClipFeedback, "no shot plays a range the creator ruled out (%d %s on %d of the edit's clips)", total, plural(total, "rule", "rules"), clips)
	}
	lines := make([]string, 0, min(len(vs), 3))
	for _, v := range vs[:min(len(vs), 3)] {
		lines = append(lines, v.Detail())
	}
	more := ""
	if len(vs) > 3 {
		more = fmt.Sprintf(" (and %d more)", len(vs)-3)
	}
	return c.fail(CheckClipFeedback, "%s%s. get_clip_feedback lists every rule", strings.Join(lines, "; "), more)
}
