package beats

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// Limits of a valid grid.
const (
	MinBPM             = 40.0
	MaxBPM             = 240.0
	DefaultBeatsPerBar = 4
	MaxBeatsPerBar     = 16

	// MinBeatGapMs is the closest two beats of an explicit list may be: one
	// beat at MaxBPM.
	MinBeatGapMs = 60000.0 / MaxBPM

	// maxIndex caps a constant-tempo grid: it has beats up to this index
	// (about 16.7 million, far beyond any timeline).
	maxIndex = 1 << 24

	// downbeatMatchMs is how far a downbeat of an explicit list may sit from
	// the beat it belongs to.
	downbeatMatchMs = 1.0
)

// ErrInvalidGrid is returned (wrapped) by the constructors for a grid that
// violates the limits above.
var ErrInvalidGrid = errors.New("beats: invalid grid")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidGrid, fmt.Sprintf(format, args...))
}

// Beat is one beat of a grid.
type Beat struct {
	// Index counts from 0 at the first beat of the grid (after Place, the
	// first beat that is still on the timeline).
	Index int
	// AtMs is ExactMs rounded half up to a millisecond.
	AtMs int64
	// ExactMs is the unrounded time. Snap errors are measured against it.
	ExactMs float64
	// Downbeat is true for the first beat of a bar.
	Downbeat bool

	i int // index in the underlying grid, before rebasing
}

// ConstantSpec describes a constant-tempo grid, the usual shape of provider
// metadata (tempo and first-beat offset).
type ConstantSpec struct {
	// BPM is the tempo, 40 to 240.
	BPM float64
	// FirstBeatMs is the time of the first beat, in ms from the start of the
	// sound. It may be fractional. Must be >= 0.
	FirstBeatMs float64
	// BeatsPerBar defaults to 4. At most 16.
	BeatsPerBar int
	// DownbeatOffset is the number of beats from the first beat to the first
	// downbeat, 0 to BeatsPerBar-1 (default 0: the first beat is a downbeat).
	DownbeatOffset int
}

// ListSpec describes an explicit, possibly irregular beat list, the usual
// shape of analysed tracks (Beat This!, librosa).
type ListSpec struct {
	// BeatsMs are the beat times in ms from the start of the sound: at least
	// two, finite, >= 0, strictly increasing and at least MinBeatGapMs apart.
	BeatsMs []float64
	// DownbeatsMs are the downbeat times. Each must coincide (within 1 ms)
	// with one of the beats. When empty, every BeatsPerBar-th beat starting
	// with the first is a downbeat.
	DownbeatsMs []float64
	// BeatsPerBar defaults to 4. It is used for the inferred downbeats and by
	// Grid.BeatsPerBar.
	BeatsPerBar int
}

// Grid is an immutable beat grid. The zero value has no beats. A Grid is
// either constant tempo (NewConstant) or an explicit list (NewList); both
// answer the same queries.
type Grid struct {
	list bool

	// constant tempo: beat i is at first + shift + i × 60000 / bpm
	bpm     float64
	first   float64
	downOff int

	// explicit list: beat i is at times[i] + shift
	times []float64
	down  []bool

	bpb   int
	shift float64

	// lo and hi are the first and last valid index (inclusive). A grid with
	// hi < lo is empty.
	lo, hi int
}

// NewConstant validates spec and returns a constant-tempo grid.
func NewConstant(spec ConstantSpec) (Grid, error) {
	if math.IsNaN(spec.BPM) || spec.BPM < MinBPM || spec.BPM > MaxBPM {
		return Grid{}, invalidf("bpm %v outside %v to %v", spec.BPM, MinBPM, MaxBPM)
	}
	if math.IsNaN(spec.FirstBeatMs) || math.IsInf(spec.FirstBeatMs, 0) || spec.FirstBeatMs < 0 {
		return Grid{}, invalidf("first beat %v must be a finite number >= 0", spec.FirstBeatMs)
	}
	bpb := spec.BeatsPerBar
	if bpb == 0 {
		bpb = DefaultBeatsPerBar
	}
	if bpb < 1 || bpb > MaxBeatsPerBar {
		return Grid{}, invalidf("beats per bar %d outside 1 to %d", bpb, MaxBeatsPerBar)
	}
	if spec.DownbeatOffset < 0 || spec.DownbeatOffset >= bpb {
		return Grid{}, invalidf("downbeat offset %d outside 0 to %d", spec.DownbeatOffset, bpb-1)
	}
	return Grid{
		bpm:     spec.BPM,
		first:   spec.FirstBeatMs,
		downOff: spec.DownbeatOffset,
		bpb:     bpb,
		lo:      0,
		hi:      maxIndex,
	}, nil
}

// NewList validates spec and returns an explicit-list grid. The input slices
// are copied.
func NewList(spec ListSpec) (Grid, error) {
	n := len(spec.BeatsMs)
	if n < 2 {
		return Grid{}, invalidf("need at least 2 beats, got %d", n)
	}
	bpb := spec.BeatsPerBar
	if bpb == 0 {
		bpb = DefaultBeatsPerBar
	}
	if bpb < 1 || bpb > MaxBeatsPerBar {
		return Grid{}, invalidf("beats per bar %d outside 1 to %d", bpb, MaxBeatsPerBar)
	}
	times := make([]float64, n)
	for i, t := range spec.BeatsMs {
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 {
			return Grid{}, invalidf("beat %d (%v) must be a finite number >= 0", i, t)
		}
		if i > 0 {
			gap := t - spec.BeatsMs[i-1]
			if gap <= 0 {
				return Grid{}, invalidf("beats must be strictly increasing: beat %d (%v) is not after %v", i, t, spec.BeatsMs[i-1])
			}
			if gap < MinBeatGapMs {
				return Grid{}, invalidf("beats %d and %d are %.1f ms apart, closer than %.0f ms (%v bpm)", i-1, i, gap, MinBeatGapMs, MaxBPM)
			}
		}
		times[i] = t
	}
	down := make([]bool, n)
	if len(spec.DownbeatsMs) == 0 {
		for i := 0; i < n; i += bpb {
			down[i] = true
		}
	} else {
		for k, d := range spec.DownbeatsMs {
			if math.IsNaN(d) || math.IsInf(d, 0) {
				return Grid{}, invalidf("downbeat %d (%v) is not finite", k, d)
			}
			j := sort.SearchFloat64s(times, d)
			best := -1
			for _, c := range []int{j - 1, j} {
				if c >= 0 && c < n && math.Abs(times[c]-d) <= downbeatMatchMs {
					if best < 0 || math.Abs(times[c]-d) < math.Abs(times[best]-d) {
						best = c
					}
				}
			}
			if best < 0 {
				return Grid{}, invalidf("downbeat %d (%v) is not on a beat", k, d)
			}
			down[best] = true
		}
	}
	return Grid{list: true, times: times, down: down, bpb: bpb, lo: 0, hi: n - 1}, nil
}

// IsConstant reports whether the grid has a constant tempo.
func (g Grid) IsConstant() bool { return !g.list && g.bpm > 0 }

// BPM returns the tempo of a constant grid, or the mean tempo of a list.
func (g Grid) BPM() float64 {
	if !g.list {
		return g.bpm
	}
	if len(g.times) < 2 {
		return 0
	}
	return 60000 * float64(len(g.times)-1) / (g.times[len(g.times)-1] - g.times[0])
}

// BeatsPerBar returns the bar length in beats.
func (g Grid) BeatsPerBar() int { return g.bpb }

// Empty reports whether the grid has no beats.
func (g Grid) Empty() bool { return (!g.list && g.bpm == 0) || g.hi < g.lo }

// at is the exact time of the underlying beat i.
func (g Grid) at(i int) float64 {
	if g.list {
		return g.times[i] + g.shift
	}
	return (g.first + g.shift) + float64(i)*60000/g.bpm
}

func (g Grid) isDown(i int) bool {
	if g.list {
		return g.down[i]
	}
	r := (i - g.downOff) % g.bpb
	if r < 0 {
		r += g.bpb
	}
	return r == 0
}

func (g Grid) beat(i int) Beat {
	x := g.at(i)
	return Beat{Index: i - g.lo, AtMs: roundMs(x), ExactMs: x, Downbeat: g.isDown(i), i: i}
}

// lowerBound is the first valid index whose time satisfies at(i) >= t, or
// hi+1 when there is none.
func (g Grid) lowerBound(t float64) int {
	if g.Empty() {
		return g.lo
	}
	n := g.hi - g.lo + 1
	return g.lo + sort.Search(n, func(k int) bool { return g.at(g.lo+k) >= t })
}

// upperBound is the first valid index whose time satisfies at(i) > t, or
// hi+1 when there is none.
func (g Grid) upperBound(t float64) int {
	if g.Empty() {
		return g.lo
	}
	n := g.hi - g.lo + 1
	return g.lo + sort.Search(n, func(k int) bool { return g.at(g.lo+k) > t })
}

// Beats returns the beats whose rounded time lies in [fromMs, toMs), in time
// order.
func (g Grid) Beats(fromMs, toMs int64) []Beat {
	// round(x) >= from  <=>  x >= from - 0.5, and round(x) < to  <=>  x < to - 0.5.
	a := g.lowerBound(float64(fromMs) - 0.5)
	b := g.lowerBound(float64(toMs) - 0.5)
	if b <= a {
		return nil
	}
	out := make([]Beat, 0, b-a)
	for i := a; i < b; i++ {
		out = append(out, g.beat(i))
	}
	return out
}

// Downbeats returns the downbeats in [fromMs, toMs), in time order.
func (g Grid) Downbeats(fromMs, toMs int64) []Beat {
	var out []Beat
	for _, b := range g.Beats(fromMs, toMs) {
		if b.Downbeat {
			out = append(out, b)
		}
	}
	return out
}

// Nearest returns the beat closest to ms, measured against the unrounded beat
// time. A tie goes to the earlier beat. ok is false for an empty grid.
func (g Grid) Nearest(ms int64) (b Beat, ok bool) {
	return g.nearest(float64(ms))
}

func (g Grid) nearest(t float64) (Beat, bool) {
	if g.Empty() {
		return Beat{}, false
	}
	j := g.lowerBound(t)
	switch {
	case j > g.hi:
		return g.beat(g.hi), true
	case j == g.lo:
		return g.beat(g.lo), true
	}
	if t-g.at(j-1) <= g.at(j)-t {
		return g.beat(j - 1), true
	}
	return g.beat(j), true
}

// NearestDownbeat is Nearest restricted to downbeats.
func (g Grid) NearestDownbeat(ms int64) (b Beat, ok bool) {
	t := float64(ms)
	before, okB := g.scanDown(g.lowerBound(t)-1, -1)
	after, okA := g.scanDown(g.lowerBound(t), +1)
	switch {
	case okB && okA:
		if t-before.ExactMs <= after.ExactMs-t {
			return before, true
		}
		return after, true
	case okB:
		return before, true
	case okA:
		return after, true
	}
	return Beat{}, false
}

// scanDown walks from index i in direction dir to the first downbeat.
func (g Grid) scanDown(i, dir int) (Beat, bool) {
	for ; i >= g.lo && i <= g.hi; i += dir {
		if g.isDown(i) {
			return g.beat(i), true
		}
	}
	return Beat{}, false
}

// Next returns the first beat strictly after ms (by rounded time).
func (g Grid) Next(ms int64) (b Beat, ok bool) {
	// round(x) > ms  <=>  x >= ms + 0.5
	j := g.lowerBound(float64(ms) + 0.5)
	if j > g.hi {
		return Beat{}, false
	}
	return g.beat(j), true
}

// Prev returns the last beat strictly before ms (by rounded time).
func (g Grid) Prev(ms int64) (b Beat, ok bool) {
	// round(x) < ms  <=>  x < ms - 0.5
	j := g.lowerBound(float64(ms)-0.5) - 1
	if j < g.lo {
		return Beat{}, false
	}
	return g.beat(j), true
}

// localInterval is the gap between the beats around t, used to turn a shift in
// beats into milliseconds. It is 0 when the grid has fewer than two beats.
func (g Grid) localInterval(t float64) float64 {
	if g.Empty() || g.hi-g.lo < 1 {
		return 0
	}
	j := g.lowerBound(t)
	switch {
	case j <= g.lo:
		return g.at(g.lo+1) - g.at(g.lo)
	case j > g.hi:
		return g.at(g.hi) - g.at(g.hi-1)
	}
	return g.at(j) - g.at(j-1)
}

// pos converts a time to a fractional beat position in the underlying index
// space (beat i is at position i). Between two beats it interpolates linearly;
// outside an explicit list it extrapolates with the interval at that end.
func (g Grid) pos(t float64) float64 {
	if !g.list {
		return (t - (g.first + g.shift)) * g.bpm / 60000
	}
	n := len(g.times)
	k := sort.Search(n, func(i int) bool { return g.times[i]+g.shift > t }) // first beat after t
	switch {
	case k == 0:
		k = 1
	case k >= n:
		k = n - 1
	}
	a, b := g.times[k-1]+g.shift, g.times[k]+g.shift
	return float64(k-1) + (t-a)/(b-a)
}

// timeAt is the inverse of pos.
func (g Grid) timeAt(p float64) float64 {
	if !g.list {
		return (g.first + g.shift) + p*60000/g.bpm
	}
	n := len(g.times)
	k := int(math.Floor(p))
	k = max(0, min(k, n-2))
	a, b := g.times[k]+g.shift, g.times[k+1]+g.shift
	return a + (p-float64(k))*(b-a)
}

// Placement puts a sound on the timeline: the sound's time source_offset_ms
// plays at timeline time at_ms (the popular-snippet start of a trend sound).
type Placement struct {
	// AtMs is where the sound starts on the timeline (audio item at_ms).
	AtMs int64
	// SourceOffsetMs is where in the sound playback starts (source_offset_ms).
	SourceOffsetMs int64
	// DurationMs is how long the sound plays; 0 means until the end of the
	// timeline.
	DurationMs int64
}

// ToTimeline maps a time in the sound to the timeline.
func (p Placement) ToTimeline(soundMs int64) int64 { return p.AtMs + soundMs - p.SourceOffsetMs }

// ToSound maps a timeline time to the time in the sound.
func (p Placement) ToSound(timelineMs int64) int64 { return timelineMs - p.AtMs + p.SourceOffsetMs }

// Place returns the grid on the timeline for a sound placed with p. Beat times
// move by AtMs - SourceOffsetMs, and beats that are not heard (before AtMs, or
// at or after AtMs+DurationMs when DurationMs > 0) are dropped. The phase of
// the bars is kept: a downbeat in the sound is still a downbeat. Beat.Index
// counts from the first audible beat. Place can be applied more than once;
// each placement maps the previous timeline.
func (g Grid) Place(p Placement) (Grid, error) {
	if p.AtMs < 0 || p.SourceOffsetMs < 0 || p.DurationMs < 0 {
		return Grid{}, invalidf("placement at %d, offset %d, duration %d: all must be >= 0", p.AtMs, p.SourceOffsetMs, p.DurationMs)
	}
	out := g
	out.shift += float64(p.AtMs - p.SourceOffsetMs)
	out.lo = out.lowerBound(float64(p.AtMs))
	if p.DurationMs > 0 {
		out.hi = out.lowerBound(float64(p.AtMs+p.DurationMs)) - 1
	}
	return out, nil
}
