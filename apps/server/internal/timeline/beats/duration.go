package beats

import (
	"fmt"
	"math"
)

// ErrNoFit is returned (wrapped) by FitDuration when no beat gives a duration
// inside the bounds.
var ErrNoFit = fmt.Errorf("beats: no beat fits the bounds")

// DurationForBeats returns how long `beats` beats last when they start at
// startMs, in whole milliseconds. beats may be fractional (0.5 is an eighth
// note at 4/4).
//
// The start does not have to be on a beat. The span is measured in the grid's
// beat positions: end = timeAt(pos(startMs) + beats), so on a constant tempo
//
//	duration = round(startMs + beats × 60000 / bpm) - startMs
//
// with a single rounding of the end, which keeps long spans from drifting
// (adding a rounded beat length n times would be off by up to n/2 ms). On an
// explicit list the position is interpolated between beats, and extrapolated
// with the first or last gap outside the list. An empty grid, a beat count that
// is not positive and finite, and a span that rounds to 0 ms or less are
// errors.
func DurationForBeats(g Grid, startMs int64, beats float64) (int64, error) {
	if g.Empty() {
		return 0, invalidf("empty grid")
	}
	if math.IsNaN(beats) || math.IsInf(beats, 0) || beats <= 0 {
		return 0, fmt.Errorf("beats: beat count %v must be > 0", beats)
	}
	if startMs < 0 || startMs > MaxTimeMs {
		return 0, fmt.Errorf("beats: start %d outside 0 to %d", startMs, MaxTimeMs)
	}
	end := g.timeAt(g.pos(float64(startMs)) + beats)
	if end > float64(math.MaxInt64/2) {
		return 0, fmt.Errorf("beats: %v beats from %d ms is too long", beats, startMs)
	}
	d := roundMs(end) - startMs
	if d <= 0 {
		return 0, fmt.Errorf("beats: %v beats from %d ms is %d ms, less than 1 ms", beats, startMs, d)
	}
	return d, nil
}

// Unit is what FitDuration ends a duration on.
type Unit int

// Units of FitDuration.
const (
	// UnitBeat ends on any beat.
	UnitBeat Unit = iota
	// UnitBar ends on a downbeat.
	UnitBar
)

// FitOpts configures FitDuration.
type FitOpts struct {
	// FPS is the output frame rate: 24, 25, 30 or 60. Required.
	FPS int
	// Unit is beat (default) or bar.
	Unit Unit
	// MinMs and MaxMs bound the resulting duration. MinMs defaults to one
	// frame; MaxMs 0 means no upper bound.
	MinMs, MaxMs int64
}

// Fit is the result of FitDuration.
type Fit struct {
	// StartMs is the start, aligned to its frame.
	StartMs int64
	// EndMs is the end: frame-aligned and on the chosen beat.
	EndMs int64
	// DurationMs is EndMs - StartMs.
	DurationMs int64
	// Beats is how many beats the duration spans (fractional if the start is
	// off the beat).
	Beats float64
	// EndBeat is the beat the duration ends on.
	EndBeat Beat
	// EndErrorMs is EndMs - EndBeat.ExactMs; its magnitude is under one frame.
	EndErrorMs float64
	// MissMs is DurationMs - the requested target.
	MissMs int64
}

// FitDuration finds the duration closest to targetMs that ends on a beat (or a
// bar, with UnitBar), measured from startMs.
//
// Both ends are frame-aligned: the start is aligned first (Fit.StartMs) and
// the end is placed on frame(beat) like a snapped cut, so the end is within
// one frame of the exact beat and the duration is the difference of the two
// aligned times. The search starts at the beats on either side of the target
// end (clamped into the bounds) and walks outward in both directions over every
// beat whose aligned duration may still lie in [MinMs, MaxMs]; the one whose
// duration is nearest the target wins and a tie goes to the shorter one. Bounds
// are checked on the aligned durations, so a beat that is just outside the
// bounds before alignment but inside after it is found, and one that is inside
// before but outside after is skipped. ErrNoFit means no beat gives a duration
// in the bounds.
func FitDuration(g Grid, startMs, targetMs int64, o FitOpts) (Fit, error) {
	if err := checkFPS(o.FPS); err != nil {
		return Fit{}, err
	}
	if g.Empty() {
		return Fit{}, invalidf("empty grid")
	}
	if targetMs <= 0 || startMs < 0 || startMs > MaxTimeMs || targetMs > MaxTimeMs || o.MinMs > MaxTimeMs || o.MaxMs > MaxTimeMs {
		return Fit{}, fmt.Errorf("beats: start %d and target %d must be 0 to %d (target > 0), bounds at most %d", startMs, targetMs, MaxTimeMs, MaxTimeMs)
	}
	if o.MinMs < 0 || o.MaxMs < 0 || (o.MaxMs != 0 && o.MaxMs < o.MinMs) {
		return Fit{}, fmt.Errorf("beats: bounds min %d, max %d are inconsistent", o.MinMs, o.MaxMs)
	}
	startFrame := MsToFrame(startMs, o.FPS)
	start := FrameToMs(startFrame, o.FPS)
	bar := o.Unit == UnitBar

	// Clamp the wanted end into the bounds, then look at the beats around it.
	wantEnd := float64(start + targetMs)
	wantEnd = math.Max(wantEnd, float64(start+o.MinMs))
	if o.MaxMs > 0 {
		wantEnd = math.Min(wantEnd, float64(start+o.MaxMs))
	}

	var best *Fit
	// consider returns the duration of b when it is valid.
	consider := func(b Beat) (int64, bool) {
		endFrame := frameOfExact(b.ExactMs, o.FPS)
		if endFrame <= startFrame {
			return 0, false
		}
		end := FrameToMs(endFrame, o.FPS)
		dur := end - start
		if dur < o.MinMs || (o.MaxMs > 0 && dur > o.MaxMs) {
			return 0, false
		}
		cand := Fit{
			StartMs:    start,
			EndMs:      end,
			DurationMs: dur,
			Beats:      g.pos(b.ExactMs) - g.pos(float64(start)),
			EndBeat:    b,
			EndErrorMs: float64(end) - b.ExactMs,
			MissMs:     dur - targetMs,
		}
		if best == nil || absInt(cand.MissMs) < absInt(best.MissMs) ||
			(absInt(cand.MissMs) == absInt(best.MissMs) && cand.DurationMs < best.DurationMs) {
			best = &cand
		}
		return dur, true
	}

	// Alignment moves an end by under one frame, so a beat can only be valid
	// when its exact time is within a frame of the bounds.
	slack := 1000/float64(o.FPS) + 1
	lowest := float64(start+o.MinMs) - slack
	highest := math.Inf(1)
	if o.MaxMs > 0 {
		highest = float64(start+o.MaxMs) + slack
	}
	before, after, okB, okA := g.around(wantEnd, bar)
	if okB {
		g.walk(before.i, -1, bar, func(b Beat) bool {
			if b.ExactMs < lowest {
				return false
			}
			dur, ok := consider(b)
			return !ok || dur > targetMs // further down only gets farther from the target
		})
	}
	if okA {
		g.walk(after.i, +1, bar, func(b Beat) bool {
			if b.ExactMs > highest {
				return false
			}
			dur, ok := consider(b)
			return !ok || dur < targetMs
		})
	}
	if best == nil {
		return Fit{}, fmt.Errorf("%w: start %d ms, target %d ms, bounds %d to %d ms", ErrNoFit, startMs, targetMs, o.MinMs, o.MaxMs)
	}
	return *best, nil
}

func absInt(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
