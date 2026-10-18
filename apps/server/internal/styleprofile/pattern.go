package styleprofile

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Title templates: a creator's titles follow a few forms ("<MUSCLE OR EXERCISE> 101" in capitals, a lowercase witty line), and a title
// that follows one reads as theirs. The forms are found from the titles the vision read gave (untrusted text, bounded by the read's
// validator), classified by shape, never by meaning.

// The kinds of title.
const (
	// KindSeries is "<TOPIC> <number>": a series title such as "BUILDING LATS 101".
	KindSeries = "series"
	// KindLowercase is a lowercase line of three words or more: a witty sentence.
	KindLowercase = "lowercase_line"
	// KindQuestion is a title that ends in a question mark.
	KindQuestion = "question"
	// KindCaps is a title in capitals that is not a series: a label such as "BACK DAY".
	KindCaps = "caps_label"
	// KindOther is any other.
	KindOther = "other"
)

var seriesRe = regexp.MustCompile(`^(.*\S)\s+(\d{1,3})$`)

// TitleKind classifies a title by its shape. For a series it also returns the number it ends in ("101").
func TitleKind(text string) (kind, number string) {
	t := strings.Join(strings.Fields(text), " ")
	if t == "" {
		return KindOther, ""
	}
	if m := seriesRe.FindStringSubmatch(t); m != nil && utf8.RuneCountInString(m[1]) >= 2 && CaseOf(m[1]) == "upper" {
		return KindSeries, m[2] // "<TOPIC> <number>" in capitals; "week 2" or "I trained legs for 30" are not a series
	}
	if strings.HasSuffix(t, "?") {
		return KindQuestion, ""
	}
	switch CaseOf(t) {
	case "lower":
		if len(strings.Fields(t)) >= 3 {
			return KindLowercase, ""
		}
	case "upper":
		return KindCaps, ""
	}
	return KindOther, ""
}

// TitlePattern is one form of title the creator uses: how many of the titled references have it and a couple of examples (the creator's
// own words, untrusted).
type TitlePattern struct {
	Kind     string   `json:"kind"`
	Share    float64  `json:"share"`
	Examples []string `json:"examples,omitempty"`
	// Number is, for a series, the number the series ends in when all its titles share one ("101").
	Number string `json:"number,omitempty"`
}

// MaxPatternExamples is how many examples a pattern keeps.
const MaxPatternExamples = 2

// patternsOf groups the titles by kind, most common first.
func patternsOf(titles []string) []TitlePattern {
	type acc struct {
		n        int
		examples []string
		numbers  map[string]bool
	}
	by := map[string]*acc{}
	var order []string
	total := 0
	for _, t := range titles {
		if strings.TrimSpace(t) == "" {
			continue
		}
		total++
		k, num := TitleKind(t)
		a := by[k]
		if a == nil {
			a = &acc{numbers: map[string]bool{}}
			by[k], order = a, append(order, k)
		}
		a.n++
		if len(a.examples) < MaxPatternExamples {
			a.examples = append(a.examples, t)
		}
		if num != "" {
			a.numbers[num] = true
		}
	}
	if total == 0 {
		return nil
	}
	var out []TitlePattern
	for _, k := range order {
		a := by[k]
		p := TitlePattern{Kind: k, Share: round2(float64(a.n) / float64(total)), Examples: a.examples}
		if len(a.numbers) == 1 {
			for n := range a.numbers {
				p.Number = n
			}
		}
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b TitlePattern) int {
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

// kindWords names a kind for the editor.
func kindWords(k string) string {
	switch k {
	case KindSeries:
		return "a series title"
	case KindLowercase:
		return "a witty line"
	case KindQuestion:
		return "a question"
	case KindCaps:
		return "a short label in capitals"
	}
	return "another form"
}

// Template is the form of the pattern as a template to fill, never a title to reuse: "<TOPIC> 101" with the length of the topic, "a
// lowercase line of 4 to 7 words", and so on. The lengths come from the creator's titles (counted, not quoted).
func (p TitlePattern) Template() string {
	lo, hi := 0, 0
	for _, e := range p.Examples {
		n := len(strings.Fields(e))
		if p.Kind == KindSeries {
			n-- // the number is not part of the topic
		}
		if lo == 0 || n < lo {
			lo = n
		}
		hi = max(hi, n)
	}
	words := func(what string) string {
		switch {
		case lo <= 0:
			return what
		case lo == hi:
			return fmt.Sprintf("%s of %d %s", what, lo, map[bool]string{true: "word", false: "words"}[lo == 1])
		}
		return fmt.Sprintf("%s of %d to %d words", what, lo, hi)
	}
	switch p.Kind {
	case KindSeries:
		num := p.Number
		if num == "" {
			num = "<number>"
		}
		return fmt.Sprintf(`"<TOPIC> %s" in capitals (%s: the muscle, the exercise or the session of this video)`, num, words("a topic"))
	case KindLowercase:
		return words("a lowercase line") + " about this video"
	case KindQuestion:
		return words("a question") + " about this video"
	case KindCaps:
		return words(`"<LABEL>" in capitals, a label`) + " for this video"
	}
	return "another form"
}

// patternLine is the patterns as one line for the creator_style block: the forms as templates to fill, with their shares, and never the
// creator's own titles (an editor shown a title copies it; check_edit's title_copied fails a copy).
func patternLine(ps []TitlePattern) string {
	if len(ps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, fmt.Sprintf("%s, %s (%.0f %% of the titles)", kindWords(p.Kind), p.Template(), p.Share*100))
	}
	return "title templates: " + strings.Join(parts, "; ") + ". Write a new title for this video in the most common template: fill it with the topic of this footage, and never reuse a title of the creator's own videos."
}

// normTitle is a title compared without case or spaces.
func normTitle(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

// ReferenceTitles are the titles read from the creator's references (untrusted text, for comparing only): every title of a profile made
// since the field was added, and the examples of an older one.
func (p *Profile) ReferenceTitles() []string {
	if p == nil || p.Text == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(list []string) {
		for _, t := range list {
			if k := normTitle(t); k != "" && !seen[k] {
				seen[k] = true
				out = append(out, t)
			}
		}
	}
	add(p.Text.Titles)
	add(p.Text.Examples)
	for _, pt := range p.Text.Patterns {
		add(pt.Examples)
	}
	return out
}

// CopiesReferenceTitle says the text is, without regard to case or spaces, the title of one of the creator's reference videos.
func (p *Profile) CopiesReferenceTitle(text string) bool {
	k := normTitle(text)
	if k == "" {
		return false
	}
	for _, t := range p.ReferenceTitles() {
		if normTitle(t) == k {
			return true
		}
	}
	return false
}

// PatternMinShare is the share of the creator's titles a form needs to count as theirs.
const PatternMinShare = 0.3

// Follows says whether a title of this kind is one of the creator's forms (a share of at least PatternMinShare). A profile with no
// patterns follows nothing and judges nothing.
func (t *TextProfile) Follows(kind string) bool {
	if t == nil {
		return false
	}
	for _, p := range t.Patterns {
		if p.Kind == kind && p.Share >= PatternMinShare {
			return true
		}
	}
	return false
}
