package editor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/camshift"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The energy checks (P6-06): an edit must not sit dead still, and at the lively and hype vibes it uses transitions on the strong beats
// and the drop (with variety), and text that enters and leaves. The founder's verdict on the edits before these checks: "static/stiff,
// not playful/vibey", "all hard cuts, no visual effects on entering the text".

// The check ids.
const (
	CheckStaticShot          = "static_shot"
	CheckNoTransitions       = "no_transitions_at_lively"
	CheckTransitionVariety   = "transition_variety"
	CheckTextWithoutEntrance = "text_without_entrance"
	CheckChapterTitles       = "chapter_titles"
)

// staticShotMaxMs is the longest a shot may sit with no camera motion at all: past it a tripod shot reads as dead.
const staticShotMaxMs = 2000

// hypeTransitionShare is the share of the cuts that are transitions at hype.
const hypeTransitionShare = 0.25

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

func (c *checker) noTransitions() Check {
	vibe := VibeOr(c.in.Vibe)
	cuts := len(c.items) - 1
	switch {
	case !energetic(vibe):
		return c.skip(CheckNoTransitions, "the vibe is calm: hard cuts are the style")
	case cuts < 2:
		return c.skip(CheckNoTransitions, "fewer than two cuts")
	}
	n := 0
	for _, it := range c.items[1:] {
		if isTransition(it) {
			n++
		}
	}
	if n == 0 {
		return c.fail(CheckNoTransitions, "the vibe is %s and all %d cuts are hard cuts: make the cut into the drop and the cuts on strong beats or phrase starts a whip_left, whip_right, zoom_through, punch or flash of 250 to 400 ms (set_transition), and keep the cuts on ordinary beats hard", vibe, cuts)
	}
	if vibe == VibeHype && float64(n) < hypeTransitionShare*float64(cuts) {
		return c.fail(CheckNoTransitions, "the vibe is hype and %d of %d cuts are transitions: put one on at least every fourth cut (the strong beats, the phrase starts, the drop)", n, cuts)
	}
	// The cut that lands on the drop carries the hit.
	if m := c.in.Music; m != nil && m.HasDrop {
		for _, it := range c.items[1:] {
			if absI(it.AtMs-m.DropMs) <= offBeatToleranceMs*2 && !isTransition(it) {
				return c.fail(CheckNoTransitions, "the cut on the drop (%s at %d ms) is a hard cut: make it a punch, zoom_through or whip (250 to 400 ms), the hit of the video", it.ID, it.AtMs)
			}
		}
	}
	return c.pass(CheckNoTransitions, "%d of %d cuts are transitions", n, cuts)
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
			return c.fail(CheckTransitionVariety, "three %s transitions in a row: vary them (whip_left, whip_right, zoom_through, punch, flash)", kinds[i])
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
		switch tt := t.(type) {
		case *timeline.TextTrack:
			for _, it := range tt.Items {
				if IsChapterTitle(it.Text) {
					numbered = append(numbered, it.ID)
				}
			}
		case *timeline.GraphicsTrack: // a chapter_card, or a graphic whose words are a numbered title (D-35)
			for _, it := range tt.Items {
				if it.Template == timeline.TemplateChapterCard || IsChapterTitle(it.DisplayText()) {
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
