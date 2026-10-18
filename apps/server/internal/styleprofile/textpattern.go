package styleprofile

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// All the text on screen (P6-05d, stylevision.v4): not only the hook title. The text read sees a frame of every shot (up to
// MaxShotFrames), and reports the text in each with where it sits, how big, its colour and shadow, whether it lies over the person's
// body and whether it is a line of dialogue. From those lines, in code, a reference's text pattern:
//
//   - single_title: one title (in the hook, perhaps held or repeated), and at most numbered chapter titles besides;
//   - story_captions: a new short line on most shots, a story told caption by caption;
//   - dialogue: quoted lines, often two speakers answering each other (a meme exchange), usually before a reveal;
//   - none: no text.
//
// The look of the lines (where they sit, their size, case, colour and shadow, and how often they lie over the body) is measured for the
// pattern's own lines, and the language from the text. The lines are the creator's words (untrusted): the editor never sees them, only
// the pattern, the look, the length of a line and the language.

// The text patterns.
const (
	PatternSingleTitle   = "single_title"
	PatternStoryCaptions = "story_captions"
	PatternDialogue      = "dialogue"
	PatternNone          = "none"
)

// TextPatterns lists the patterns.
var TextPatterns = []string{PatternSingleTitle, PatternStoryCaptions, PatternDialogue, PatternNone}

// The positions of a line of text on the frame (the four bands of a vertical video, top to bottom).
const (
	TextTop        = "top"
	TextUpperThird = "upper_third"
	TextMiddle     = "middle"
	TextLowerThird = "lower_third"
)

// MaxShotFrames is the most shots the text read sees a frame of; MaxShotTextChars the longest line kept.
const (
	MaxShotFrames    = 10
	MaxShotTextChars = 120
)

// ShotText is the text in the frame of one shot, as read: the line (untrusted), where it sits, the cap height as a percentage of the
// frame height, its colour and shadow, whether it lies over the person's body (the torso or the head), and whether it is a line of
// dialogue (quoted speech, a character talking).
type ShotText struct {
	// Frame is the number of the frame in the read (only while it is parsed; 0 once AtS is set).
	Frame     int     `json:"frame,omitempty"`
	AtS       float64 `json:"at_s"`
	Text      string  `json:"text,omitempty"`
	Position  string  `json:"position,omitempty"`
	HeightPct float64 `json:"height_pct,omitempty"`
	Colour    string  `json:"colour,omitempty"`
	Shadow    string  `json:"shadow,omitempty"`
	OverBody  bool    `json:"over_body,omitempty"`
	Dialogue  bool    `json:"dialogue,omitempty"`
}

// ShotTimes are the times of the frames of the shots the text read sees: one a little into each shot (after the text of a new shot has
// come on, before the next cut), up to MaxShotFrames spread over the shots. A video of one or two long takes gets frames every few
// seconds instead, so lines that change within a take are seen too.
func ShotTimes(cutsS []float64, durationS float64) []float64 {
	if durationS <= 0 {
		return nil
	}
	starts := append([]float64{0}, cutsS...)
	if len(starts) < 3 {
		var out []float64
		step := math.Max(2.5, durationS/float64(MaxShotFrames))
		for t := 1.0; t < durationS-0.2 && len(out) < MaxShotFrames; t += step {
			out = append(out, round2(t))
		}
		return out
	}
	pick := make([]float64, 0, MaxShotFrames)
	n := len(starts)
	for i := range min(n, MaxShotFrames) {
		k := (i * n) / min(n, MaxShotFrames)
		end := durationS
		if k+1 < n {
			end = starts[k+1]
		}
		at := starts[k] + math.Min(0.8, (end-starts[k])*0.5)
		if at < durationS-0.05 {
			pick = append(pick, round2(at))
		}
	}
	return pick
}

var (
	chapterLine = regexp.MustCompile(`^\s*\d{1,2}\s*[.):]`)
	quotedLine  = regexp.MustCompile(`^\s*["“”‘’'«»„].+["“”‘’'«»]\s*$`)
)

// normLine is a line compared without case, spaces or punctuation.
func normLine(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// patternLines are the lines of the shots that are not a numbered chapter title, one per line (a line held over several shots counts
// once): what a story or a dialogue is told in. The first line counts too: a creator of story captions opens with one, and the read may
// call it the title.
func patternLines(t *TextStyle) []ShotText {
	seen := map[string]bool{}
	var out []ShotText
	for _, s := range t.ShotTexts {
		k := normLine(s.Text)
		if k == "" || seen[k] || chapterLine.MatchString(s.Text) {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// spoken says a line reads as speech: in quotes, or flagged as dialogue and a question or an exclamation. The flag alone is not enough:
// the read flags plain story captions in the first person as dialogue too.
func spoken(l ShotText) bool {
	if quotedLine.MatchString(l.Text) {
		return true
	}
	t := strings.TrimSpace(l.Text)
	return l.Dialogue && (strings.HasSuffix(t, "?") || strings.HasSuffix(t, "!"))
}

// ClassifyText is the text pattern of one read: dialogue when two lines or more read as speech (spoken: quoted, or a question or an
// exclamation the read saw as a character speaking);
// story_captions when a new line comes on half of the shots seen or more (at least two lines, three when more than three shots were
// seen); single_title when there is a title or a line; none when there is no text at all. A read made before the shots were read
// (stylevision.v3 and older) is single_title or none.
func ClassifyText(t *TextStyle) string {
	if t == nil {
		return ""
	}
	lines := patternLines(t)
	dialogue := 0
	for _, l := range lines {
		if spoken(l) {
			dialogue++
		}
	}
	shots := len(t.ShotTexts)
	need := max(2, int(math.Ceil(0.5*float64(shots))))
	if shots > 3 {
		need = max(need, 3)
	}
	switch {
	case dialogue >= 2:
		return PatternDialogue
	case shots > 0 && len(lines) >= need:
		return PatternStoryCaptions
	case t.TitlePresent || len(lines) > 0 || len(t.Chapters) > 0:
		return PatternSingleTitle
	}
	return PatternNone
}

// lineCase is the case of a line: upper, lower, sentence (a capital first letter and the rest mostly lowercase) or mixed; "" without
// letters.
func lineCase(s string) string {
	var up, low int
	first := rune(0)
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		if first == 0 {
			first = r
		}
		if unicode.IsUpper(r) {
			up++
		} else if unicode.IsLower(r) {
			low++
		}
	}
	switch {
	case up+low == 0:
		return ""
	case low == 0:
		return "upper"
	case up == 0:
		return "lower"
	case unicode.IsUpper(first) && laterCapitals(s) <= 1:
		return "sentence"
	}
	return "mixed"
}

// laterCapitals counts the words after the first that start with a capital, past one in three of them (a name or two in a sentence is
// still sentence case; Title Case is not).
func laterCapitals(s string) int {
	words := strings.Fields(s)
	if len(words) < 2 {
		return 0
	}
	capped := 0
	for _, w := range words[1:] {
		for _, r := range w {
			if unicode.IsLetter(r) {
				if unicode.IsUpper(r) {
					capped++
				}
				break
			}
		}
	}
	return max(0, capped-len(words[1:])/3)
}

// TextLook is the look of the lines of a pattern: where they sit (the majority), their cap height (the median percentage of the frame
// height), case, colour (the median), shadow, the share of them over the person's body, and how many words a line has.
type TextLook struct {
	Lines         int     `json:"lines"`
	Position      string  `json:"position,omitempty"`
	HeightPct     float64 `json:"height_pct,omitempty"`
	Case          string  `json:"case,omitempty"`
	Colour        string  `json:"colour,omitempty"`
	Shadow        string  `json:"shadow,omitempty"`
	OverBodyShare float64 `json:"over_body_share"`
	WordsLo       int     `json:"words_lo,omitempty"`
	WordsHi       int     `json:"words_hi,omitempty"`
}

// lookOf measures the look of lines.
func lookOf(lines []ShotText) *TextLook {
	if len(lines) == 0 {
		return nil
	}
	l := &TextLook{Lines: len(lines)}
	var pos, cases, shadows, colours []string
	var heights, words []float64
	over := 0
	for _, s := range lines {
		if s.Position != "" {
			pos = append(pos, s.Position)
		}
		if c := lineCase(s.Text); c != "" {
			cases = append(cases, c)
		}
		if s.Shadow != "" {
			shadows = append(shadows, s.Shadow)
		}
		if s.Colour != "" {
			colours = append(colours, s.Colour)
		}
		if s.HeightPct > 0 {
			heights = append(heights, s.HeightPct)
		}
		if s.OverBody {
			over++
		}
		words = append(words, float64(len(strings.Fields(s.Text))))
	}
	l.Position, l.Case, l.Shadow, l.Colour = majority(pos), majority(cases), majority(shadows), medianColour(colours)
	if len(heights) > 0 {
		l.HeightPct = round1(RangeOf(heights).Median)
	}
	l.OverBodyShare = round2(float64(over) / float64(len(lines)))
	if len(words) > 0 {
		sorted := sortedCopy(words)
		l.WordsLo, l.WordsHi = int(percentile(sorted, 0.1)+0.5), int(percentile(sorted, 0.9)+0.5)
	}
	return l
}

// aggregatePattern sets the text pattern of the profile from the reads that saw the shots: the majority pattern (ties to the more
// specific one: dialogue, then story captions), its share, the look of the lines of that pattern, and the language of the text.
func aggregatePattern(t *TextProfile, reads []*TextStyle) {
	votes := map[string]int{}
	var langs []string
	byPattern := map[string][]ShotText{}
	for _, r := range reads {
		if !r.ShotsRead {
			continue
		}
		t.PatternRead++
		p := ClassifyText(r)
		votes[p]++
		if r.Language != "" {
			langs = append(langs, r.Language)
		}
		if p == PatternStoryCaptions || p == PatternDialogue {
			byPattern[p] = append(byPattern[p], patternLines(r)...)
		}
	}
	if t.PatternRead == 0 {
		return
	}
	best := ""
	for _, p := range []string{PatternDialogue, PatternStoryCaptions, PatternSingleTitle, PatternNone} {
		if best == "" || votes[p] > votes[best] {
			best = p
		}
	}
	t.Pattern = best
	t.PatternShare = round2(float64(votes[best]) / float64(t.PatternRead))
	t.Language = majority(langs)
	// The look of the lines: of the pattern, or of the captions and dialogue together when the majority is a title (a creator who mixes).
	lines := byPattern[best]
	if len(lines) == 0 {
		lines = append(byPattern[PatternStoryCaptions], byPattern[PatternDialogue]...)
	}
	t.LineLook = lookOf(lines)
	for _, p := range []string{PatternStoryCaptions, PatternDialogue} {
		if votes[p] > 0 {
			t.PatternsSeen = append(t.PatternsSeen, p)
		}
	}
}

// patternWords says the pattern in words for the editor and the summary (no line of the creator's).
func (t *TextProfile) patternWords() string {
	if t == nil || t.Pattern == "" {
		return ""
	}
	var b strings.Builder
	switch t.Pattern {
	case PatternStoryCaptions:
		b.WriteString("on-screen text: story captions, a new short line on each shot that tells a little story")
	case PatternDialogue:
		b.WriteString("on-screen text: dialogue, quoted lines (often two speakers answering each other) before a reveal")
	case PatternSingleTitle:
		b.WriteString("on-screen text: one title")
	default:
		b.WriteString("on-screen text: none")
	}
	fmt.Fprintf(&b, " (%.0f %% of the %d videos read for it)", t.PatternShare*100, t.PatternRead)
	if l := t.LineLook; l != nil && (t.Pattern == PatternStoryCaptions || t.Pattern == PatternDialogue) {
		var parts []string
		if l.Case != "" {
			parts = append(parts, map[string]string{"sentence": "sentence case", "upper": "capitals", "lower": "lowercase", "mixed": "mixed case"}[l.Case])
		}
		if l.Colour != "" {
			parts = append(parts, l.Colour)
		}
		if l.HeightPct > 0 {
			parts = append(parts, fmt.Sprintf("letters about %.1f %% of the frame height", l.HeightPct))
		}
		if l.Position != "" {
			parts = append(parts, "in the "+strings.ReplaceAll(l.Position, "_", " ")+" of the frame")
		}
		if l.Shadow != "" {
			parts = append(parts, l.Shadow+" shadow")
		}
		if l.WordsHi > 0 {
			parts = append(parts, fmt.Sprintf("%d to %d words a line", max(l.WordsLo, 1), l.WordsHi))
		}
		parts = append(parts, fmt.Sprintf("%.0f %% of the lines over the person's body", l.OverBodyShare*100))
		b.WriteString(": " + strings.Join(parts, ", "))
	}
	if t.Language != "" {
		fmt.Fprintf(&b, "; the language of the text: %s", LanguageName(t.Language))
	}
	return b.String()
}

// languageNames are the names of the languages the read is likely to report (ISO 639-1); others are shown as their code.
var languageNames = map[string]string{
	"en": "English", "nl": "Dutch", "de": "German", "fr": "French", "es": "Spanish", "it": "Italian", "pt": "Portuguese", "pl": "Polish",
	"sv": "Swedish", "da": "Danish", "no": "Norwegian", "fi": "Finnish", "tr": "Turkish",
}

// LanguageName is the English name of an ISO 639-1 code (the code itself when it is not a known one).
func LanguageName(code string) string {
	if n, ok := languageNames[code]; ok {
		return n
	}
	return code
}

var languageCode = regexp.MustCompile(`^[a-z]{2}$`)

// validPattern says p is a text pattern.
func validPattern(p string) bool { return slices.Contains(TextPatterns, p) }
