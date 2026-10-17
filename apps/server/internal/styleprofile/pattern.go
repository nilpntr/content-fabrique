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
	if m := seriesRe.FindStringSubmatch(t); m != nil && utf8.RuneCountInString(m[1]) >= 2 {
		return KindSeries, m[2]
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

// PatternsOf groups the titles by kind, most common first.
func PatternsOf(titles []string) []TitlePattern { return patternsOf(titles) }

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
		return "a series title, <TOPIC> <number> in capitals"
	case KindLowercase:
		return "a lowercase witty line"
	case KindQuestion:
		return "a question"
	case KindCaps:
		return "a short label in capitals"
	}
	return "another form"
}

// patternLine is the patterns as one line for the creator_style block (the examples are the creator's words: the block is untrusted data).
func patternLine(ps []TitlePattern) string {
	if len(ps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		s := fmt.Sprintf("%s (%.0f %% of the titles: %s", kindWords(p.Kind), p.Share*100, "'"+strings.Join(p.Examples, "', '")+"'")
		if p.Number != "" {
			s += "; always ends in " + p.Number
		}
		parts = append(parts, s+")")
	}
	return "title templates: " + strings.Join(parts, "; ") + ". Prefer the most common template, with a topic taken from the footage (the muscle or the exercise)."
}

// SeriesNumber is the number the creator's series titles end in ("101"), or "" when they have no series or no single number.
func (t *TextProfile) SeriesNumber() string {
	if t == nil {
		return ""
	}
	for _, p := range t.Patterns {
		if p.Kind == KindSeries {
			return p.Number
		}
	}
	return ""
}

// PatternMinShare is the share of the creator's titles a form needs to count as theirs.
const PatternMinShare = 0.3

// Follows says whether a title of this kind is one of the creator's forms (a share of at least PatternMinShare). A profile with no
// patterns follows nothing and judges nothing.
func (t *TextProfile) Follows(kind string) bool {
	for _, p := range t.Patterns {
		if p.Kind == kind && p.Share >= PatternMinShare {
			return true
		}
	}
	return false
}
