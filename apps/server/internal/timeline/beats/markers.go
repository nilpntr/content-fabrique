package beats

import (
	"fmt"
	"sort"
)

// Marker kinds and strengths, matching the timeline schema's marker
// definition (kind, at_ms, strength).
const (
	KindBeat     = "beat"
	KindDownbeat = "downbeat"
	KindDrop     = "drop"

	StrengthDownbeat = 1.0
	StrengthBeat     = 0.6
	StrengthDrop     = 1.0
)

// Marker is a timeline marker as the schema defines it. The JSON field names
// match the schema; strength is always written.
type Marker struct {
	AtMs     int64   `json:"at_ms"`
	Kind     string  `json:"kind"`
	Strength float64 `json:"strength"`
}

// MarkerOpts configures Markers.
type MarkerOpts struct {
	// FPS, when set (24, 25, 30 or 60), puts every marker on a frame, so a
	// marker at a snapped cut has exactly the cut's time. 0 leaves markers at
	// the beat time rounded to a millisecond.
	FPS int
	// DropsMs are the drop times on the timeline (from the sound's metadata,
	// mapped through Placement.ToTimeline). Each becomes a "drop" marker of
	// strength 1.0 next to the beat marker at the same time.
	DropsMs []int64
}

// Markers returns the markers for the timeline range [fromMs, toMs): a
// "downbeat" (strength 1.0) at each downbeat and a "beat" (0.6) at every other
// beat, plus the drops inside the range. The result is sorted by at_ms, with a
// drop after the beat marker at the same time. A downbeat gets one marker,
// not a beat and a downbeat.
//
// The range is the timeline range; use Grid.Place first so the grid is already
// in timeline time. Ranges are checked after alignment, so a marker never
// lands outside [fromMs, toMs) because of frame rounding.
func (g Grid) Markers(fromMs, toMs int64, o MarkerOpts) ([]Marker, error) {
	if o.FPS != 0 {
		if err := checkFPS(o.FPS); err != nil {
			return nil, err
		}
	}
	if fromMs < 0 || toMs < fromMs {
		return nil, fmt.Errorf("beats: marker range [%d, %d) is invalid", fromMs, toMs)
	}
	// Nothing on a timeline is past MaxTimeMs; clamping keeps the arithmetic
	// below far from overflow for a caller that passes math.MaxInt64.
	fromMs = min(fromMs, MaxTimeMs)
	toMs = min(toMs, MaxTimeMs)
	place := func(ms int64) int64 {
		if o.FPS != 0 {
			return AlignMs(ms, o.FPS)
		}
		return ms
	}
	// Alignment moves a beat by under one frame (42 ms at the slowest rate), so
	// widen the search and filter on the final value.
	const margin = 50
	var out []Marker
	for _, b := range g.Beats(max(fromMs-margin, 0), toMs+margin) {
		ms := b.AtMs
		if o.FPS != 0 {
			// From the exact time, like a snapped cut, not from the rounded
			// millisecond: rounding first can flip a frame at a .5 boundary.
			ms = FrameToMs(frameOfExact(b.ExactMs, o.FPS), o.FPS)
		}
		if ms < fromMs || ms >= toMs {
			continue
		}
		m := Marker{AtMs: ms, Kind: KindBeat, Strength: StrengthBeat}
		if b.Downbeat {
			m.Kind, m.Strength = KindDownbeat, StrengthDownbeat
		}
		out = append(out, m)
	}
	for _, d := range o.DropsMs {
		if d < fromMs-margin || d > toMs+margin {
			continue // far outside; also keeps AlignMs away from overflow
		}
		ms := place(d)
		if ms < fromMs || ms >= toMs {
			continue
		}
		out = append(out, Marker{AtMs: ms, Kind: KindDrop, Strength: StrengthDrop})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AtMs != out[j].AtMs {
			return out[i].AtMs < out[j].AtMs
		}
		return (out[i].Kind == KindDrop) != (out[j].Kind == KindDrop) && out[j].Kind == KindDrop
	})
	return out, nil
}
