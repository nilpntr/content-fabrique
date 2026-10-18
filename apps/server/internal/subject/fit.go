package subject

import (
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Fitting a text in time (P6-06): a text is never drawn over the person's body. When PlaceTextAvoiding finds no place for it over its own
// time (no room above the head, beside it or below the person while it is on screen), the text is shown where it fits instead: first in
// the part of its own time that has room (a title over two shots, one of them a close-up, keeps the other), then at the nearest other time
// of the same length that has room (another shot), and only as the very last resort it is left out. The renderer and check_edit both call
// FitText, so the check says what the render does.

// How a text was fitted.
const (
	// FitInPlace: shown at its own time (moved or shrunk in the frame as PlaceTextAvoiding says, or not at all).
	FitInPlace = "in_place"
	// FitTrimmed: shown in the part of its own time where it fits.
	FitTrimmed = "trimmed"
	// FitMoved: shown at another time of the same length, the nearest where it fits.
	FitMoved = "moved"
	// FitDropped: there is no time it fits; it is left out.
	FitDropped = "dropped"
)

// Fitting steps and bounds.
const (
	// fitStepMs is the grain of the search in time.
	fitStepMs = 100
	// minTrimMs is the shortest part of its own time a trimmed text keeps (and at least half its length): a title that flashes up is worse
	// than one shown at another time.
	minTrimMs = 800
	// MinTextMs is the shortest a text may be (the timeline's own minimum).
	MinTextMs = 300
)

// Fit is where a text is shown.
type Fit struct {
	// How is one of FitInPlace, FitTrimmed, FitMoved and FitDropped.
	How string
	// AtMs and DurationMs are the time it is shown (its own for FitInPlace; meaningless for FitDropped).
	AtMs, DurationMs int64
	// Placement and Avoidance are PlaceTextAvoiding's result at that time (for FitDropped, at its own time).
	Placement timeline.TextPlacement
	Avoidance timeline.Avoidance
	// Regions are the subjects at that time.
	Regions []timeline.Subject
}

// FitText says where and when the text item it is shown: at its own time when PlaceTextAvoiding clears it there, else trimmed, moved or
// dropped (see the constants). An item that does not avoid the subject, or a spec with no subject boxes for the clips under it, is shown in
// place. totalMs is the length of the timeline.
func FitText(spec *timeline.Spec, lookup Lookup, it timeline.TextItem, totalMs int64) (Fit, error) {
	out := spec.Output
	place := func(at, dur int64) (Fit, error) {
		var regions []timeline.Subject
		if it.AvoidsSubject() && lookup != nil {
			regions = Regions(spec, lookup, at, at+dur)
		}
		cand := it
		cand.AtMs, cand.DurationMs = at, dur
		pl, av, err := timeline.PlaceTextAvoiding(out, cand, regions)
		return Fit{AtMs: at, DurationMs: dur, Placement: pl, Avoidance: av, Regions: regions}, err
	}
	own, err := place(it.AtMs, it.DurationMs)
	if err != nil || own.Avoidance.Cleared {
		own.How = FitInPlace
		return own, err
	}
	// 1. The longest part of its own time where it fits, kept when it is long enough.
	need := max(min(it.DurationMs, minTrimMs), it.DurationMs/2, MinTextMs)
	if f, ok := longestFit(place, it.AtMs, it.AtMs+it.DurationMs, need); ok {
		f.How = FitTrimmed
		return f, nil
	}
	// 2. The nearest other time of the same length where it fits, inside the timeline.
	last := totalMs - it.DurationMs
	for d := int64(fitStepMs); d <= max(it.AtMs, last-it.AtMs); d += fitStepMs {
		for _, at := range []int64{it.AtMs + d, it.AtMs - d} {
			if at < 0 || at > last {
				continue
			}
			f, err := place(at, it.DurationMs)
			if err != nil {
				return own, err
			}
			if f.Avoidance.Cleared {
				f.How = FitMoved
				return f, nil
			}
		}
	}
	own.How = FitDropped
	return own, nil
}

// longestFit finds the longest window inside [from, to) where the text fits, at least need long: the steps where it fits alone, the longest
// run of them, and that run checked as a whole (shortened from its ends until it fits, the subjects of a longer window being more).
func longestFit(place func(at, dur int64) (Fit, error), from, to, need int64) (Fit, bool) {
	var ok []bool
	for t := from; t < to; t += fitStepMs {
		f, err := place(t, min(fitStepMs, to-t))
		ok = append(ok, err == nil && f.Avoidance.Cleared)
	}
	bestI, bestN := -1, 0
	for i := 0; i < len(ok); {
		if !ok[i] {
			i++
			continue
		}
		j := i
		for j < len(ok) && ok[j] {
			j++
		}
		if j-i > bestN {
			bestI, bestN = i, j-i
		}
		i = j
	}
	if bestI < 0 {
		return Fit{}, false
	}
	start, end := from+int64(bestI)*fitStepMs, min(from+int64(bestI+bestN)*fitStepMs, to)
	// The longest window first, the earliest of a length first (a title keeps its entrance); at most maxFitTries placements.
	tries := 0
	for l := end - start; l >= need; l -= fitStepMs {
		for at := start; at+l <= end; at += fitStepMs {
			if tries++; tries > maxFitTries {
				return Fit{}, false
			}
			if f, err := place(at, l); err == nil && f.Avoidance.Cleared {
				return f, true
			}
		}
	}
	return Fit{}, false
}

// maxFitTries bounds the placements longestFit makes for one text.
const maxFitTries = 200
