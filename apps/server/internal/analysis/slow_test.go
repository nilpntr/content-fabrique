package analysis

import (
	"math"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// A tired lifter slows down: the reps get longer gradually (3 s to 5.4 s over a minute). The slowdown is not irregularity, and the
// last reps are not lost.
func TestSlowingRepsAreAllFound(t *testing.T) {
	// the phase of a rep rate that falls from 1/3 to 1/5.4 reps a second over 60 s: the integral of 1/(3 + 0.04 t)
	phase := func(t float64) float64 {
		return 2 * math.Pi * 25 * (math.Log(3+0.04*t) - math.Log(3))
	}
	pos := func(t float64) float64 { return 0.5 + 0.2*math.Sin(phase(t)) }
	a := analyzeSynth(synth{seconds: 62, pos: pos, noise: 3, seed: 31})
	trueReps := phase(62) / (2 * math.Pi)
	if got := float64(a.Summary.Reps); got < trueReps-3 || got > trueReps+1 {
		t.Fatalf("%d reps for %.1f true ones: %s", a.Summary.Reps, trueReps, a.Summary.Line())
	}
	reps := momentsOf(a, KindRep)
	if last := reps[len(reps)-1]; last.EndMs < 52000 {
		t.Errorf("the last rep ends at %d ms: the slow tail was lost", last.EndMs)
	}
	for _, r := range reps {
		if strings.Contains(r.Description, "uneven tempo") {
			t.Errorf("a gradual slowdown is not uneven tempo: rep at %d ms: %s", r.StartMs, r.Description)
		}
	}
}

// Movement next to a set that travels as far as its reps but does not repeat is slow reps, not dead time, and the editor is not
// told to skip it; walking about (travelling far more) is dead time.
func TestSlowMovementNextToASetIsACandidateNotDeadTime(t *testing.T) {
	// reps for 30 s, then one very slow swing (a 20 s period, far outside the rep range) for 20 s, then still
	pos := func(t float64) float64 {
		switch {
		case t < 30:
			return 0.5 + 0.2*math.Sin(2*math.Pi*t/3)
		case t < 50:
			return 0.5 + 0.2*math.Sin(2*math.Pi*(t-30)/20)
		}
		return 0.5
	}
	a := analyzeSynth(synth{seconds: 70, pos: pos, noise: 0.4, seed: 32})
	cands := momentsOf(a, KindRepCandidate)
	if len(cands) == 0 {
		t.Fatalf("no rep candidate: %s", a.Summary.Line())
	}
	for _, c := range cands {
		if c.Score > 0.35 || c.StartMs < 28000 || c.StartMs > 36000 {
			t.Errorf("candidate %+v", c)
		}
	}
	for _, d := range momentsOf(a, KindDead) {
		if d.StartMs >= 29000 && d.EndMs <= 33500 {
			t.Errorf("the slow swing next to the set is labelled dead time: %+v", d)
		}
	}
	if a.Summary.CandidateMs == 0 || !strings.Contains(a.Summary.Line(), "slow reps") {
		t.Errorf("summary %s", a.Summary.Line())
	}

	// walking about (a far bigger movement) next to the same set is dead time
	walk := func(t float64) float64 {
		if t < 30 {
			return 0.5 + 0.2*math.Sin(2*math.Pi*t/3)
		}
		if t < 44 {
			return 0.5 + 0.45*math.Sin(2*math.Pi*(t-30)/28)
		}
		return 0.5
	}
	w := analyzeSynth(synth{seconds: 70, pos: walk, noise: 0.4, seed: 32})
	for _, c := range momentsOf(w, KindRepCandidate) {
		if c.StartMs >= 29000 && c.EndMs <= 45000 {
			t.Errorf("walking about is a rep candidate: %+v", c)
		}
	}
}

func TestPackedSeriesRoundTrip(t *testing.T) {
	s := BuildSeries(synth{seconds: 20, pos: sine(3), noise: 3, seed: 33, sharp: func(i int) float64 { return 4 + float64(i%7)/10 }, scene: func(i int) float64 {
		if i == 50 {
			return 40.5
		}
		return 0.3
	}}.raw())
	p, err := Pack(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Motion) != 2*s.Len() || len(p.Luma) != s.Len() || len(p.Scene) != 2*s.Len() {
		t.Fatalf("sizes %d %d %d for %d samples", len(p.Motion), len(p.Luma), len(p.Scene), s.Len())
	}
	u, err := Unpack(p, s.StepMs)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < s.Len(); i++ {
		if math.Abs(u.Motion[i]-s.Motion[i]) > 0.006 || math.Abs(u.Sharp[i]-s.Sharp[i]) > 0.006 || math.Abs(u.Scene[i]-s.Scene[i]) > 0.06 ||
			math.Abs(u.Luma[i]-s.Luma[i]) > 0.51 || math.Abs(u.High[i]-s.High[i]) > 0.51 || math.Abs(u.Contrast[i]-s.Contrast[i]) > 0.51 {
			t.Fatalf("sample %d differs after the round trip", i)
		}
	}
	// values past the range saturate instead of wrapping
	big := Series{StepMs: 100, Motion: []float64{1e6, -1e6}, Luma: []float64{300, -5}, Contrast: []float64{0, 0}, High: []float64{0, 0}, Sharp: []float64{0, 0}, Scene: []float64{0, 0}}
	pb, _ := Pack(big)
	ub, _ := Unpack(pb, 100)
	if ub.Motion[0] < 327 || ub.Motion[1] > -327 || ub.Luma[0] != 255 || ub.Luma[1] != 0 {
		t.Errorf("saturation: %+v %+v", ub.Motion, ub.Luma)
	}
	if _, err := Unpack(store.PackedSeries{Luma: []byte{1}}, 100); err == nil {
		t.Error("mismatched series were accepted")
	}
}

func TestDriftSeesSlowMovementThatMotionMisses(t *testing.T) {
	// one slow swing over 14 s: little motion from sample to sample, a long way from where it started
	slow := synth{seconds: 20, pos: func(t float64) float64 { return 0.5 + 0.2*math.Sin(2*math.Pi*t/14) }, noise: 0.2, seed: 34}.raw()
	still := synth{seconds: 20, pos: func(float64) float64 { return 0.5 }, noise: 0.2, seed: 34}.raw()
	fast := synth{seconds: 20, pos: sine(3), noise: 0.2, seed: 34}.raw()
	ds, dst, df := slow.Drift(0, 140), still.Drift(0, 140), fast.Drift(0, 140)
	if dst > 1 || ds < 5*dst+3 || df < ds/3 {
		t.Errorf("drift: still %.2f slow %.2f fast %.2f", dst, ds, df)
	}
	if m := mean(BuildSeries(slow).Motion); m > mean(BuildSeries(fast).Motion)/2 {
		t.Errorf("the slow swing's motion %.2f is not much lower than the fast one's", m)
	}
	if slow.Drift(5, 6) != 0 || slow.Drift(-1, 10) != 0 || slow.Drift(0, 9999) != 0 {
		t.Error("an empty or out-of-range stretch drifts nowhere")
	}
}
