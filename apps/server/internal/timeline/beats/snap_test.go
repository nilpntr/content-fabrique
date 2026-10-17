package beats

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func ats(s []Snap) []int64 {
	out := make([]int64, len(s))
	for i, x := range s {
		out[i] = x.AtMs
	}
	return out
}

func snap(t testing.TB, cuts []int64, g Grid, o SnapOpts) []Snap {
	t.Helper()
	s, err := SnapCuts(cuts, g, o)
	if err != nil {
		t.Fatalf("SnapCuts(%v): %v", cuts, err)
	}
	return s
}

func TestSnapBasic(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120}) // 500 ms
	s := snap(t, []int64{1040, 2490, 3700}, g, SnapOpts{FPS: 30})
	if got, want := ats(s), []int64{1000, 2500, 3500}; !slices.Equal(got, want) {
		t.Fatalf("cuts = %v, want %v", got, want)
	}
	for i, x := range s {
		if !x.Snapped || x.Reason != ReasonNone {
			t.Errorf("cut %d: %+v", i, x)
		}
		if x.ErrorMs != 0 || x.ErrorFrames != 0 {
			t.Errorf("cut %d: beats on frame edges at 30 fps should have no error, got %v ms", i, x.ErrorMs)
		}
	}
	if s[0].ShiftMs != -40 || s[0].InputMs != 1040 || s[0].BeatIndex != 2 || s[0].Frame != 30 {
		t.Errorf("cut 0 = %+v", s[0])
	}
}

func TestSnapBeatsOnAndOffFrameEdges(t *testing.T) {
	// 90 bpm at 24 fps: beats every 666.67 ms; beat 3 is at 2000 ms = frame 48
	// (on a frame edge), beat 1 at 666.67 ms is 16 frames (on an edge too), but
	// at 25 fps beat 1 is frame 16.67 -> 17 (666.67 -> 680 ms, off by 13.3).
	g := mustConstant(t, ConstantSpec{BPM: 90})
	s := snap(t, []int64{650}, g, SnapOpts{FPS: 24, MaxShiftMs: 100})
	if !s[0].Snapped || s[0].AtMs != 667 || math.Abs(s[0].ErrorMs-(667-2000.0/3)) > 1e-9 {
		t.Errorf("24 fps: %+v", s[0])
	}
	s = snap(t, []int64{650}, g, SnapOpts{FPS: 25, MaxShiftMs: 100})
	if !s[0].Snapped || s[0].AtMs != 680 {
		t.Errorf("25 fps: %+v", s[0])
	}
	if math.Abs(s[0].ErrorMs) >= 40 || math.Abs(s[0].ErrorFrames) >= 1 {
		t.Errorf("25 fps error = %v ms, %v frames", s[0].ErrorMs, s[0].ErrorFrames)
	}
	for _, x := range s {
		if !IsAligned(x.AtMs, 25) {
			t.Errorf("%d is not frame-aligned at 25 fps", x.AtMs)
		}
	}
}

func TestSnapMaxShiftRefusal(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 60}) // 1000 ms
	// 1400 is 400 ms from the nearest beat.
	for name, o := range map[string]SnapOpts{
		"ms":                     {FPS: 30, MaxShiftMs: 100},
		"beats":                  {FPS: 30, MaxShiftBeats: 0.1},
		"both, the smaller wins": {FPS: 30, MaxShiftMs: 1000, MaxShiftBeats: 0.2},
	} {
		s := snap(t, []int64{1401}, g, o)
		x := s[0]
		if x.Snapped || x.Reason != ReasonOutOfRange || x.AtMs != 1401 || x.ShiftMs != 0 {
			t.Errorf("%s: %+v, want a refusal that leaves the cut at 1401", name, x)
		}
		if x.Frame != MsToFrame(1401, 30) {
			t.Errorf("%s: frame = %d", name, x.Frame)
		}
	}
	// Exactly at the limit still snaps.
	s := snap(t, []int64{1400}, g, SnapOpts{FPS: 30, MaxShiftMs: 400})
	if !s[0].Snapped || s[0].AtMs != 1000 {
		t.Errorf("at the limit: %+v", s[0])
	}
	// The default limit is half a beat.
	s = snap(t, []int64{1499, 2501}, g, SnapOpts{FPS: 30})
	if got := ats(s); !slices.Equal(got, []int64{1000, 3000}) { // 499 ms away each
		t.Errorf("default limit: %v", got)
	}
	// A limit below one frame is one frame.
	s = snap(t, []int64{1010}, g, SnapOpts{FPS: 30, MaxShiftMs: 1})
	if !s[0].Snapped || s[0].AtMs != 1000 {
		t.Errorf("tiny limit: %+v", s[0])
	}
}

func TestSnapAlreadyOnBeatIsKept(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	s := snap(t, []int64{1000, 1500}, g, SnapOpts{FPS: 30, PreferDownbeats: true, MaxShiftMs: 1000})
	if got := ats(s); !slices.Equal(got, []int64{1000, 1500}) {
		t.Errorf("cuts on beats moved: %v", got)
	}
	for _, x := range s {
		if !x.Snapped || x.ShiftMs != 0 {
			t.Errorf("%+v", x)
		}
	}
}

func TestSnapPreferDownbeats(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120}) // downbeats at 0, 2000, 4000
	// 1100 is 100 from beat 1000 and 900 from downbeat 2000.
	plain := snap(t, []int64{1100}, g, SnapOpts{FPS: 30, MaxShiftMs: 1000})
	if plain[0].AtMs != 1000 || plain[0].Downbeat {
		t.Errorf("plain: %+v", plain[0])
	}
	pref := snap(t, []int64{1100}, g, SnapOpts{FPS: 30, MaxShiftMs: 1000, PreferDownbeats: true})
	if pref[0].AtMs != 2000 || !pref[0].Downbeat {
		t.Errorf("prefer: %+v", pref[0])
	}
	// No downbeat in range: falls back to the nearest beat.
	far := snap(t, []int64{1100}, g, SnapOpts{FPS: 30, MaxShiftMs: 400, PreferDownbeats: true})
	if far[0].AtMs != 1000 || far[0].Downbeat || !far[0].Snapped {
		t.Errorf("fallback: %+v", far[0])
	}
	// Preferring downbeats stays idempotent: the fallback result is on a beat.
	again := snap(t, ats(far), g, SnapOpts{FPS: 30, MaxShiftMs: 1500, PreferDownbeats: true})
	if again[0].AtMs != far[0].AtMs {
		t.Errorf("second run moved the cut to %d", again[0].AtMs)
	}
}

func TestSnapMinShotRefusal(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	// Cut 900 wants beat 1000, but the last shot would be 250 ms (end 1250).
	s := snap(t, []int64{900}, g, SnapOpts{FPS: 30, EndMs: 1250})
	x := s[0]
	if x.Snapped || x.Reason != ReasonMinShot || x.AtMs != 900 {
		t.Errorf("%+v, want min_shot refusal at 900", x)
	}
	// With room, it snaps.
	s = snap(t, []int64{900}, g, SnapOpts{FPS: 30, EndMs: 1500})
	if !s[0].Snapped || s[0].AtMs != 1000 {
		t.Errorf("%+v", s[0])
	}
	// The first shot is bounded by StartMs: cut 330 would take beat 500 (170
	// away) and does, since the first shot 100..500 is long enough. Beat 0
	// (330 away) would make the first shot negative and is never reached.
	s = snap(t, []int64{330}, g, SnapOpts{FPS: 30, MaxShiftMs: 400, StartMs: 100})
	if !s[0].Snapped || s[0].AtMs != 500 {
		t.Errorf("start bound: %+v", s[0])
	}
	// Cut 250 is nearest to beat 500 (250 away) and beat 0 (250 away); the
	// earlier one is out because the first shot 100..0 doesn't exist, the
	// later one is taken.
	s = snap(t, []int64{250}, g, SnapOpts{FPS: 30, MaxShiftMs: 250, StartMs: 100})
	if !s[0].Snapped || s[0].AtMs != 500 {
		t.Errorf("start bound, tie: %+v", s[0])
	}
	// Cut 150 with start 100: the shot is already 50 ms, so it may stay 50
	// ms but not shrink; beat 0 is before the start and beat 500 is 350 away.
	s = snap(t, []int64{150}, g, SnapOpts{FPS: 30, MaxShiftMs: 250, StartMs: 100})
	if s[0].Snapped || s[0].AtMs != 150 {
		t.Errorf("start bound, short first shot: %+v", s[0])
	}
}

func TestSnapFallsBackToNextBestBeat(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	// Cut 1 at 1250 is 250 from beats 1000 and 1500; 1000 would be only 100
	// ms after cut 0 (1100 with the end at ... ) so it takes 1500 instead.
	s := snap(t, []int64{1000, 1250}, g, SnapOpts{FPS: 30, MinShotMs: 300})
	if got := ats(s); !slices.Equal(got, []int64{1000, 1500}) {
		t.Errorf("cuts = %v", got)
	}
	if !s[0].Snapped || !s[1].Snapped {
		t.Errorf("%+v", s)
	}
}

func TestSnapNeverMergesOrCrosses(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	// Two cuts 40 ms apart, both nearest beat 1000. They are already shorter
	// than the minimum, so the shot between them may not shrink at all.
	s := snap(t, []int64{980, 1020}, g, SnapOpts{FPS: 30})
	if got := ats(s); !slices.Equal(got, []int64{980, 1020}) {
		t.Errorf("cuts = %v", got)
	}
	for _, x := range s {
		if x.Snapped || x.Reason != ReasonMinShot {
			t.Errorf("%+v", x)
		}
	}
}

func TestSnapRefusedCutBecomesPossibleOnceNeighbourMoves(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	// Left to right, cut 0 (900) can't take 1000 because cut 1 (1250) is in
	// the way, but cut 1 then moves on to 1500 and cut 0 can have 1000. The
	// result is the same as snapping the result again.
	s := snap(t, []int64{900, 1250}, g, SnapOpts{FPS: 30})
	if got := ats(s); !slices.Equal(got, []int64{1000, 1500}) {
		t.Fatalf("cuts = %v", got)
	}
	if !s[0].Snapped || !s[1].Snapped || s[0].InputMs != 900 {
		t.Errorf("%+v", s)
	}
	again := snap(t, ats(s), g, SnapOpts{FPS: 30})
	if !slices.Equal(ats(again), ats(s)) {
		t.Errorf("not idempotent: %v then %v", ats(s), ats(again))
	}
}

func TestSnapIrregularGrid(t *testing.T) {
	g := mustList(t, ListSpec{BeatsMs: []float64{0, 500, 1100, 1550, 2200, 2650, 3300}})
	// 1300: 200 from 1100 and 250 from 1550 -> 1100. 2400: 200 from 2200,
	// 250 from 2650 -> 2200. With the default limit of half the local gap
	// (225 for 1100..1550 around 1300... the bracket is 1100..1550 = 450).
	s := snap(t, []int64{1300, 2400}, g, SnapOpts{FPS: 30})
	if got := ats(s); !slices.Equal(got, []int64{1100, 2200}) {
		t.Errorf("cuts = %v", got)
	}
	// 800 sits in the 600 ms gap 500..1100: 300 from either, which is the
	// limit (half of 600), so the earlier beat wins.
	s = snap(t, []int64{800}, g, SnapOpts{FPS: 30})
	if s[0].BeatMs != 500 {
		t.Errorf("tie: %+v", s[0])
	}
	// Half a beat is not enough from 920 (420 from 500, 180 from 1100)? It is:
	// 180 < 300.
	s = snap(t, []int64{920}, g, SnapOpts{FPS: 30, MaxShiftBeats: 0.25}) // 0.25 × 600 = 150 < 180
	if s[0].Snapped {
		t.Errorf("beats limit follows the local gap: %+v", s[0])
	}
	s = snap(t, []int64{920}, g, SnapOpts{FPS: 30, MaxShiftBeats: 0.5})
	if !s[0].Snapped || s[0].BeatMs != 1100 {
		t.Errorf("%+v", s[0])
	}
}

func TestSnapInvalidInput(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	tests := []struct {
		name string
		cuts []int64
		o    SnapOpts
		fps  bool
	}{
		{"fps zero", []int64{1000}, SnapOpts{}, true},
		{"fps 29", []int64{1000}, SnapOpts{FPS: 29}, true},
		{"unsorted", []int64{2000, 1000}, SnapOpts{FPS: 30}, false},
		{"duplicate", []int64{1000, 1000}, SnapOpts{FPS: 30}, false},
		{"at start", []int64{0}, SnapOpts{FPS: 30}, false},
		{"before start", []int64{500}, SnapOpts{FPS: 30, StartMs: 600}, false},
		{"after end", []int64{2000}, SnapOpts{FPS: 30, EndMs: 2000}, false},
		{"negative shift", []int64{1000}, SnapOpts{FPS: 30, MaxShiftMs: -1}, false},
		{"nan beats", []int64{1000}, SnapOpts{FPS: 30, MaxShiftBeats: math.NaN()}, false},
		{"end before start", []int64{1000}, SnapOpts{FPS: 30, StartMs: 5000, EndMs: 4000}, false},
	}
	for _, tc := range tests {
		_, err := SnapCuts(tc.cuts, g, tc.o)
		if err == nil {
			t.Errorf("%s: no error", tc.name)
			continue
		}
		want := ErrInvalidSnap
		if tc.fps {
			want = ErrInvalidFPS
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, want)
		}
	}
	if s, err := SnapCuts(nil, g, SnapOpts{FPS: 30}); err != nil || len(s) != 0 {
		t.Errorf("no cuts: %v, %v", s, err)
	}
}

func TestSnapEmptyGrid(t *testing.T) {
	var zero Grid
	s := snap(t, []int64{1000}, zero, SnapOpts{FPS: 30})
	if s[0].Snapped || s[0].Reason != ReasonOutOfRange || s[0].AtMs != 1000 {
		t.Errorf("%+v", s[0])
	}
}

func TestSnapPlacedSound(t *testing.T) {
	// The sound starts at 1 s on the timeline from 20 s into the track.
	g := mustConstant(t, ConstantSpec{BPM: 120, FirstBeatMs: 130})
	placed, _ := g.Place(Placement{AtMs: 1000, SourceOffsetMs: 20000})
	// Sound beat 20130 -> timeline 1130, then every 500 ms.
	s := snap(t, []int64{2100, 3600}, placed, SnapOpts{FPS: 25})
	if got, want := ats(s), []int64{2120, 3640}; !slices.Equal(got, want) {
		t.Errorf("cuts = %v, want %v", got, want)
	}
	for _, x := range s {
		if math.Abs(x.ErrorMs) >= 40 {
			t.Errorf("error %v ms at 25 fps", x.ErrorMs)
		}
	}
}

// --- property tests -------------------------------------------------------

type propCase struct {
	grid  Grid
	cuts  []int64
	opts  SnapOpts
	label string
}

// randomCuts returns strictly increasing cuts with gaps in [minGap, maxGap]
// and an end bound after the last one.
func randomCuts(r *rand.Rand, n int, minGap, maxGap int64) ([]int64, int64) {
	cuts := make([]int64, n)
	at := minGap + r.Int64N(maxGap-minGap+1)
	for i := range cuts {
		cuts[i] = at
		at += minGap + r.Int64N(maxGap-minGap+1)
	}
	return cuts, at
}

func randomConstant(r *rand.Rand) Grid {
	g, err := NewConstant(ConstantSpec{
		BPM:            60 + r.Float64()*140,
		FirstBeatMs:    r.Float64() * 2000,
		BeatsPerBar:    []int{3, 4, 4, 4, 6}[r.IntN(5)],
		DownbeatOffset: 0,
	})
	if err != nil {
		panic(err)
	}
	return g
}

func randomList(r *rand.Rand) Grid {
	n := 20 + r.IntN(60)
	beats := make([]float64, n)
	at := r.Float64() * 1500
	for i := range beats {
		beats[i] = at
		at += 250 + r.Float64()*700 // irregular, between 240 and ~63 bpm
	}
	g, err := NewList(ListSpec{BeatsMs: beats})
	if err != nil {
		panic(err)
	}
	return g
}

func checkSnapInvariants(t *testing.T, tc propCase, s []Snap) {
	t.Helper()
	fps := tc.opts.FPS
	frame := 1000 / float64(fps)
	minShot := tc.opts.MinShotMs
	if minShot == 0 {
		minShot = DefaultMinShotMs
	}
	if len(s) != len(tc.cuts) {
		t.Fatalf("%s: %d snaps for %d cuts", tc.label, len(s), len(tc.cuts))
	}
	prevIn, prevOut := tc.opts.StartMs, tc.opts.StartMs
	for i, x := range s {
		if x.InputMs != tc.cuts[i] {
			t.Fatalf("%s: cut %d input %d != %d", tc.label, i, x.InputMs, tc.cuts[i])
		}
		if x.Snapped {
			if !IsAligned(x.AtMs, fps) {
				t.Fatalf("%s: cut %d at %d is not frame-aligned at %d fps", tc.label, i, x.AtMs, fps)
			}
			if math.Abs(x.ErrorMs) >= frame {
				t.Fatalf("%s: cut %d error %.3f ms >= 1 frame (%.3f ms)", tc.label, i, x.ErrorMs, frame)
			}
			if math.Abs(x.ErrorFrames) >= 1 {
				t.Fatalf("%s: cut %d error %.3f frames", tc.label, i, x.ErrorFrames)
			}
			if math.Abs(float64(x.AtMs)-x.BeatMs) != math.Abs(x.ErrorMs) {
				t.Fatalf("%s: cut %d reported error %v does not match %d vs beat %v", tc.label, i, x.ErrorMs, x.AtMs, x.BeatMs)
			}
			if x.Reason != ReasonNone {
				t.Fatalf("%s: snapped cut %d has reason %q", tc.label, i, x.Reason)
			}
		} else {
			if x.AtMs != x.InputMs || x.ShiftMs != 0 {
				t.Fatalf("%s: refused cut %d moved: %+v", tc.label, i, x)
			}
			if x.Reason == ReasonNone {
				t.Fatalf("%s: refused cut %d has no reason", tc.label, i)
			}
		}
		if x.Frame != MsToFrame(x.AtMs, fps) {
			t.Fatalf("%s: cut %d frame %d != frame(%d)", tc.label, i, x.Frame, x.AtMs)
		}
		// Order kept: strictly increasing, and no shot shorter than
		// min(minimum, what it was).
		if x.AtMs <= prevOut {
			t.Fatalf("%s: cut %d at %d is not after %d", tc.label, i, x.AtMs, prevOut)
		}
		if got, want := x.AtMs-prevOut, min(minShot, x.InputMs-prevIn); got < want {
			t.Fatalf("%s: shot before cut %d is %d ms, want >= %d", tc.label, i, got, want)
		}
		prevIn, prevOut = x.InputMs, x.AtMs
	}
	if tc.opts.EndMs != 0 {
		if got, want := tc.opts.EndMs-prevOut, min(minShot, tc.opts.EndMs-prevIn); got < want {
			t.Fatalf("%s: last shot is %d ms, want >= %d", tc.label, got, want)
		}
	}
}

func checkIdempotent(t *testing.T, tc propCase, s []Snap) {
	t.Helper()
	again, err := SnapCuts(ats(s), tc.grid, tc.opts)
	if err != nil {
		t.Fatalf("%s: second run: %v", tc.label, err)
	}
	if !slices.Equal(ats(again), ats(s)) {
		t.Fatalf("%s: not idempotent:\n first  %v\n second %v\n input  %v", tc.label, ats(s), ats(again), tc.cuts)
	}
	for i := range s {
		if again[i].Snapped != s[i].Snapped {
			t.Fatalf("%s: cut %d changed snapped state on the second run", tc.label, i)
		}
	}
}

func TestSnapPropertyAllFrameRates(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test data, not security
	iterations := 4000
	if testing.Short() {
		iterations = 400
	}
	var snapped, refused int
	for it := 0; it < iterations; it++ {
		fps := SupportedFPS[it%len(SupportedFPS)]
		var g Grid
		kind := "constant"
		if it%5 == 4 {
			g, kind = randomList(r), "list"
		} else {
			g = randomConstant(r)
		}
		// Mix of sparse and dense cuts: dense ones (down to the 300 ms
		// minimum) force min-shot conflicts.
		maxGap := []int64{450, 900, 3000}[r.IntN(3)]
		cuts, end := randomCuts(r, 2+r.IntN(14), 300, maxGap)
		o := SnapOpts{FPS: fps, EndMs: end}
		switch r.IntN(4) {
		case 1:
			o.MaxShiftMs = 20 + r.Int64N(500)
		case 2:
			o.MaxShiftBeats = 0.1 + r.Float64()
		case 3:
			o.PreferDownbeats = true
			o.MaxShiftMs = 100 + r.Int64N(1500)
		}
		if r.IntN(3) == 0 {
			o.MinShotMs = 300 + r.Int64N(300)
			// The input must satisfy the rule, or "never shorter than it
			// was" applies; both are checked by checkSnapInvariants.
		}
		tc := propCase{grid: g, cuts: cuts, opts: o, label: fmt.Sprintf("iter %d %s fps %d %+v", it, kind, fps, o)}
		s, err := SnapCuts(cuts, g, o)
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		checkSnapInvariants(t, tc, s)
		checkIdempotent(t, tc, s)
		for _, x := range s {
			if x.Snapped {
				snapped++
			} else {
				refused++
			}
		}
	}
	// The generator has to exercise both outcomes, or the test proves little.
	if snapped < 1000 || refused < 100 {
		t.Errorf("weak coverage: %d snapped, %d refused", snapped, refused)
	}
	t.Logf("%d cuts snapped, %d refused", snapped, refused)
}

// Cuts with room to move all snap, and the error bound holds at every beat
// phase (random tempo and first beat), not just for the nearest-beat choice.
func TestSnapPropertyEverythingSnapsWithRoom(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // deterministic test data, not security
	for it := 0; it < 2000; it++ {
		fps := SupportedFPS[it%len(SupportedFPS)]
		g := randomConstant(r)
		// The cuts start after the first beat (a cut before the first beat has
		// no beat on its left and may be out of range).
		first, _ := g.Nearest(0)
		period := 60000 / g.BPM()
		// Gaps of at least two beats plus the minimum shot: moving each cut
		// by at most half a beat can't break the shot rule.
		minGap := int64(2*period) + 400
		cuts, end := randomCuts(r, 3+r.IntN(8), minGap, minGap+2000)
		if cuts[0] < first.AtMs+int64(period) {
			shift := first.AtMs + int64(period) - cuts[0]
			for i := range cuts {
				cuts[i] += shift
			}
			end += shift
		}
		o := SnapOpts{FPS: fps, EndMs: end}
		tc := propCase{grid: g, cuts: cuts, opts: o, label: fmt.Sprintf("iter %d fps %d bpm %.3f", it, fps, g.BPM())}
		s, err := SnapCuts(cuts, g, o)
		if err != nil {
			t.Fatal(err)
		}
		checkSnapInvariants(t, tc, s)
		checkIdempotent(t, tc, s)
		for i, x := range s {
			if !x.Snapped {
				t.Fatalf("%s: cut %d (%d) was refused: %s", tc.label, i, x.InputMs, x.Reason)
			}
			// With plain nearest-beat snapping the cut lands within half a
			// beat plus the frame error of where it was.
			if d := math.Abs(float64(x.AtMs - x.InputMs)); d > period/2+1000/float64(fps) {
				t.Fatalf("%s: cut %d moved %.1f ms, more than half a beat (%.1f)", tc.label, i, d, period/2)
			}
		}
	}
}

// The error bound itself, at every frame rate, over many tempos and every
// beat of a long grid: the cut for any beat is within one frame of it.
func TestSnapErrorBoundEveryBeat(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6)) //nolint:gosec // deterministic test data, not security
	for _, fps := range SupportedFPS {
		frame := 1000 / float64(fps)
		worst := 0.0
		for it := 0; it < 300; it++ {
			g := randomConstant(r)
			for _, b := range g.Beats(0, 180_000) {
				cut := FrameToMs(frameOfExact(b.ExactMs, fps), fps)
				e := math.Abs(float64(cut) - b.ExactMs)
				worst = max(worst, e)
				if e >= frame {
					t.Fatalf("fps %d bpm %v beat %d: error %v ms >= %v", fps, g.BPM(), b.Index, e, frame)
				}
			}
		}
		if bound := 500/float64(fps) + 0.5; worst > bound {
			t.Errorf("fps %d: worst error %.3f ms exceeds the proven bound %.3f", fps, worst, bound)
		}
		t.Logf("fps %d: worst error %.3f ms of %.3f ms per frame", fps, worst, frame)
	}
}

func FuzzSnapCuts(f *testing.F) {
	f.Add(int64(1000), int64(2300), 120.0, 30)
	f.Add(int64(900), int64(1250), 97.3, 24)
	f.Fuzz(func(t *testing.T, a, b int64, bpm float64, fpsIdx int) {
		if bpm < MinBPM || bpm > MaxBPM || math.IsNaN(bpm) {
			t.Skip()
		}
		a, b = a%200000, b%200000
		if a < 1 || b <= a {
			t.Skip()
		}
		fps := SupportedFPS[((fpsIdx%4)+4)%4]
		g, err := NewConstant(ConstantSpec{BPM: bpm})
		if err != nil {
			t.Skip()
		}
		cuts := []int64{a, b}
		o := SnapOpts{FPS: fps}
		tc := propCase{grid: g, cuts: cuts, opts: o, label: "fuzz"}
		s, err := SnapCuts(cuts, g, o)
		if err != nil {
			t.Fatal(err)
		}
		checkSnapInvariants(t, tc, s)
		checkIdempotent(t, tc, s)
	})
}
