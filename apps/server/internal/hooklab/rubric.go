package hooklab

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/nilpntr/content-fabrique/internal/speechcut"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

// The criteria of the rubric. Each scores a variant from 0 to 1 and passes at PassAt or more.
const (
	// InFirst2s: the hook lands in the first two seconds: the text is on screen and the spoken line has begun.
	InFirst2s = "hook_in_first_2s"
	// Readable: the text hook is short enough to read in the time it has (or, without text, the spoken line is short enough to land).
	Readable = "hook_readable"
	// TextOffBody: the text hook covers neither the face nor the body of the person in the shot.
	TextOffBody = "text_off_body"
	// FirstFrame: the first frames are not dead time, not near black and not soft.
	FirstFrame = "first_frame_alive"
	// CleanStart: the spoken line starts on the first word of a sentence, and the shot does not start inside a word.
	CleanStart = "line_starts_clean"
	// NoFillerStart: the first word spoken is not a filler.
	NoFillerStart = "no_filler_start"
)

// Criteria lists the criteria in the order a card shows them.
var Criteria = []string{InFirst2s, Readable, TextOffBody, FirstFrame, CleanStart, NoFillerStart}

// The thresholds of the rubric.
const (
	// PassAt is the score a criterion passes at, and PassTotal the total an opening is good enough at (check_edit's hook_score).
	PassAt    = 0.7
	PassTotal = 0.6
	// HookByMs is the two seconds a hook has; PromptMs is how early is as good as it gets (the title of check_edit's hook_title is
	// on by then). Between the two the score falls from 1 to LateScore.
	HookByMs  = 2000
	PromptMs  = 500
	LateScore = 0.7
	// MaxHookWords and MaxHookChars are the longest text hook that reads at a glance; twice that is no hook any more.
	MaxHookWords = 7
	MaxHookChars = 40
	// MinTextMs is the shortest a text hook stays up, and WordReadMs what each of its words needs on top of the first three.
	MinTextMs  = 1000
	WordReadMs = 300
	// MaxLineMs and MaxLineWords are the longest spoken line that still lands as a hook.
	MaxLineMs    = 4000
	MaxLineWords = 14
	// FirstFrameMs is the stretch at the start of the first shot that is judged as "the first frame".
	FirstFrameMs = 1000
	// DarkLit is the 90th percentile luma under which the first frames are near black (check_edit's crushed uses the same 0.04), and
	// SoftSharpness the sharpness score under which they are soft (the clip analysis calls a moment "soft" under 0.4).
	DarkLit       = 0.04
	SoftSharpness = 0.4
)

// Statuses of a result.
const (
	StatusPass = "pass"
	StatusFail = "fail"
	// StatusNA: the criterion does not apply (nothing to judge, or the facts are missing). It counts neither for nor against.
	StatusNA = "n/a"
)

// Result is one criterion's verdict on a variant.
type Result struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Score  float64 `json:"score"`
	Weight float64 `json:"weight"`
	// Detail is one line with the numbers. It never quotes the transcript or the text.
	Detail string `json:"detail"`
}

func scored(id string, score float64, format string, a ...any) Result {
	score = math.Max(0, math.Min(1, score))
	st := StatusFail
	if score >= PassAt {
		st = StatusPass
	}
	return Result{ID: id, Status: st, Score: round3(score), Detail: fmt.Sprintf(format, a...)}
}

func na(id, format string, a ...any) Result {
	return Result{ID: id, Status: StatusNA, Detail: fmt.Sprintf(format, a...)}
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// Score scores one variant on every criterion with the given weights (a criterion without a weight takes its default).
func Score(v Variant, ev Evidence, w Weights) Card {
	op := openingOf(v, ev.Speech)
	card := Card{Variant: v, Line: op.line}
	card.Results = []Result{inFirst2s(v, ev, op), readable(v, op), textOffBody(v, ev), firstFrame(v, ev), cleanStart(v, op), noFillerStart(op)}
	var sum, weights float64
	for i := range card.Results {
		r := &card.Results[i]
		r.Weight = w.Of(r.ID)
		switch r.Status {
		case StatusNA:
			continue
		case StatusFail:
			card.Failed++
		}
		card.Applied++
		sum += r.Weight * r.Score
		weights += r.Weight
	}
	if weights > 0 {
		card.Total = round3(sum / weights)
	}
	return card
}

// opening is what the first shot says first.
type opening struct {
	speech *Speech
	// first is the index of the first word the shot plays, -1 when it plays none.
	first int
	// sentence is the index of the sentence that word is in, -1 when it is in none; start is the first word of that sentence that is
	// not a leading filler.
	sentence, start int
	// insideWord says the shot starts inside the first word; uncertain that the word's time cannot be trusted.
	insideWord, uncertain bool
	// leadMs is how long into the video the first word begins.
	leadMs int64
	line   *Line
}

func openingOf(v Variant, sp *Speech) opening {
	op := opening{first: -1, sentence: -1, start: -1}
	if sp == nil || len(sp.Words) == 0 || !speechcut.Valid(sp.Words) {
		return op
	}
	op.speech = sp
	// The first word the shot plays, as check_edit's hook_line reads it.
	if op.first = speechcut.FirstPlayed(sp.Words, v.InMs, v.OutMs); op.first < 0 {
		return op
	}
	w := sp.Words[op.first]
	op.uncertain = speechcut.Low(sp.Words, op.first)
	op.insideWord = v.InMs > w.StartMs+speechcut.CutToleranceMs
	op.leadMs = int64(float64(max(w.StartMs-v.InMs, 0)) / v.speed())
	for i, s := range sp.Structure.Sentences {
		if op.first < s.FirstWord || op.first > s.LastWord || s.LastWord >= len(sp.Words) {
			continue
		}
		op.sentence = i
		op.start = speechcut.SentenceStart(sp.Words, sp.Structure, s)
		op.line = &Line{Sentence: i, StartMs: s.StartMs, EndMs: s.EndMs, Words: s.LastWord - s.FirstWord + 1, Text: cutLine(s.Text)}
		break
	}
	return op
}

// hasText says the variant has a text hook.
func hasText(v Variant) bool { return strings.TrimSpace(v.Text) != "" }

func inFirst2s(v Variant, ev Evidence, op opening) Result {
	text, spoken := hasText(v), op.first >= 0
	if !text && !spoken {
		switch {
		case ev.NoTitle:
			return na(InFirst2s, "no text and no speech, as in this creator's videos: the picture is the hook")
		case ev.TitleProbe:
			return na(InFirst2s, "no speech, and the title is not written yet: have it on screen by %d ms", PromptMs)
		}
		return scored(InFirst2s, 0, "the opening has no text and no spoken line: nothing but the picture hooks in the first %d ms", HookByMs)
	}
	// Every part of the hook has to land: the later of the text and the first word.
	var late int64
	var parts []string
	if text {
		late = max(late, v.TextAtMs)
		parts = append(parts, fmt.Sprintf("the text comes on at %d ms", v.TextAtMs))
	}
	if spoken {
		late = max(late, op.leadMs)
		parts = append(parts, fmt.Sprintf("the first word is spoken at %d ms", op.leadMs))
	}
	what := strings.Join(parts, " and ")
	switch {
	case late > HookByMs:
		return scored(InFirst2s, 0, "%s: the hook must land within %d ms", what, HookByMs)
	case late <= PromptMs:
		return scored(InFirst2s, 1, "%s (by %d ms is as early as it gets)", what, PromptMs)
	}
	return scored(InFirst2s, 1-(1-LateScore)*float64(late-PromptMs)/float64(HookByMs-PromptMs), "%s: inside the first %d ms, but %d ms or earlier is better", what, HookByMs, PromptMs)
}

// WeakOpenerScore is what no_filler_start gives a line that starts on a weak opener which belongs to its sentence.
const WeakOpenerScore = 0.7

func fillerAt(fillers []transcribe.Filler, word int) (transcribe.Filler, bool) {
	for _, f := range fillers {
		if word >= f.FirstWord && word <= f.LastWord {
			return f, true
		}
	}
	return transcribe.Filler{}, false
}

// TextWords counts the words of a text hook.
func TextWords(text string) int { return len(strings.Fields(text)) }

// ReadMs is how long a text hook of n words needs on screen.
func ReadMs(words int) int64 { return MinTextMs + int64(max(words-3, 0))*WordReadMs }

func readable(v Variant, op opening) Result {
	if hasText(v) {
		words, chars := TextWords(v.Text), utf8.RuneCountInString(strings.Join(strings.Fields(v.Text), " "))
		score := 1.0
		problem := ""
		switch {
		case words > 2*MaxHookWords || chars > 2*MaxHookChars:
			score, problem = 0, fmt.Sprintf(": far too long for a hook, use at most %d words and %d characters", MaxHookWords, MaxHookChars)
		case words > MaxHookWords || chars > MaxHookChars:
			score, problem = 0.5, fmt.Sprintf(": too long to read at a glance, use at most %d words and %d characters", MaxHookWords, MaxHookChars)
		}
		detail := fmt.Sprintf("the text is %d words, %d characters", words, chars)
		if v.TextDurationMs > 0 {
			need := ReadMs(words)
			detail += fmt.Sprintf(", on screen for %d ms", v.TextDurationMs)
			switch {
			case v.TextDurationMs < need/2:
				score, problem = 0, problem+fmt.Sprintf(": it needs %d ms to be read", need)
			case v.TextDurationMs < need:
				score, problem = math.Min(score, 0.5), problem+fmt.Sprintf(": it needs %d ms to be read", need)
			}
		}
		return scored(Readable, score, "%s%s", detail, problem)
	}
	if op.line == nil {
		return na(Readable, "no text and no spoken line to read")
	}
	dur := op.line.EndMs - op.line.StartMs
	switch {
	case dur > 2*MaxLineMs || op.line.Words > 2*MaxLineWords:
		return scored(Readable, 0, "the opening line runs %d ms over %d words: far too long to hook, open on a line of at most %d ms", dur, op.line.Words, MaxLineMs)
	case dur > MaxLineMs || op.line.Words > MaxLineWords:
		return scored(Readable, 0.5, "the opening line runs %d ms over %d words: a hook is at most %d ms and %d words", dur, op.line.Words, MaxLineMs, MaxLineWords)
	}
	return scored(Readable, 1, "the opening line runs %d ms over %d words", dur, op.line.Words)
}

// textScores is the score of each place a text can end up at.
var textScores = map[string]float64{TextClear: 1, TextAdjusted: 0.8, TextTrimmed: 0.5, TextMoved: 0.2, TextDropped: 0, TextOnBody: 0, TextOnFace: 0}

var textDetails = map[string]string{
	TextClear:    "the text is clear of the person's face and body",
	TextAdjusted: "the text would cover the person, so the renderer moves or shrinks it in the frame: a shorter text keeps its size",
	TextTrimmed:  "the text is clear of the person for a part of its time only",
	TextMoved:    "the text has no room clear of the person on the first shot, so it would be shown later: shorten it, or open on a shot with room above the head",
	TextDropped:  "the text has no room clear of the person at any time, so it would be left out: shorten it, or open on a shot with room above the head",
	TextOnBody:   "the text covers the person's body",
	TextOnFace:   "the text covers the person's face",
}

func textOffBody(v Variant, ev Evidence) Result {
	switch {
	case !hasText(v) && !ev.TitleProbe:
		return na(TextOffBody, "no text")
	case ev.Text == "":
		return na(TextOffBody, "the clip has no subject boxes, so where the person stands is not known")
	}
	score, ok := textScores[ev.Text]
	if !ok {
		return na(TextOffBody, "the place of the text is not known")
	}
	if !hasText(v) {
		return scored(TextOffBody, score, "for a title of %d characters on this shot: %s", len(ProbeTitle), textDetails[ev.Text])
	}
	return scored(TextOffBody, score, "%s", textDetails[ev.Text])
}

func firstFrame(v Variant, ev Evidence) Result {
	p := ev.Picture
	if p == nil {
		return na(FirstFrame, "the clip has not been analysed")
	}
	window := min(v.OutMs-v.InMs, FirstFrameMs)
	if window <= 0 {
		return na(FirstFrame, "the first shot has no length")
	}
	var dead int64
	for _, d := range p.Dead {
		if lo, hi := max(d.StartMs, v.InMs), min(d.EndMs, v.InMs+window); hi > lo {
			dead += hi - lo
		}
	}
	score := 1.0
	var problems []string
	switch share := float64(dead) / float64(window); {
	case share >= 0.5:
		score = 0
		problems = append(problems, fmt.Sprintf("%d of its first %d ms are dead time", dead, window))
	case dead > 0:
		score = 0.5
		problems = append(problems, fmt.Sprintf("%d of its first %d ms are dead time", dead, window))
	}
	if p.Lit != nil && *p.Lit < DarkLit {
		score = 0
		problems = append(problems, fmt.Sprintf("it is near black (lit part %.2f, under %.2f)", *p.Lit, DarkLit))
	}
	if p.Sharpness != nil && *p.Sharpness < SoftSharpness {
		score = math.Min(score, 0.4)
		problems = append(problems, fmt.Sprintf("it is soft (sharpness %.2f, under %.2f)", *p.Sharpness, SoftSharpness))
	}
	if len(problems) > 0 {
		return scored(FirstFrame, score, "the first shot starts at %d ms of the clip: %s", v.InMs, strings.Join(problems, "; "))
	}
	return scored(FirstFrame, 1, "the first shot starts at %d ms of the clip, on live picture", v.InMs)
}

func cleanStart(v Variant, op opening) Result {
	switch {
	case op.speech == nil:
		return na(CleanStart, "the first shot's clip has no transcript with speech")
	case op.first < 0:
		return na(CleanStart, "the first shot has no speech in it")
	case op.uncertain:
		return na(CleanStart, "the time of the first word is not certain, so the start is not judged")
	case op.sentence < 0:
		return na(CleanStart, "the transcript has no sentence at the first word")
	}
	s := op.speech.Structure.Sentences[op.sentence]
	switch {
	case op.insideWord:
		w := op.speech.Words[op.first]
		return scored(CleanStart, 0, "the shot starts at %d ms, inside a word (%d to %d ms): start it in the gap before the word", v.InMs, w.StartMs, w.EndMs)
	case op.first > op.start:
		return scored(CleanStart, 0, "the shot opens at %d ms, in the middle of the sentence that runs from %d to %d ms: start on its first word", v.InMs, s.StartMs, s.EndMs)
	}
	return scored(CleanStart, 1, "the shot opens on the first word of a sentence (%d ms of the clip)", op.speech.Words[op.start].StartMs)
}

func noFillerStart(op opening) Result {
	switch {
	case op.speech == nil:
		return na(NoFillerStart, "the first shot's clip has no transcript with speech")
	case op.first < 0:
		return na(NoFillerStart, "the first shot has no speech in it")
	}
	// A filler the speech planner cuts (an um, a "you know", a weak one set off by a pause) fails: the shot starts after it, where
	// line_starts_clean takes the sentence to start. A weak opener that is part of its sentence ("So today ...") cannot be cut off
	// cleanly, so it passes, lower than a line that starts on its point.
	if f, ok := fillerAt(speechcut.Actionable(op.speech.Words, op.speech.Structure), op.first); ok {
		return scored(NoFillerStart, 0, "the first word spoken (%d to %d ms) is a filler: start the shot after it", f.StartMs, f.EndMs)
	}
	if f, ok := fillerAt(op.speech.Structure.Fillers, op.first); ok {
		return scored(NoFillerStart, WeakOpenerScore, "the first word spoken (%d to %d ms) is a weak opener; a line that starts on its point hooks harder", f.StartMs, f.EndMs)
	}
	return scored(NoFillerStart, 1, "the first word spoken is not a filler")
}
