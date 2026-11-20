package editor

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// chapterTitle matches a numbered chapter title of a listicle ("1. SQUATS", "2) ROWS", "3 - PULL").
var chapterTitle = regexp.MustCompile(`^\s*[0-9]{1,2}\s*[.):\-]\s*\S`)

// IsChapterTitle says whether a text is a numbered chapter title.
func IsChapterTitle(text string) bool { return chapterTitle.MatchString(text) }

// textOverSubject checks the texts against the person in the clips they are on top of (P6-06): no text may cover the head or the torso,
// on any frame it is on screen. The renderer keeps a text off them the way this check says (subject.FitText, the same function): above the
// head at any legible size, stacked beside the head over the background (wrapped to two or three lines), below all of the person; where
// none fits at the text's own time, in the part of its time that has room, at the nearest other time that has, or not at all. So the check
// fails when the result is not what the editor meant: a hook or chapter title that ends up at another time or left out (with the room the
// placement found and what would fit it), and a text set not to avoid the subject that covers it. Everything else the renderer does is a
// note.
func (c *checker) textOverSubject() Check {
	if len(c.in.Subjects) == 0 {
		return c.skip(CheckTextOverSubject, "the clips have no subject boxes (the quick look reads them: run cfctl assets quicklook --subjects for older clips)")
	}
	out := c.in.Spec.Output
	var texts int
	var bad, notes []string
	first := true
	for _, tr := range c.in.Spec.Tracks {
		tt, ok := tr.(*timeline.TextTrack)
		if !ok {
			continue
		}
		for _, it := range tt.Items {
			if msg := c.faceOnlyMisused(it); msg != "" {
				bad = append(bad, msg)
				texts++
				first = false
				continue
			}
			essential := (first && it.AtMs <= hookTitleByMs) || IsChapterTitle(it.Text)
			first = false
			regions := subject.Regions(c.in.Spec, c.in.Subjects.Lookup, it.AtMs, it.AtMs+it.DurationMs)
			if len(regions) == 0 {
				continue
			}
			texts++
			cover, err := timeline.TextCovers(out, it, regions)
			if err != nil || !cover.Body {
				continue
			}
			where := c.headBand(regions)
			fix := "use one line, a scale down to 0.6 or a shorter text so it fits in the band above the head, or put it on a shot where the person stands lower or smaller"
			if !it.AvoidsSubject() {
				part := "torso"
				if cover.Head {
					part = "head"
				}
				bad = append(bad, fmt.Sprintf("%s covers the person's %s and is set not to avoid them (%s): remove avoid_subject false", it.ID, part, where))
				continue
			}
			fit, err := subject.FitText(c.in.Spec, c.in.Subjects.Lookup, it, c.total)
			if err != nil {
				continue
			}
			av := fit.Avoidance
			switch fit.How {
			case subject.FitDropped:
				if essential {
					bad = append(bad, fmt.Sprintf("%s would cover the person's body at every time of the video, so the renderer leaves it out (%s): %s", it.ID, where, c.titleFix(it, fit)))
				} else {
					notes = append(notes, fmt.Sprintf("borderline: %s is left out by the renderer, it would cover the person's body at every time (%s)", it.ID, where))
				}
			case subject.FitMoved:
				msg := fmt.Sprintf("%s has no room clear of the person's body at %d-%d ms (%s), so the renderer shows it at %d-%d ms", it.ID, it.AtMs, it.AtMs+it.DurationMs, where, fit.AtMs, fit.AtMs+fit.DurationMs)
				if essential {
					bad = append(bad, msg+": "+c.titleFix(it, fit))
				} else {
					notes = append(notes, msg+"; set at_ms there yourself, or "+fix)
				}
			case subject.FitTrimmed:
				notes = append(notes, fmt.Sprintf("%s fits clear of the person's body only at %d-%d ms of its %d-%d ms (%s), so the renderer shows it then; set those times yourself, or %s",
					it.ID, fit.AtMs, fit.AtMs+fit.DurationMs, it.AtMs, it.AtMs+it.DurationMs, where, fix))
			default:
				switch {
				case av.Where == timeline.WhereSmaller:
					notes = append(notes, fmt.Sprintf("%s covers the subject, so the renderer shrinks it to scale %g (%s); one line keeps it at its size above the head", it.ID, av.Scale, where))
				case av.Where == timeline.WhereAbove && av.Scale > 0:
					notes = append(notes, fmt.Sprintf("%s covers the subject, so the renderer moves it above the head at scale %g (%s); one line keeps it at its size", it.ID, av.Scale, where))
				case av.Where == timeline.WhereBeside:
					how := "on one line"
					if n := len(fit.Placement.Layout.Lines); n > 1 {
						how = fmt.Sprintf("stacked in %d lines", n)
					}
					size := "at its size"
					if av.Scale > 0 {
						size = fmt.Sprintf("at scale %g", av.Scale)
					}
					notes = append(notes, fmt.Sprintf("%s covers the subject, so the renderer stacks it beside the head over the background, %s %s (%s)", it.ID, how, size, where))
				case av.Overlapped:
					notes = append(notes, fmt.Sprintf("%s covers the subject, so the renderer moves it %s them (%s); one line and a smaller scale keep it above the head", it.ID, av.Where, where))
				}
			}
		}
	}
	switch {
	case len(bad) > 0:
		return c.fail(CheckTextOverSubject, "%s", strings.Join(bad[:min(3, len(bad))], "; "))
	case texts == 0:
		return c.skip(CheckTextOverSubject, "no text is on top of a clip with a subject box")
	case len(notes) > 0:
		return c.pass(CheckTextOverSubject, "%s", strings.Join(notes[:min(3, len(notes))], "; "))
	}
	return c.pass(CheckTextOverSubject, "%d texts, none on the person", texts)
}

// enabledCaptions is the spec's enabled captions track, nil when there is none.
func enabledCaptions(spec *timeline.Spec) *timeline.CaptionsTrack {
	for _, t := range spec.Tracks {
		if ct, ok := t.(*timeline.CaptionsTrack); ok && ct.Enabled {
			return ct
		}
	}
	return nil
}

// CheckCaptionOverFace is the check for captions drawn over the speaker's face, or under a text that the renderer shows on them (Captions v2).
const CheckCaptionOverFace = "caption_over_face"

// captionOverFace fails when the captions of a clip cover the speaker's face (the head and shoulders) in the band the renderer puts them
// in: the renderer moves a clip's captions to the upper band when the lower one is on the face (subject.CaptionPlaces, the same function),
// so the check fails for the face only when both bands are, and the fix is on the picture, not in the captions. It also fails a text item
// that is drawn on top of the moved (upper) captions of the clip it is on: the validator reserves the lower band for every text, but only
// this check knows where the face is. A moved band is otherwise a note.
func (c *checker) captionOverFace(tr *timeline.CaptionsTrack) Check {
	if len(c.in.Subjects) == 0 {
		return c.skip(CheckCaptionOverFace, "the clips have no subject boxes (the quick look reads them: run cfctl assets quicklook --subjects for older clips)")
	}
	st, ok := style.CaptionStyleOf(tr.Style, tr.Fill, tr.Accent)
	if !ok {
		return c.skip(CheckCaptionOverFace, "unknown captions style")
	}
	places := subject.CaptionPlaces(c.in.Spec, c.in.Subjects.Lookup, st)
	var bad, moved []string
	for _, p := range places {
		switch {
		case p.OverFace:
			bad = append(bad, fmt.Sprintf("%s (%d-%d ms; the face is at y %d to %d)", p.ClipID, p.AtMs, p.EndMs, p.Face.Top, p.Face.Bottom))
		case p.Moved:
			moved = append(moved, p.ClipID)
		}
	}
	if len(bad) > 0 {
		return c.fail(CheckCaptionOverFace, "the captions of %s cover the speaker's face in the lower and in the upper band: the face fills the frame there; reframe the clip (set_reframe: a wider crop or contain) or use another shot",
			strings.Join(bad[:min(3, len(bad))], ", "))
	}
	var onCaptions []string
	for _, t := range textItems(c.in.Spec) {
		fit, err := subject.FitText(c.in.Spec, c.in.Subjects.Lookup, t, c.total)
		if err != nil || fit.How == subject.FitDropped {
			continue
		}
		ink := fit.Placement.Layout.InkBox(fit.Placement.Box)
		for _, p := range places {
			if p.Moved && fit.AtMs < p.EndMs && fit.AtMs+fit.DurationMs > p.AtMs && ink.Overlaps(p.Band) {
				onCaptions = append(onCaptions, fmt.Sprintf("%s on %s", t.ID, p.ClipID))
				break
			}
		}
	}
	if len(onCaptions) > 0 {
		return c.fail(CheckCaptionOverFace, "%s would be drawn on the captions: the speaker's face is in the lower band there, so the captions are in the upper one; put the text in the lower third, or on a shot where the face is high",
			strings.Join(onCaptions[:min(3, len(onCaptions))], ", "))
	}
	if len(moved) > 0 {
		return c.pass(CheckCaptionOverFace, "%d clips, captions in the upper band on %s because the face is in the lower one", len(places), strings.Join(moved[:min(3, len(moved))], ", "))
	}
	return c.pass(CheckCaptionOverFace, "%d clips, the captions are clear of the face", len(places))
}

// titleFix is the fix for a hook or chapter title that has no place clear of the person at its own time: what the placement tried, the
// room it found (in pixels) and what would fit there, in numbers: the longest line of whole words that fits beside the head at the smallest
// legible scale, a shot where the title fits, and, when the clip's head was not measured on its own, the command that measures it.
func (c *checker) titleFix(it timeline.TextItem, fit subject.Fit) string {
	tr := fit.OwnTime.Tried
	out := c.in.Spec.Output
	floor := tr.FloorScale
	if floor == 0 {
		floor = 1
		if it.Scale != nil {
			floor = *it.Scale
		}
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("tried: above the head (a band of %d px), beside it (at most %d x %d px of background) and below the person, down to the smallest legible scale %g",
		tr.AboveBandPx, tr.BesideWidthPx, tr.BesideHeightPx, floor))
	if n := charsThatFit(out, it, floor, tr.BesideWidthPx); n > 0 {
		parts = append(parts, fmt.Sprintf("fix: a shorter title, at most %d characters a line in up to %d lines (whole words), stacks beside the head at scale %g", n, timeline.MaxBesideLines, floor))
	} else {
		parts = append(parts, "fix: there is no background beside the head; put the title on a shot where the person stands lower or smaller")
	}
	if fit.How == subject.FitMoved {
		if i := c.itemAt(fit.AtMs); i >= 0 {
			parts = append(parts, fmt.Sprintf("or open on %s, where it fits at %d ms", c.items[i].ID, fit.AtMs))
		}
	}
	if c.headUnmeasured(it) {
		parts = append(parts, "(the clip's head was not measured on its own: cfctl assets quicklook --subjects reads it, and a narrow head leaves room beside it)")
	}
	return strings.Join(parts, "; ")
}

// charsThatFit is how many characters of the item's text, on average, fit on one line of width px at a scale: 0 when not even one does.
func charsThatFit(out timeline.Output, it timeline.TextItem, scale float64, width int) int {
	if width <= 0 || strings.TrimSpace(it.Text) == "" {
		return 0
	}
	one := it
	one.Text = strings.Join(strings.Fields(it.Text), " ")
	sc := scale
	one.Scale = &sc
	if sc < style.MinScale {
		one.Scale = nil
	}
	pl, err := timeline.PlaceText(out, one)
	if err != nil {
		return 0
	}
	if sc < style.MinScale {
		pl.Layout = style.Measure(pl.Style.Shrunk(sc), out.ShortSide(), one.Text, 1<<20)
	} else {
		pl.Layout = style.Measure(pl.Style, out.ShortSide(), one.Text, 1<<20)
	}
	w, _ := pl.Layout.Box()
	runes := len([]rune(one.Text))
	if w <= 0 || runes == 0 {
		return 0
	}
	per := float64(w-2*pl.Layout.Pad) / float64(runes)
	return int(float64(width-2*pl.Layout.Pad) / per)
}

// itemAt is the index of the clip on screen at t ms, -1 when none.
func (c *checker) itemAt(t int64) int {
	for i := range c.items {
		if t >= c.items[i].AtMs && t < c.items[i].AtMs+c.durs[i] {
			return i
		}
	}
	return -1
}

// headUnmeasured says a clip under the item has subject boxes whose head was not read on its own (quicklook.v2 or older).
func (c *checker) headUnmeasured(it timeline.TextItem) bool {
	for i, v := range c.items {
		if v.AtMs >= it.AtMs+it.DurationMs || v.AtMs+c.durs[i] <= it.AtMs {
			continue
		}
		tr, ok := c.in.Subjects.Lookup(v.AssetID)
		if !ok {
			continue
		}
		for _, s := range tr.Samples {
			if s.HeadW == 0 {
				return true
			}
		}
	}
	return false
}

// headBand says where the highest head is and how tall the clear band above it inside the safe zone is, in numbers.
func (c *checker) headBand(regions []timeline.Subject) string {
	out := c.in.Spec.Output
	top := regions[0].Head.Top
	for _, r := range regions[1:] {
		top = min(top, r.Head.Top)
	}
	return fmt.Sprintf("the head starts at y %d (%.0f %% from the top) and the clear band above it is %d px tall", top, float64(top)/float64(max(out.Height, 1))*100, max(top-c.zoneTop(), 0))
}
