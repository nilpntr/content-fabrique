// Package subject is where the person in a clip is, as far as a title needs to know it: a box and the top of the head, per sampled frame,
// read by the quick look (P1-04b) and stored with the asset's quick-look detail. The compiler uses it to keep a title off the head (the
// placement avoids it), and the editor's check_edit says when a title would sit on it.
//
// The numbers come from a vision model reading a picture, so they are untrusted and bounded: every box is a fraction of the frame, finite,
// inside the frame, not degenerate, and the top of the head lies inside its box; a sample that fails is dropped, never repaired; at most
// MaxSamples are kept. Nothing here reads text: the model's words never reach it.
//
// Coordinates are fractions of the displayed frame (0 is the left and the top). A sample is a sampled time of the clip: the subject is
// taken to stand where it stood in the nearest sample for as long as the clip plays near it.
package subject

import (
	"encoding/json"
	"math"
	"slices"
)

// The limits of what is kept.
const (
	// MaxSamples is the most samples a clip keeps (the quick look samples four frames).
	MaxSamples = 8
	// MinSize is the smallest width and height of a box, as a fraction of the frame: a smaller box is a speck, not a subject.
	MinSize = 0.03
	// MinAspect and MaxAspect bound the width over the height of the frame the fractions refer to.
	MinAspect = 0.2
	MaxAspect = 5.0
	// headFraction is how tall the head is, as a fraction of the subject box, when only the top of it is known; headCentre is the share of
	// the box's width that the head spans around its centre line.
	headFraction = 0.22
	headCentre   = 0.6
)

// Sample is the subject in one sampled frame.
type Sample struct {
	// AtMs is the time in the source clip the frame was taken at.
	AtMs int64 `json:"at_ms"`
	// X, Y, W and H are the box of the subject (the whole person, hands and raised arms included) as fractions of the frame.
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
	// HeadTopY is the top of the head as a fraction of the frame height, inside the box.
	HeadTopY float64 `json:"head_top_y"`
}

// Track is the subject of a clip: the aspect of the frames the fractions refer to (width over height) and the samples, by time.
type Track struct {
	Aspect  float64  `json:"aspect"`
	Samples []Sample `json:"samples"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Valid says whether the sample is inside the bounds. A box may overrun the frame by a hair (a model's rounding), no more.
func (s Sample) Valid() bool {
	const eps = 0.02
	for _, v := range []float64{s.X, s.Y, s.W, s.H, s.HeadTopY} {
		if !finite(v) {
			return false
		}
	}
	return s.AtMs >= 0 && s.X >= 0 && s.Y >= 0 && s.W >= MinSize && s.H >= MinSize && s.X+s.W <= 1+eps && s.Y+s.H <= 1+eps &&
		s.HeadTopY >= s.Y-eps && s.HeadTopY <= s.Y+s.H*0.6
}

// Clamp keeps a valid sample inside the frame exactly (the hair a box overran by, and a head top a hair above the box).
func (s Sample) Clamp() Sample {
	s.W = math.Min(s.W, 1-s.X)
	s.H = math.Min(s.H, 1-s.Y)
	s.HeadTopY = math.Max(s.HeadTopY, s.Y)
	return s
}

// Clean returns the track with every invalid sample dropped, the rest clamped, sorted by time and cut to MaxSamples. ok is false when the
// aspect is out of bounds or no sample is left.
func Clean(t Track) (Track, bool) {
	if !finite(t.Aspect) || t.Aspect < MinAspect || t.Aspect > MaxAspect {
		return Track{}, false
	}
	out := Track{Aspect: math.Round(t.Aspect*1000) / 1000}
	for _, s := range t.Samples {
		if s.Valid() {
			out.Samples = append(out.Samples, s.Clamp())
		}
	}
	slices.SortStableFunc(out.Samples, func(a, b Sample) int { return int(a.AtMs - b.AtMs) })
	if len(out.Samples) > MaxSamples {
		out.Samples = out.Samples[:MaxSamples]
	}
	return out, len(out.Samples) > 0
}

// FromDetail reads the "subjects" member of an asset's quick-look detail (a JSON object) as a clean Track. ok is false when there is none
// or none of it is usable.
func FromDetail(detail json.RawMessage) (Track, bool) {
	if len(detail) == 0 {
		return Track{}, false
	}
	var d struct {
		Subjects *Track `json:"subjects"`
	}
	if err := json.Unmarshal(detail, &d); err != nil || d.Subjects == nil {
		return Track{}, false
	}
	return Clean(*d.Subjects)
}

// FromSubjects reads a track from the bare JSON of the "subjects" member.
func FromSubjects(raw json.RawMessage) (Track, bool) {
	if len(raw) == 0 {
		return Track{}, false
	}
	var t Track
	if err := json.Unmarshal(raw, &t); err != nil {
		return Track{}, false
	}
	return Clean(t)
}

// Near returns the samples that apply to the source range [inMs, outMs]: those taken inside it, or, when none was, the one nearest to
// its middle. Empty for an empty track.
func (t Track) Near(inMs, outMs int64) []Sample {
	var in []Sample
	for _, s := range t.Samples {
		if s.AtMs >= inMs && s.AtMs <= outMs {
			in = append(in, s)
		}
	}
	if len(in) > 0 || len(t.Samples) == 0 {
		return in
	}
	mid := (inMs + outMs) / 2
	best := t.Samples[0]
	for _, s := range t.Samples[1:] {
		if abs64(s.AtMs-mid) < abs64(best.AtMs-mid) {
			best = s
		}
	}
	return []Sample{best}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Head is the head and shoulders of a sample as a box of fractions: the middle 60 % of the subject's width, from the top of the head down
// a fifth of the subject's height. It is the part a title must not cover.
func (s Sample) Head() (x0, y0, x1, y1 float64) {
	cx := s.X + s.W/2
	half := s.W * headCentre / 2
	return cx - half, s.HeadTopY, cx + half, math.Min(s.HeadTopY+s.H*headFraction, s.Y+s.H)
}

// TopPct is the top of the head of the highest sample as a percentage of the frame height, for the editor's line about a clip.
func TopPct(samples []Sample) (pct float64, ok bool) {
	if len(samples) == 0 {
		return 0, false
	}
	top := samples[0].HeadTopY
	for _, s := range samples[1:] {
		top = math.Min(top, s.HeadTopY)
	}
	return math.Round(top * 100), true
}
