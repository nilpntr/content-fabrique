package beats

import "math"

// TapFit is the result of EstimateFromTaps.
type TapFit struct {
	// Spec is the constant-tempo grid that fits the taps.
	Spec ConstantSpec
	// MaxResidualMs is the largest distance between a tap and the fitted beat
	// it belongs to. A large value means the taps were uneven.
	MaxResidualMs float64
}

// EstimateFromTaps fits a constant tempo to tap-tempo input (the UI fallback
// when no provider metadata or analysis is available). tapsMs are the tap times
// in ms from the start of the sound, strictly increasing, one tap per beat
// with no beat skipped: at least four.
//
// Tap k is modelled as t_k = first + k × period and the two parameters come
// from an ordinary least-squares line through (k, t_k), which averages out
// human jitter. The first beat is moved forward by whole periods until it is
// >= 0. The tempo must land inside [MinBPM, MaxBPM].
func EstimateFromTaps(tapsMs []float64) (TapFit, error) {
	n := len(tapsMs)
	if n < 4 {
		return TapFit{}, invalidf("need at least 4 taps, got %d", n)
	}
	prev := math.Inf(-1)
	for i, t := range tapsMs {
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 {
			return TapFit{}, invalidf("tap %d (%v) must be a finite number >= 0", i, t)
		}
		if t <= prev {
			return TapFit{}, invalidf("taps must be strictly increasing: tap %d (%v)", i, t)
		}
		prev = t
	}
	var sk, st, skk, skt float64
	for k, t := range tapsMs {
		x := float64(k)
		sk += x
		st += t
		skk += x * x
		skt += x * t
	}
	fn := float64(n)
	period := (fn*skt - sk*st) / (fn*skk - sk*sk)
	first := (st - period*sk) / fn
	if period <= 0 {
		return TapFit{}, invalidf("taps do not advance")
	}
	bpm := 60000 / period
	if bpm < MinBPM || bpm > MaxBPM {
		return TapFit{}, invalidf("taps give %.1f bpm, outside %v to %v", bpm, MinBPM, MaxBPM)
	}
	if first < 0 {
		first += period * math.Ceil(-first/period)
	}
	return TapFit{Spec: ConstantSpec{BPM: bpm, FirstBeatMs: first}, MaxResidualMs: residual(tapsMs, first, period)}, nil
}

// residual is the largest |tap - nearest fitted beat|. The fitted beats are
// first + j × period for any integer j, so a first beat moved forward by whole
// periods leaves the residuals unchanged.
func residual(taps []float64, first, period float64) float64 {
	var worst float64
	for _, t := range taps {
		j := math.Floor((t-first)/period + 0.5)
		worst = math.Max(worst, math.Abs(t-(first+j*period)))
	}
	return worst
}
