package speechcut

import (
	"fmt"

	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

// Finding kinds.
const (
	FindingInsideWord = "inside_word"
	FindingFiller     = "filler"
	FindingPause      = "pause"
)

// Finding is one thing wrong with a range of a clip that is played.
type Finding struct {
	Kind string
	// AtMs is where in the clip (its own milliseconds): the cut, the filler's start or the pause's start.
	AtMs int64
	// Detail is a short description with the numbers. It never quotes the transcript: it reaches the model.
	Detail string
}

// Range is a stretch of a clip being played: the clip's own milliseconds and the speed it plays at.
type Range struct {
	InMs, OutMs int64
	Speed       float64
	// LastOfEdit says the range closes the edit: the silence after its last word is the beat the ending may hold.
	LastOfEdit bool
}

// Audit judges a played range of a clip against the rules the planner follows: no cut inside a word, no filler kept, no pause over
// MaxPauseMs kept. A cut or a pause next to a word with a low confidence is not reported (its time cannot be trusted either way).
// It returns the findings and how many of the checked places were uncertain.
func Audit(words []transcribe.Word, st transcribe.Structure, r Range) (findings []Finding, uncertain int) {
	if len(words) == 0 || !Valid(words) || r.OutMs <= r.InMs {
		return nil, 0
	}
	speed := r.Speed
	if speed <= 0 {
		speed = 1
	}
	// Words that are in the range at all.
	first, last := -1, -1
	for i, w := range words {
		if w.EndMs > r.InMs && w.StartMs < r.OutMs {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil, 0 // no speech in the range: a cutaway, nothing to cut
	}

	// A cut (the start or the end of the range) inside a word.
	for _, edge := range []struct {
		at   int64
		name string
	}{{r.InMs, "starts"}, {r.OutMs, "ends"}} {
		for i := first; i <= last; i++ {
			w := words[i]
			if edge.at > w.StartMs+CutToleranceMs && edge.at < w.EndMs-CutToleranceMs {
				if Low(words, i) {
					uncertain++
					continue
				}
				findings = append(findings, Finding{Kind: FindingInsideWord, AtMs: edge.at, Detail: fmt.Sprintf("the shot %s at %d ms, inside a word (%d to %d ms)", edge.name, edge.at, w.StartMs, w.EndMs)})
			}
		}
	}

	inRange := func(i int) bool { // the word is played (more than the tolerance of it is inside the range)
		return i >= 0 && i < len(words) && words[i].EndMs-r.InMs > CutToleranceMs && r.OutMs-words[i].StartMs > CutToleranceMs
	}
	for _, f := range st.Fillers {
		if f.FirstWord < 0 || f.LastWord >= len(words) || f.FirstWord > f.LastWord {
			continue
		}
		played := true
		for i := f.FirstWord; i <= f.LastWord; i++ {
			played = played && inRange(i)
		}
		if !played {
			continue
		}
		if uncertainAround(words, f.FirstWord, f.LastWord) != "" {
			uncertain++
			continue
		}
		findings = append(findings, Finding{Kind: FindingFiller, AtMs: f.StartMs, Detail: fmt.Sprintf("a filler word is kept at %d to %d ms", f.StartMs, f.EndMs)})
	}

	// Silence kept: between two played words, and before the first word of the range (and after the last unless it closes the edit).
	longest := int64(MaxPauseMs * speed)
	for i := first; i < last; i++ {
		if !inRange(i) || !inRange(i+1) {
			continue
		}
		if gap := words[i+1].StartMs - words[i].EndMs; gap > longest {
			if uncertainWords(words, i, i+1) != "" {
				uncertain++
				continue
			}
			findings = append(findings, Finding{Kind: FindingPause, AtMs: words[i].EndMs, Detail: fmt.Sprintf("a pause of %d ms is kept at %d ms", int64(float64(gap)/speed), words[i].EndMs)})
		}
	}
	if lead := words[first].StartMs - r.InMs; lead > longest && inRange(first) {
		findings = append(findings, Finding{Kind: FindingPause, AtMs: r.InMs, Detail: fmt.Sprintf("%d ms of silence before the first word, at %d ms", int64(float64(lead)/speed), r.InMs)})
	}
	if tail := r.OutMs - words[last].EndMs; tail > longest && !r.LastOfEdit && inRange(last) {
		findings = append(findings, Finding{Kind: FindingPause, AtMs: words[last].EndMs, Detail: fmt.Sprintf("%d ms of silence after the last word, at %d ms", int64(float64(tail)/speed), words[last].EndMs)})
	}
	return findings, uncertain
}
