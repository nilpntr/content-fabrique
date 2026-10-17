package styleprofile

import (
	"strings"
	"testing"
)

func TestTitleKind(t *testing.T) {
	for text, want := range map[string]string{
		"BUILDING LATS 101":                KindSeries,
		"BRACHIORADIALIS BUILDER 101":      KindSeries,
		"  chest   day 201 ":               KindSeries,
		"how to bake a croissant tricep":   KindLowercase,
		"is this the best row?":            KindQuestion,
		"BACK DAY":                         KindCaps,
		"BACK DAY BLUEPRINT":               KindCaps,
		"101":                              KindOther,
		"Heavy Lat Day":                    KindOther,
		"two words":                        KindOther, // lowercase, but not a sentence
		"":                                 KindOther,
		"BUILDING LATS 101 AND MORE WORDS": KindCaps,
	} {
		if got, _ := TitleKind(text); got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
	if k, n := TitleKind("BUILDING LATS 101"); k != KindSeries || n != "101" {
		t.Errorf("series number %s %s", k, n)
	}
}

func TestTitlePatternsOfTheReferences(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	tx := p.Text
	if len(tx.Patterns) != 2 || tx.Patterns[0].Kind != KindSeries || tx.Patterns[0].Share < 0.6 || tx.Patterns[0].Number != "101" || len(tx.Patterns[0].Examples) != 2 {
		t.Fatalf("patterns %+v", tx.Patterns)
	}
	if tx.Patterns[1].Kind != KindLowercase || tx.SeriesNumber() != "101" {
		t.Errorf("patterns %+v", tx.Patterns)
	}
	if !tx.Follows(KindSeries) || !tx.Follows(KindLowercase) || tx.Follows(KindCaps) || tx.Follows(KindQuestion) {
		t.Error("Follows")
	}
	block := p.CompactBlock()
	for _, want := range []string{"title templates: a series title, <TOPIC> <number> in capitals (67 % of the titles", "'BUILDING LATS 101'", "always ends in 101", "a lowercase witty line", "Prefer the most common template"} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q:\n%s", want, block)
		}
	}
	// The creator's words are only in the block (untrusted data): not in the one-line fix.
	if fix := titlePatternFix(tx); strings.Contains(fix, "BUILDING") || !strings.Contains(fix, "ending in 101") {
		t.Errorf("fix %q", fix)
	}
	var none *TextProfile
	if none.SeriesNumber() != "" {
		t.Error("nil text profile")
	}
	if patternsOf(nil) != nil || patternsOf([]string{"  "}) != nil {
		t.Error("patterns of nothing")
	}
}

// The one-line length is between the longest title that fitted one line and the shortest that wrapped.
func TestOneLineChars(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	if got := p.Text.OneLineChars; got != 22 { // "BUILDING LATS 101" is 17 characters on one line, "BRACHIORADIALIS BUILDER 101" is 27 on two
		t.Errorf("one line chars %d", got)
	}
	r := p.TitleRecipe()
	if !r.OneLine || r.OneLineChars != 22 || !strings.Contains(r.Describe(), "up to 22 characters on one line") {
		t.Errorf("recipe %+v / %s", r, r.Describe())
	}
	if !strings.Contains(p.Describe(), "titles up to 22 characters on one line") {
		t.Errorf("describe lacks it:\n%s", p.Describe())
	}
	// Only one-line titles: a little more than the longest. Only wrapped ones: no preference.
	m := referenceMeasures(t)
	for i := range m {
		if m[i].Text != nil && m[i].Text.TitlePresent {
			m[i].Text.Lines = 1
		}
	}
	if got := Aggregate(m).Text.OneLineChars; got != 34 { // the longest title is 30 characters
		t.Errorf("all one line: %d", got)
	}
	for i := range m {
		if m[i].Text != nil && m[i].Text.TitlePresent {
			m[i].Text.Lines = 2
		}
	}
	if got := Aggregate(m).Text.OneLineChars; got != 0 {
		t.Errorf("all wrapped: %d", got)
	}
}

func TestTheLinesAndPatternDimensions(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	base := buildingLats()
	base.TitleFill, base.TitleLines, base.TitleHeightPct, base.TitleShadow = "#F2E9A0", 1, 4.0, "none"
	base.TitleChars, base.TitleKind = 17, KindSeries
	dim := func(d Distance, id string) (Dimension, bool) {
		for _, x := range d.Dimensions {
			if x.ID == id {
				return x, true
			}
		}
		return Dimension{}, false
	}
	// A short title on two lines: one line is what the creator does at this length; a longer one may wrap, as theirs do.
	two := base
	two.TitleLines, two.TitleChars = 2, 18
	d := p.Compare(two)
	if x, ok := dim(d, "title_lines"); !ok || x.OK || !strings.Contains(x.Fix, "shorten") {
		t.Errorf("two lines at 18 characters: %+v", x)
	}
	two.TitleBreak = true
	if x, _ := dim(p.Compare(two), "title_lines"); !strings.Contains(x.Fix, "line break") {
		t.Errorf("a break of its own: %q", x.Fix)
	}
	two.TitleBreak, two.TitleFitScale = false, 0.85
	if x, _ := dim(p.Compare(two), "title_lines"); !strings.Contains(x.Fix, "scale to 0.85") {
		t.Errorf("a scale that fits: %q", x.Fix)
	}
	long := base
	long.TitleLines, long.TitleChars = 2, 27
	if x, ok := dim(p.Compare(long), "title_lines"); ok {
		t.Errorf("a long title was held to one line: %+v", x)
	}

	// The pattern is soft: a label in capitals is borderline, and does not fail the edit.
	label := base
	label.TitleKind = KindCaps
	d = p.Compare(label)
	x, ok := dim(d, "title_pattern")
	if !ok || x.OK || !x.Soft || !strings.Contains(x.Fix, "ending in 101") {
		t.Fatalf("pattern %+v", x)
	}
	for _, f := range d.Failed() {
		if f.ID == "title_pattern" {
			t.Error("a soft dimension failed the edit")
		}
	}
	if b := d.Borderline(); len(b) != 1 || b[0].ID != "title_pattern" {
		t.Errorf("borderline %+v", b)
	}
	if x, _ := dim(p.Compare(base), "title_pattern"); !x.OK {
		t.Errorf("a series title: %+v", x)
	}
	if !strings.Contains(d.Text(), "adv  title_pattern") || strings.Contains(d.Text(), "OUT  title_pattern") {
		t.Errorf("text:\n%s", d.Text())
	}
	// Advice does not count in the share: the same edit with a title in the creator's form has the same score.
	if good := p.Compare(base); good.Score != d.Score {
		t.Errorf("score %v against %v", d.Score, good.Score)
	}
}

// When a title that wrapped is no longer than one that did not, the one-line length stays under the shortest that wrapped.
func TestOneLineCharsWhenTheLengthsOverlap(t *testing.T) {
	m := referenceMeasures(t)
	for i := range m {
		if m[i].Text == nil || !m[i].Text.TitlePresent {
			continue
		}
		switch m[i].Text.Text {
		case "BUILDING LATS 101":
			m[i].Text.Text, m[i].Text.Lines = "TWENTY CHARACTERS 20", 1
		case "BRACHIORADIALIS BUILDER 101":
			m[i].Text.Text, m[i].Text.Lines = "FOURTEEN CHARS", 2
		}
	}
	if got := Aggregate(m).Text.OneLineChars; got != 13 {
		t.Errorf("one line chars %d, want 13 (under the 14 that wrapped)", got)
	}
}

// A form the creator hardly uses is not a template to follow.
func TestARareTemplateIsNotFollowed(t *testing.T) {
	titles := []string{"BUILDING LATS 101", "how to bake a croissant tricep", "is this the best row?", "BACK DAY", "Heavy Lat Day"}
	ps := patternsOf(titles)
	if len(ps) != 5 || ps[0].Share != 0.2 {
		t.Fatalf("%+v", ps)
	}
	tp := &TextProfile{Patterns: ps}
	if tp.Follows(KindSeries) {
		t.Error("a form at 20 % is followed")
	}
	if patternKinds(tp) != "" {
		t.Errorf("kinds %q", patternKinds(tp))
	}
}
