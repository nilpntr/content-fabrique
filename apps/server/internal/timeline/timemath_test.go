package timeline

import (
	"bytes"
	"fmt"
	"math"
	"path/filepath"
	"testing"
)

func TestOutputDuration(t *testing.T) {
	cases := []struct {
		in, out int64
		speed   *float64
		want    int64
	}{
		{0, 3000, nil, 3000},
		{0, 3001, ptr(2.0), 1501}, // 1500.5 rounds half up
		{0, 3000, ptr(2.0), 1500},
		{0, 3002, ptr(2.0), 1501},
		{0, 1, ptr(2.0), 1},  // 0.5 -> 1
		{0, 1, ptr(4.0), 0},  // 0.25 -> 0
		{0, 2, ptr(4.0), 1},  // 0.5 -> 1
		{0, 3, ptr(4.0), 1},  // 0.75 -> 1
		{0, 1, ptr(0.25), 4}, // slowest speed
		{500, 3500, ptr(0.25), 12000},
		{0, 1000, ptr(1.5), 667}, // 666.67
		{0, 1001, ptr(1.5), 667}, // 667.33
		{0, 3000, ptr(1.25), 2400},
		{1000, 1000, nil, 0}, // in == out
		{2000, 1000, nil, 0}, // in > out
		{0, 86_400_000, ptr(0.25), 345_600_000},
	}
	for _, tc := range cases {
		it := VideoItem{Source: Source{InMs: tc.in, OutMs: tc.out}, Speed: tc.speed}
		if got := OutputDuration(it); got != tc.want {
			t.Errorf("OutputDuration(%d..%d, speed %v) = %d, want %d", tc.in, tc.out, tc.speed, got, tc.want)
		}
	}
	// Agrees with math.Floor(x + 0.5) for every integer span at the speeds whose quotient is exact.
	for _, speed := range []float64{0.25, 0.5, 1, 2, 4} {
		for span := int64(1); span < 5000; span++ {
			it := VideoItem{Source: Source{OutMs: span}, Speed: ptr(speed)}
			if got, want := OutputDuration(it), int64(math.Floor(float64(span)/speed+0.5)); got != want {
				t.Fatalf("span %d speed %v: %d != %d", span, speed, got, want)
			}
		}
	}
}

func TestTotalDuration(t *testing.T) {
	if got := TotalDuration(baseSpec()); got != 5000 {
		t.Fatalf("got %d", got)
	}
	c3 := clipAt("c3", 0, 2000, 1700)
	c3.TransitionIn = &Transition{Type: TransitionWhip, DurationMs: 300}
	s := withClips(baseSpec(), clipAt("c1", 0, 2000, 0), clipAt("c2", 5000, 4000, 1700), c3)
	if got := TotalDuration(s); got != 3700 { // c2 has in > out and is left out
		t.Fatalf("got %d", got)
	}
	if got := TotalDuration(withClips(baseSpec())); got != 0 {
		t.Fatalf("empty main: %d", got)
	}
	if got := TotalDuration(nil); got != 0 {
		t.Fatalf("nil: %d", got)
	}
	if got := TotalDuration(&Spec{}); got != 0 {
		t.Fatalf("no tracks: %d", got)
	}
}

func TestFrame(t *testing.T) {
	cases := []struct {
		ms   int64
		fps  int
		want int64
	}{
		{0, 30, 0},
		{16, 30, 0}, // 0.48
		{17, 30, 1}, // 0.51
		{1000, 30, 30},
		{500, 25, 13}, // 12.5 rounds half up
		{20, 25, 1},   // 0.5 exactly
		{19, 25, 0},   // 0.475
		{20, 24, 0},   // 0.48
		{21, 24, 1},   // 0.504
		{1, 60, 0},    // 0.06
		{9, 60, 1},    // 0.54
		{86_400_000, 60, 5_184_000},
	}
	for _, tc := range cases {
		if got := Frame(tc.ms, tc.fps); got != tc.want {
			t.Errorf("Frame(%d, %d) = %d, want %d", tc.ms, tc.fps, got, tc.want)
		}
	}
	// The integer formula agrees with floor(ms * fps / 1000 + 0.5).
	for _, fps := range []int{24, 25, 30, 60} {
		for ms := int64(0); ms <= 200_000; ms++ {
			want := int64(math.Floor(float64(ms)*float64(fps)/1000 + 0.5))
			if got := Frame(ms, fps); got != want {
				t.Fatalf("Frame(%d, %d) = %d, want %d", ms, fps, got, want)
			}
		}
	}
}

func TestFrameStartMsRoundTrips(t *testing.T) {
	for _, fps := range []int{24, 25, 30, 60} {
		for n := int64(0); n < 20_000; n++ {
			if got := Frame(FrameStartMs(n, fps), fps); got != n {
				t.Fatalf("fps %d frame %d -> %d ms -> frame %d", fps, n, FrameStartMs(n, fps), got)
			}
		}
	}
	if FrameStartMs(5, 0) != 0 {
		t.Fatal("fps 0")
	}
}

// Item boundaries snap from the cumulative at_ms, so rounding cannot drift: a long run of 33 ms items at 30 fps ends
// on the frame of its total, not on the sum of per-item rounded lengths.
func TestFrameSnapsFromCumulativeTime(t *testing.T) {
	var sumOfRounded, at int64
	prev := int64(0)
	for range 3000 {
		at += 33
		f := Frame(at, 30)
		sumOfRounded += f - prev
		prev = f
	}
	if want := Frame(at, 30); sumOfRounded != want {
		t.Fatalf("cumulative snapping drifted: %d != %d", sumOfRounded, want)
	}
	// Whereas summing the per-item lengths drifts: Frame(33) is 1 and 3000 of them make 3000 frames, but 99000 ms is 2970.
	if perItem := 3000 * Frame(33, 30); perItem == Frame(at, 30) {
		t.Fatal("expected the naive sum to drift; the test has lost its point")
	}
}

func TestFloorDiv(t *testing.T) {
	cases := [][3]int64{{7, 2, 3}, {-7, 2, -4}, {7, -2, -4}, {-7, -2, 3}, {6, 3, 2}, {-6, 3, -2}, {0, 5, 0}}
	for _, c := range cases {
		if got := floorDiv(c[0], c[1]); got != c[2] {
			t.Errorf("floorDiv(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

func TestDefaultsOnRead(t *testing.T) {
	var v VideoItem
	if v.SpeedOrDefault() != 1 || v.TransitionDurationMs() != 0 {
		t.Fatal("video defaults")
	}
	v.TransitionIn = &Transition{Type: TransitionCut}
	if v.TransitionDurationMs() != 0 {
		t.Fatal("a cut has no duration")
	}
	v.TransitionIn = &Transition{Type: TransitionWhip, DurationMs: 250}
	if v.TransitionDurationMs() != 250 {
		t.Fatal("whip duration")
	}
	r := Reframe{Mode: ReframeCover}
	if r.FocusOrDefault() != (Point{0.5, 0.5}) || r.ZoomOrDefault() != 1 {
		t.Fatal("reframe defaults")
	}
	if (VideoAudio{}).UseSourceOrDefault() || (VideoAudio{}).GainDBOrDefault() != 0 {
		t.Fatal("audio defaults")
	}
	if (TextItem{}).AnimationOrDefault() != "none" {
		t.Fatal("animation default")
	}
	if !(CaptionsTrack{}).UppercaseOrDefault() || (CaptionsTrack{}).MaxWordsOrDefault() != 2 {
		t.Fatal("captions defaults")
	}
	if (Marker{}).StrengthOrDefault() != 1 {
		t.Fatal("marker strength")
	}
	c := AudioCommon{AtMs: 1000}
	if c.EffectiveDurationMs(5000) != 4000 || c.GainDBOrDefault() != 0 {
		t.Fatal("audio defaults")
	}
	if in, out := c.FadeMs(); in != 0 || out != 0 {
		t.Fatal("fade defaults")
	}
	// Defaults are applied on read only: they are never written into an encoded spec.
	b, _ := Parse([]byte(`{"schema":"contentfabrique.timeline/v1","output":{"format":"1:1","width":1080,"height":1080,"fps":30,"background":"#000000"},` +
		`"tracks":[{"id":"main","type":"video","items":[{"id":"c1","asset_id":"` + assetVideo60 + `","source":{"in_ms":0,"out_ms":2000},"at_ms":0}]}],"markers":[]}`))
	enc, _ := marshalCompact(b)
	for _, key := range []string{"speed", "reframe", "look", "transition_in", "audio", "why"} {
		if bytes.Contains(enc, []byte(fmt.Sprintf("%q", key))) {
			t.Errorf("encoding added %q", key)
		}
	}
}

func marshalCompact(s *Spec) ([]byte, error) { return s.MarshalJSON() }

func TestMemoryCatalog(t *testing.T) {
	cat := fixtureCatalog(t)
	if a, ok := cat.Lookup("ast_01j3er3kwcgrasdgd4aznmxxe0"); !ok || a != (Asset{DurationMs: 60000, Kind: AssetVideo, Rights: RightsAttested}) {
		t.Fatalf("lookup %+v %v", a, ok)
	}
	if a, ok := cat.Lookup("ast_01j0h9rew727hk2pt0v5b31h44"); !ok || a.Kind != AssetAudio || a.Rights != RightsLicensed {
		t.Fatalf("lookup %+v %v", a, ok)
	}
	if _, ok := cat.Lookup(assetUnknown); ok {
		t.Fatal("unknown asset reported as known")
	}
	if _, err := ParseMemoryCatalog([]byte(`{`)); err == nil {
		t.Fatal("want an error")
	}
	if _, err := LoadMemoryCatalog(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("want an error")
	}
	var _ AssetCatalog = cat
}

// TestEmbeddedSchemaMatchesSource is the in-test twin of the CI drift check: the copy under schema/ is what
// packages/schemas/timeline/v1.schema.json says. `task gen` refreshes it.
func TestEmbeddedSchemaMatchesSource(t *testing.T) {
	src := readFile(t, filepath.Join(fixturesDir, "..", "v1.schema.json"))
	if !bytes.Equal(src, SchemaJSON()) {
		t.Fatal("internal/timeline/schema/v1.schema.json differs from packages/schemas/timeline/v1.schema.json; run `task gen`")
	}
	if _, err := compiled(); err != nil {
		t.Fatal(err)
	}
}
