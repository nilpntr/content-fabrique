package beats

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"
)

func TestEstimateFromTaps(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12)) //nolint:gosec // deterministic test data, not security
	for _, bpm := range []float64{60, 93.5, 128, 174} {
		period := 60000 / bpm
		taps := make([]float64, 16)
		for k := range taps {
			taps[k] = 210 + float64(k)*period + (r.Float64()-0.5)*20 // ±10 ms of jitter
		}
		fit, err := EstimateFromTaps(taps)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(fit.Spec.BPM-bpm) > 1 {
			t.Errorf("bpm %v: estimated %v", bpm, fit.Spec.BPM)
		}
		if fit.MaxResidualMs > 12 {
			t.Errorf("bpm %v: residual %v", bpm, fit.MaxResidualMs)
		}
		if _, err := NewConstant(fit.Spec); err != nil {
			t.Errorf("the fitted spec must be a valid grid: %v", err)
		}
		// The first beat is within a period of the start and near the first tap.
		if fit.Spec.FirstBeatMs < 0 || fit.Spec.FirstBeatMs > period+20 {
			t.Errorf("first beat %v", fit.Spec.FirstBeatMs)
		}
	}
}

func TestEstimateFromTapsMovesFirstBeatForward(t *testing.T) {
	// Taps that start at 0 with jitter below zero give a fitted first beat just
	// under 0, which must be moved a period forward.
	taps := []float64{0, 500, 1000, 1500, 2000}
	for i := range taps {
		taps[i] += 3 // fit intercept 3
	}
	fit, err := EstimateFromTaps(taps)
	if err != nil || math.Abs(fit.Spec.FirstBeatMs-3) > 1e-9 {
		t.Fatalf("%+v, %v", fit, err)
	}
	neg := []float64{1, 501, 999, 1500, 2000}
	fit, err = EstimateFromTaps(neg)
	if err != nil || fit.Spec.FirstBeatMs < 0 {
		t.Fatalf("%+v, %v", fit, err)
	}
}

func TestEstimateFromTapsErrors(t *testing.T) {
	for name, taps := range map[string][]float64{
		"too few":    {0, 500, 1000},
		"unsorted":   {0, 500, 400, 1500},
		"too slow":   {0, 3000, 6000, 9000},
		"too fast":   {0, 100, 200, 300},
		"negative":   {-5, 495, 995, 1495},
		"not finite": {0, 500, math.NaN(), 1500},
	} {
		if _, err := EstimateFromTaps(taps); !errors.Is(err, ErrInvalidGrid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestNormalizeFirst(t *testing.T) {
	tests := []struct {
		name       string
		first      float64
		period     float64
		wantFirst  float64
		wantOffset int
	}{
		{"already valid", 130, 500, 130, 0},
		{"float noise", -1e-13, 500, 0, 0},
		{"jitter before 0 stays on the first tap", -20, 500, 0, 0},
		{"one period", -100, 500, 400, 3},
		{"exactly one period", -500, 500, 0, 3},
		{"three periods", -1200, 500, 300, 1},
		{"four periods wrap", -1700, 500, 300, 0},
	}
	for _, tc := range tests {
		first, off := normalizeFirst(tc.first, tc.period, 4)
		if math.Abs(first-tc.wantFirst) > 1e-9 || off != tc.wantOffset {
			t.Errorf("%s: got %v, %d; want %v, %d", tc.name, first, off, tc.wantFirst, tc.wantOffset)
		}
		if first < 0 {
			t.Errorf("%s: negative first beat %v", tc.name, first)
		}
		// The beat at the original position (tap 0) is still a downbeat.
		g, err := NewConstant(ConstantSpec{BPM: 120, FirstBeatMs: first, DownbeatOffset: off})
		if err != nil {
			t.Fatal(err)
		}
		if tc.first < -firstBeatClampMs {
			// original index 0 sits at tc.first = first - m*period
			m := int(math.Round((first - tc.first) / tc.period))
			if !g.isDown(-m) {
				t.Errorf("%s: the original first beat (index %d) lost its downbeat", tc.name, -m)
			}
		}
	}
}

func TestEstimateFromTapsKeepsFirstTapAsDownbeat(t *testing.T) {
	// The first tap is late and the rest are early: the fitted first beat is
	// before 0, and the spec must still be valid with a beat near every tap.
	taps := []float64{60, 460, 960, 1460, 1960, 2460}
	fit, err := EstimateFromTaps(taps)
	if err != nil {
		t.Fatal(err)
	}
	if fit.Spec.FirstBeatMs < 0 || fit.Spec.DownbeatOffset < 0 || fit.Spec.DownbeatOffset >= DefaultBeatsPerBar {
		t.Fatalf("%+v", fit.Spec)
	}
	g, err := NewConstant(fit.Spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, tap := range taps {
		b, _ := g.Nearest(int64(tap))
		if math.Abs(b.ExactMs-tap) > fit.MaxResidualMs+1e-9 {
			t.Errorf("no beat near tap %v: nearest %v", tap, b.ExactMs)
		}
	}
}
