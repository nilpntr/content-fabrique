package beats

import (
	"errors"
	"math"
	"slices"
	"testing"
)

func mustConstant(t testing.TB, spec ConstantSpec) Grid {
	t.Helper()
	g, err := NewConstant(spec)
	if err != nil {
		t.Fatalf("NewConstant(%+v): %v", spec, err)
	}
	return g
}

func mustList(t testing.TB, spec ListSpec) Grid {
	t.Helper()
	g, err := NewList(spec)
	if err != nil {
		t.Fatalf("NewList(%+v): %v", spec, err)
	}
	return g
}

func times(bs []Beat) []int64 {
	out := make([]int64, len(bs))
	for i, b := range bs {
		out[i] = b.AtMs
	}
	return out
}

func TestNewConstantValidation(t *testing.T) {
	tests := []struct {
		name string
		spec ConstantSpec
		ok   bool
	}{
		{"typical", ConstantSpec{BPM: 120}, true},
		{"min bpm", ConstantSpec{BPM: 40}, true},
		{"max bpm", ConstantSpec{BPM: 240}, true},
		{"below min", ConstantSpec{BPM: 39.99}, false},
		{"above max", ConstantSpec{BPM: 240.01}, false},
		{"zero", ConstantSpec{}, false},
		{"nan", ConstantSpec{BPM: math.NaN()}, false},
		{"negative first beat", ConstantSpec{BPM: 120, FirstBeatMs: -1}, false},
		{"infinite first beat", ConstantSpec{BPM: 120, FirstBeatMs: math.Inf(1)}, false},
		{"bars of 3", ConstantSpec{BPM: 120, BeatsPerBar: 3, DownbeatOffset: 2}, true},
		{"offset too big", ConstantSpec{BPM: 120, BeatsPerBar: 4, DownbeatOffset: 4}, false},
		{"negative offset", ConstantSpec{BPM: 120, DownbeatOffset: -1}, false},
		{"bar too long", ConstantSpec{BPM: 120, BeatsPerBar: 17}, false},
		{"negative bar", ConstantSpec{BPM: 120, BeatsPerBar: -4}, false},
	}
	for _, tc := range tests {
		_, err := NewConstant(tc.spec)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidGrid) {
			t.Errorf("%s: err = %v, want ErrInvalidGrid", tc.name, err)
		}
	}
}

func TestNewListValidation(t *testing.T) {
	tests := []struct {
		name string
		spec ListSpec
		ok   bool
	}{
		{"typical", ListSpec{BeatsMs: []float64{0, 500, 1000}}, true},
		{"one beat", ListSpec{BeatsMs: []float64{0}}, false},
		{"empty", ListSpec{}, false},
		{"unsorted", ListSpec{BeatsMs: []float64{0, 1000, 500}}, false},
		{"duplicate", ListSpec{BeatsMs: []float64{0, 500, 500}}, false},
		{"negative", ListSpec{BeatsMs: []float64{-5, 500}}, false},
		{"nan", ListSpec{BeatsMs: []float64{0, math.NaN()}}, false},
		{"too close (above 240 bpm)", ListSpec{BeatsMs: []float64{0, 249}}, false},
		{"exactly 240 bpm", ListSpec{BeatsMs: []float64{0, 250}}, true},
		{"downbeats on beats", ListSpec{BeatsMs: []float64{0, 500, 1000, 1500}, DownbeatsMs: []float64{0, 1000.4}}, true},
		{"downbeat off the grid", ListSpec{BeatsMs: []float64{0, 500, 1000}, DownbeatsMs: []float64{750}}, false},
		{"bad bar", ListSpec{BeatsMs: []float64{0, 500}, BeatsPerBar: 99}, false},
	}
	for _, tc := range tests {
		_, err := NewList(tc.spec)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidGrid) {
			t.Errorf("%s: err = %v, want ErrInvalidGrid", tc.name, err)
		}
	}
}

func TestListCopiesInput(t *testing.T) {
	in := []float64{0, 500, 1000}
	g := mustList(t, ListSpec{BeatsMs: in})
	in[1] = 9999
	if got := times(g.Beats(0, 2000)); !slices.Equal(got, []int64{0, 500, 1000}) {
		t.Errorf("grid changed with its input: %v", got)
	}
}

func TestConstantBeatsAndDownbeats(t *testing.T) {
	// 120 bpm: a beat every 500 ms, first at 100 ms, downbeat on every 4th starting at beat 1.
	g := mustConstant(t, ConstantSpec{BPM: 120, FirstBeatMs: 100, DownbeatOffset: 1})
	bs := g.Beats(0, 2600)
	if got, want := times(bs), []int64{100, 600, 1100, 1600, 2100}; !slices.Equal(got, want) {
		t.Fatalf("beats = %v, want %v", got, want)
	}
	var down []int64
	for _, b := range bs {
		if b.Downbeat {
			down = append(down, b.AtMs)
		}
	}
	if !slices.Equal(down, []int64{600}) {
		t.Errorf("downbeats = %v, want [600]", down)
	}
	if got := times(g.Downbeats(0, 6000)); !slices.Equal(got, []int64{600, 2600, 4600}) {
		t.Errorf("Downbeats = %v", got)
	}
	for i, b := range bs {
		if b.Index != i {
			t.Errorf("beat %d has Index %d", i, b.Index)
		}
	}
	// [from, to) is half open.
	if got := times(g.Beats(100, 600)); !slices.Equal(got, []int64{100}) {
		t.Errorf("Beats(100, 600) = %v, want [100]", got)
	}
	if got := g.Beats(600, 600); got != nil {
		t.Errorf("empty range = %v", got)
	}
}

func TestBeatTimesDoNotDrift(t *testing.T) {
	// 128 bpm is 468.75 ms per beat: beat 4000 is at exactly 1 875 000 ms. A
	// grid that added a rounded 469 ms would be 1 000 ms off.
	g := mustConstant(t, ConstantSpec{BPM: 128})
	bs := g.Beats(1_875_000, 1_875_001)
	if len(bs) != 1 || bs[0].AtMs != 1_875_000 || bs[0].ExactMs != 1_875_000 {
		t.Fatalf("beat 4000 = %+v", bs)
	}
	// Half-millisecond beats round up: beat 1 at 468.75 -> 469; at 100 bpm
	// (600 ms) with a first beat of 0.5 the beat is at 0.5 -> 1.
	h := mustConstant(t, ConstantSpec{BPM: 100, FirstBeatMs: 0.5})
	if got := h.Beats(0, 10)[0].AtMs; got != 1 {
		t.Errorf("0.5 rounds to %d, want 1", got)
	}
}

func TestNearestNextPrev(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120, FirstBeatMs: 0})
	tests := []struct {
		ms                  int64
		nearest, next, prev int64
		hasPrev             bool
	}{
		{0, 0, 500, 0, false},
		{1, 0, 500, 0, true},
		{249, 0, 500, 0, true},
		{250, 0, 500, 0, true}, // tie goes to the earlier beat
		{251, 500, 500, 0, true},
		{500, 500, 1000, 0, true},
		{501, 500, 1000, 500, true},
	}
	for _, tc := range tests {
		n, ok := g.Nearest(tc.ms)
		if !ok || n.AtMs != tc.nearest {
			t.Errorf("Nearest(%d) = %v, %v; want %d", tc.ms, n.AtMs, ok, tc.nearest)
		}
		nx, ok := g.Next(tc.ms)
		if !ok || nx.AtMs != tc.next {
			t.Errorf("Next(%d) = %v, %v; want %d", tc.ms, nx.AtMs, ok, tc.next)
		}
		pv, ok := g.Prev(tc.ms)
		if ok != tc.hasPrev || (ok && pv.AtMs != tc.prev) {
			t.Errorf("Prev(%d) = %v, %v; want %d, %v", tc.ms, pv.AtMs, ok, tc.prev, tc.hasPrev)
		}
	}
	// Before the first beat of a grid that starts late.
	late := mustConstant(t, ConstantSpec{BPM: 120, FirstBeatMs: 700})
	if n, _ := late.Nearest(0); n.AtMs != 700 {
		t.Errorf("Nearest before first beat = %d", n.AtMs)
	}
	if _, ok := late.Prev(700); ok {
		t.Error("Prev(first beat) should have no result")
	}
}

func TestNearestDownbeat(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120}) // downbeats at 0, 2000, 4000
	for _, tc := range []struct{ ms, want int64 }{{0, 0}, {900, 0}, {1000, 0}, {1001, 2000}, {2900, 2000}, {3100, 4000}} {
		b, ok := g.NearestDownbeat(tc.ms)
		if !ok || b.AtMs != tc.want || !b.Downbeat {
			t.Errorf("NearestDownbeat(%d) = %+v, %v; want %d", tc.ms, b, ok, tc.want)
		}
	}
}

func TestIrregularList(t *testing.T) {
	g := mustList(t, ListSpec{
		BeatsMs:     []float64{0, 500, 1100, 1550, 2200, 2650},
		DownbeatsMs: []float64{0, 2200},
	})
	if got := times(g.Beats(0, 10000)); !slices.Equal(got, []int64{0, 500, 1100, 1550, 2200, 2650}) {
		t.Fatalf("beats = %v", got)
	}
	if got := times(g.Downbeats(0, 10000)); !slices.Equal(got, []int64{0, 2200}) {
		t.Errorf("downbeats = %v", got)
	}
	if n, _ := g.Nearest(1300); n.AtMs != 1100 { // 200 from 1100, 250 from 1550
		t.Errorf("Nearest(1300) = %d", n.AtMs)
	}
	if n, _ := g.Nearest(9999); n.AtMs != 2650 {
		t.Errorf("Nearest past the end = %d", n.AtMs)
	}
	if _, ok := g.Next(2650); ok {
		t.Error("Next after the last beat should have no result")
	}
	if b, ok := g.Prev(1100); !ok || b.AtMs != 500 {
		t.Errorf("Prev(1100) = %+v", b)
	}
}

func TestListInferredDownbeats(t *testing.T) {
	g := mustList(t, ListSpec{BeatsMs: []float64{0, 500, 1000, 1500, 2000, 2500}, BeatsPerBar: 2})
	if got := times(g.Downbeats(0, 10000)); !slices.Equal(got, []int64{0, 1000, 2000}) {
		t.Errorf("inferred downbeats = %v", got)
	}
}

func TestPlace(t *testing.T) {
	// A 120 bpm sound with its first beat at 100 ms. The snippet starts 10 s
	// into the sound and is placed at 2 s on the timeline.
	g := mustConstant(t, ConstantSpec{BPM: 120, FirstBeatMs: 100})
	p := Placement{AtMs: 2000, SourceOffsetMs: 10000}
	placed, err := g.Place(p)
	if err != nil {
		t.Fatal(err)
	}
	// Sound beat 10100 is the first one at or after the offset: timeline 2100.
	// Sound beats before 10000 (9600 -> timeline 1600) are not heard.
	bs := placed.Beats(0, 4000)
	if got := times(bs); !slices.Equal(got, []int64{2100, 2600, 3100, 3600}) {
		t.Fatalf("placed beats = %v", got)
	}
	// Sound beat index (10100-100)/500 = 20 is a downbeat; the phase is kept.
	if !bs[0].Downbeat || bs[1].Downbeat {
		t.Errorf("downbeat phase lost: %+v", bs)
	}
	if bs[0].Index != 0 {
		t.Errorf("first audible beat has Index %d", bs[0].Index)
	}
	if got := p.ToTimeline(10100); got != 2100 {
		t.Errorf("ToTimeline(10100) = %d", got)
	}
	if got := p.ToSound(2100); got != 10100 {
		t.Errorf("ToSound(2100) = %d", got)
	}
	// The original grid is untouched.
	if got := g.Beats(0, 700); len(got) != 2 {
		t.Errorf("Place mutated the source grid: %v", times(got))
	}
}

func TestPlaceDurationAndOffsetBeforeFirstBeat(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120, FirstBeatMs: 300})
	// Offset 0 and at 1000: the first beat is at 1300; the sound ends after 2 s.
	placed, err := g.Place(Placement{AtMs: 1000, SourceOffsetMs: 0, DurationMs: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if got := times(placed.Beats(0, 10000)); !slices.Equal(got, []int64{1300, 1800, 2300, 2800}) {
		t.Errorf("beats = %v", got)
	}
	// List grids clip the same way.
	l := mustList(t, ListSpec{BeatsMs: []float64{0, 500, 1000, 1500, 2000}})
	lp, _ := l.Place(Placement{AtMs: 100, SourceOffsetMs: 600, DurationMs: 1000})
	// Sound beats 1000, 1500 are at or after 600 -> timeline 500, 1000; 2000 -> 1500 is past 100+1000.
	if got := times(lp.Beats(0, 10000)); !slices.Equal(got, []int64{500, 1000}) {
		t.Errorf("list beats = %v", got)
	}
	// A window with no beats is empty, not an error.
	none, _ := g.Place(Placement{AtMs: 0, SourceOffsetMs: 0, DurationMs: 100})
	if !none.Empty() || none.Beats(0, 10000) != nil {
		t.Errorf("expected an empty grid, got %v", times(none.Beats(0, 10000)))
	}
	if _, ok := none.Nearest(0); ok {
		t.Error("Nearest on an empty grid should have no result")
	}
}

func TestPlaceValidation(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	for _, p := range []Placement{{AtMs: -1}, {SourceOffsetMs: -1}, {DurationMs: -1}} {
		if _, err := g.Place(p); !errors.Is(err, ErrInvalidGrid) {
			t.Errorf("Place(%+v) err = %v", p, err)
		}
	}
}

func TestPlaceTwice(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	a, _ := g.Place(Placement{AtMs: 1000, SourceOffsetMs: 0})
	b, _ := a.Place(Placement{AtMs: 500, SourceOffsetMs: 0})
	if got := times(b.Beats(0, 2600)); !slices.Equal(got, []int64{1500, 2000, 2500}) {
		t.Errorf("beats = %v", got)
	}
}
