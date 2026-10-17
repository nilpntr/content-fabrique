package beats

import (
	"errors"
	"fmt"
	"math"
)

// SupportedFPS lists the output frame rates of the timeline schema. The rates
// are integers; fractional rates such as 29.97 are not supported.
var SupportedFPS = [...]int{24, 25, 30, 60}

// ErrInvalidFPS is returned (wrapped) when a frame rate is not in SupportedFPS.
var ErrInvalidFPS = errors.New("beats: unsupported fps")

// ValidFPS reports whether fps is one of SupportedFPS.
func ValidFPS(fps int) bool {
	for _, v := range SupportedFPS {
		if v == fps {
			return true
		}
	}
	return false
}

func checkFPS(fps int) error {
	if !ValidFPS(fps) {
		return fmt.Errorf("%w: %d (want 24, 25, 30 or 60)", ErrInvalidFPS, fps)
	}
	return nil
}

// floorDiv is integer division that rounds toward negative infinity.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// MsToFrame returns frame(ms) = floor(ms × fps / 1000 + 0.5), computed exactly
// as floor((2 × ms × fps + 1000) / 2000).
func MsToFrame(ms int64, fps int) int64 {
	return floorDiv(2*ms*int64(fps)+1000, 2000)
}

// FrameToMs returns the millisecond of a frame, floor(frame × 1000 / fps + 0.5),
// computed exactly as floor((2000 × frame + fps) / (2 × fps)).
func FrameToMs(frame int64, fps int) int64 {
	return floorDiv(2000*frame+int64(fps), 2*int64(fps))
}

// AlignMs moves ms to its frame: FrameToMs(MsToFrame(ms)). It changes a time by
// at most half a frame plus half a millisecond.
func AlignMs(ms int64, fps int) int64 {
	return FrameToMs(MsToFrame(ms, fps), fps)
}

// IsAligned reports whether ms is a frame boundary at fps.
func IsAligned(ms int64, fps int) bool {
	return AlignMs(ms, fps) == ms
}

// frameOfExact is frame(x) for a fractional millisecond.
func frameOfExact(x float64, fps int) int64 {
	return int64(math.Floor(x*float64(fps)/1000 + 0.5))
}

// roundMs rounds half up, the same rule as the frame formula.
func roundMs(x float64) int64 {
	return int64(math.Floor(x + 0.5))
}
