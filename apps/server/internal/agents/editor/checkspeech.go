package editor

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/speechcut"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The speech checks (docs/design/ai-video-editor-plan.md, piece 1). They apply to an edit of a speech-led kind (talking, tutorial,
// unboxing_product) whose clips have a transcript, and judge it by the same rules internal/speechcut plans with. Their lines never
// quote the transcript (the speaker's words reach the model only through get_transcript, in data blocks): they give times.
const (
	// CheckSpeechCutClean: no cut inside a word, no filler word kept, no pause over 400 ms kept.
	CheckSpeechCutClean = "speech_cut_clean"
	// CheckHookLine: a speech-led edit opens on the first word of a sentence, not in the middle of one.
	CheckHookLine = "hook_line"
)

// speechLed says the edit is of a kind that follows speech.
func speechLed(typ string) bool {
	return typ == TypeTalking || typ == TypeTutorial || typ == TypeUnboxing
}

// speechChecks returns the speech checks, or none for an edit that is not speech-led.
func (c *checker) speechChecks() []Check {
	if !speechLed(c.typ) {
		return nil
	}
	return []Check{c.speechCutClean(), c.hookLine()}
}

func (c *checker) speechOf(it timeline.VideoItem) *Speech { return c.in.Speech[it.AssetID] }

func (c *checker) speechCutClean() Check {
	var problems []string
	total, shots, uncertain := 0, 0, 0
	for i, it := range c.items {
		sp := c.speechOf(it)
		if sp == nil {
			continue
		}
		shots++
		speed := 1.0
		if len(it.SpeedRamp) == 0 {
			speed = it.SpeedOrDefault()
		}
		found, unc := speechcut.Audit(sp.Words, sp.Structure, speechcut.Range{InMs: it.Source.InMs, OutMs: it.Source.OutMs, Speed: speed, LastOfEdit: i == len(c.items)-1})
		uncertain += unc
		for _, f := range found {
			total++
			if len(problems) < 4 {
				problems = append(problems, fmt.Sprintf("shot %d (%s): %s", i+1, it.ID, f.Detail))
			}
		}
	}
	if shots == 0 {
		return c.skip(CheckSpeechCutClean, "no clip of the edit has a transcript with speech")
	}
	if total > 0 {
		more := ""
		if total > len(problems) {
			more = fmt.Sprintf(" (and %d more)", total-len(problems))
		}
		return c.fail(CheckSpeechCutClean, "%d speech cut problem(s) in %d shots with speech: %s%s. Cut on the keep ranges get_transcript proposes: every cut in a gap between words, no filler, no pause over %d ms", total, shots, strings.Join(problems, "; "), more, speechcut.MaxPauseMs)
	}
	note := ""
	if uncertain > 0 {
		note = fmt.Sprintf("; %d place(s) next to a word of low confidence are not judged", uncertain)
	}
	return c.pass(CheckSpeechCutClean, "%d shots with speech: no cut inside a word, no filler kept, no pause over %d ms%s", shots, speechcut.MaxPauseMs, note)
}

func (c *checker) hookLine() Check {
	if len(c.items) == 0 {
		return c.skip(CheckHookLine, "the edit has no shots")
	}
	it := c.items[0]
	sp := c.speechOf(it)
	if sp == nil {
		return c.skip(CheckHookLine, "the edit opens on a clip with no transcript with speech")
	}
	// The first word the opening shot plays (the hook lab's line_starts_clean reads the same one).
	first := speechcut.FirstPlayed(sp.Words, it.Source.InMs, it.Source.OutMs)
	if first < 0 {
		return c.skip(CheckHookLine, "the opening shot has no speech in it")
	}
	for _, s := range sp.Structure.Sentences {
		if first < s.FirstWord || first > s.LastWord {
			continue
		}
		// A sentence starts at its first word that is not a filler: a leading "um" is for speech_cut_clean to flag, not a mid-sentence
		// opening.
		start := speechcut.SentenceStart(sp.Words, sp.Structure, s)
		if first <= start {
			return c.pass(CheckHookLine, "the edit opens on the first word of a sentence (%d ms of the clip)", sp.Words[start].StartMs)
		}
		return c.fail(CheckHookLine, "the edit opens at %d ms of the clip, in the middle of the sentence that runs from %d to %d ms. Start the first shot at its first word (get_transcript lists the sentences), or open on another sentence", it.Source.InMs, s.StartMs, s.EndMs)
	}
	return c.skip(CheckHookLine, "the transcript has no sentence at the opening word")
}
