package speechcut

import "github.com/nilpntr/content-fabrique/internal/transcribe"

// Actionable returns the fillers the planner cuts and the audit holds an edit to. A strong filler (an um, a "you know") is always
// cuttable. A weak one ("like", "right", "so", "well") is a real word as often as not, so it is cut only when a pause over MaxPauseMs
// sets it off, before or after it; "that's right" and "things like this" stay intact.
func Actionable(words []transcribe.Word, st transcribe.Structure) []transcribe.Filler {
	var out []transcribe.Filler
	for _, f := range st.Fillers {
		if f.FirstWord < 0 || f.LastWord >= len(words) || f.FirstWord > f.LastWord {
			continue
		}
		if f.Strength != transcribe.StrengthStrong && !setOffByPause(words, f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func setOffByPause(words []transcribe.Word, f transcribe.Filler) bool {
	if f.FirstWord > 0 && words[f.FirstWord].StartMs-words[f.FirstWord-1].EndMs > MaxPauseMs {
		return true
	}
	return f.LastWord+1 < len(words) && words[f.LastWord+1].StartMs-words[f.LastWord].EndMs > MaxPauseMs
}

// FirstPlayed is the index of the first word a shot from inMs to outMs plays: the first that is not left out by the cut (more than
// CutToleranceMs of it is inside). -1 when the shot plays none.
func FirstPlayed(words []transcribe.Word, inMs, outMs int64) int {
	for i, w := range words {
		if w.EndMs-inMs > CutToleranceMs && w.StartMs < outMs {
			return i
		}
	}
	return -1
}

// SentenceStart is the first word of a sentence that is not a leading filler: where a shot that opens on the sentence starts. A
// sentence made of fillers alone starts at its first word.
func SentenceStart(words []transcribe.Word, st transcribe.Structure, s transcribe.Sentence) int {
	skip := map[int]bool{}
	for _, f := range Actionable(words, st) {
		for i := f.FirstWord; i <= f.LastWord; i++ {
			skip[i] = true
		}
	}
	for i := s.FirstWord; i <= s.LastWord; i++ {
		if !skip[i] {
			return i
		}
	}
	return s.FirstWord
}
