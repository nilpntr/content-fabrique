package beats

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// DefaultMinShotMs is the shortest shot the schema allows (an output duration
// of at least 300 ms), used when SnapOpts.MinShotMs is 0.
const DefaultMinShotMs = 300

// defaultMaxShiftBeats is the shift limit when neither limit is set: half a
// beat, which on a regular grid means "always the nearest beat".
const defaultMaxShiftBeats = 0.5

// maxShiftCapMs bounds the search window around a cut, so a huge limit can't
// make a snap expensive. Real edits never want to move a cut a minute.
const maxShiftCapMs = 60000

// ErrInvalidSnap is returned (wrapped) by SnapCuts for options or cuts that
// can't be snapped.
var ErrInvalidSnap = errors.New("beats: invalid snap input")

// Reason says why a cut was not snapped.
type Reason string

// Reasons a cut stays where it was.
const (
	// ReasonNone: the cut was snapped.
	ReasonNone Reason = ""
	// ReasonOutOfRange: no beat within the maximum shift.
	ReasonOutOfRange Reason = "out_of_range"
	// ReasonMinShot: a beat was in range, but moving there would make a shot
	// shorter than the minimum (or cross or merge a neighbouring cut).
	ReasonMinShot Reason = "min_shot"
)

// SnapOpts configures SnapCuts.
type SnapOpts struct {
	// FPS is the output frame rate: 24, 25, 30 or 60. Required.
	FPS int

	// MaxShiftMs and MaxShiftBeats limit how far a cut may move to reach a
	// beat. A beat is the local gap between the grid beats around the cut. When
	// both are set the smaller applies; when neither is set the limit is half a
	// beat. A limit below one frame counts as one frame (that is the precision
	// of the alignment), and a limit above 60 s as 60 s.
	MaxShiftMs    int64
	MaxShiftBeats float64

	// PreferDownbeats picks the nearest downbeat within the maximum shift when
	// there is one, and the nearest beat otherwise. A cut that is already on a
	// beat (within one frame) is not moved to a downbeat.
	PreferDownbeats bool

	// MinShotMs is the shortest shot to keep (default 300). A shot that was
	// already shorter than this in the input is never made shorter than it was.
	MinShotMs int64

	// StartMs is where the first shot starts (default 0) and EndMs where the
	// last shot ends (0: open-ended). They bound the first and last shot for
	// the minimum shot length; they are not cuts and are never moved.
	StartMs int64
	EndMs   int64

	// Order is always kept: SnapCuts never moves a cut across, or onto, its
	// neighbour. There is no option to turn that off.
}

// Snap is the outcome for one cut. The slice SnapCuts returns has one Snap per
// input cut, in the same order.
type Snap struct {
	// InputMs is the cut as given.
	InputMs int64
	// AtMs is the cut after snapping. When Snapped it is frame-aligned. When
	// not, it is InputMs, unchanged.
	AtMs int64
	// Frame is the frame of AtMs, the renderer's frame(AtMs).
	Frame int64
	// ShiftMs is AtMs - InputMs.
	ShiftMs int64

	// Snapped is true when the cut sits on a beat.
	Snapped bool
	// Reason says why the cut was not snapped. It is empty when Snapped.
	Reason Reason

	// The fields below are set only when Snapped.

	// BeatMs is the exact (unrounded) time of the target beat.
	BeatMs float64
	// BeatIndex and Downbeat describe the target beat (Beat.Index, Beat.Downbeat).
	BeatIndex int
	Downbeat  bool
	// ErrorMs is AtMs - BeatMs; its magnitude is less than one frame. ErrorFrames
	// is the same in frames.
	ErrorMs     float64
	ErrorFrames float64
}

// SnapCuts moves each cut to a beat of grid and returns one Snap per cut. cuts
// are the interior cut times (the at_ms of main items 1..n) in strictly
// increasing order.
//
// For a cut at t the target is the beat chosen as follows: if a beat is within
// one frame of t, that beat; otherwise, with PreferDownbeats, the nearest
// downbeat within the maximum shift, else the nearest beat within the maximum
// shift. The cut goes to frame(beat) and its time is FrameToMs of that frame,
// so it is frame-aligned and off the exact beat by less than one frame (see the
// package comment). Distances are measured from t, as given.
//
// A cut stays exactly where it is (Snapped false, with a Reason) when no beat
// is in range, or when every beat in range would break the shot rule. The shot
// rule: after snapping, each shot is at least MinShotMs long, or at least as
// long as it was if it started shorter. The rule is checked against the
// neighbours, so cuts never cross or merge. When the first-choice beat doesn't
// fit, the next-best beat in range is tried.
//
// Cuts are processed left to right, and the result is repeated on its own
// output until it stops changing (a refused cut can become possible once its
// neighbour has moved), so SnapCuts is idempotent: snapping the result again
// changes nothing. A cut that was snapped never moves again, and a cut moves at
// most once, so at most len(cuts)+1 passes run.
func SnapCuts(cuts []int64, grid Grid, opts SnapOpts) ([]Snap, error) {
	s, err := newSnapper(grid, opts, cuts)
	if err != nil {
		return nil, err
	}

	cur := append([]int64(nil), cuts...)
	rec := make([]Snap, len(cuts))
	done := make([]bool, len(cuts))
	reasons := make([]Reason, len(cuts))

	for pass := 0; pass <= len(cuts)+1; pass++ {
		next, ch := s.pass(cur)
		changed := false
		for i := range cur {
			if next[i] != cur[i] {
				changed = true
			}
			if ch[i].ok && !done[i] {
				done[i] = true
				rec[i] = s.snapped(cuts[i], next[i], ch[i].beat)
			}
			if !ch[i].ok {
				reasons[i] = ch[i].reason
			}
		}
		cur = next
		if !changed {
			break
		}
	}

	out := make([]Snap, len(cuts))
	for i, c := range cuts {
		if done[i] {
			out[i] = rec[i]
			continue
		}
		out[i] = Snap{
			InputMs: c,
			AtMs:    c,
			Frame:   MsToFrame(c, s.fps),
			Reason:  reasons[i],
		}
	}
	return out, nil
}

type snapper struct {
	g       Grid
	fps     int
	frameMs float64
	minShot int64
	start   int64
	end     int64
	hasEnd  bool
	o       SnapOpts
}

func newSnapper(g Grid, o SnapOpts, cuts []int64) (*snapper, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidSnap, fmt.Sprintf(format, args...))
	}
	if err := checkFPS(o.FPS); err != nil {
		return nil, err
	}
	if o.MaxShiftMs < 0 || o.MaxShiftBeats < 0 || math.IsNaN(o.MaxShiftBeats) || math.IsInf(o.MaxShiftBeats, 0) {
		return nil, bad("max shift must be >= 0 (got %d ms, %v beats)", o.MaxShiftMs, o.MaxShiftBeats)
	}
	if o.MinShotMs < 0 || o.StartMs < 0 || o.EndMs < 0 {
		return nil, bad("min shot, start and end must be >= 0")
	}
	if o.EndMs != 0 && o.EndMs <= o.StartMs {
		return nil, bad("end %d must be after start %d", o.EndMs, o.StartMs)
	}
	s := &snapper{
		g:       g,
		fps:     o.FPS,
		frameMs: 1000 / float64(o.FPS),
		minShot: o.MinShotMs,
		start:   o.StartMs,
		end:     o.EndMs,
		hasEnd:  o.EndMs != 0,
		o:       o,
	}
	if s.minShot == 0 {
		s.minShot = DefaultMinShotMs
	}
	prev := s.start
	for i, c := range cuts {
		if c <= prev {
			return nil, bad("cut %d (%d ms) must be after %d ms", i, c, prev)
		}
		prev = c
	}
	if s.hasEnd && prev >= s.end {
		return nil, bad("last cut (%d ms) must be before the end (%d ms)", prev, s.end)
	}
	return s, nil
}

type choice struct {
	ok     bool
	beat   Beat
	reason Reason
}

// req is the length the shot from a to b must keep.
func (s *snapper) req(a, b int64) int64 { return min(s.minShot, b-a) }

// pass runs one left-to-right sweep over cur and returns the new positions and
// what happened to each cut.
//
// The bounds for cut i are L = (new previous cut) + the shot rule and
// U = (next cut as it is now) - the shot rule. U leaves the next cut a place it
// can always stay at, and cur[i] itself is always inside [L, U]: so "stay put"
// is always a valid answer and the sweep can't paint itself into a corner.
func (s *snapper) pass(cur []int64) ([]int64, []choice) {
	n := len(cur)
	next := make([]int64, n)
	ch := make([]choice, n)
	for i := range cur {
		prevCur, prevNew := s.start, s.start
		if i > 0 {
			prevCur, prevNew = cur[i-1], next[i-1]
		}
		lower := prevNew + s.req(prevCur, cur[i])
		upper := int64(math.MaxInt64)
		switch {
		case i+1 < n:
			upper = cur[i+1] - s.req(cur[i], cur[i+1])
		case s.hasEnd:
			upper = s.end - s.req(cur[i], s.end)
		}

		next[i] = cur[i]
		t := float64(cur[i])
		cands := s.candidates(t)
		if len(cands) == 0 {
			ch[i] = choice{reason: ReasonOutOfRange}
			continue
		}
		ch[i] = choice{reason: ReasonMinShot}
		for _, b := range cands {
			ms := FrameToMs(frameOfExact(b.ExactMs, s.fps), s.fps)
			if ms >= lower && ms <= upper {
				next[i] = ms
				ch[i] = choice{ok: true, beat: b}
				break
			}
		}
	}
	return next, ch
}

// window is the largest distance a cut at t may move.
func (s *snapper) window(t float64) float64 {
	w := math.Inf(1)
	if s.o.MaxShiftMs > 0 {
		w = float64(s.o.MaxShiftMs)
	}
	if s.o.MaxShiftBeats > 0 || s.o.MaxShiftMs == 0 {
		beats := s.o.MaxShiftBeats
		if beats == 0 {
			beats = defaultMaxShiftBeats
		}
		w = min(w, beats*s.g.localInterval(t))
	}
	return min(max(w, s.frameMs), maxShiftCapMs)
}

// candidates lists the beats a cut at t may snap to, best first.
func (s *snapper) candidates(t float64) []Beat {
	near, ok := s.g.nearest(t)
	if !ok {
		return nil
	}
	if math.Abs(near.ExactMs-t) <= s.frameMs {
		return []Beat{near}
	}
	w := s.window(t)
	a, b := s.g.lowerBound(t-w), s.g.upperBound(t+w)
	if b <= a {
		return nil
	}
	cands := make([]Beat, 0, b-a)
	for i := a; i < b; i++ {
		cands = append(cands, s.g.beat(i))
	}
	dist := func(x Beat) float64 { return math.Abs(x.ExactMs - t) }
	sort.SliceStable(cands, func(x, y int) bool {
		if s.o.PreferDownbeats && cands[x].Downbeat != cands[y].Downbeat {
			return cands[x].Downbeat
		}
		return dist(cands[x]) < dist(cands[y])
	})
	return cands
}

func (s *snapper) snapped(input, at int64, b Beat) Snap {
	errMs := float64(at) - b.ExactMs
	return Snap{
		InputMs:     input,
		AtMs:        at,
		Frame:       MsToFrame(at, s.fps),
		ShiftMs:     at - input,
		Snapped:     true,
		BeatMs:      b.ExactMs,
		BeatIndex:   b.Index,
		Downbeat:    b.Downbeat,
		ErrorMs:     errMs,
		ErrorFrames: errMs * float64(s.fps) / 1000,
	}
}
