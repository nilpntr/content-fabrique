package editor

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// chapterTitle matches a numbered chapter title of a listicle ("1. SHRUGS", "2) STRAPS", "3 - PULL").
var chapterTitle = regexp.MustCompile(`^\s*[0-9]{1,2}\s*[.):\-]\s*\S`)

// IsChapterTitle says whether a text is a numbered chapter title.
func IsChapterTitle(text string) bool { return chapterTitle.MatchString(text) }

// textOverSubject checks the texts against the person in the clips they are on top of (P6-06): no text may cover the head or the torso,
// on any frame it is on screen. The renderer keeps a text off them the way this check says (subject.FitText, the same function): above the
// head at any size down to 0.6, beside the head over the background, below all of the person; where none fits at the text's own time, in
// the part of its time that has room, at the nearest other time that has, or not at all. So the check fails when the result is not what
// the editor meant: a hook or chapter title that ends up at another time or left out, and a text set not to avoid the subject that covers
// it. Everything else the renderer does is a note.
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
					bad = append(bad, fmt.Sprintf("%s would cover the person's body at every time of the video, so the renderer leaves it out (%s): %s", it.ID, where, fix))
				} else {
					notes = append(notes, fmt.Sprintf("borderline: %s is left out by the renderer, it would cover the person's body at every time (%s)", it.ID, where))
				}
			case subject.FitMoved:
				msg := fmt.Sprintf("%s has no room clear of the person's body at %d-%d ms (%s), so the renderer shows it at %d-%d ms", it.ID, it.AtMs, it.AtMs+it.DurationMs, where, fit.AtMs, fit.AtMs+fit.DurationMs)
				if essential {
					bad = append(bad, msg+": "+fix)
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

// headBand says where the highest head is and how tall the clear band above it inside the safe zone is, in numbers.
func (c *checker) headBand(regions []timeline.Subject) string {
	out := c.in.Spec.Output
	top := regions[0].Head.Top
	for _, r := range regions[1:] {
		top = min(top, r.Head.Top)
	}
	return fmt.Sprintf("the head starts at y %d (%.0f %% from the top) and the clear band above it is %d px tall", top, float64(top)/float64(max(out.Height, 1))*100, max(top-c.zoneTop(), 0))
}
