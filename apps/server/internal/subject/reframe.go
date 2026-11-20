package subject

import (
	"math"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// aspectTolerance is how far the aspect of a clip may be from the output's (as a ratio) for the clip to count as the same shape: a cover
// of such a clip crops almost nothing, so there is no subject to follow.
const aspectTolerance = 0.02

// MinInside is the share of the subject's box that must stay inside the output frame (docs/design/ai-video-editor-plan.md, P3 part A).
const MinInside = 0.9

// Centre is the middle of the subject over the source range [inMs, outMs]: the median, over the samples that apply (Track.Near), of the
// centre of the subject box, as fractions of the frame. False when the track has no sample for the range.
func (t Track) Centre(inMs, outMs int64) (x, y float64, ok bool) {
	near := t.Near(inMs, outMs)
	if len(near) == 0 {
		return 0, 0, false
	}
	xs, ys := make([]float64, len(near)), make([]float64, len(near))
	for i, s := range near {
		xs[i], ys[i] = s.X+s.W/2, s.Y+s.H/2
	}
	return median(xs), median(ys), true
}

func median(v []float64) float64 {
	slices.Sort(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

// Crops says whether fitting a clip of this aspect (width over height) into the output cuts part of the picture away: a cover of a clip
// that is not the output's shape does. A contain or a blur_fill never does.
func Crops(rf timeline.Reframe, aspect float64, out timeline.Output) bool {
	if rf.Mode != timeline.ReframeCover || aspect <= 0 || out.Width <= 0 || out.Height <= 0 {
		return false
	}
	return math.Abs(aspect/(float64(out.Width)/float64(out.Height))-1) > aspectTolerance
}

// WithFocus is the reframe of a clip as it plays: rf (the default reframe when nil), and, when it is a cover that crops the clip and
// leaves the focus out, the focus set on the subject's centre over the part of the source the clip plays. The renderer, the title
// placement and the checks all read the reframe through this, so they agree on where the crop is. A focus the model gave is never changed.
func WithFocus(rf *timeline.Reframe, t *Track, inMs, outMs int64, out timeline.Output) timeline.Reframe {
	r := timeline.Reframe{Mode: timeline.DefaultReframe}
	if rf != nil {
		r = *rf
	}
	if r.Focus != nil || t == nil || !Crops(r, t.Aspect, out) {
		return r
	}
	if x, y, ok := t.Centre(inMs, outMs); ok {
		r.Focus = &timeline.Point{X: clampF(x, 0, 1), Y: clampF(y, 0, 1)}
	}
	return r
}

// Inside is the share (0 to 1) of the subject box of a sample that lies inside the output frame under this reframe.
func Inside(s Sample, aspect float64, rf timeline.Reframe, out timeline.Output) float64 {
	x0, y0, x1, y1 := s.Body()
	// MapBox rounds outward to whole pixels, which is the safe side for "is it inside".
	r := MapBox(x0, y0, x1, y1, aspect, rf, out)
	bw, bh := float64(r.Right-r.Left), float64(r.Bottom-r.Top)
	if bw <= 0 || bh <= 0 {
		return 1
	}
	iw := math.Min(float64(r.Right), float64(out.Width)) - math.Max(float64(r.Left), 0)
	ih := math.Min(float64(r.Bottom), float64(out.Height)) - math.Max(float64(r.Top), 0)
	if iw <= 0 || ih <= 0 {
		return 0
	}
	return iw * ih / (bw * bh)
}
