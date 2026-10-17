package beats

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

func TestDurationForBeats(t *testing.T) {
	tests := []struct {
		name  string
		spec  ConstantSpec
		start int64
		beats float64
		want  int64
	}{
		{"8 beats at 120", ConstantSpec{BPM: 120}, 0, 8, 4000},
		{"half a beat", ConstantSpec{BPM: 120}, 0, 0.5, 250},
		{"3 beats at 90", ConstantSpec{BPM: 90}, 0, 3, 2000},
		{"one beat at 128 rounds once", ConstantSpec{BPM: 128}, 0, 1, 469},       // 468.75
		{"off-beat start at 128", ConstantSpec{BPM: 128}, 1000, 1, 469},          // end 1468.75 -> 1469
		{"half a beat at 128", ConstantSpec{BPM: 128}, 1000, 0.5, 234},           // 1234.375 -> 1234
		{"no drift over 4000 beats", ConstantSpec{BPM: 128}, 0, 4000, 1_875_000}, // 4000 × 468.75
		{"a bar of 4 at 128", ConstantSpec{BPM: 128}, 0, 4, 1875},                //
		{"first beat does not matter", ConstantSpec{BPM: 120, FirstBeatMs: 123}, 0, 8, 4000},
	}
	for _, tc := range tests {
		g := mustConstant(t, tc.spec)
		got, err := DurationForBeats(g, tc.start, tc.beats)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
}

func TestDurationForBeatsList(t *testing.T) {
	g := mustList(t, ListSpec{BeatsMs: []float64{0, 500, 1100, 1550, 2200}})
	for _, tc := range []struct {
		start int64
		beats float64
		want  int64
	}{
		{0, 1, 500},
		{0, 2, 1100},
		{500, 2, 1050},  // 500 -> 1550
		{250, 1, 425},   // half-way through gap 0 (250) + one beat -> half-way through gap 1 (800): 800-250 = 550? no: 250 is pos 0.5, +1 = 1.5 -> 800
		{0, 0.5, 250},   // half of the first gap
		{1550, 1, 650},  // 1550 -> 2200
		{1550, 2, 1200}, // extrapolates with the last gap (650): 2200 + 650 = 2850
	} {
		got, err := DurationForBeats(g, tc.start, tc.beats)
		if err != nil {
			t.Fatal(err)
		}
		// The expectations above are checked against an independent calculation.
		want := listDuration(g.times, tc.start, tc.beats)
		if got != want {
			t.Errorf("start %d, %v beats: got %d, want %d", tc.start, tc.beats, got, want)
		}
	}
}

// listDuration is an independent, slow calculation of DurationForBeats for a
// list: walk the gaps.
func listDuration(beats []float64, start int64, n float64) int64 {
	gap := func(k int) float64 { // gap k lies between beat k and k+1, clamped at the ends
		k = max(0, min(k, len(beats)-2))
		return beats[k+1] - beats[k]
	}
	// locate start
	k := 0
	for k+1 < len(beats)-1 && float64(start) >= beats[k+1] {
		k++
	}
	frac := (float64(start) - beats[k]) / gap(k)
	remaining := n
	t := float64(start)
	// finish the current gap
	left := (1 - frac)
	for remaining > 0 {
		step := math.Min(remaining, left)
		t += step * gap(k)
		remaining -= step
		k++
		left = 1
	}
	return roundMs(t) - start
}

func TestDurationForBeatsErrors(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	for _, b := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := DurationForBeats(g, 0, b); err == nil {
			t.Errorf("beats %v: no error", b)
		}
	}
	if _, err := DurationForBeats(Grid{}, 0, 1); !errors.Is(err, ErrInvalidGrid) {
		t.Errorf("zero grid: %v", err)
	}
}

func TestFitDuration(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120}) // beats 500 apart, downbeats 2000 apart
	tests := []struct {
		name   string
		start  int64
		target int64
		o      FitOpts
		end    int64
		beats  float64
	}{
		{"nearest beat above", 0, 7300, FitOpts{FPS: 30}, 7500, 15},
		{"nearest beat below", 0, 7200, FitOpts{FPS: 30}, 7000, 14},
		{"tie goes to the shorter", 0, 7250, FitOpts{FPS: 30}, 7000, 14},
		{"on a beat already", 0, 7000, FitOpts{FPS: 30}, 7000, 14},
		{"bar", 0, 7300, FitOpts{FPS: 30, Unit: UnitBar}, 8000, 16},
		{"bar below", 0, 6900, FitOpts{FPS: 30, Unit: UnitBar}, 6000, 12},
		{"max pulls it down", 0, 7300, FitOpts{FPS: 30, MaxMs: 7200}, 7000, 14},
		{"min pushes it up", 0, 7300, FitOpts{FPS: 30, MinMs: 7600}, 8000, 16},
		{"bar with max", 0, 7300, FitOpts{FPS: 30, Unit: UnitBar, MaxMs: 7500}, 6000, 12},
		{"start off the beat", 100, 3000, FitOpts{FPS: 30}, 3000, 5.8}, // aligned start 100; end 3000
		{"start after the target beat", 1200, 500, FitOpts{FPS: 30}, 1500, 0.6},
	}
	for _, tc := range tests {
		f, err := FitDuration(g, tc.start, tc.target, tc.o)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if f.EndMs != tc.end || math.Abs(f.Beats-tc.beats) > 0.01 {
			t.Errorf("%s: end %d (%.2f beats), want %d (%.2f)", tc.name, f.EndMs, f.Beats, tc.end, tc.beats)
		}
		if f.DurationMs != f.EndMs-f.StartMs || f.MissMs != f.DurationMs-tc.target {
			t.Errorf("%s: inconsistent %+v", tc.name, f)
		}
	}
}

func TestFitDurationFrameAligned(t *testing.T) {
	// 128 bpm: beats at multiples of 468.75 ms, mostly off the frame grid.
	g := mustConstant(t, ConstantSpec{BPM: 128, FirstBeatMs: 37.3})
	for _, fps := range SupportedFPS {
		for _, unit := range []Unit{UnitBeat, UnitBar} {
			f, err := FitDuration(g, 1234, 9000, FitOpts{FPS: fps, Unit: unit})
			if err != nil {
				t.Fatal(err)
			}
			if !IsAligned(f.StartMs, fps) || !IsAligned(f.EndMs, fps) {
				t.Errorf("fps %d: %+v not aligned", fps, f)
			}
			if math.Abs(f.EndErrorMs) >= 1000/float64(fps) {
				t.Errorf("fps %d: end error %v ms", fps, f.EndErrorMs)
			}
			if unit == UnitBar && !f.EndBeat.Downbeat {
				t.Errorf("fps %d: bar fit ended on a plain beat", fps)
			}
		}
	}
}

func TestFitDurationErrors(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	if _, err := FitDuration(g, 0, 7200, FitOpts{FPS: 30, MinMs: 7100, MaxMs: 7400}); !errors.Is(err, ErrNoFit) {
		t.Errorf("no beat in bounds: %v", err)
	}
	if _, err := FitDuration(g, 0, 1000, FitOpts{}); !errors.Is(err, ErrInvalidFPS) {
		t.Errorf("fps: %v", err)
	}
	if _, err := FitDuration(g, 0, 0, FitOpts{FPS: 30}); err == nil {
		t.Error("zero target")
	}
	if _, err := FitDuration(g, 0, 1000, FitOpts{FPS: 30, MinMs: 500, MaxMs: 100}); err == nil {
		t.Error("max < min")
	}
	if _, err := FitDuration(Grid{}, 0, 1000, FitOpts{FPS: 30}); err == nil {
		t.Error("empty grid")
	}
	// A bar can't be fitted when the sound has no downbeat in reach.
	one := mustList(t, ListSpec{BeatsMs: []float64{0, 500, 1000}, DownbeatsMs: []float64{0}})
	if _, err := FitDuration(one, 0, 800, FitOpts{FPS: 30, Unit: UnitBar}); !errors.Is(err, ErrNoFit) {
		t.Errorf("bar with only the downbeat at the start: %v", err)
	}
}

func TestFitDurationProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8)) //nolint:gosec // deterministic test data, not security
	for it := 0; it < 3000; it++ {
		fps := SupportedFPS[it%4]
		g := randomConstant(r)
		start := r.Int64N(5000)
		target := 300 + r.Int64N(20000)
		o := FitOpts{FPS: fps, Unit: Unit(r.IntN(2))}
		if r.IntN(2) == 0 {
			o.MinMs = 300
			o.MaxMs = target + r.Int64N(4000)
		}
		label := fmt.Sprintf("it %d fps %d bpm %.3f start %d target %d %+v", it, fps, g.BPM(), start, target, o)
		f, err := FitDuration(g, start, target, o)
		if errors.Is(err, ErrNoFit) {
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if !IsAligned(f.StartMs, fps) || !IsAligned(f.EndMs, fps) {
			t.Fatalf("%s: not aligned: %+v", label, f)
		}
		if math.Abs(f.EndErrorMs) >= 1000/float64(fps) {
			t.Fatalf("%s: end error %v", label, f.EndErrorMs)
		}
		if f.DurationMs < o.MinMs || (o.MaxMs > 0 && f.DurationMs > o.MaxMs) || f.DurationMs < 1 {
			t.Fatalf("%s: duration %d out of bounds", label, f.DurationMs)
		}
		// No other beat of the unit gives a nearer valid duration.
		beats := g.Beats(f.StartMs, f.StartMs+target+int64(4*60000/g.BPM())+o.MaxMs+10000)
		for _, b := range beats {
			if o.Unit == UnitBar && !b.Downbeat {
				continue
			}
			end := FrameToMs(frameOfExact(b.ExactMs, fps), fps)
			d := end - f.StartMs
			if end <= f.StartMs || d < o.MinMs || (o.MaxMs > 0 && d > o.MaxMs) {
				continue
			}
			if absInt(d-target) < absInt(f.MissMs) {
				t.Fatalf("%s: beat at %d (duration %d) is nearer than the fit %+v", label, end, d, f)
			}
		}
	}
}
