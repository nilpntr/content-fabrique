package editor

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/formats"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// format_followed (docs/design/formats-v1.md): the edit has the beats of the format its plan chose. It is structural and creator-agnostic:
// the hook carries what the format's text needs (a title by a second, a first caption, a quoted line), the text pattern is there (a
// count title and numbered chapter cards, a line on most shots, two lines of dialogue before a bare reveal, or no text), the payoff lands
// where the format says (on the song's drop for a reveal), a loop ends on its opening clip, and the edit has the shots the format needs.
// The title's form against the format's forms is advice ("borderline"), like the creator's own title form in style_match. The creator's
// own choice of text pattern (their setting) wins over the format's: the check then leaves the text to the style's checks.

// CheckFormatFollowed is the check's id.
const CheckFormatFollowed = "format_followed"

// hookTextByMs is when the hook's text must be on screen at the latest.
const hookTextByMs = 1000

// timedTexts are the edit's texts and graphics (screenTexts) in time order.
func (c *checker) timedTexts() []screenText {
	out := screenTexts(c.in.Spec)
	slices.SortStableFunc(out, func(a, b screenText) int { return cmp.Compare(a.AtMs, b.AtMs) })
	return out
}

// formatPattern maps a card's text pattern to the style's patterns (a count title with chapter cards is told as one title).
func formatPattern(p string) string {
	switch p {
	case formats.TextChapterCards:
		return styleprofile.PatternSingleTitle
	case formats.TextSingleTitle, formats.TextStoryCaptions, formats.TextDialogue, formats.TextNone:
		return p
	}
	return ""
}

// formatSettings are the settings the text checks follow for a plan in a format: the format's text pattern replaces a measured,
// suggested or default one, never the creator's own choice. Nil settings stay nil unless the format names a pattern that tells the
// video in lines.
func formatSettings(plan *Plan, set *styleprofile.Resolved) *styleprofile.Resolved {
	if plan == nil || plan.Format == nil {
		return set
	}
	card, ok := formats.Default().Card(plan.Format.ID)
	if !ok {
		return set
	}
	want := formatPattern(card.Text.Pattern)
	if want == "" {
		return set
	}
	if set == nil {
		if want != styleprofile.PatternStoryCaptions && want != styleprofile.PatternDialogue {
			return nil
		}
		r := styleprofile.Resolve(nil, nil, styleprofile.Settings{}, "")
		set = &r
	}
	if set.Sources.TextPattern == styleprofile.SourceYou || set.TextPattern == want {
		return set
	}
	out := *set
	out.TextPattern = want
	out.Sources.TextPattern = styleprofile.SourceDefault
	return &out
}

func (c *checker) formatFollowed() Check {
	p := c.in.Plan
	if p == nil || p.Format == nil {
		return c.skip(CheckFormatFollowed, "the plan names no format")
	}
	card, ok := formats.Default().Card(p.Format.ID)
	if !ok {
		return c.skip(CheckFormatFollowed, "the format %s is not in this build's library", p.Format.ID)
	}
	var have, miss, advice []string
	if n := len(c.items); n < card.Check.MinShots {
		miss = append(miss, fmt.Sprintf("the format needs at least %d shots and the edit has %d", card.Check.MinShots, n))
	}
	// The text: the format's pattern, unless the creator chose their own (it then wins, and the style's checks hold the text).
	texts := c.timedTexts()
	pattern := card.Text.Pattern
	if s := c.in.Settings; s != nil && s.Sources.TextPattern == styleprofile.SourceYou && formatPattern(pattern) != "" && s.TextPattern != formatPattern(pattern) {
		have = append(have, fmt.Sprintf("the text follows the creator's own choice (%s) over the format's", strings.ReplaceAll(s.TextPattern, "_", " ")))
		pattern = formats.TextAny
	}
	if pattern == formats.TextChapterCards && c.in.Settings != nil && c.in.Settings.ChapterTitles == styleprofile.LevelOff {
		pattern = formats.TextSingleTitle
	}
	payoffAt := c.payoffCut(card)
	h, m, a := c.formatText(card, pattern, texts, payoffAt)
	have, miss, advice = append(have, h...), append(miss, m...), append(advice, a...)
	// The payoff.
	switch card.Check.Payoff {
	case formats.PayoffDrop:
		mu := c.in.Music
		switch {
		case mu != nil && mu.HasDrop && len(c.items) >= 2:
			near := int64(-1)
			for _, it := range c.items[1:] {
				if d := absI(it.AtMs - mu.DropMs); near < 0 || d < near {
					near = d
				}
			}
			if near <= offBeatToleranceMs {
				have = append(have, fmt.Sprintf("the reveal cut on the drop at %s", secs(mu.DropMs)))
			} else {
				miss = append(miss, fmt.Sprintf("no cut on the song's drop at %s (the nearest is %d ms off): the reveal of this format lands on the drop", secs(mu.DropMs), near))
			}
		case c.planHas("reveal", "payoff", "peak"):
			have = append(have, "a reveal in the plan (no song drop to land it on)")
		default:
			miss = append(miss, "no reveal: the plan has no shot with the role reveal or payoff")
		}
	default:
		payoff, _ := card.Beat("payoff")
		if c.planHas(payoff.Roles...) || len(c.items) == 1 {
			have = append(have, "a payoff")
		} else {
			miss = append(miss, fmt.Sprintf("no payoff: the plan has no shot with the role %s", strings.Join(payoff.Roles, " or ")))
		}
	}
	// A loop: the card's own, or the device mixed in.
	loop := card.Check.Loop
	if mix, ok := formats.Default().Card(p.Format.Mix); ok && mix.Check.Loop {
		loop = true
	}
	if loop && len(c.items) >= 2 {
		first, last := c.items[0], c.items[len(c.items)-1]
		if first.AssetID == last.AssetID {
			have = append(have, "a loop (it ends on the opening clip)")
		} else {
			miss = append(miss, fmt.Sprintf("no loop: the last shot (%s) is not from the opening clip; end on the clip the video opens on, at the same phase", last.ID))
		}
	}
	name := fmt.Sprintf("%s (%s)", card.Name, card.ID)
	if len(miss) > 0 {
		return c.fail(CheckFormatFollowed, "%s: %s. Build the format's beats (plan_edit's answer lists them)", name, strings.Join(miss, "; "))
	}
	if len(advice) > 0 {
		return c.pass(CheckFormatFollowed, "borderline, not a failure: %s: %s; %s", name, strings.Join(have, ", "), strings.Join(advice, "; "))
	}
	return c.pass(CheckFormatFollowed, "%s: %s", name, strings.Join(have, ", "))
}

// planHas says the plan has a shot of one of the roles.
func (c *checker) planHas(roles ...string) bool {
	return c.in.Plan != nil && slices.ContainsFunc(c.in.Plan.Shots, func(s PlanShot) bool { return slices.Contains(roles, s.Role) })
}

// payoffCut is when the payoff starts: the cut on the song's drop for a reveal on the drop, else the last cut (0 with one shot).
func (c *checker) payoffCut(card formats.Card) int64 {
	if card.Check.Payoff == formats.PayoffDrop && c.in.Music != nil && c.in.Music.HasDrop {
		return c.in.Music.DropMs
	}
	if len(c.items) >= 2 {
		return c.items[len(c.items)-1].AtMs
	}
	return 0
}

// formatText checks the text pattern of a format.
func (c *checker) formatText(card formats.Card, pattern string, texts []screenText, payoffAt int64) (have, miss, advice []string) {
	var titles, chapters []screenText
	for _, t := range texts {
		if t.IsChapter() {
			chapters = append(chapters, t)
		} else {
			titles = append(titles, t)
		}
	}
	hookTitle := func() (screenText, bool) {
		for _, t := range titles {
			if t.AtMs <= hookTextByMs {
				return t, true
			}
		}
		return screenText{}, false
	}
	formAdvice := func(t screenText) {
		if len(card.Text.TitleForms) == 0 || MatchesAnyForm(t.Text, card.Text.TitleForms) {
			return
		}
		if s := c.in.Settings; s != nil && s.TitleTemplate != "" {
			return // the creator's own title form
		}
		var forms []string
		for _, f := range card.Text.TitleForms {
			forms = append(forms, fmt.Sprintf("%q", f))
		}
		advice = append(advice, fmt.Sprintf("the hook title is in none of the format's forms (%s)", strings.Join(forms, ", ")))
	}
	switch pattern {
	case formats.TextSingleTitle:
		if t, ok := hookTitle(); ok {
			have = append(have, "a hook title")
			formAdvice(t)
		} else {
			miss = append(miss, fmt.Sprintf("no hook title by %s: the format opens on its title", secs(hookTextByMs)))
		}
	case formats.TextChapterCards:
		if t, ok := hookTitle(); ok {
			have = append(have, "a count title")
			formAdvice(t)
		} else {
			miss = append(miss, fmt.Sprintf("no count title by %s (\"<N> ...\")", secs(hookTextByMs)))
		}
		if len(chapters) >= 2 {
			have = append(have, fmt.Sprintf("%d numbered chapter cards", len(chapters)))
		} else {
			miss = append(miss, fmt.Sprintf("%d numbered chapter cards: the format needs one per item, two or more (\"1. ...\" on each item's first shot)", len(chapters)))
		}
	case formats.TextStoryCaptions:
		need := max(2, (len(c.items)+1)/2)
		first := len(titles) > 0 && titles[0].AtMs <= hookTextByMs
		switch {
		case !first:
			miss = append(miss, fmt.Sprintf("no first caption by %s: the first line is the hook", secs(hookTextByMs)))
		case len(titles) < need:
			miss = append(miss, fmt.Sprintf("%d caption lines over %d shots: the story needs a line on most shots (%d or more)", len(titles), len(c.items), need))
		default:
			have = append(have, fmt.Sprintf("%d story lines", len(titles)))
		}
	case formats.TextDialogue:
		before, on := 0, 0
		for _, t := range titles {
			if t.AtMs < payoffAt || payoffAt == 0 {
				before++
			} else if t.AtMs < payoffAt+1500 {
				on++
			}
		}
		switch {
		case before < 2:
			miss = append(miss, fmt.Sprintf("%d dialogue lines before the twist: the format needs two or more, one per shot", before))
		case on > 0:
			miss = append(miss, "a line on the twist: the twist plays with no text")
		default:
			have = append(have, fmt.Sprintf("%d dialogue lines, then a bare twist", before))
		}
	case formats.TextNone:
		if len(texts) > 0 {
			miss = append(miss, fmt.Sprintf("%d texts: the format has none", len(texts)))
		} else {
			have = append(have, "no text")
		}
	}
	return have, miss, advice
}

// MatchesAnyForm says whether a title (a text or a graphic's words) fits one of a format's title forms.
func MatchesAnyForm(title string, forms []string) bool { return formats.MatchesTitleForm(title, forms) }
