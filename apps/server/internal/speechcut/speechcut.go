// Package speechcut plans jump cuts on speech (D-28, docs/design/ai-video-editor-plan.md piece 1): from a transcript's words and
// speech structure it returns the ranges of a clip to keep. Fillers and pauses over PauseMinMs are removed, every cut falls in a
// gap between two words (padded 60 to 100 ms into the gap, never into a word) and sentences stay whole: only fillers and silence
// go, never a word that carries meaning.
//
// The package is pure: no I/O, no clock, no model. The editor reads the plan through its get_transcript tool and judges an edit
// against the same rules with Audit (check_edit's speech_cut_clean), so the planner and the check cannot drift apart.
//
// Whisper's word times drift on music and noise, so a cut is refused (the filler or pause is kept, and listed as uncertain) where
// a word next to it has a confidence under LowConfidence. A confidence of 0 means the provider reports none and is trusted.
package speechcut

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

const (
	// DefaultPadMs is the padding a cut leaves into the gap on each side of the kept words (the plan says 60 to 100 ms).
	DefaultPadMs = 80
	// MinPadMs and MaxPadMs bound Options.PadMs.
	MinPadMs = 60
	MaxPadMs = 100
	// MaxPauseMs is the longest silence a speech edit keeps between two words: the structure's PauseMinMs (D-28).
	MaxPauseMs = transcribe.PauseMinMs
	// LowConfidence is the word probability under which the time of a word is not trusted to cut next to.
	LowConfidence = 0.35
	// CutToleranceMs is how far into a word a cut may be before it counts as cutting the word: word times are not exact.
	CutToleranceMs = 40
)

// Options tune the planner; the zero value takes the defaults.
type Options struct {
	// PadMs is the padding into the gap on each side of a cut, clamped to MinPadMs..MaxPadMs (0 takes DefaultPadMs).
	PadMs int64
	// InMs and OutMs bound the plan to a range of the clip. OutMs 0 means the end of the clip. Only words that lie fully inside take
	// part, so a word is never cut by the edge of the range.
	InMs, OutMs int64
}

// Keep is a range of the clip to play, in the clip's own milliseconds, with the words in it.
type Keep struct {
	StartMs   int64 `json:"start_ms"`
	EndMs     int64 `json:"end_ms"`
	FirstWord int   `json:"first_word"`
	LastWord  int   `json:"last_word"`
	// Sentence is the index (into Structure.Sentences) of the sentence the range starts in.
	Sentence int `json:"sentence"`
}

// Removal kinds.
const (
	KindFiller = "filler"
	KindPause  = "pause"
)

// Removal is something the plan cuts out, or would have cut out but kept because the cut is not certain.
type Removal struct {
	Kind    string `json:"kind"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	// Text is the filler's words; empty for a pause.
	Text string `json:"text,omitempty"`
	// Kept is true when the removal was refused (Reason says why): the filler or pause stays in the keep ranges.
	Kept   bool   `json:"kept,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Plan is the result.
type Plan struct {
	Keep    []Keep    `json:"keep"`
	Removed []Removal `json:"removed"`
	// KeptMs is the total length of the keep ranges; RemovedMs the time cut out between the first and the last of them.
	KeptMs    int64 `json:"kept_ms"`
	RemovedMs int64 `json:"removed_ms"`
}

// Uncertain returns the removals that were refused.
func (p Plan) Uncertain() []Removal {
	var out []Removal
	for _, r := range p.Removed {
		if r.Kept {
			out = append(out, r)
		}
	}
	return out
}

func clampPad(p int64) int64 {
	switch {
	case p == 0:
		return DefaultPadMs
	case p < MinPadMs:
		return MinPadMs
	case p > MaxPadMs:
		return MaxPadMs
	}
	return p
}

// Valid reports whether words can be planned on: the times run forward and the words do not overlap.
func Valid(words []transcribe.Word) bool {
	var prevEnd int64
	for i, w := range words {
		if w.StartMs < 0 || w.EndMs < w.StartMs || (i > 0 && w.StartMs < prevEnd) {
			return false
		}
		prevEnd = w.EndMs
	}
	return true
}

// Low reports whether the time of word i cannot be trusted to place a cut next to.
func Low(words []transcribe.Word, i int) bool {
	return i >= 0 && i < len(words) && words[i].Confidence > 0 && words[i].Confidence < LowConfidence
}

// Compute plans the cuts of a clip with the given words and structure. It returns an empty plan for words that are not valid.
func Compute(words []transcribe.Word, st transcribe.Structure, opt Options) Plan {
	pad := clampPad(opt.PadMs)
	var plan Plan
	if len(words) == 0 || !Valid(words) {
		return plan
	}
	outMs := opt.OutMs
	if outMs <= 0 {
		outMs = words[len(words)-1].EndMs
	}
	inside := func(i int) bool { return words[i].StartMs >= opt.InMs && words[i].EndMs <= outMs }

	// Which words are cut out. A filler goes unless it, or a word next to it, has a time that cannot be trusted.
	drop := make([]bool, len(words))
	for _, f := range st.Fillers {
		if f.FirstWord < 0 || f.LastWord >= len(words) || f.FirstWord > f.LastWord || !inside(f.FirstWord) || !inside(f.LastWord) {
			continue
		}
		r := Removal{Kind: KindFiller, StartMs: words[f.FirstWord].StartMs, EndMs: words[f.LastWord].EndMs, Text: f.Text}
		if why := uncertainAround(words, f.FirstWord, f.LastWord); why != "" {
			r.Kept, r.Reason = true, why
		} else {
			for i := f.FirstWord; i <= f.LastWord; i++ {
				drop[i] = true
			}
		}
		plan.Removed = append(plan.Removed, r)
	}
	// Which silences are cut: a gap over MaxPauseMs between two words.
	cutAfter := make([]bool, len(words)) // cutAfter[i]: the silence between word i and the next is cut
	for i := 0; i+1 < len(words); i++ {
		if !inside(i) || !inside(i+1) {
			continue
		}
		if words[i+1].StartMs-words[i].EndMs <= MaxPauseMs {
			continue
		}
		r := Removal{Kind: KindPause, StartMs: words[i].EndMs, EndMs: words[i+1].StartMs}
		if why := uncertainWords(words, i, i+1); why != "" {
			r.Kept, r.Reason = true, why
		} else {
			cutAfter[i] = true
		}
		plan.Removed = append(plan.Removed, r)
	}

	// Runs of kept words that follow each other without a cut or a removed word between them, each padded into the gaps around it.
	runStart, last := -1, -1
	flush := func() {
		if runStart < 0 {
			return
		}
		first := runStart
		runStart = -1
		start := words[first].StartMs
		if first > 0 {
			start -= min(pad, words[first].StartMs-words[first-1].EndMs)
		} else {
			start -= min(pad, words[first].StartMs)
		}
		end := words[last].EndMs
		if last+1 < len(words) {
			end += min(pad, words[last+1].StartMs-words[last].EndMs)
		} else {
			end += pad
		}
		start, end = max(start, opt.InMs), min(end, outMs)
		if end > start {
			plan.Keep = append(plan.Keep, Keep{StartMs: start, EndMs: end, FirstWord: first, LastWord: last, Sentence: sentenceOf(st, first)})
		}
	}
	for i := range words {
		if !inside(i) || drop[i] {
			flush()
			last = -1
			continue
		}
		if runStart >= 0 && cutAfter[last] {
			flush()
		}
		if runStart < 0 {
			runStart = i
		}
		last = i
	}
	flush()

	for _, k := range plan.Keep {
		plan.KeptMs += k.EndMs - k.StartMs
	}
	if n := len(plan.Keep); n > 0 {
		plan.RemovedMs = plan.Keep[n-1].EndMs - plan.Keep[0].StartMs - plan.KeptMs
	}
	return plan
}

// uncertainAround says why a cut around words first..last cannot be trusted ("" when it can): the word before, the words
// themselves or the word after has a low confidence.
func uncertainAround(words []transcribe.Word, first, last int) string {
	return uncertainWords(words, first-1, first, last, last+1)
}

// uncertainWords is the same for any words.
func uncertainWords(words []transcribe.Word, idx ...int) string {
	for _, i := range idx {
		if Low(words, i) {
			return fmt.Sprintf("the time of %q is not certain (confidence %.2f), so the cut next to it is not made", strings.TrimSpace(words[i].Text), words[i].Confidence)
		}
	}
	return ""
}

func sentenceOf(st transcribe.Structure, word int) int {
	for i, s := range st.Sentences {
		if word >= s.FirstWord && word <= s.LastWord {
			return i
		}
	}
	return 0
}
