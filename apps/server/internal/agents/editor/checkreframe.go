package editor

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// CheckSubjectInCrop (docs/design/ai-video-editor-plan.md, piece 3 part A): the person stays in the picture. For every clip with a
// quick-look subject box, the box mapped through the clip's reframe (subject.MapBox, the renderer's own placement) must stay at least
// 90 % inside the output frame. A cover that leaves the focus out is centred on the subject (subject.WithFocus), as the renderer does.
//
// It never asks for another orientation: the output format is the project's, and a clip is only cropped when the format and the clip
// differ in shape. When a crop cannot hold the person the fix is blur_fill (the whole clip, nothing cut away); when a different focus
// would hold them, the line says which.
const CheckSubjectInCrop = "subject_in_crop"

// subjectInCrop returns the check, and false when no clip of the edit has a subject box (the check is then left out of the report, as the
// speech and graphics checks are, rather than listed as skipped).
func (c *checker) subjectInCrop() (Check, bool) {
	if c.in.Spec == nil || len(c.in.Subjects) == 0 {
		return Check{}, false
	}
	out := c.in.Spec.Output
	var bad []string
	checked, cropped := 0, 0
	for i, it := range c.items {
		tr, ok := c.in.Subjects.Lookup(it.AssetID)
		if !ok {
			continue
		}
		samples := tr.Near(it.Source.InMs, it.Source.OutMs)
		if len(samples) == 0 {
			continue
		}
		checked++
		rf := subject.WithFocus(it.Reframe, &tr, it.Source.InMs, it.Source.OutMs, out)
		if subject.Crops(rf, tr.Aspect, out) {
			cropped++
		}
		worst := 1.0
		for _, s := range samples {
			worst = min(worst, subject.Inside(s, tr.Aspect, rf, out))
		}
		if worst >= subject.MinInside {
			continue
		}
		bad = append(bad, fmt.Sprintf("shot %d (%s): only %d %% of the person is in the frame (%s)%s", i+1, it.ID, int(worst*100), reframeWords(rf), c.cropFix(it, tr, samples)))
	}
	switch {
	case checked == 0:
		return Check{}, false
	case len(bad) > 0:
		more := ""
		if len(bad) > 3 {
			more = fmt.Sprintf("; and %d more", len(bad)-3)
		}
		return c.fail(CheckSubjectInCrop, "%s%s", strings.Join(bad[:min(3, len(bad))], "; "), more), true
	case cropped == 0:
		return c.pass(CheckSubjectInCrop, "%d shots with a subject box, none cropped", checked), true
	}
	return c.pass(CheckSubjectInCrop, "%d shots with a subject box, %d cropped, the person at least %d %% inside the frame in all", checked, cropped, int(subject.MinInside*100)), true
}

func reframeWords(rf timeline.Reframe) string {
	f := rf.FocusOrDefault()
	s := fmt.Sprintf("%s, focus x %.2f y %.2f", rf.Mode, f.X, f.Y)
	if z := rf.ZoomOrDefault(); z != 1 {
		s += fmt.Sprintf(", zoom %.2f", z)
	}
	return s
}

// cropFix is the advice for a shot that loses the person: the subject's centre as the focus when that holds them, else blur_fill.
func (c *checker) cropFix(it timeline.VideoItem, tr subject.Track, samples []subject.Sample) string {
	out := c.in.Spec.Output
	rf := subject.WithFocus(&timeline.Reframe{Mode: timeline.ReframeCover}, &tr, it.Source.InMs, it.Source.OutMs, out)
	if it.Reframe != nil {
		rf.Zoom = it.Reframe.Zoom
	}
	if rf.Focus != nil {
		ok := true
		for _, s := range samples {
			ok = ok && subject.Inside(s, tr.Aspect, rf, out) >= subject.MinInside
		}
		if ok {
			return fmt.Sprintf(". Fix: set_reframe on %s with mode cover and focus x %.2f y %.2f (the person's centre), or leave the focus out", it.ID, rf.Focus.X, rf.Focus.Y)
		}
	}
	return fmt.Sprintf(". A crop cannot hold the person here. Fix: set_reframe on %s with mode blur_fill (the whole clip, nothing cut away)", it.ID)
}
