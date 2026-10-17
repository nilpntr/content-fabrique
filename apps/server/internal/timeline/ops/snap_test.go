package ops

import (
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// snapSpec has cuts at 3100 and 7100 ms, which are between beats of the usual test grids, and enough room in every
// asset for a cut to move 150 ms either way.
func snapSpec(t testing.TB) timeline.Spec {
	t.Helper()
	return parseSpec(t, []byte(`{
  "schema": "contentfabrique.timeline/v1",
  "output": {"format": "9:16", "width": 1080, "height": 1920, "fps": 30, "background": "#000000"},
  "tracks": [
    {"id": "main", "type": "video", "items": [
      {"id": "c1", "asset_id": "`+vid60+`", "source": {"in_ms": 1000, "out_ms": 4100}, "at_ms": 0},
      {"id": "c2", "asset_id": "`+vid45+`", "source": {"in_ms": 2000, "out_ms": 6000}, "at_ms": 3100},
      {"id": "c3", "asset_id": "`+vid30+`", "source": {"in_ms": 1000, "out_ms": 4000}, "at_ms": 7100}
    ]},
    {"id": "sound", "type": "audio", "items": [
      {"id": "s1", "kind": "trend_sound", "trend_id": "`+trend+`", "at_ms": 0, "source_offset_ms": 0, "export": "cue_only"}
    ]}
  ],
  "markers": []
}`))
}

func cuts(t testing.TB, spec timeline.Spec) []int64 {
	t.Helper()
	var out []int64
	for _, c := range clips(t, spec)[1:] {
		out = append(out, c.AtMs)
	}
	return out
}

// assertOnBeats checks that every cut is within one frame of one of the beats.
func assertOnBeats(t testing.TB, spec timeline.Spec, beatsMs []float64) {
	t.Helper()
	frameMs := 1000 / float64(spec.Output.FPS)
	for _, cut := range cuts(t, spec) {
		best := math.MaxFloat64
		for _, b := range beatsMs {
			best = min(best, math.Abs(float64(cut)-b))
		}
		if best > frameMs {
			t.Errorf("cut at %d ms is %.1f ms from the nearest beat, more than one frame (%.1f ms)", cut, best, frameMs)
		}
	}
}

func constantBeats(bpm, first float64, upTo int) []float64 {
	var out []float64
	for i := 0; ; i++ {
		b := first + float64(i)*60000/bpm
		if b > float64(upTo) {
			return out
		}
		out = append(out, b)
	}
}

func TestSnapCutsToBeats(t *testing.T) {
	base := snapSpec(t)
	total := timeline.TotalDuration(&base)

	t.Run("moves the cuts onto the beats and keeps the length", func(t *testing.T) {
		out, res := apply(t, base, `[{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0}}]`)
		if !res.OK() || len(res.Issues) != 0 {
			t.Fatalf("%+v", res.Issues)
		}
		if got := clipTimes(t, out); got != "c1:0-3000 c2:3000-7000 c3:7000-10100" {
			t.Fatalf("clips: %s", got)
		}
		if timeline.TotalDuration(&out) != total {
			t.Fatal("the total duration changed")
		}
		// The source ranges moved with the cuts: c1 ends 100 ms earlier, c2 and c3 start 100 ms earlier in their assets.
		c := clips(t, out)
		if c[0].Source.OutMs != 4000 || c[1].Source.InMs != 1900 || c[1].Source.OutMs != 5900 || c[2].Source.InMs != 900 {
			t.Fatalf("sources: %+v", c)
		}
	})
	t.Run("cuts land within one frame of a tempo that does not divide the frame", func(t *testing.T) {
		for _, bpm := range []float64{90, 100.5, 128, 133.3, 174} {
			for _, first := range []float64{0, 37.5, 210} {
				js := `[{"op":"snap_cuts_to_beats","grid":{"bpm":` + ftoa(bpm) + `,"first_beat_ms":` + ftoa(first) + `}}]`
				out, res := apply(t, base, js)
				if !res.OK() {
					t.Fatalf("%s: %+v", js, res.Issues)
				}
				assertOnBeats(t, out, constantBeats(bpm, first, 12000))
			}
		}
	})
	t.Run("works at other speeds and frame rates", func(t *testing.T) {
		for _, fps := range []int{24, 25, 30, 60} {
			for _, speed := range []string{"0.25", "0.5", "1.25", "2", "3.7"} {
				spec := base
				spec.Output.FPS = fps
				spec = mustApply(t, spec, `[{"op":"set_speed","id":"c2","speed":`+speed+`}]`)
				// The speed changed the length of c2; the cuts are wherever they are now.
				out, res := apply(t, spec, `[{"op":"snap_cuts_to_beats","grid":{"bpm":128,"first_beat_ms":10}}]`)
				if !res.OK() {
					t.Fatalf("fps %d speed %s: %+v", fps, speed, res.Issues)
				}
				// With a slow clip a source millisecond is up to 4 ms of output, so allow that on top of a frame.
				frameMs := 1000/float64(fps) + 4
				for _, cut := range cuts(t, out) {
					best := math.MaxFloat64
					for _, b := range constantBeats(128, 10, 40000) {
						best = min(best, math.Abs(float64(cut)-b))
					}
					if best > frameMs {
						t.Errorf("fps %d speed %s: cut at %d ms is %.1f ms from a beat", fps, speed, cut, best)
					}
				}
			}
		}
	})
	t.Run("is idempotent", func(t *testing.T) {
		once := mustApply(t, base, `[{"op":"snap_cuts_to_beats","grid":{"bpm":128,"first_beat_ms":10}}]`)
		twice := mustApply(t, once, `[{"op":"snap_cuts_to_beats","grid":{"bpm":128,"first_beat_ms":10}}]`)
		if !sameSpec(t, once, twice) {
			t.Fatalf("second snap changed the spec: %s vs %s", clipTimes(t, once), clipTimes(t, twice))
		}
	})
	t.Run("places the grid with a sound", func(t *testing.T) {
		out := mustApply(t, base, `[
			{"op":"set_sound","id":"s1","kind":"trend_sound","trend_id":"`+trend+`","at_ms":250},
			{"op":"snap_cuts_to_beats","sound_id":"s1","grid":{"bpm":120,"first_beat_ms":0}}]`)
		// The sound's beats are at 250 + 500k on the timeline.
		assertOnBeats(t, out, constantBeats(120, 250, 12000))
		if got := cuts(t, out); got[0] < 3200 || got[0] > 3300 {
			t.Fatalf("cuts: %v", got)
		}
	})
	t.Run("a beat list, and downbeats", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"snap_cuts_to_beats","grid":{"beats_ms":[0,1000,2000,3000,4000,5000,6000,7000,8000,9000],"downbeats_ms":[0,4000,8000]}}]`)
		assertOnBeats(t, out, []float64{3000, 7000})
		out = mustApply(t, base, `[{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0},"options":{"prefer_downbeats":true,"max_shift_ms":1000}}]`)
		if got := clipTimes(t, out); got != "c1:0-4000 c2:4000-8000 c3:8000-10100" {
			t.Fatalf("clips: %s", got)
		}
	})
	t.Run("writes markers when asked", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0},"options":{"write_markers":true}}]`)
		if len(out.Markers) != 21 || out.Markers[0].Kind != "downbeat" || out.Markers[1].Kind != "beat" || out.Markers[1].AtMs != 500 {
			t.Fatalf("markers: %+v", out.Markers)
		}
		// A marker sits where a snapped cut is.
		found := false
		for _, m := range out.Markers {
			if m.AtMs == cuts(t, out)[0] {
				found = true
			}
		}
		if !found {
			t.Fatalf("no marker at the cut %d", cuts(t, out)[0])
		}
		// Other markers stay; the beat markers are replaced, not added to.
		spec := out
		spec.Markers = append(slices.Clone(spec.Markers), timeline.Marker{AtMs: 5100, Kind: "section"})
		again := mustApply(t, spec, `[{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0},"options":{"write_markers":true}}]`)
		if len(again.Markers) != 22 {
			t.Fatalf("markers: %d", len(again.Markers))
		}
		none := mustApply(t, base, `[{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0}}]`)
		if len(none.Markers) != 0 {
			t.Fatal("markers written without write_markers")
		}
	})
	t.Run("a cut with no beat in range stays, with a warning", func(t *testing.T) {
		out, res := apply(t, base, `[{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0},"options":{"max_shift_ms":10}}]`)
		if !res.OK() || len(res.Issues) != 2 || res.Issues[0].Code != CodeCutNotSnapped || res.Issues[0].Severity != timeline.SeverityWarning {
			t.Fatalf("%+v", res.Issues)
		}
		if !sameSpec(t, out, base) {
			t.Fatal("the spec changed")
		}
	})
	t.Run("a cut that would take a clip past its asset stays, with a warning", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"trim_item","id":"c3","in_ms":0,"out_ms":3000}]`)
		out, res := apply(t, spec, `[{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0}}]`)
		if !res.OK() {
			t.Fatalf("%+v", res.Issues)
		}
		if got := cuts(t, out); got[0] != 3000 || got[1] != 7100 {
			t.Fatalf("cuts: %v", got)
		}
		var warned bool
		for _, is := range res.Issues {
			if is.Code == CodeCutNotSnapped && is.Severity == timeline.SeverityWarning {
				warned = true
			}
		}
		if !warned {
			t.Fatalf("no warning: %+v", res.Issues)
		}
	})
	t.Run("one clip has no cut", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"remove_item","id":"c2"},{"op":"remove_item","id":"c3"},{"op":"trim_item","id":"c1","out_ms":4000}]`)
		out, res := apply(t, spec, `[{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0}}]`)
		if !res.OK() || len(res.Issues) != 1 || res.Issues[0].Code != CodeNoCuts || !sameSpec(t, out, spec) {
			t.Fatalf("%+v", res.Issues)
		}
	})
	t.Run("errors", func(t *testing.T) {
		bpm := 10.0
		first := 0.0
		for name, op := range map[string]*SnapCutsToBeats{
			"bpm too low":   {Grid: BeatGrid{BPM: &bpm, FirstBeatMs: &first}},
			"empty grid":    {Grid: BeatGrid{}},
			"missing first": {Grid: BeatGrid{BPM: ptr(120.0)}},
			"mixed grid":    {Grid: BeatGrid{BPM: ptr(120.0), FirstBeatMs: &first, BeatsMs: []float64{0, 500}}},
			"one beat":      {Grid: BeatGrid{BeatsMs: []float64{0}}},
			"unknown sound": {Grid: BeatGrid{BPM: ptr(120.0), FirstBeatMs: &first}, SoundID: "s9"},
			"not a sound":   {Grid: BeatGrid{BPM: ptr(120.0), FirstBeatMs: &first}, SoundID: "c1"},
			"bad max shift": {Grid: BeatGrid{BPM: ptr(120.0), FirstBeatMs: &first}, Options: &SnapOptions{MaxShiftBeats: ptr(math.NaN())}},
		} {
			out, res := Apply(base, []Op{op}, fixtureCatalog(t))
			if res.OK() || !sameSpec(t, out, base) {
				t.Errorf("%s: %+v", name, res.Issues)
			}
		}
	})
}

func ptr[T any](v T) *T { return &v }

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
