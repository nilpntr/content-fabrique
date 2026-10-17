package beats

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestMarkers(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	ms, err := g.Markers(0, 2600, MarkerOpts{DropsMs: []int64{2000, 9999}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Marker{
		{0, "downbeat", 1.0},
		{500, "beat", 0.6},
		{1000, "beat", 0.6},
		{1500, "beat", 0.6},
		{2000, "downbeat", 1.0},
		{2000, "drop", 1.0}, // after the downbeat at the same time
		{2500, "beat", 0.6},
	}
	if !slices.Equal(ms, want) {
		t.Errorf("markers = %+v\nwant      %+v", ms, want)
	}
}

func TestMarkersRangeHalfOpen(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	ms, _ := g.Markers(500, 1500, MarkerOpts{FPS: 30})
	if len(ms) != 2 || ms[0].AtMs != 500 || ms[1].AtMs != 1000 {
		t.Errorf("markers = %+v", ms)
	}
	if _, err := g.Markers(10, 5, MarkerOpts{}); err == nil {
		t.Error("inverted range")
	}
	if _, err := g.Markers(0, 100, MarkerOpts{FPS: 29}); err == nil {
		t.Error("bad fps")
	}
}

func TestMarkersJSONShape(t *testing.T) {
	b, err := json.Marshal(Marker{AtMs: 500, Kind: KindBeat, Strength: StrengthBeat})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"at_ms":500,"kind":"beat","strength":0.6}` {
		t.Errorf("json = %s", b)
	}
}

func TestMarkersMatchSnappedCuts(t *testing.T) {
	// With FPS set, a marker has exactly the time a cut snapped to that beat has.
	r := rand.New(rand.NewPCG(9, 10)) //nolint:gosec // deterministic test data, not security
	for it := 0; it < 200; it++ {
		fps := SupportedFPS[it%4]
		g := randomConstant(r)
		markers, err := g.Markers(0, 60000, MarkerOpts{FPS: fps})
		if err != nil {
			t.Fatal(err)
		}
		set := make(map[int64]Marker, len(markers))
		prev := int64(-1)
		for _, m := range markers {
			if m.AtMs <= prev {
				t.Fatalf("markers not strictly increasing at %d", m.AtMs)
			}
			prev = m.AtMs
			if !IsAligned(m.AtMs, fps) || m.AtMs < 0 || m.AtMs >= 60000 {
				t.Fatalf("marker %+v not aligned or out of range", m)
			}
			set[m.AtMs] = m
		}
		for _, b := range g.Beats(0, 60000) {
			at := FrameToMs(frameOfExact(b.ExactMs, fps), fps)
			if at >= 60000 {
				continue
			}
			m, ok := set[at]
			if !ok {
				t.Fatalf("no marker at the snapped time %d of beat %v", at, b.ExactMs)
			}
			if (m.Kind == KindDownbeat) != b.Downbeat {
				t.Fatalf("marker %+v for beat %+v", m, b)
			}
			if math.Abs(float64(m.AtMs)-b.ExactMs) >= 1000/float64(fps) {
				t.Fatalf("marker %+v more than a frame from beat %v", m, b.ExactMs)
			}
		}
	}
}

func TestMarkersOfPlacedSound(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120, FirstBeatMs: 100})
	p := Placement{AtMs: 2000, SourceOffsetMs: 10000}
	placed, _ := g.Place(p)
	// A drop at 14.1 s in the sound lands at 6100 ms on the timeline.
	ms, _ := placed.Markers(0, 4000, MarkerOpts{DropsMs: []int64{p.ToTimeline(14100), p.ToTimeline(99999)}})
	if ms[0].AtMs != 2100 || ms[0].Kind != KindDownbeat {
		t.Errorf("first marker = %+v", ms[0])
	}
	for _, m := range ms {
		if m.AtMs < 2000 {
			t.Errorf("marker %+v before the sound starts", m)
		}
		if m.Kind == KindDrop {
			t.Errorf("drop outside the range: %+v", m)
		}
	}
}

func TestMarkersHugeRangeDoesNotOverflow(t *testing.T) {
	g := mustConstant(t, ConstantSpec{BPM: 120})
	ms, err := g.Markers(0, math.MaxInt64, MarkerOpts{FPS: 30, DropsMs: []int64{math.MaxInt64, 1000}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 1000 || ms[len(ms)-1].AtMs >= MaxTimeMs {
		t.Errorf("%d markers, last %+v", len(ms), ms[len(ms)-1])
	}
	if _, err := g.Markers(math.MaxInt64-10, math.MaxInt64, MarkerOpts{}); err != nil {
		t.Error(err)
	}
}
