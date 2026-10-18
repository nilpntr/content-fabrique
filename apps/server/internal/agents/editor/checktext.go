package editor

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The text-pattern checks (P6-05d), for a creator who tells the text of a video in lines over the shots (story captions or dialogue):
// a line on the shots, each line long enough to read, and no line over a face. They are added to the report only for such a style, so a
// title creator's report is what it was.

// The check ids.
const (
	CheckStoryCaptions   = "story_captions"
	CheckCaptionReadable = "caption_readable"
	CheckTextOverFace    = "text_over_face"
)

// captionReadableMs is the shortest a line may stay on screen: a short sentence is read in about a second.
const captionReadableMs = 1000

// textItems are the edit's text items, in time order.
func textItems(spec *timeline.Spec) []timeline.TextItem {
	var out []timeline.TextItem
	for _, tr := range spec.Tracks {
		if tt, ok := tr.(*timeline.TextTrack); ok {
			out = append(out, tt.Items...)
		}
	}
	return out
}

// linesOn says the style tells its text in lines (story captions or dialogue).
func (c *checker) linesOn() bool { return c.in.Settings != nil && c.in.Settings.Lines() }

// storyCaptions holds a story-caption edit to a line on its shots: every shot but the last (the reveal or the loop may be bare) shows a
// line at its middle, and a new line comes on with most cuts.
func (c *checker) storyCaptions() Check {
	if c.in.Settings.TextPattern != styleprofile.PatternStoryCaptions {
		return c.skip(CheckStoryCaptions, "the style's text is %s, not story captions", strings.ReplaceAll(c.in.Settings.TextPattern, "_", " "))
	}
	texts := textItems(c.in.Spec)
	if len(c.items) < 2 {
		return c.skip(CheckStoryCaptions, "one shot")
	}
	var bare []string
	for i, it := range c.items[:len(c.items)-1] {
		mid := it.AtMs + c.durs[i]/2
		shown := false
		for _, t := range texts {
			if t.AtMs <= mid && mid < t.AtMs+t.DurationMs {
				shown = true
				break
			}
		}
		if !shown {
			bare = append(bare, it.ID)
		}
	}
	need := (len(c.items) + 1) / 2
	switch {
	case len(bare) > 0:
		return c.fail(CheckStoryCaptions, "%s %s no line: this creator tells the video in short story captions, one line on every shot (the last may be bare); add one (add_text, a few words in their language) that carries the story on",
			strings.Join(bare[:min(5, len(bare))], ", "), plural(len(bare), "shows", "show"))
	case len(texts) < need:
		return c.fail(CheckStoryCaptions, "%d lines over %d shots: a new line comes on with most cuts in this creator's videos; split the story into at least %d short lines, each starting on a cut", len(texts), len(c.items), need)
	}
	return c.pass(CheckStoryCaptions, "%d lines over %d shots, a line on every shot", len(texts), len(c.items))
}

// captionReadable fails a line that is on screen for less than a second.
func (c *checker) captionReadable() Check {
	var short []string
	n := 0
	for _, t := range textItems(c.in.Spec) {
		n++
		if t.DurationMs < captionReadableMs {
			short = append(short, fmt.Sprintf("%s (%s)", t.ID, secs(t.DurationMs)))
		}
	}
	if n == 0 {
		return c.skip(CheckCaptionReadable, "no text")
	}
	if len(short) > 0 {
		return c.fail(CheckCaptionReadable, "%s %s on screen for less than %s, too short to read: keep every line up for at least %s (the length of its shot)",
			strings.Join(short[:min(5, len(short))], ", "), plural(len(short), "is", "are"), secs(captionReadableMs), secs(captionReadableMs))
	}
	return c.pass(CheckCaptionReadable, "%d lines, each on screen for %s or more", n, secs(captionReadableMs))
}

// textOverFace fails a line that is drawn over a face: where the renderer shows it (subject.FitText), its letters never on a head. A line
// that does not avoid the subject is tested where its anchor puts it.
func (c *checker) textOverFace() Check {
	if len(c.in.Subjects) == 0 {
		return c.skip(CheckTextOverFace, "the clips have no subject boxes")
	}
	var bad []string
	n := 0
	for _, t := range textItems(c.in.Spec) {
		n++
		if !t.AvoidsSubject() {
			face := t
			face.Avoid = timeline.AvoidFace
			regions := subject.Regions(c.in.Spec, c.in.Subjects.Lookup, t.AtMs, t.AtMs+t.DurationMs)
			if cover, err := timeline.TextCovers(c.in.Spec.Output, face, regions); err == nil && cover.Head {
				bad = append(bad, t.ID+" (set not to avoid the person: remove avoid_subject false)")
			}
			continue
		}
		fit, err := subject.FitText(c.in.Spec, c.in.Subjects.Lookup, t, c.total)
		if err != nil || fit.How == subject.FitDropped {
			continue
		}
		ink := fit.Placement.Layout.InkBox(fit.Placement.Box)
		for _, s := range fit.Regions {
			if ink.Overlaps(s.Head) {
				bad = append(bad, t.ID)
				break
			}
		}
	}
	if n == 0 {
		return c.skip(CheckTextOverFace, "no text")
	}
	if len(bad) > 0 {
		return c.fail(CheckTextOverFace, "%s %s over a face: a line may stand on the chest, never on the face; move it lower (anchor center or lower_third) or onto another shot",
			strings.Join(bad, ", "), plural(len(bad), "is", "are"))
	}
	return c.pass(CheckTextOverFace, "%d lines, none over a face", n)
}

// faceOnlyMisused is the text_over_subject failure of a text that asks to keep off the face only in a style that keeps text off the
// whole body (a title creator, or lines that sit clear of the body): "" when it is allowed.
func (c *checker) faceOnlyMisused(it timeline.TextItem) string {
	if !it.AvoidsFaceOnly() || c.faceOnly {
		return ""
	}
	return fmt.Sprintf("%s is set to keep off the face only (avoid face), but this creator's text keeps off the whole body: remove avoid face", it.ID)
}
