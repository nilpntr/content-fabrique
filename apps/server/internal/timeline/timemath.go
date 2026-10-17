package timeline

import "math"

// OutputDuration is how long a clip plays on the timeline: floor((out_ms - in_ms) / speed + 0.5), that is, round half
// up (for these non-negative values, the same as round half away from zero). With a speed that is a power of two the
// quotient is exact, so 3001 ms at speed 2 is exactly 1500.5 and rounds to 1501. A clip with out_ms <= in_ms plays
// for 0 ms.
func OutputDuration(it VideoItem) int64 {
	span := it.Source.OutMs - it.Source.InMs
	if span <= 0 {
		return 0
	}
	speed := it.SpeedOrDefault()
	if speed <= 0 || math.IsNaN(speed) {
		return 0
	}
	return int64(math.Floor(float64(span)/speed + 0.5))
}

// MainTrack returns the video track and its index in spec.Tracks. The schema guarantees exactly one; ok is false on a
// spec that has none.
func MainTrack(spec *Spec) (track *VideoTrack, index int, ok bool) {
	if spec == nil {
		return nil, 0, false
	}
	for i, t := range spec.Tracks {
		if v, isVideo := t.(*VideoTrack); isVideo {
			return v, i, true
		}
	}
	return nil, 0, false
}

// Playable reports whether a clip plays at all: its source range has in_ms < out_ms. A clip that does not is left out
// of the length, contiguity and total-duration checks (Validate raises source_out_of_range for it and nothing else).
func Playable(it VideoItem) bool { return it.Source.InMs < it.Source.OutMs }

// EndMs is where a clip ends on the timeline: at_ms plus its output duration.
func EndMs(it VideoItem) int64 { return it.AtMs + OutputDuration(it) }

// PlayableEnd is the latest end of a playable clip in items, or 0 when there is none. It is the one definition of the
// timeline's end, shared by TotalDuration and Validate.
func PlayableEnd(items []VideoItem) int64 {
	var end int64
	for _, it := range items {
		if Playable(it) {
			end = max(end, EndMs(it))
		}
	}
	return end
}

// TotalDuration is the end of the timeline: the latest end (at_ms plus output duration) of a playable clip on the main
// track, or 0 when the track has none.
func TotalDuration(spec *Spec) int64 {
	main, _, ok := MainTrack(spec)
	if !ok {
		return 0
	}
	return PlayableEnd(main.Items)
}

// Frame converts a time to a frame number at fps: floor(ms * fps / 1000 + 0.5), in integers. The compiler snaps item
// boundaries with it from the cumulative at_ms, never from summed durations, so rounding cannot drift (design note
// section 6). Item i covers frames [Frame(at_i), Frame(at_i+1)).
func Frame(ms int64, fps int) int64 {
	// floor((2*ms*fps + 1000) / 2000): the +0.5 is +1000/2000.
	return floorDiv(2*ms*int64(fps)+1000, 2000)
}

// FrameStartMs converts a frame number back to the time it starts at, rounded half up to a millisecond. Frame(
// FrameStartMs(n, fps), fps) == n for every n at the supported frame rates.
func FrameStartMs(frame int64, fps int) int64 {
	if fps <= 0 {
		return 0
	}
	return floorDiv(2*frame*1000+int64(fps), 2*int64(fps))
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
