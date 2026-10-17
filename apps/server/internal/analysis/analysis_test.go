package analysis

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func momentsOf(a Analysis, kind string) []Moment {
	var out []Moment
	for _, m := range a.Moments {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

func analyzeSynth(s synth) Analysis {
	return Analyze(s.raw(), DefaultParams(), DefaultShotParams())
}

// phaseOffset is how far t is from the nearest multiple of period (plus offset), in seconds.
func phaseDist(t, period, offset float64) float64 {
	d := math.Mod(t-offset, period)
	if d < 0 {
		d += period
	}
	return math.Min(d, period-d)
}

func TestPeriodicMotionGivesReps(t *testing.T) {
	// 60 s of a box moving with a 3 s period, with noise: about 19 reps whose boundaries are the box's tops or its bottoms.
	a := analyzeSynth(synth{seconds: 60, pos: sine(3), noise: 4, seed: 1})
	reps := momentsOf(a, KindRep)
	if len(reps) < 17 || len(reps) > 20 {
		t.Fatalf("%d reps, want 17 to 20: %s", len(reps), a.Summary.Line())
	}
	if a.Summary.Sets != 1 {
		t.Fatalf("%d sets, want 1", a.Summary.Sets)
	}
	// every boundary is a turning point of the box: a quarter or three quarters of a period into the sine
	var offset float64
	for i, r := range reps {
		start := float64(r.StartMs) / 1000
		if i == 0 {
			// the first boundary decides which kind (top or bottom) the boundaries are
			offset = start
		}
		if d := phaseDist(start, 3, offset); d > 0.35 {
			t.Errorf("rep %d starts at %.1f s, %.2f s off the rhythm", i+1, start, d)
		}
		if dur := float64(r.EndMs-r.StartMs) / 1000; math.Abs(dur-3) > 0.4 {
			t.Errorf("rep %d is %.1f s long, want about 3", i+1, dur)
		}
	}
	// the tops and bottoms of sin(2 pi t / 3) are at 0.75 + 1.5k and 2.25 + 1.5k
	quarter := math.Mod(offset, 1.5)
	if math.Abs(quarter-0.75) > 0.35 {
		t.Errorf("boundaries are %.2f s into a half period, want about 0.75 (a top or a bottom)", quarter)
	}
	if p := float64(a.Summary.PeriodMs) / 1000; math.Abs(p-3) > 0.3 {
		t.Errorf("period %.2f s, want 3", p)
	}
	for _, r := range reps {
		if r.Score < 0.6 {
			t.Errorf("a clean rep at %d ms scores %.2f: %s", r.StartMs, r.Score, r.Description)
		}
	}
	if len(momentsOf(a, KindDead)) != 0 {
		t.Errorf("dead time in a clip that is all reps: %+v", momentsOf(a, KindDead))
	}
}

func TestFasterAndSlowerReps(t *testing.T) {
	for _, period := range []float64{1.8, 2.5, 4.5} {
		t.Run(fmt.Sprintf("period %.1f", period), func(t *testing.T) {
			secs := int(period*12) + 5
			a := analyzeSynth(synth{seconds: secs, pos: sine(period), noise: 3, seed: 2})
			if p := float64(a.Summary.PeriodMs) / 1000; math.Abs(p-period) > 0.3 {
				t.Fatalf("period %.2f s, want %.1f (%s)", p, period, a.Summary.Line())
			}
			want := float64(secs) / period
			if n := float64(a.Summary.Reps); n < want-4 || n > want+1 {
				t.Fatalf("%d reps in %d s of %.1f s reps, want about %.0f", a.Summary.Reps, secs, period, want)
			}
		})
	}
}

func TestRestBetweenSetsIsDeadTime(t *testing.T) {
	// still for 20 s, reps for 30 s, still for 20 s, reps again for 30 s
	pos := phases(phase{20, 0}, phase{50, 3}, phase{70, 0}, phase{100, 3})
	a := analyzeSynth(synth{seconds: 100, pos: pos, noise: 3, seed: 3})
	if a.Summary.Sets != 2 {
		t.Fatalf("%d sets, want 2: %s", a.Summary.Sets, a.Summary.Line())
	}
	if n := a.Summary.Reps; n < 15 || n > 20 {
		t.Fatalf("%d reps, want about 18", n)
	}
	reps := momentsOf(a, KindRep)
	for _, r := range reps {
		inFirst := r.StartMs >= 18000 && r.EndMs <= 52000
		inSecond := r.StartMs >= 68000 && r.EndMs <= 102000
		if !inFirst && !inSecond {
			t.Errorf("a rep at %d-%d ms is outside the two sets", r.StartMs, r.EndMs)
		}
	}
	dead := momentsOf(a, KindDead)
	var total int
	for _, d := range dead {
		total += d.EndMs - d.StartMs
	}
	// 40 s of rest; the first and last seconds next to a set are not counted, so allow some slack
	if total < 30000 || total > 46000 {
		t.Errorf("%d ms of dead time, want about 40000: %+v", total, dead)
	}
	if len(momentsOf(a, KindHold)) != 0 {
		t.Errorf("rest between sets is not a hold in a clip with reps: %+v", momentsOf(a, KindHold))
	}
	if a.Summary.DeadMs != total {
		t.Errorf("summary says %d ms of dead time, the moments add up to %d", a.Summary.DeadMs, total)
	}
}

func TestMovementThatIsNotPeriodicIsDeadTime(t *testing.T) {
	a := analyzeSynth(synth{seconds: 40, pos: walk(5, 40), noise: 2, seed: 5})
	if a.Summary.Reps != 0 {
		t.Fatalf("%d reps found in a random walk: %+v", a.Summary.Reps, momentsOf(a, KindRep))
	}
	if a.Summary.DeadMs < 20000 {
		t.Errorf("only %d ms of dead time in 40 s of aimless movement", a.Summary.DeadMs)
	}
}

func TestStillClipIsAHold(t *testing.T) {
	// a box that does not move, for 12 s, and no reps anywhere: a hold, whatever the clip is called
	a := analyzeSynth(synth{seconds: 12, pos: func(float64) float64 { return 0.5 }, noise: 0.3, seed: 6})
	holds := momentsOf(a, KindHold)
	if len(holds) != 1 {
		t.Fatalf("%d holds, want 1: %s", len(holds), a.Summary.Line())
	}
	if holds[0].StartMs > 1500 || holds[0].EndMs < 10000 {
		t.Errorf("hold %d-%d ms does not cover the clip", holds[0].StartMs, holds[0].EndMs)
	}
	if a.Summary.Reps != 0 || a.Summary.DeadMs != 0 {
		t.Errorf("reps %d, dead %d ms in a still clip", a.Summary.Reps, a.Summary.DeadMs)
	}
}

// Without the quick look's words (they may not exist yet), a still stretch after reps is a hold only when it is long, steady and
// well exposed; the rest between sets is dead time.
func TestStillnessAfterRepsIsAHoldOnlyWhenLongSteadyAndWellLit(t *testing.T) {
	pos := phases(phase{30, 3}, phase{45, 0})
	dark := analyzeSynth(synth{seconds: 45, pos: pos, noise: 0.4, seed: 7, bg: 12, fg: 40})
	if len(momentsOf(dark, KindHold)) != 0 || len(momentsOf(dark, KindDead)) == 0 {
		t.Errorf("a dark rest after reps should be dead time: holds %d dead %d", len(momentsOf(dark, KindHold)), len(momentsOf(dark, KindDead)))
	}
	lit := analyzeSynth(synth{seconds: 45, pos: pos, noise: 0.4, seed: 7, bg: 90, fg: 200})
	if len(momentsOf(lit, KindHold)) == 0 {
		t.Errorf("a long, steady, well lit stretch after reps should be a hold: %s", lit.Summary.Line())
	}
	short := analyzeSynth(synth{seconds: 34, pos: phases(phase{30, 3}, phase{34, 0}), noise: 0.4, seed: 7, bg: 90, fg: 200})
	if len(momentsOf(short, KindHold)) != 0 {
		t.Errorf("a 3 s pause is not a hold")
	}
}

func TestIrregularRepScoresLower(t *testing.T) {
	// reps of 3 s with one cut short: a rep that moves half as far scores below its neighbours and says so
	pos := func(t float64) float64 {
		amp := 0.2
		if t > 30 && t < 36 {
			amp = 0.08
		}
		return 0.5 + amp*math.Sin(2*math.Pi*t/3)
	}
	a := analyzeSynth(synth{seconds: 60, pos: pos, noise: 3, seed: 8})
	reps := momentsOf(a, KindRep)
	var short, full []float64
	for _, r := range reps {
		if r.StartMs >= 29000 && r.EndMs <= 37000 {
			short = append(short, r.Score)
			if !strings.Contains(r.Description, "short range") && !strings.Contains(r.Description, "uneven") {
				t.Errorf("a short rep at %d ms is described as %q", r.StartMs, r.Description)
			}
		} else if r.StartMs > 6000 && r.EndMs < 54000 {
			full = append(full, r.Score)
		}
	}
	if len(short) == 0 || len(full) == 0 {
		t.Fatalf("short %v full %v", short, full)
	}
	if mean(short) >= mean(full) {
		t.Errorf("short reps score %.2f, full reps %.2f", mean(short), mean(full))
	}
}

func TestRepScoreFollowsSharpnessAndExposure(t *testing.T) {
	// the same movement, but a blurred stretch: those reps score lower and are flagged
	blur := func(i int) float64 {
		if i >= 300 && i < 400 {
			return 2.5
		}
		return 5
	}
	a := analyzeSynth(synth{seconds: 60, pos: sine(3), noise: 3, seed: 9, sharp: blur})
	var soft, sharp []float64
	for _, r := range momentsOf(a, KindRep) {
		switch {
		case r.StartMs >= 30000 && r.EndMs <= 40000:
			soft = append(soft, r.Score)
		case r.EndMs < 28000 || r.StartMs > 42000:
			sharp = append(sharp, r.Score)
		}
	}
	if len(soft) == 0 || mean(soft) >= mean(sharp) {
		t.Errorf("blurred reps score %.2f against %.2f", mean(soft), mean(sharp))
	}
}

func TestShotDetection(t *testing.T) {
	scene := func(i int) float64 {
		switch i {
		case 200, 450: // clear cuts at 20 s and 45 s
			return 40
		case 300: // a weak one
			return 6
		case 203: // too close to the cut at 200 and weaker
			return 15
		}
		return 0
	}
	s := synth{seconds: 60, pos: sine(3), noise: 3, seed: 10, scene: scene}
	a := analyzeSynth(s)
	if len(a.Shots) != 3 {
		t.Fatalf("%d shots, want 3: %+v", len(a.Shots), a.Shots)
	}
	if a.Shots[0].EndMs != 20000 || a.Shots[1].StartMs != 20000 || a.Shots[1].EndMs != 45000 || a.Shots[2].EndMs != 60000 {
		t.Errorf("boundaries %+v", a.Shots)
	}
	if got := len(momentsOf(a, KindShot)); got != 3 {
		t.Errorf("%d shot moments", got)
	}
	// reps never cross a cut
	for _, r := range momentsOf(a, KindRep) {
		for _, cut := range []int{20000, 45000} {
			if r.StartMs < cut && r.EndMs > cut {
				t.Errorf("rep %d-%d ms crosses the cut at %d", r.StartMs, r.EndMs, cut)
			}
		}
	}
}

func TestShotMinimumLength(t *testing.T) {
	sp := DefaultShotParams()
	s := BuildSeries(synth{seconds: 10, pos: sine(3), seed: 11, scene: func(i int) float64 {
		switch i {
		case 50:
			return 30
		case 55: // half a second after the first: a shot of 0.5 s is not a shot
			return 20
		}
		return 0
	}}.raw())
	shots := DetectShots(s, sp)
	if len(shots) != 2 || shots[0].EndMs != 5000 {
		t.Fatalf("%+v", shots)
	}
	if one := DetectShots(BuildSeries(synth{seconds: 10, pos: sine(3), seed: 11}.raw()), sp); len(one) != 1 || one[0].StartMs != 0 || one[0].EndMs != 10000 {
		t.Fatalf("no cut: %+v", one)
	}
}

func TestExposureHint(t *testing.T) {
	dark := NewExposureHint(0.1)
	if dark.Level != "dark" || dark.Gain <= 1 || dark.Gamma <= 1 {
		t.Errorf("dark: %+v", dark)
	}
	ok := NewExposureHint(TargetLuma)
	if ok.Level != "ok" || math.Abs(ok.Gain-1) > 0.01 || math.Abs(ok.Gamma-1) > 0.01 {
		t.Errorf("at the target: %+v", ok)
	}
	bright := NewExposureHint(0.8)
	if bright.Level != "bright" || bright.Gain >= 1 || bright.Gamma >= 1 {
		t.Errorf("bright: %+v", bright)
	}
	black := NewExposureHint(0)
	if black.Gain != 2.5 || black.Gamma > 2.2 {
		t.Errorf("clamps: %+v", black)
	}
	// applying the hint's gamma to the mean brings it to the target: out = in^(1/gamma)
	if got := math.Pow(0.2, 1/NewExposureHint(0.2).Gamma); math.Abs(got-TargetLuma) > 0.02 {
		t.Errorf("gamma maps 0.2 to %.3f, want %.2f", got, TargetLuma)
	}
	if ExposureScore(TargetLuma) != 1 || ExposureScore(0) != 0 || ExposureScore(1) != 0 {
		t.Errorf("exposure score")
	}
	// a dark background does not make the exposure dark when the lit part is bright
	if KeyLuma(0.08, 0.5) < 0.25 || KeyLuma(0.1, 0.1) > 0.12 {
		t.Errorf("key luma %.2f %.2f", KeyLuma(0.08, 0.5), KeyLuma(0.1, 0.1))
	}
}

func TestSummaryLine(t *testing.T) {
	a := analyzeSynth(synth{seconds: 90, pos: phases(phase{15, 0}, phase{60, 3}, phase{90, 0}), noise: 3, seed: 12})
	line := a.Summary.Line()
	for _, want := range []string{"1 set,", "reps (best at", "dead time"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q lacks %q", line, want)
		}
	}
	if got := (Summary{}).Line(); got != "no reps or holds found" {
		t.Errorf("empty: %q", got)
	}
}

func TestAnalysisIsDeterministic(t *testing.T) {
	s := synth{seconds: 45, pos: phases(phase{10, 0}, phase{45, 3.5}), noise: 5, seed: 13}
	a, b := analyzeSynth(s), analyzeSynth(s)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("two runs on the same input differ")
	}
}

func TestTooShortClipsDoNotPanic(t *testing.T) {
	for _, secs := range []int{1, 2, 4, 6} {
		a := analyzeSynth(synth{seconds: secs, pos: sine(2), noise: 2, seed: 14})
		if a.Summary.Reps != 0 {
			t.Errorf("%d s: %d reps", secs, a.Summary.Reps)
		}
		if len(a.Shots) != 1 {
			t.Errorf("%d s: %d shots", secs, len(a.Shots))
		}
	}
	if a := Analyze(Raw{}, DefaultParams(), DefaultShotParams()); len(a.Moments) != 0 || len(a.Shots) != 0 {
		t.Errorf("empty: %+v", a)
	}
}

func TestParseRaw(t *testing.T) {
	thumbs := make([]byte, 3*1024)
	sharp := []byte("frame:0    pts:0       pts_time:0\nlavfi.signalstats.YAVG=3.5\nframe:1    pts:1       pts_time:0.1\nlavfi.signalstats.YAVG=3.75\nframe:2    pts:2       pts_time:0.2\nlavfi.signalstats.YAVG=4\n")
	scene := []byte("frame:0    pts:0       pts_time:0\nlavfi.scd.score=0.000\nframe:1    pts:1       pts_time:0.1\nlavfi.scd.score=12.500\n")
	r, err := ParseRaw(thumbs, sharp, scene)
	if err != nil {
		t.Fatal(err)
	}
	if r.N() != 3 || r.Sharp[1] != 3.75 || r.Scene[1] != 12.5 || r.Scene[2] != 12.5 {
		t.Errorf("%+v %+v", r.Sharp, r.Scene)
	}
	for name, c := range map[string]struct{ thumbs, sharp, scene []byte }{
		"empty thumbs":    {nil, sharp, scene},
		"ragged thumbs":   {thumbs[:1500], sharp, scene},
		"bad value":       {thumbs, []byte("frame:0 pts:0\nlavfi.signalstats.YAVG=abc\n"), scene},
		"value no frame":  {thumbs, []byte("lavfi.signalstats.YAVG=3\n"), scene},
		"nan":             {thumbs, []byte("frame:0 pts:0\nlavfi.signalstats.YAVG=nan\n"), scene},
		"negative frame":  {thumbs, []byte("frame:-1 pts:0\nlavfi.signalstats.YAVG=3\n"), scene},
		"too many frames": {make([]byte, (30*60*10+200)*1024), sharp, scene},
	} {
		if _, err := ParseRaw(c.thumbs, c.sharp, c.scene); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSeriesFromThumbs(t *testing.T) {
	// a still box: no motion; a box that moves: motion; brightness is the thumbnail's mean
	still := BuildSeries(synth{seconds: 5, pos: func(float64) float64 { return 0.5 }, seed: 1}.raw())
	moving := BuildSeries(synth{seconds: 5, pos: sine(2), seed: 1}.raw())
	if mean(still.Motion) > 0.01 {
		t.Errorf("still motion %.3f", mean(still.Motion))
	}
	if mean(moving.Motion) < 1 {
		t.Errorf("moving motion %.3f", mean(moving.Motion))
	}
	if l := mean(still.Luma); l < 50 || l > 90 {
		t.Errorf("luma %.1f", l)
	}
	// a global brightness change (the camera's auto exposure) is not motion
	r := synth{seconds: 5, pos: func(float64) float64 { return 0.5 }, seed: 1}.raw()
	for i := 20; i < 50; i++ {
		for p := 0; p < 1024; p++ {
			if v := int(r.Thumbs[i*1024+p]) + 20; v < 256 {
				r.Thumbs[i*1024+p] = byte(v)
			}
		}
	}
	if m := mean(BuildSeries(r).Motion); m > 0.1 {
		t.Errorf("a brightness step counts as motion %.3f", m)
	}
}
