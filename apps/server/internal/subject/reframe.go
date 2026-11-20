package subject

import (
	"cmp"
	"math"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// aspectTolerance is how far the aspect of a clip may be from the output's (as a ratio) for the clip to count as the same shape: a cover
// of such a clip crops almost nothing, so there is no subject to follow.
const aspectTolerance = 0.02

// MinInside is the share of the subject's box that must stay inside the output frame (docs/design/ai-video-editor-plan.md, P3 part A).
const MinInside = 0.9

// MaxSampleGapMs is how far from a clip's source range a sample may be for the reframe to use it. Track.Near falls back to the nearest
// sample at any distance, which is fine for keeping a title off a head; for a crop a person seen seconds away from the clip may stand
// elsewhere, so a clip with no sample inside or this close has no subject for the reframe (it is unchecked, not guessed).
const MaxSampleGapMs = 1000

// ForCrop returns the samples that apply to a clip's source range for the reframe: those inside it, else the nearest one when it is within
// MaxSampleGapMs of the range, else none.
func (t Track) ForCrop(src timeline.Source) []Sample {
	var in []Sample
	for _, s := range t.Samples {
		if s.AtMs >= src.InMs && s.AtMs <= src.OutMs {
			in = append(in, s)
		}
	}
	if len(in) > 0 {
		return in
	}
	var best *Sample
	gap := int64(MaxSampleGapMs) + 1
	for i, s := range t.Samples {
		g := max(src.InMs-s.AtMs, s.AtMs-src.OutMs)
		if g < gap {
			gap, best = g, &t.Samples[i]
		}
	}
	if best == nil {
		return nil
	}
	return []Sample{*best}
}

// Centre is the middle of the subject over a clip's source range, as fractions of the frame. It is the centre of the median sample: the
// samples (ForCrop) ordered by the x of their box centre, the middle one (the lower of two), taken whole, so x and y come from one real
// box and a person who walks across the frame is put where they stood at the middle of the walk, never at a point nobody stood at. False
// when the clip has no sample for the reframe.
func (t Track) Centre(src timeline.Source) (x, y float64, ok bool) {
	near := t.ForCrop(src)
	if len(near) == 0 {
		return 0, 0, false
	}
	sorted := slices.Clone(near)
	slices.SortStableFunc(sorted, func(a, b Sample) int { return cmp.Compare(a.X+a.W/2, b.X+b.W/2) })
	m := sorted[(len(sorted)-1)/2]
	return m.X + m.W/2, m.Y + m.H/2, true
}

// Crops says whether fitting a clip of this aspect (width over height) into the output cuts part of the picture away: a cover of a clip
// that is not the output's shape does. A contain or a blur_fill never does.
func Crops(rf timeline.Reframe, aspect float64, out timeline.Output) bool {
	if rf.Mode != timeline.ReframeCover || aspect <= 0 || out.Width <= 0 || out.Height <= 0 {
		return false
	}
	return math.Abs(aspect/(float64(out.Width)/float64(out.Height))-1) > aspectTolerance
}

// WithFocus is the one place the automatic focus is worked out. All four readers call it with the clip's whole source range (never a
// title's window): the renderer (render/video.go), the camera limit (render/camera.go), the title placement (Regions) and subject_in_crop. It is
// the reframe of a clip as it plays: rf (the default reframe when nil), and, when it is a cover that crops the clip and
// leaves the focus out, the focus set on the subject's centre over the part of the source the clip plays. The renderer, the title
// placement and the checks all read the reframe through this, so they agree on where the crop is. A focus the model gave is never changed.
func WithFocus(rf *timeline.Reframe, t *Track, src timeline.Source, out timeline.Output) timeline.Reframe {
	r := timeline.Reframe{Mode: timeline.DefaultReframe}
	if rf != nil {
		r = *rf
	}
	if r.Focus != nil || t == nil || !Crops(r, t.Aspect, out) {
		return r
	}
	if x, y, ok := t.Centre(src); ok {
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
