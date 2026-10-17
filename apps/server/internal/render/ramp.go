package render

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// rampSetpts is the setpts expression of a speed ramp (P3-10). The clip arrives with timestamps restarted at 0 in source
// time; the expression maps the source time s (seconds) of each frame to its output time T(s), the integral of 1/speed
// (timeline/ramp.go has the maths and the derivation), and divides by TB to give the timestamp in time-base units. It is a
// chain of if() on the pieces of the ramp, each piece the closed form of the integral with the numbers written in. So the
// expression is the same function that decides the clip's output duration and the time map, and the frames land where
// those say.
//
// A segment-split (one trim and setpts per piece, joined by concat) was the alternative. It was not needed: setpts takes
// arbitrary expressions, the whole ramp stays one filter, and no frame can fall between two pieces.
//
// Every number is formatted here with strconv; the expression holds nothing from the spec but those numbers.
func rampSetpts(points []timeline.SpeedPoint) (string, error) {
	if len(points) < timeline.MinRampPoints || len(points) > timeline.MaxRampPoints {
		return "", fmt.Errorf("a speed ramp has %d to %d points, not %d", timeline.MinRampPoints, timeline.MaxRampPoints, len(points))
	}
	// Validate refuses these; the compiler checks again, since it must never write a number it has not checked.
	prev := int64(-1)
	for i, p := range points {
		if !(p.Speed >= timeline.MinSpeed && p.Speed <= timeline.MaxSpeed) { // false for NaN as well
			return "", fmt.Errorf("speed ramp point %d has speed %s, outside %s to %s", i, num(p.Speed), num(timeline.MinSpeed), num(timeline.MaxSpeed))
		}
		if p.AtMs < 0 || p.AtMs <= prev {
			return "", errors.New("the speed ramp's points must be sorted with increasing at_ms")
		}
		prev = p.AtMs
	}
	segs := timeline.RampSegments(points)
	const x = "((PTS-STARTPTS)*TB)" // the frame's source time in seconds
	piece := func(g timeline.RampSegment) string {
		s0, t0 := g.S0/1000, g.T0/1000
		if g.K == 0 {
			return fmt.Sprintf("%s+(%s-%s)/%s", sec(t0), x, sec(s0), sec(g.V0))
		}
		k := decimals(g.K*1000, 1e12) // the slope of the speed, per second
		return fmt.Sprintf("%s+log(1+%s*(%s-%s)/%s)/%s", sec(t0), k, x, sec(s0), sec(g.V0), k)
	}
	var b strings.Builder
	b.WriteString("(")
	closers := 0
	for i, g := range segs {
		if i == len(segs)-1 {
			b.WriteString(piece(g))
			break
		}
		fmt.Fprintf(&b, "if(lt(%s,%s),%s,", x, sec(g.S1/1000), piece(g))
		closers++
	}
	b.WriteString(strings.Repeat(")", closers))
	b.WriteString(")/TB")
	return b.String(), nil
}

// sec formats a number of the expression: at most nine decimals, no trailing zeros. Rounding keeps the last digits of a
// logarithm, which can differ by one unit in the last place between platforms, out of the plan.
func sec(f float64) string { return decimals(f, 1e9) }

// decimals rounds f to the scale (1e9 is nine decimals) and formats it, in brackets when it is negative so that it can
// follow any operator.
func decimals(f, scale float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "0"
	}
	s := num(math.Round(f*scale) / scale)
	if f < 0 {
		return "(" + s + ")"
	}
	return s
}
