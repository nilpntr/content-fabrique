package hooklab

import (
	"cmp"
	"slices"
	"strings"
	"unicode"

	"github.com/nilpntr/content-fabrique/internal/speechcut"
)

// Proposing variants from what is known about the footage, without a model: the spoken lines worth opening on, the best moments of
// the picture, and the clip the plan opens on. It answers "which first shot, which opening line"; the words of a text hook are the
// editor's to write, so a proposed variant carries the text it was given (Input.Text), or none.

// Limits of a proposal.
const (
	// DefaultShotMs is the length of a proposed first shot when the plan gives none.
	DefaultShotMs = 2000
	// maxLineCandidates and maxMomentCandidates bound how many lines and moments of one clip are considered.
	maxLineCandidates   = 40
	maxMomentCandidates = 10
)

// Moment is a stretch of a clip worth showing (a rep, a hold) with its score from the clip analysis (0 to 1).
type Moment struct {
	StartMs int64   `json:"start_ms" yaml:"start_ms"`
	EndMs   int64   `json:"end_ms" yaml:"end_ms"`
	Score   float64 `json:"score" yaml:"score"`
}

// Clip is one clip of the project as the proposer reads it.
type Clip struct {
	AssetID    string
	DurationMs int64
	// Speech is the clip's transcript, nil without speech; Moments its best moments; Dead its dead time.
	Speech  *Speech
	Moments []Moment
	Dead    []Span
}

// Input is what Propose works from.
type Input struct {
	// Clips are the project's video clips, in the creator's order.
	Clips []Clip
	// Planned is the clip the plan opens on ("" when it names none), and ShotMs the length of its first shot (0: DefaultShotMs).
	Planned string
	ShotMs  int64
	// Text is the text hook every proposed variant carries ("" for none), with its time on screen.
	Text           string
	TextAtMs       int64
	TextDurationMs int64
}

// Propose returns up to Variants distinct variants, each with an id ("a", "b", "c"): the best spoken line, the best moment of the
// picture and the plan's own clip first, then the next best of either, so that the three differ in kind where the footage allows it.
// Fewer come back when the footage does not hold three different openings. The result is deterministic.
func Propose(in Input) []Variant {
	shot := in.ShotMs
	if shot <= 0 {
		shot = DefaultShotMs
	}
	var lines, moments []candidate
	for _, c := range in.Clips {
		lines = append(lines, lineCandidates(c)...)
		moments = append(moments, momentCandidates(c, shot)...)
	}
	byMerit := func(a, b candidate) int { return cmp.Compare(b.merit, a.merit) }
	slices.SortStableFunc(lines, byMerit)
	slices.SortStableFunc(moments, byMerit)

	var picked []Variant
	add := func(c candidate) bool {
		v := c.v
		v.Text, v.TextAtMs, v.TextDurationMs = in.Text, in.TextAtMs, in.TextDurationMs
		if len(picked) == Variants || slices.ContainsFunc(picked, func(o Variant) bool { return Same(o, v) }) {
			return false
		}
		picked = append(picked, v)
		return true
	}
	first := func(list []candidate) {
		for _, c := range list {
			if add(c) {
				return
			}
		}
	}
	first(lines)
	first(moments)
	if i := slices.IndexFunc(in.Clips, func(c Clip) bool { return in.Planned != "" && c.AssetID == in.Planned }); i >= 0 {
		first(plannedCandidates(in.Clips[i], shot))
	}
	// Fill up, taking a line and a moment in turn.
	for i := 0; len(picked) < Variants && (i < len(lines) || i < len(moments)); i++ {
		if i < len(lines) {
			add(lines[i])
		}
		if i < len(moments) {
			add(moments[i])
		}
	}
	// A project with nothing measured still has the start of each clip.
	for _, c := range in.Clips {
		if len(picked) == Variants {
			break
		}
		first(startCandidates(c, shot))
	}
	for i := range picked {
		picked[i].ID = string(rune('a' + i))
	}
	return picked
}

type candidate struct {
	v     Variant
	merit float64
}

// lineCandidates are the sentences of a clip a video can open on: each starts in the gap before its first word that is not a
// filler and runs to the end of the sentence. A line's merit is how well it suits a hook: short enough to land, a question, an
// exclamation or a number in it, and no filler at its head.
func lineCandidates(c Clip) []candidate {
	sp := c.Speech
	if sp == nil || len(sp.Words) == 0 || !speechcut.Valid(sp.Words) {
		return nil
	}
	var cands []candidate
	for _, s := range sp.Structure.Sentences {
		if len(cands) == maxLineCandidates {
			break
		}
		if s.FirstWord < 0 || s.LastWord >= len(sp.Words) || s.FirstWord > s.LastWord {
			continue
		}
		start := speechcut.SentenceStart(sp.Words, sp.Structure, s)
		if speechcut.Low(sp.Words, start) || speechcut.Low(sp.Words, start-1) {
			continue // the time of the cut cannot be trusted
		}
		// The shot starts and ends in the gaps around the line, padded as the speech planner pads a cut.
		inMs := sp.Words[start].StartMs
		if start > 0 {
			inMs -= min(speechcut.DefaultPadMs, inMs-sp.Words[start-1].EndMs)
		} else {
			inMs -= min(speechcut.DefaultPadMs, inMs)
		}
		outMs := sp.Words[s.LastWord].EndMs
		if s.LastWord+1 < len(sp.Words) {
			outMs += min(speechcut.DefaultPadMs, sp.Words[s.LastWord+1].StartMs-outMs)
		} else {
			outMs += speechcut.DefaultPadMs
		}
		if c.DurationMs > 0 {
			outMs = min(outMs, c.DurationMs)
		}
		if outMs <= inMs {
			continue
		}
		words, dur := s.LastWord-start+1, sp.Words[s.LastWord].EndMs-sp.Words[start].StartMs
		merit := 1.0
		switch {
		case dur > MaxLineMs || words > MaxLineWords:
			merit -= 0.5
		case words < 3:
			merit -= 0.3 // "Okay." is not a hook
		}
		if _, weak := fillerAt(sp.Structure.Fillers, start); weak || start > s.FirstWord {
			merit -= 0.1 // the sentence is led by a filler that has to be cut off, or opens on a weak word
		}
		if bold(s.Text) {
			merit += 0.3
		}
		cands = append(cands, candidate{v: Variant{Kind: KindSpokenLine, AssetID: c.AssetID, InMs: inMs, OutMs: outMs}, merit: merit})
	}
	return cands
}

// bold says a sentence reads as a strong line: a question, an exclamation, or a number in it. It only looks at the characters.
func bold(text string) bool {
	t := strings.TrimRight(strings.TrimSpace(text), `"'”’)]»`)
	return strings.HasSuffix(t, "?") || strings.HasSuffix(t, "!") || strings.ContainsFunc(t, unicode.IsDigit)
}

// momentCandidates are the clip's best moments, each as a first shot of shotMs from where the moment starts. A moment that starts
// in dead time is left out.
func momentCandidates(c Clip, shotMs int64) []candidate {
	ms := slices.Clone(c.Moments)
	slices.SortStableFunc(ms, func(a, b Moment) int { return cmp.Compare(b.Score, a.Score) })
	var cands []candidate
	for _, m := range ms {
		if len(cands) == maxMomentCandidates {
			break
		}
		if m.StartMs < 0 || m.EndMs <= m.StartMs || inDead(c.Dead, m.StartMs) {
			continue
		}
		if v, ok := shotFrom(c, m.StartMs, shotMs, KindBestMoment); ok {
			cands = append(cands, candidate{v: v, merit: m.Score})
		}
	}
	return cands
}

// plannedCandidates are the openings on the clip the plan names: its best line, its best moment, then its first live frame.
func plannedCandidates(c Clip, shotMs int64) []candidate {
	var cands []candidate
	lines := lineCandidates(c)
	slices.SortStableFunc(lines, func(a, b candidate) int { return cmp.Compare(b.merit, a.merit) })
	cands = append(cands, lines...)
	cands = append(cands, momentCandidates(c, shotMs)...)
	cands = append(cands, startCandidates(c, shotMs)...)
	for i := range cands {
		cands[i].v.Kind = KindPlanned
	}
	return cands
}

// startCandidates is the clip from its first frame that is not dead time.
func startCandidates(c Clip, shotMs int64) []candidate {
	var at int64
	for moved := true; moved; { // step over the dead time the clip starts with
		moved = false
		for _, d := range c.Dead {
			if at >= d.StartMs && at < d.EndMs {
				at, moved = d.EndMs, true
			}
		}
	}
	if v, ok := shotFrom(c, at, shotMs, KindClipStart); ok {
		return []candidate{{v: v}}
	}
	return nil
}

func shotFrom(c Clip, at, shotMs int64, kind string) (Variant, bool) {
	end := at + shotMs
	if c.DurationMs > 0 {
		end = min(end, c.DurationMs)
	}
	if end <= at {
		return Variant{}, false
	}
	return Variant{Kind: kind, AssetID: c.AssetID, InMs: at, OutMs: end}, true
}

func inDead(dead []Span, at int64) bool {
	return slices.ContainsFunc(dead, func(d Span) bool { return at >= d.StartMs && at < d.EndMs })
}
