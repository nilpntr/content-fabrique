// Package clipfeedback is the creator's feedback on the shots of a proof edit (docs/design/proof-edit-v1.md, rule 15 of
// docs/design/gino-edit-rules.md): "never use shot 5 again", "shot 6 a few seconds later". A proof render numbers the shots of one
// version (render.Proof, timeline.Shots); the feedback on a number is stored against the shot's clip and the range of the clip it plays,
// because the numbers change with every version, and every later edit of the workspace honours it: the editor reads it with
// get_clip_feedback and check_edit's clip_feedback check fails an edit that plays a range the creator ruled out.
//
// This file is the pure part (what a rule is, the range a piece of feedback is stored against, which shots break which rule); the
// service and the API handlers are next to it.
package clipfeedback

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The kinds of feedback.
const (
	// NeverUse: the range (the shot's, or the whole clip) is never used again.
	NeverUse = store.ClipNeverUse
	// StartLater: the take should start later; the range is the part of the clip from the shot's start to the new start.
	StartLater = store.ClipStartLater
)

// The limits of "start later".
const (
	MinOffsetMs = 500
	MaxOffsetMs = 30_000
)

// ToleranceMs is how much of a ruled-out range a shot may still touch: the frame snapping of the cuts (a frame is 33 ms at 30 fps) and
// the snap of a cut to the beat move an edge by up to about 100 ms, which is not the creator's shot coming back.
const ToleranceMs = 100

// Rule is one piece of feedback as the editor and the checks apply it.
type Rule struct {
	AssetID string
	Kind    string
	// StartMs and EndMs are the range of the clip's own time that no shot may play (end exclusive).
	StartMs, EndMs int64
	// Note is the creator's own words (untrusted), empty for none.
	Note string
	// Version and Shot say where it was given (the shot's number on that version's proof); for the history only.
	Version, Shot int
}

// RuleOf is the rule of a stored row.
func RuleOf(r store.ClipFeedback) Rule {
	return Rule{AssetID: r.AssetID.String(), Kind: r.Kind, StartMs: r.StartMs, EndMs: r.EndMs, Note: r.Note, Version: int(r.TimelineVersion), Shot: int(r.ShotNumber)}
}

// ByAsset groups the rules of stored rows by clip, each clip's in the order of their range.
func ByAsset(rows []store.ClipFeedback) map[string][]Rule {
	out := map[string][]Rule{}
	for _, r := range rows {
		rule := RuleOf(r)
		out[rule.AssetID] = append(out[rule.AssetID], rule)
	}
	for _, rs := range out {
		slices.SortFunc(rs, func(a, b Rule) int { return cmp.Or(cmp.Compare(a.StartMs, b.StartMs), cmp.Compare(a.EndMs, b.EndMs)) })
	}
	return out
}

// ErrInvalid is wrapped by every refusal of Range.
var ErrInvalid = errors.New("clipfeedback: the feedback is not valid")

// Range is the range of the clip that feedback on shot is stored against:
//
//   - never_use: the range the shot plays, or with wholeClip the whole clip (clipMs long; 0 when unknown, which wholeClip refuses);
//   - start_later: from the shot's start to offsetMs later (MinOffsetMs to MaxOffsetMs), cut at the end of the clip when its length is
//     known; the rest of the take is fine.
func Range(shot timeline.Shot, kind string, offsetMs int64, wholeClip bool, clipMs int64) (start, end int64, err error) {
	switch kind {
	case NeverUse:
		if offsetMs != 0 {
			return 0, 0, fmt.Errorf("%w: offset_ms is for start_later only", ErrInvalid)
		}
		if wholeClip {
			if clipMs <= 0 {
				return 0, 0, fmt.Errorf("%w: the clip's length is not known", ErrInvalid)
			}
			return 0, clipMs, nil
		}
		return shot.Source.InMs, shot.Source.OutMs, nil
	case StartLater:
		if wholeClip {
			return 0, 0, fmt.Errorf("%w: whole_clip is for never_use only", ErrInvalid)
		}
		if offsetMs < MinOffsetMs || offsetMs > MaxOffsetMs {
			return 0, 0, fmt.Errorf("%w: offset_ms must be %d to %d", ErrInvalid, MinOffsetMs, MaxOffsetMs)
		}
		start, end = shot.Source.InMs, shot.Source.InMs+offsetMs
		if clipMs > 0 {
			end = min(end, clipMs)
		}
		if end <= start {
			return 0, 0, fmt.Errorf("%w: the shot starts at the end of its clip", ErrInvalid)
		}
		return start, end, nil
	}
	return 0, 0, fmt.Errorf("%w: kind must be %s or %s", ErrInvalid, NeverUse, StartLater)
}

// Overlap is how many milliseconds the ranges [a0, a1) and [b0, b1) share.
func Overlap(a0, a1, b0, b1 int64) int64 { return max(0, min(a1, b1)-max(a0, b0)) }

// Touches says the shot plays the rule's range: more than ToleranceMs of it, the same bar as Violations, so the shot list shows on a
// shot exactly the feedback that holds for it (a shot that only grazes a range can still get its own).
func (r Rule) Touches(s timeline.Shot) bool {
	return s.AssetID == r.AssetID && Overlap(s.Source.InMs, s.Source.OutMs, r.StartMs, r.EndMs) > ToleranceMs
}

// RuledOut says playing [startMs, endMs) of the clip would break one of its rules (more than ToleranceMs of a ruled-out range): what
// find_moments leaves out.
func RuledOut(rules map[string][]Rule, assetID string, startMs, endMs int64) bool {
	s := timeline.Shot{AssetID: assetID, Source: timeline.Source{InMs: startMs, OutMs: endMs}}
	for _, r := range rules[assetID] {
		if r.Touches(s) {
			return true
		}
	}
	return false
}

// Violation is a shot that plays a range the creator ruled out.
type Violation struct {
	Shot      timeline.Shot
	Rule      Rule
	OverlapMs int64
}

// Violations lists the shots that play more than ToleranceMs of a range ruled out for their clip, in shot order.
func Violations(shots []timeline.Shot, rules map[string][]Rule) []Violation {
	var out []Violation
	for _, s := range shots {
		for _, r := range rules[s.AssetID] {
			if r.Touches(s) {
				out = append(out, Violation{Shot: s, Rule: r, OverlapMs: Overlap(s.Source.InMs, s.Source.OutMs, r.StartMs, r.EndMs)})
			}
		}
	}
	return out
}

// Detail is the one line check_edit gives for a violation: the numbers and what to change. It never quotes the creator's note (the
// editor reads the notes in get_clip_feedback, in data blocks).
func (v Violation) Detail() string {
	s, r := v.Shot, v.Rule
	plays := fmt.Sprintf("shot %d (%s) plays %s to %s of clip %s", s.Number, s.ItemID, Secs(s.Source.InMs), Secs(s.Source.OutMs), s.AssetID)
	if r.Kind == StartLater {
		return fmt.Sprintf("%s, but the creator asked for that take to start at %s or later: move in_ms to %d or more, or use another take",
			plays, Secs(r.EndMs), r.EndMs)
	}
	return fmt.Sprintf("%s, which the creator said never to use again (%s to %s): use another moment or another clip", plays, Secs(r.StartMs), Secs(r.EndMs))
}

// Plain says what a rule asks, for the editor (get_clip_feedback) and cfctl.
func (r Rule) Plain() string {
	if r.Kind == StartLater {
		return fmt.Sprintf("start that take at %s or later: do not play %s to %s", Secs(r.EndMs), Secs(r.StartMs), Secs(r.EndMs))
	}
	return fmt.Sprintf("never use %s to %s", Secs(r.StartMs), Secs(r.EndMs))
}

// Secs formats milliseconds as seconds with one decimal ("12.5 s").
func Secs(ms int64) string { return fmt.Sprintf("%.1f s", float64(ms)/1000) }
