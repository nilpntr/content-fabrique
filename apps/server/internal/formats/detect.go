package formats

import (
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Detection: which formats a creator's finished video is in, from what the style analysis already measured (the cuts, the drop, the
// camera) and the text it already read (the title, the chapter titles, a line per shot, the pattern). No model call: the cues are
// computed here and the cards' rules combine them. The text is the creator's (untrusted): it is only matched against our words, never
// passed on.

// The cues of a reference. Every rule of a card is made of them; keyword and keyword_hook use the card's own keywords.
const (
	CuePatternSingleTitle   = "pattern_single_title"
	CuePatternStoryCaptions = "pattern_story_captions"
	CuePatternDialogue      = "pattern_dialogue"
	CuePatternNone          = "pattern_none"
	// CueTitleSeries: the title ends in a number of the creator's series ("<TOPIC> 101").
	CueTitleSeries = "title_series"
	// CueTitleNumbered: the title starts with a count ("<N> ..."), 2 to 20.
	CueTitleNumbered = "title_numbered"
	// CueChapters: two numbered chapter titles or more ("1. ...", "2. ...").
	CueChapters = "chapters"
	// CueHookQuote: the hook's text is a quote: in quotes, or a thought in the first person of five words or more (not a series or a
	// count title).
	CueHookQuote = "hook_quote"
	// CueCutAtDrop: the first cut lands on the music's drop or lift (within 0.7 s).
	CueCutAtDrop = "cut_at_drop"
	// CueFewCuts: three cuts or fewer. CueManyCuts: five or more. CueMontage: six or more with a median shot of 2 s or less.
	CueFewCuts  = "few_cuts"
	CueManyCuts = "many_cuts"
	CueMontage  = "montage"
	// CueLongTake: no cut, or a shot of 8 s or more with five cuts or fewer.
	CueLongTake = "long_take"
	// CueHandheld: the camera itself moves.
	CueHandheld = "handheld"
	// CueFirstPerson: half the lines or more are in the first person.
	CueFirstPerson = "first_person"
	// CueVersus: a line sets two values against each other ("<a> vs <b>" with a number, two weights, two years, "day 1").
	CueVersus = "versus"
	// CueNumbersRise: three lines or more with numbers that go up (a counter).
	CueNumbersRise = "numbers_rise"
	// CueKeyword: one of the card's keywords is in the text; CueKeywordHook: in the title or the first line.
	CueKeyword     = "keyword"
	CueKeywordHook = "keyword_hook"
)

// Cues lists the cue names a rule may use.
var Cues = []string{
	CuePatternSingleTitle, CuePatternStoryCaptions, CuePatternDialogue, CuePatternNone, CueTitleSeries, CueTitleNumbered, CueChapters,
	CueHookQuote, CueCutAtDrop, CueFewCuts, CueManyCuts, CueMontage, CueLongTake, CueHandheld, CueFirstPerson, CueVersus, CueNumbersRise,
	CueKeyword, CueKeywordHook,
}

// Line is a line of on-screen text at a time.
type Line struct {
	AtS  float64
	Text string
}

// Reference is what detection reads of one finished video. The style profile fills it from its measures.
type Reference struct {
	// TextRead says the on-screen text was read (without it, only the cut and camera cues can hold).
	TextRead bool
	// Pattern is the text pattern the style read classified (single_title, story_captions, dialogue, none).
	Pattern string
	// Title is the hook's title (the creator's words), "" without one; Lines the text of the shots in time order; Chapters the numbered
	// chapter titles.
	Title    string
	Lines    []Line
	Chapters []string
	// The cuts, the shot lengths (s) and the duration.
	DurationS float64
	CutsS     []float64
	ShotsS    []float64
	// DropS is the music's drop (0 without one); LiftS the end of the quiet intro (0 without one).
	DropS, LiftS float64
	Handheld     bool
}

// Tag is a format a video was found to be in, and how sure.
type Tag struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

// MinTagScore is the least score of a tag.
const MinTagScore = 0.5

// Detect returns the formats of a video, the most likely first (a tie goes to the rule with more cues, then to the id). Devices are
// never tagged.
func (l *Library) Detect(ref Reference) []Tag {
	base := baseCues(ref)
	var out []Tag
	type ranked struct {
		Tag
		cues int
	}
	var all []ranked
	for _, c := range l.cards {
		if c.Kind != KindFormat {
			continue
		}
		cues := cloneSet(base)
		if kw := keywordCues(ref, c.Detect.Keywords); kw != nil {
			for k := range kw {
				cues[k] = true
			}
		}
		best, n := 0.0, 0
		for _, r := range c.Detect.Rules {
			if !holds(r, cues) {
				continue
			}
			if r.Score > best || (r.Score == best && len(r.All) > n) {
				best, n = r.Score, len(r.All)
			}
		}
		if best >= MinTagScore {
			all = append(all, ranked{Tag{c.ID, best}, n})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		if all[i].cues != all[j].cues {
			return all[i].cues > all[j].cues
		}
		return all[i].ID < all[j].ID
	})
	for _, r := range all {
		out = append(out, r.Tag)
	}
	return out
}

func holds(r Rule, cues map[string]bool) bool {
	for _, c := range r.All {
		if !cues[c] {
			return false
		}
	}
	for _, c := range r.None {
		if cues[c] {
			return false
		}
	}
	return true
}

func cloneSet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// CuesOf returns the cues that hold for a reference, without the keyword cues (they depend on a card): for a report and the tests.
func CuesOf(ref Reference) []string {
	var out []string
	for k, v := range baseCues(ref) {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func baseCues(ref Reference) map[string]bool {
	c := map[string]bool{}
	if ref.TextRead {
		switch ref.Pattern {
		case TextSingleTitle:
			c[CuePatternSingleTitle] = true
		case TextStoryCaptions:
			c[CuePatternStoryCaptions] = true
		case TextDialogue:
			c[CuePatternDialogue] = true
		case TextNone:
			c[CuePatternNone] = true
		}
	}
	title := fold(ref.Title)
	c[CueTitleSeries] = seriesTitle.MatchString(title)
	if m := countTitle.FindStringSubmatch(title); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 2 && n <= 20 {
			c[CueTitleNumbered] = true
		}
	}
	chapters := 0
	seen := map[string]bool{}
	for _, ch := range ref.Chapters {
		if k := fold(ch); chapterLine.MatchString(k) && !seen[k] {
			seen[k] = true
			chapters++
		}
	}
	for _, l := range ref.Lines {
		if k := fold(l.Text); chapterLine.MatchString(k) && !seen[k] {
			seen[k] = true
			chapters++
		}
	}
	c[CueChapters] = chapters >= 2
	c[CueHookQuote] = hookQuote(ref, c[CueTitleSeries], c[CueTitleNumbered])
	cuts := len(ref.CutsS)
	c[CueFewCuts] = cuts <= 3
	c[CueManyCuts] = cuts >= 5
	if cuts >= 6 {
		s := slices.Clone(ref.ShotsS)
		slices.Sort(s)
		c[CueMontage] = len(s) > 0 && s[len(s)/2] <= 2
	}
	longest := 0.0
	for _, s := range ref.ShotsS {
		longest = math.Max(longest, s)
	}
	c[CueLongTake] = cuts == 0 || (longest >= 8 && cuts <= 5)
	c[CueHandheld] = ref.Handheld
	if len(ref.CutsS) > 0 {
		first := ref.CutsS[0]
		for _, at := range []float64{ref.DropS, ref.LiftS} {
			if at > 0 && math.Abs(first-at) <= 0.7 {
				c[CueCutAtDrop] = true
			}
		}
	}
	lines := textLines(ref)
	fp := 0
	for _, l := range lines {
		if firstPerson.MatchString(fold(l)) {
			fp++
		}
	}
	// A story told in the first person: half its lines, or its opening line (the lines after it may name the exercises).
	c[CueFirstPerson] = len(lines) >= 2 && (2*fp >= len(lines) || firstPerson.MatchString(fold(hookLine(ref))))
	for _, l := range lines {
		if versus(fold(l)) {
			c[CueVersus] = true
		}
	}
	// Two values set against each other over two shots ("<a> vs" on one, "<b>" on the next): the lines read in order, as one.
	if !c[CueVersus] && len(lines) >= 2 {
		var joined []string
		for _, l := range lines {
			joined = append(joined, fold(l))
		}
		c[CueVersus] = unitPair.MatchString(strings.Join(joined, " ")) && weightOrTime.MatchString(strings.Join(joined, " "))
	}
	c[CueNumbersRise] = numbersRise(ref)
	return c
}

var (
	// seriesTitle: words, then a number of two to four digits at the end ("... 101").
	seriesTitle = regexp.MustCompile(`^\D*[\p{L}]\D*\s(\d{2,4})$`)
	countTitle  = regexp.MustCompile(`^(\d{1,2})\s+\p{L}`)
	chapterLine = regexp.MustCompile(`^\d{1,2}\s*[.):]\s*\S`)
	// firstPerson: the first person in English, Dutch and German (the languages of the first creators; more are a word list away).
	firstPerson  = regexp.MustCompile(`(^|[\s"'])(i|i'm|im|i've|ive|my|me|mine|myself|we|our|ik|mijn|mij|wij|ons|onze|ich|mein|meine|mich|mir|wir|unser)([\s"'.,!?]|$)`)
	weightOrTime = regexp.MustCompile(`\d\s*(kg|kgs|lb|lbs|%|cm|y|yrs|years|months|weeks|days)\b`)
	numberRe     = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
	unitPair     = regexp.MustCompile(`\d+(?:[.,]\d+)?\s*(kg|kgs|lb|lbs|%|cm|y|yrs|years|months|weeks|days)?\s*(vs|versus|->|→|to)\s*\d+(?:[.,]\d+)?\s*(kg|kgs|lb|lbs|%|cm|y|yrs|years|months|weeks|days)?`)
	yearPair     = regexp.MustCompile(`(19|20)\d\d\D+(19|20)\d\d`)
	dayOne       = regexp.MustCompile(`(^|\s)(day|week|month|year)\s+(1|one)(\s|$)`)
)

// fold is the text as it is matched: NFKC (so full-width letters are letters), lowercase, quotes and spaces made plain, trimmed.
func fold(s string) string {
	s = norm.NFKC.String(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '‘', '’', '‛', '`', '´':
			return '\''
		case '“', '”', '„', '«', '»':
			return '"'
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// textLines are the title and the lines, each once.
func textLines(ref Reference) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if k := fold(s); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	add(ref.Title)
	for _, l := range ref.Lines {
		add(l.Text)
	}
	return out
}

// hookLine is the title, else the first line of a shot.
func hookLine(ref Reference) string {
	if strings.TrimSpace(ref.Title) != "" {
		return ref.Title
	}
	for _, l := range ref.Lines {
		if strings.TrimSpace(l.Text) != "" {
			return l.Text
		}
	}
	return ""
}

func hookQuote(ref Reference, series, numbered bool) bool {
	if series || numbered {
		return false
	}
	h := fold(hookLine(ref))
	if h == "" || chapterLine.MatchString(h) {
		return false
	}
	if strings.HasPrefix(h, "\"") || strings.HasPrefix(h, "'") {
		return true
	}
	// An unquoted line is a quote when it is a thought in the first person ("I ..."), not a witty title ("how to ...").
	return len(strings.Fields(h)) >= 5 && firstPerson.MatchString(h)
}

func versus(l string) bool {
	if unitPair.MatchString(l) || yearPair.MatchString(l) || dayOne.MatchString(l) {
		return true
	}
	words := " " + wordsOnly(l) + " "
	return (strings.Contains(words, " before ") && strings.Contains(words, " after ")) ||
		(strings.Contains(words, " then ") && strings.Contains(words, " now "))
}

// numbersRise: three lines or more that hold a number, the numbers going up.
func numbersRise(ref Reference) bool {
	var nums []float64
	for _, l := range ref.Lines {
		m := numberRe.FindString(l.Text)
		if m == "" || chapterLine.MatchString(fold(l.Text)) {
			continue
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", "."), 64)
		if err != nil {
			continue
		}
		if len(nums) == 0 || v != nums[len(nums)-1] {
			nums = append(nums, v)
		}
	}
	if len(nums) < 3 {
		return false
	}
	up := 0
	for i := 1; i < len(nums); i++ {
		if nums[i] > nums[i-1] {
			up++
		}
	}
	return up >= len(nums)-1 && up >= 2
}

// wordsOnly is the text with everything but letters, digits and apostrophes made a space.
func wordsOnly(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	}), " ")
}

// hasKeyword says whether a text holds a keyword as whole words.
func hasKeyword(text string, keywords []string) bool {
	padded := " " + wordsOnly(fold(text)) + " "
	for _, k := range keywords {
		if strings.Contains(padded, " "+wordsOnly(k)+" ") {
			return true
		}
	}
	return false
}

// keywordCues are the keyword cues of a card's keywords for a reference, nil when the card has none.
func keywordCues(ref Reference, keywords []string) map[string]bool {
	if len(keywords) == 0 {
		return nil
	}
	out := map[string]bool{}
	if hasKeyword(hookLine(ref), keywords) {
		out[CueKeywordHook] = true
		out[CueKeyword] = true
	}
	for _, l := range textLines(ref) {
		if hasKeyword(l, keywords) {
			out[CueKeyword] = true
		}
	}
	for _, ch := range ref.Chapters {
		if hasKeyword(ch, keywords) {
			out[CueKeyword] = true
		}
	}
	return out
}

// ---- the formats of a creator --------------------------------------------------------------------------------------------

// Share is how much of a creator's videos are in one format: the videos whose most likely format it is, and their share of all the
// videos.
type Share struct {
	ID     string  `json:"id"`
	Videos int     `json:"videos"`
	Share  float64 `json:"share"`
}

// Shares counts the most likely format of each video (videos with no tag count in the total only), the most used first.
func Shares(tags [][]Tag) []Share {
	n := len(tags)
	if n == 0 {
		return nil
	}
	count := map[string]int{}
	for _, t := range tags {
		if len(t) > 0 {
			count[t[0].ID]++
		}
	}
	var out []Share
	for id, v := range count {
		out = append(out, Share{ID: id, Videos: v, Share: math.Round(float64(v)/float64(n)*100) / 100})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Videos != out[j].Videos {
			return out[i].Videos > out[j].Videos
		}
		return out[i].ID < out[j].ID
	})
	return out
}
