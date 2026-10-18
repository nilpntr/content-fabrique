package styleprofile

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Title templates (P6-05c): the form a creator's titles take, as a template with the words that change replaced by slots ("<TOPIC> <N>",
// "<N> <WORD> <WORD> <GOAL>" with the creator's fixed words in place of the <WORD>s), learnt from the titles the vision read gave. The
// words a template keeps are the ones that come back in several of the creator's titles; everything else is a slot, so a template never
// carries one video's topic. The editor gets the template to fill and the creator's titles only as examples not to reuse.

// Slots of a template.
const (
	SlotNumber = "<N>"
	SlotTopic  = "<TOPIC>"
	SlotGoal   = "<GOAL>"
	SlotMore   = "<MORE>"
)

// TitleTemplate is one form of the creator's titles: the template, the share of the titles that have it, and their typical length.
type TitleTemplate struct {
	Template string  `json:"template"`
	Share    float64 `json:"share"`
	// Chars is the median length in characters of the titles of this form, Lines the median number of lines (0: not read).
	Chars int `json:"chars,omitempty"`
	Lines int `json:"lines,omitempty"`
}

// MaxTemplateChars bounds a template, the creator's own one included.
const MaxTemplateChars = 80

// slotRe is a slot as a template may hold it: a short word in capitals between angle brackets.
var slotRe = regexp.MustCompile(`<[A-Z]{1,12}>`)

var numberToken = regexp.MustCompile(`^\d{1,4}[.):]?$`)

// normToken is a token as it is compared between titles: in capitals, without the punctuation around it.
func normToken(t string) string {
	return strings.ToUpper(strings.TrimFunc(t, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
}

// titleShape is a title read, for the templates.
type titleShape struct {
	text  string
	lines int
}

// templatesOf learns the templates of a set of titles: a word that comes back in two titles or more is kept, a number at the start is
// always a slot (it counts items), another number is kept only when it comes back, and every run of other words is a slot. The most
// common template comes first.
func templatesOf(titles []titleShape) []TitleTemplate {
	var toks [][]string
	seen := map[string]int{}
	var kept []titleShape
	for _, t := range titles {
		f := strings.Fields(t.text)
		if len(f) == 0 {
			continue
		}
		toks = append(toks, f)
		kept = append(kept, t)
		uniq := map[string]bool{}
		for _, w := range f {
			if n := normToken(w); n != "" {
				uniq[n] = true
			}
		}
		for w := range uniq {
			seen[w]++
		}
	}
	if len(toks) == 0 {
		return nil
	}
	type acc struct {
		n            int
		chars, lines []float64
	}
	by := map[string]*acc{}
	var order []string
	for i, f := range toks {
		var out []string
		slots := 0
		lastSlot := false
		for j, w := range f {
			n := normToken(w)
			switch {
			case numberToken.MatchString(w) && (j == 0 || seen[n] < 2):
				suffix := strings.TrimLeftFunc(w, unicode.IsDigit)
				out = append(out, SlotNumber+suffix)
				lastSlot = false
			case n != "" && seen[n] >= 2:
				out = append(out, w)
				lastSlot = false
			default:
				if !lastSlot {
					out = append(out, []string{SlotTopic, SlotGoal, SlotMore}[min(slots, 2)])
					slots++
				}
				lastSlot = true
			}
		}
		tpl := strings.Join(out, " ")
		if utf8.RuneCountInString(tpl) > MaxTemplateChars {
			continue
		}
		a := by[tpl]
		if a == nil {
			a = &acc{}
			by[tpl], order = a, append(order, tpl)
		}
		a.n++
		a.chars = append(a.chars, float64(utf8.RuneCountInString(strings.Join(f, " "))))
		if kept[i].lines > 0 {
			a.lines = append(a.lines, float64(kept[i].lines))
		}
	}
	out := make([]TitleTemplate, 0, len(order))
	for _, tpl := range order {
		a := by[tpl]
		t := TitleTemplate{Template: tpl, Share: round2(float64(a.n) / float64(len(toks))), Chars: int(RangeOf(a.chars).Median + 0.5)}
		if len(a.lines) > 0 {
			t.Lines = int(RangeOf(a.lines).Median + 0.5)
		}
		out = append(out, t)
	}
	slices.SortStableFunc(out, func(a, b TitleTemplate) int {
		switch {
		case a.Share > b.Share:
			return -1
		case a.Share < b.Share:
			return 1
		}
		return 0
	})
	return out
}

// TemplateMatches says whether a title fits a template: the template's fixed words in order (case does not matter), a slot standing for
// one word or more (a number slot for a number).
func TemplateMatches(template, title string) bool {
	re, err := templateRegexp(template)
	if err != nil {
		return false
	}
	return re.MatchString(strings.Join(strings.Fields(title), " "))
}

func templateRegexp(template string) (*regexp.Regexp, error) {
	var parts []string
	for _, tok := range strings.Fields(template) {
		switch {
		case strings.HasPrefix(tok, SlotNumber):
			parts = append(parts, `\d+`+regexp.QuoteMeta(strings.TrimPrefix(tok, SlotNumber)))
		case tok == SlotTopic || tok == SlotGoal || tok == SlotMore || (strings.HasPrefix(tok, "<") && strings.HasSuffix(tok, ">")):
			parts = append(parts, `\S+(?: \S+)*?`)
		default:
			parts = append(parts, regexp.QuoteMeta(tok))
		}
	}
	return regexp.Compile(`(?i)^` + strings.Join(parts, " ") + `$`)
}

// templateCharset is what a template the creator types may hold: letters, digits, spaces, the slots' brackets and common punctuation.
var templateCharset = regexp.MustCompile(`^[\p{L}\p{N} <>.,:;!?'&()/+\-#%]+$`)

// ValidTemplate says whether a template the creator typed can be used: some text, at most MaxTemplateChars, plain characters, and its
// slots well formed.
func ValidTemplate(t string) bool {
	t = strings.TrimSpace(t)
	if t == "" || utf8.RuneCountInString(t) > MaxTemplateChars || !templateCharset.MatchString(t) {
		return false
	}
	if strings.Count(t, "<") != strings.Count(t, ">") || strings.Count(t, "<") != len(slotRe.FindAllString(t, -1)) {
		return false
	}
	_, err := templateRegexp(t)
	return err == nil
}
