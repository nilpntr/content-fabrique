package editor

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// CheckSubjectInCrop (docs/design/ai-video-editor-plan.md, piece 3 part A): the person stays in the picture. For every clip with a
// quick-look subject box, the box mapped through the clip's reframe (subject.MapBox, the renderer's own placement) must stay at least
// 90 % inside the output frame. A cover that leaves the focus out is centred on the subject (subject.WithFocus, the same call as the
// renderer's, over the clip's whole source range).
//
// It judges the static framing only: the push and the punch-in of a camera move (set_motion, handheld) widen the crop by their zoom and
// are not counted (render/camera.go limits those against the subject itself). It never asks for another orientation: the output format
// is the project's, and a clip is only cropped when the format and the clip differ in shape. A clip with no sample inside its range
// (or within subject.MaxSampleGapMs of it) is unchecked, and the line says how many.
const CheckSubjectInCrop = "subject_in_crop"

// subjectInCrop returns the check, and false when no clip of the edit has a subject box (the check is then left out of the report, as the
// speech and graphics checks are, rather than listed as skipped).
func (c *checker) subjectInCrop() (Check, bool) {
	if c.in.Spec == nil || len(c.in.Subjects) == 0 {
		return Check{}, false
	}
	out := c.in.Spec.Output
	var bad []string
	checked, cropped, unchecked := 0, 0, 0
	for i, it := range c.items {
		tr, ok := c.in.Subjects.Lookup(it.AssetID)
		if !ok {
			continue
		}
		samples := tr.ForCrop(it.Source)
		if len(samples) == 0 {
			unchecked++
			continue
		}
		checked++
		rf := subject.WithFocus(it.Reframe, &tr, it.Source, out)
		if subject.Crops(rf, tr.Aspect, out) {
			cropped++
		}
		worst := worstInside(samples, tr.Aspect, rf, out)
		if worst >= subject.MinInside {
			continue
		}
		bad = append(bad, fmt.Sprintf("shot %d (%s): only %d %% of the person is in the frame (%s)%s", i+1, it.ID, int(worst*100), reframeWords(rf), c.cropFix(it, tr, samples)))
	}
	note := ""
	if unchecked > 0 {
		note = fmt.Sprintf("; %d shot(s) not checked: no subject box inside the clip's range or within %d ms of it", unchecked, subject.MaxSampleGapMs)
	}
	switch {
	case checked == 0:
		if unchecked == 0 {
			return Check{}, false
		}
		return c.skip(CheckSubjectInCrop, "%d shot(s) not checked: no subject box inside the clip's range or within %d ms of it", unchecked, subject.MaxSampleGapMs), true
	case len(bad) > 0:
		more := ""
		if len(bad) > 3 {
			more = fmt.Sprintf("; and %d more", len(bad)-3)
		}
		return c.fail(CheckSubjectInCrop, "%s%s%s", strings.Join(bad[:min(3, len(bad))], "; "), more, note), true
	case cropped == 0:
		return c.pass(CheckSubjectInCrop, "%d shots with a subject box, none cropped (static framing; camera moves not counted)%s", checked, note), true
	}
	return c.pass(CheckSubjectInCrop, "%d shots with a subject box, %d cropped, the person at least %d %% inside the frame in all (static framing; camera moves not counted)%s", checked, cropped, int(subject.MinInside*100), note), true
}

func worstInside(samples []subject.Sample, aspect float64, rf timeline.Reframe, out timeline.Output) float64 {
	worst := 1.0
	for _, s := range samples {
		worst = min(worst, subject.Inside(s, aspect, rf, out))
	}
	return worst
}

func reframeWords(rf timeline.Reframe) string {
	f := rf.FocusOrDefault()
	s := fmt.Sprintf("%s, focus x %.2f y %.2f", rf.Mode, f.X, f.Y)
	if z := rf.ZoomOrDefault(); z != 1 {
		s += fmt.Sprintf(", zoom %.2f", z)
	}
	return s
}

// cropFix is the advice for a shot that loses the person, matched to the cause: the zoom when a lower one holds them, else the
// subject's centre as the focus of a cover, else blur_fill (the clip's shape does not fit the frame and no crop of it holds the person).
func (c *checker) cropFix(it timeline.VideoItem, tr subject.Track, samples []subject.Sample) string {
	out := c.in.Spec.Output
	cur := subject.WithFocus(it.Reframe, &tr, it.Source, out)
	holds := func(rf timeline.Reframe) bool {
		return worstInside(samples, tr.Aspect, rf, out) >= subject.MinInside
	}
	one := 1.0
	// The zoom: with the same mode and focus at zoom 1 the person is held, so the zoom is the cause.
	if z := cur.ZoomOrDefault(); z > 1 {
		lower := cur
		lower.Zoom = &one
		if holds(lower) {
			best := 1.0
			for zz := z - 0.05; zz > 1.0001; zz -= 0.05 {
				try := cur
				try.Zoom = &zz
				if holds(try) {
					best = zz
					break
				}
			}
			return fmt.Sprintf(". The zoom %.2f crops them out. Fix: set_reframe on %s with zoom %.2f or lower (1 shows the whole fitted frame)", z, it.ID, best)
		}
	}
	if cur.Mode == timeline.ReframeCover {
		centred := subject.WithFocus(&timeline.Reframe{Mode: timeline.ReframeCover, Zoom: cur.Zoom}, &tr, it.Source, out)
		if centred.Focus != nil && holds(centred) {
			return fmt.Sprintf(". The focus is off the person. Fix: set_reframe on %s with mode cover and focus x %.2f y %.2f (the person's centre), or leave the focus out", it.ID, centred.Focus.X, centred.Focus.Y)
		}
		return fmt.Sprintf(". A %s clip does not fit this %dx%d frame as a crop and no focus holds the person. Fix: set_reframe on %s with mode blur_fill (the whole clip, nothing cut away)", aspectWords(tr.Aspect), out.Width, out.Height, it.ID)
	}
	return fmt.Sprintf(". The reframe is %s, which shows the whole clip; check the zoom and the clip's subject box with get_brief", cur.Mode)
}

func aspectWords(a float64) string {
	switch {
	case a > 1.05:
		return "wide"
	case a < 0.95:
		return "tall"
	}
	return "square"
}
