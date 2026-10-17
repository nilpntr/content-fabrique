package timeline

import "math"

// Speed ramps (P3-10).
//
// A ramp is a list of points {at_ms, speed}: at_ms is a source offset (milliseconds into the clip's source range, from
// source.in_ms) and speed the playback speed there, in source seconds per output second. Between two points the speed
// changes linearly in source time. Before the first point the clip plays at the first point's speed and after the last
// one at the last point's speed.
//
// With the speed v(s) = v0 + k (s - s0) on a piece, the output time of a source offset s is
//
//	T(s) = T0 + integral of ds / v(s) = T0 + ln(v(s) / v0) / k        (k != 0)
//	T(s) = T0 + (s - s0) / v0                                          (k = 0)
//
// and its inverse is s(T) = s0 + v0 (exp(k (T - T0)) - 1) / k. The renderer writes T(s) as the setpts expression of the
// clip, the output duration of the clip is T(span), and the time map uses the inverse, so all three agree by
// construction. Every unit here is milliseconds and k is per millisecond.

// RampSegment is one piece of a speed ramp in which the speed is linear in source time.
type RampSegment struct {
	// S0 is the source offset the piece starts at; S1 is where it ends, +Inf for the last piece, which goes on at its
	// last speed for as long as the clip plays.
	S0, S1 float64
	// T0 is the output time at S0.
	T0 float64
	// V0 is the speed at S0 and K its slope per source millisecond (0 for a constant piece).
	V0, K float64
}

// outAt is the output time of source offset s (S0 <= s <= S1).
func (g RampSegment) outAt(s float64) float64 {
	d := s - g.S0
	if g.K == 0 {
		return g.T0 + d/g.V0
	}
	return g.T0 + math.Log1p(g.K*d/g.V0)/g.K
}

// srcAt is the source offset at output time t, the inverse of outAt.
func (g RampSegment) srcAt(t float64) float64 {
	u := t - g.T0
	if g.K == 0 {
		return g.S0 + u*g.V0
	}
	return g.S0 + g.V0*math.Expm1(g.K*u)/g.K
}

// end is the output time at the end of the piece, +Inf for the last one.
func (g RampSegment) end() float64 {
	if math.IsInf(g.S1, 1) {
		return math.Inf(1)
	}
	return g.outAt(g.S1)
}

// RampSegments turns the points of a speed ramp into its pieces. It is forgiving about a ramp that Validate rejects: a
// point whose at_ms does not increase, or whose speed is not a positive number, is left out, so that the arithmetic stays
// finite for any input. It returns nil for fewer than one usable point.
func RampSegments(points []SpeedPoint) []RampSegment {
	var usable []SpeedPoint
	for _, p := range points {
		if !(p.Speed > 0) || math.IsInf(p.Speed, 0) || p.AtMs < 0 {
			continue
		}
		if n := len(usable); n > 0 && p.AtMs <= usable[n-1].AtMs {
			continue
		}
		usable = append(usable, p)
	}
	if len(usable) == 0 {
		return nil
	}
	var segs []RampSegment
	t := 0.0
	if first := usable[0]; first.AtMs > 0 {
		g := RampSegment{S0: 0, S1: float64(first.AtMs), T0: 0, V0: first.Speed}
		segs = append(segs, g)
		t = g.end()
	}
	for i, p := range usable {
		g := RampSegment{S0: float64(p.AtMs), T0: t, V0: p.Speed, S1: math.Inf(1)}
		if i+1 < len(usable) {
			next := usable[i+1]
			g.S1 = float64(next.AtMs)
			g.K = (next.Speed - p.Speed) / (g.S1 - g.S0)
			if math.Abs(g.K) < 1e-9 { // a slope of under 1e-6 per second is no slope: a constant piece, which is exact
				g.K = 0
			}
			t = g.end()
		}
		segs = append(segs, g)
	}
	return segs
}

// RampOutMs is the output time, in milliseconds, of source offset s (milliseconds from the clip's in point) under the
// ramp. A ramp with no usable point plays at speed 1.
func RampOutMs(points []SpeedPoint, s float64) float64 {
	segs := RampSegments(points)
	if len(segs) == 0 {
		return s
	}
	return segsOutMs(segs, s)
}

func segsOutMs(segs []RampSegment, s float64) float64 {
	if s <= 0 {
		return 0
	}
	for _, g := range segs {
		if s <= g.S1 {
			return g.outAt(s)
		}
	}
	return segs[len(segs)-1].outAt(s) // unreachable: the last piece ends at +Inf
}

// RampSourceMs is the source offset, in milliseconds from the clip's in point, shown at output time t (milliseconds from
// the clip's first frame): the inverse of RampOutMs.
func RampSourceMs(points []SpeedPoint, t float64) float64 {
	segs := RampSegments(points)
	if len(segs) == 0 {
		return t
	}
	return segsSourceMs(segs, t)
}

func segsSourceMs(segs []RampSegment, t float64) float64 {
	if t <= 0 {
		return 0
	}
	for _, g := range segs {
		if t <= g.end() {
			return g.srcAt(t)
		}
	}
	return segs[len(segs)-1].srcAt(t)
}

// HasRamp reports whether the clip has a speed ramp, which replaces its speed.
func (v VideoItem) HasRamp() bool { return len(v.SpeedRamp) > 0 }

// OutOffsetMs is where the clip shows source offset s (milliseconds from source.in_ms) in its own output, in
// milliseconds from its first frame, whether it plays at a constant speed or along a ramp.
func (v VideoItem) OutOffsetMs(s float64) float64 {
	if v.HasRamp() {
		return RampOutMs(v.SpeedRamp, s)
	}
	return s / v.SpeedOrDefault()
}

// SourceOffsetMs is the source offset (milliseconds from source.in_ms) the clip shows at output offset t (milliseconds
// from its first frame): the inverse of OutOffsetMs.
func (v VideoItem) SourceOffsetMs(t float64) float64 {
	if v.HasRamp() {
		return RampSourceMs(v.SpeedRamp, t)
	}
	return t * v.SpeedOrDefault()
}

// AverageSpeed is the source span divided by the output duration of the clip: its speed, or for a ramp the one constant
// speed that takes as long. The time map reports it as `speed` for readers that do not know about ramps.
func (v VideoItem) AverageSpeed() float64 {
	if !v.HasRamp() {
		return v.SpeedOrDefault()
	}
	span := float64(v.Source.OutMs - v.Source.InMs)
	out := v.OutOffsetMs(span)
	if span <= 0 || out <= 0 {
		return 1
	}
	return span / out
}

// MinSpeedOf is the lowest speed the clip plays at: its speed, or the lowest point of its ramp.
func (v VideoItem) MinSpeedOf() float64 {
	if !v.HasRamp() {
		return v.SpeedOrDefault()
	}
	lo := math.Inf(1)
	for _, p := range v.SpeedRamp {
		lo = math.Min(lo, p.Speed)
	}
	if math.IsInf(lo, 1) {
		return 1
	}
	return lo
}
