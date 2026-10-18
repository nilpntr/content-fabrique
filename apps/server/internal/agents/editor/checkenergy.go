package editor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/camshift"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The energy checks (P6-06): an edit must not sit dead still, and at the lively and hype vibes its text enters and leaves. The founder's
// verdict on the edits before these checks: "static/stiff, not playful/vibey", "no visual effects on entering the text". Transitions are
// not energy: a creator's verdict on edits with punch and zoom_through transitions was "nobody uses those transition effects, only
// someone who throws it into an automatic edit", so an edit has the transitions of the creator's effective style and no more
// (transitions_match_style): none by default at every vibe, the kinds and the rate of their own videos when those use them, or what the
// creator set.

// The check ids.
const (
	CheckStaticShot          = "static_shot"
	CheckTransitionsStyle    = "transitions_match_style"
	CheckTransitionVariety   = "transition_variety"
	CheckTextWithoutEntrance = "text_without_entrance"
	CheckChapterTitles       = "chapter_titles"
)

// staticShotMaxMs is the longest a shot may sit with no camera motion at all: past it a tripod shot reads as dead.
const staticShotMaxMs = 2000

// lotsTransitionShare is the share of the cuts that are transitions, at least, when the style asks for lots; subtleTransitionShare the
// most when it asks for a few.
const (
	lotsTransitionShare   = 0.25
	subtleTransitionShare = 0.25
)

// measuredTransitionTypes are the transitions of the timeline that stand for each kind measured at the creator's cuts (styleprofile's
// transitions: a blend spread over frames, a flash, a zoom).
var measuredTransitionTypes = map[string][]string{
	"blend": {timeline.TransitionCrossfade, timeline.TransitionDipToBlack},
	"flash": {timeline.TransitionFlash},
	"zoom":  {timeline.TransitionZoomThrough, timeline.TransitionPunch},
}

// countGimmicks counts the effects a creator of no effects never uses whatever the vibe: speed ramps and shakes.
func countGimmicks(items []timeline.VideoItem) int {
	n := 0
	for _, it := range items {
		if it.HasRamp() || (it.Motion.Active() && it.Motion.Preset == timeline.MotionShake) {
			n++
		}
	}
	return n
}

// isTransition says whether a clip starts with a transition that is not a cut.
func isTransition(it timeline.VideoItem) bool {
	return it.TransitionIn != nil && it.TransitionIn.Type != "" && it.TransitionIn.Type != timeline.TransitionCut
}

// transitionKind folds the old name of whip_left into it, for the variety rule.
func transitionKind(t string) string {
	if t == timeline.TransitionWhip {
		return timeline.TransitionWhipLeft
	}
	return t
}

func (c *checker) staticShot() Check {
	vibe := VibeOr(c.in.Vibe)
	if vibe == VibeCalm && c.in.Style != nil && c.in.Style.Firm() && c.in.Style.Camera.Verdict == camshift.Static {
		return c.skip(CheckStaticShot, "the vibe is calm and the creator's own camera is static")
	}
	var dead []string
	for i, it := range c.items {
		if c.durs[i] <= staticShotMaxMs || it.Motion.Active() {
			continue
		}
		if a, ok := c.in.Assets[it.AssetID]; ok && a.Camera == camshift.Handheld {
			continue // the footage moves by itself
		}
		dead = append(dead, fmt.Sprintf("%s (%s)", it.ID, secs(c.durs[i])))
	}
	if len(dead) > 0 {
		return c.fail(CheckStaticShot, "%d shots longer than %s sit dead still: %s. Give them the living camera: set_motion with preset handheld (without an id for every clip), and a punch-in at_ms on the drop or a strong beat",
			len(dead), secs(staticShotMaxMs), strings.Join(dead[:min(4, len(dead))], ", "))
	}
	return c.pass(CheckStaticShot, "no shot longer than %s sits still", secs(staticShotMaxMs))
}

// transitionsMatch holds the edit to the transitions of the creator's effective style: none when it is off (the default of every vibe),
// at least one cut in four when it is lots, a few when it is subtle; and when the level was measured from the creator's videos, only
// the kinds those use, at about their rate.
func (c *checker) transitionsMatch() Check {
	r := styleprofile.Resolve(c.in.Style, nil, styleprofile.Settings{}, c.in.Vibe)
	if c.in.Settings != nil {
		r = *c.in.Settings
	}
	cuts := len(c.items) - 1
	var ids, kinds []string
	for _, it := range c.items[1:] {
		if isTransition(it) {
			ids = append(ids, it.ID)
			kinds = append(kinds, transitionKind(it.TransitionIn.Type))
		}
	}
	n := len(ids)
	from := sourceWords(r.Sources.Transitions)
	switch r.Transitions {
	case styleprofile.LevelOff:
		if n > 0 {
			return c.fail(CheckTransitionsStyle, "%d %s (%s) but this style is plain cuts on the beat (transitions off, %s): set_transition cut on each. Effect transitions read as an automatic AI edit to creators; the energy comes from the cuts on the beat, the pace, the camera and the text",
				n, plural(n, "transition", "transitions"), strings.Join(ids[:min(4, n)], ", "), from)
		}
		return c.pass(CheckTransitionsStyle, "plain cuts on the beat, as the style asks (transitions off, %s)", from)
	case styleprofile.LevelLots:
		if cuts >= 2 && float64(n) < lotsTransitionShare*float64(cuts) {
			return c.fail(CheckTransitionsStyle, "%d of %d cuts are transitions; the style asks for lots (%s): one on at least every fourth cut, on the strong beats and the drop", n, cuts, from)
		}
	default: // subtle
		if hi := max(1, int(subtleTransitionShare*float64(cuts)+0.5)); n > hi {
			return c.fail(CheckTransitionsStyle, "%d of %d cuts are transitions; the style asks for a few (%s): at most %d, on the strongest moments, plain cuts elsewhere", n, cuts, from, hi)
		}
	}
	if r.Sources.Transitions == styleprofile.SourceMeasured && c.in.Style != nil {
		tp := c.in.Style.Transitions
		var allowed []string
		for _, k := range tp.Kinds {
			allowed = append(allowed, measuredTransitionTypes[k]...)
		}
		for i, k := range kinds {
			if !slices.Contains(allowed, k) {
				return c.fail(CheckTransitionsStyle, "%s is a %s transition; the creator's videos use only %s at their cuts: use one of %s, or a plain cut",
					ids[i], k, strings.Join(tp.Kinds, " and "), strings.Join(allowed, ", "))
			}
		}
		if hi := int(tp.Share*float64(cuts)*1.5 + 1); n > hi {
			return c.fail(CheckTransitionsStyle, "%d of %d cuts are transitions; the creator's videos have one on %.0f %% of their cuts: at most %d here", n, cuts, tp.Share*100, hi)
		}
	}
	return c.pass(CheckTransitionsStyle, "%d of %d cuts are transitions (transitions %s, %s)", n, cuts, r.Transitions, from)
}

func (c *checker) transitionVariety() Check {
	var kinds []string
	for _, it := range c.items[1:] {
		if isTransition(it) {
			kinds = append(kinds, transitionKind(it.TransitionIn.Type))
		}
	}
	if len(kinds) < 3 {
		return c.skip(CheckTransitionVariety, "fewer than three transitions")
	}
	for i := 2; i < len(kinds); i++ {
		if kinds[i] == kinds[i-1] && kinds[i] == kinds[i-2] {
			return c.fail(CheckTransitionVariety, "three %s transitions in a row: vary them, or make one a plain cut", kinds[i])
		}
	}
	return c.pass(CheckTransitionVariety, "%d transitions, never the same three in a row", len(kinds))
}

func (c *checker) textWithoutEntrance() Check {
	if !energetic(c.in.Vibe) {
		return c.skip(CheckTextWithoutEntrance, "the vibe is calm")
	}
	var bare []string
	texts := 0
	for _, t := range c.in.Spec.Tracks {
		tt, ok := t.(*timeline.TextTrack)
		if !ok {
			continue
		}
		for _, it := range tt.Items {
			texts++
			var miss []string
			if it.AnimationOrDefault() == timeline.AnimationNone {
				miss = append(miss, "entrance")
			}
			if it.Exit == "" || it.Exit == "none" {
				miss = append(miss, "exit")
			}
			if len(miss) > 0 {
				bare = append(bare, fmt.Sprintf("%s has no %s", it.ID, strings.Join(miss, " and no ")))
			}
		}
	}
	switch {
	case texts == 0:
		return c.skip(CheckTextWithoutEntrance, "there is no text")
	case len(bare) > 0:
		return c.fail(CheckTextWithoutEntrance, "%s: give every text an entrance (animation pop, slide_up or fade) and an exit (exit pop_out, slide_down or fade) with update_text", strings.Join(bare[:min(3, len(bare))], "; "))
	}
	return c.pass(CheckTextWithoutEntrance, "all %d texts enter and leave", texts)
}

func (c *checker) chapterTitles() Check {
	if s := c.in.Settings; s != nil && s.ChapterTitles == styleprofile.LevelOff {
		return c.skip(CheckChapterTitles, "the creator's settings turn chapter titles off")
	}
	var chapters []int
	if c.in.Plan != nil {
		for _, s := range c.in.Plan.Shots {
			if s.Chapter > 0 && !slices.Contains(chapters, s.Chapter) {
				chapters = append(chapters, s.Chapter)
			}
		}
	}
	if len(chapters) < 2 {
		return c.skip(CheckChapterTitles, "the plan has no chapters (a listicle of two or more exercises has them)")
	}
	var numbered []string
	for _, t := range c.in.Spec.Tracks {
		if tt, ok := t.(*timeline.TextTrack); ok {
			for _, it := range tt.Items {
				if IsChapterTitle(it.Text) {
					numbered = append(numbered, it.ID)
				}
			}
		}
	}
	if len(numbered) < len(chapters) {
		return c.fail(CheckChapterTitles, "the plan has %d chapters and the edit %d numbered chapter titles: add one per chapter (\"1. <EXERCISE>\" in the creator's title style, with an entrance and an exit, at the chapter's first shot)", len(chapters), len(numbered))
	}
	return c.pass(CheckChapterTitles, "%d chapters, %d numbered titles", len(chapters), len(numbered))
}
