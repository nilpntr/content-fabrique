package ops

import (
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func TestSetMotion(t *testing.T) {
	base := baseSpec(t)
	t.Run("sets every preset", func(t *testing.T) {
		for js, want := range map[string]timeline.Motion{
			`[{"op":"set_motion","id":"c2","preset":"punch_in","at_ms":500,"strength":0.2}]`: {Preset: "punch_in", AtMs: ptr(int64(500)), Strength: ptr(0.2)},
			`[{"op":"set_motion","id":"c2","preset":"push_in"}]`:                             {Preset: "push_in"},
			`[{"op":"set_motion","id":"c2","preset":"pull_back","strength":0.35}]`:           {Preset: "pull_back", Strength: ptr(0.35)},
			`[{"op":"set_motion","id":"c2","preset":"shake","at_ms":1000}]`:                  {Preset: "shake", AtMs: ptr(int64(1000))},
		} {
			out := mustApply(t, base, js)
			got := clips(t, out)[1].Motion
			if got == nil || got.Preset != want.Preset || !sameOpt(got.AtMs, want.AtMs) || !sameOpt(got.Strength, want.Strength) {
				t.Fatalf("%s: motion %+v, want %+v", js, got, want)
			}
		}
	})
	t.Run("replaces the whole motion, and none removes it", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"set_motion","id":"c2","preset":"punch_in","at_ms":500,"strength":0.2},{"op":"set_motion","id":"c2","preset":"shake"}]`)
		if m := clips(t, out)[1].Motion; m.Preset != "shake" || m.AtMs != nil || m.Strength != nil {
			t.Fatalf("motion %+v: fields left out must go back to their defaults", m)
		}
		out = mustApply(t, out, `[{"op":"set_motion","id":"c2","preset":"none"}]`)
		if clips(t, out)[1].Motion != nil {
			t.Fatal("none must remove the motion from the spec")
		}
	})
	t.Run("handheld, on one clip with a punch-in and without an id on every clip", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"set_motion","preset":"handheld"},{"op":"set_motion","id":"c2","preset":"handheld","at_ms":1000}]`)
		cs := clips(t, out)
		for i, c := range cs {
			if c.Motion == nil || c.Motion.Preset != timeline.MotionHandheld {
				t.Fatalf("clip %d: %+v", i, c.Motion)
			}
		}
		if cs[1].Motion.AtMs == nil || *cs[1].Motion.AtMs != 1000 || cs[0].Motion.AtMs != nil {
			t.Fatalf("at_ms: %+v %+v", cs[0].Motion, cs[1].Motion)
		}
		// at_ms belongs to one clip.
		if _, res := apply(t, base, `[{"op":"set_motion","preset":"handheld","at_ms":100}]`); res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
		// none without an id clears every clip.
		if out = mustApply(t, out, `[{"op":"set_motion","preset":"none"}]`); clips(t, out)[0].Motion != nil || clips(t, out)[2].Motion != nil {
			t.Fatal("none without an id must clear every clip")
		}
	})
	t.Run("does not change the timing", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"set_motion","id":"c1","preset":"push_in"}]`)
		if got := clipTimes(t, out); got != "c1:0-3000 c2:3000-7000 c3:7000-10000" {
			t.Fatalf("clips: %s", got)
		}
	})
	t.Run("a clip that is too short for it", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"trim_item","id":"c3","out_ms":350}]`) // plays 350 ms
		out, res := apply(t, spec, `[{"op":"set_motion","id":"c3","preset":"push_in"}]`)
		if res.OK() || res.Errors()[0].Code != timeline.CodeInvalidMotion || res.Errors()[0].OpIndex != 0 || !sameSpec(t, out, spec) {
			t.Fatalf("%+v", res.Issues)
		}
		if !strings.Contains(res.Errors()[0].Message, "400") {
			t.Fatalf("the message must say the minimum: %s", res.Errors()[0].Message)
		}
	})
	t.Run("at_ms outside the clip, and on a preset without one", func(t *testing.T) {
		for _, js := range []string{
			`[{"op":"set_motion","id":"c1","preset":"punch_in","at_ms":3000}]`,
			`[{"op":"set_motion","id":"c1","preset":"push_in","at_ms":500}]`,
		} {
			_, res := apply(t, base, js)
			if res.OK() || !slices.Contains(errorCodes(res), "invalid_motion") {
				t.Fatalf("%s: %+v", js, res.Issues)
			}
		}
	})
	t.Run("a trim that leaves the at_ms outside the clip is refused, naming the trim", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"set_motion","id":"c1","preset":"shake","at_ms":2500}]`)
		_, res := apply(t, spec, `[{"op":"trim_item","id":"c1","out_ms":2000}]`)
		if res.OK() || !slices.Contains(errorCodes(res), "invalid_motion") {
			t.Fatalf("%+v", res.Issues)
		}
	})
	t.Run("typed none with parameters", func(t *testing.T) {
		_, res := Apply(base, []Op{&SetMotion{ID: "c1", Preset: "none", AtMs: ptr(int64(5))}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
	})
	t.Run("not a clip", func(t *testing.T) {
		_, res := apply(t, base, `[{"op":"set_motion","id":"t1","preset":"push_in"}]`)
		if res.OK() || !slices.Contains(errorCodes(res), "wrong_item_kind") {
			t.Fatalf("%+v", res.Issues)
		}
	})
}

func TestSetSpeedRamp(t *testing.T) {
	base := baseSpec(t)
	const ramp = `[{"op":"set_speed_ramp","id":"c1","points":[{"at_ms":0,"speed":3},{"at_ms":1500,"speed":3},{"at_ms":2200,"speed":0.4}]}]`
	t.Run("sets the ramp and ripples by its integral", func(t *testing.T) {
		out := mustApply(t, base, ramp)
		c := clips(t, out)[0]
		if len(c.SpeedRamp) != 3 || c.Speed != nil {
			t.Fatalf("clip: %+v", c)
		}
		// 1500 ms at 3x is 500; 700 ms from 3x to 0.4x is ln(7.5)/((0.4-3)/700) = 542.5; the last 800 ms at 0.4x is 2000.
		if want := int64(500 + 542 + 2000); timeline.OutputDuration(c) < want || timeline.OutputDuration(c) > want+2 {
			t.Fatalf("the clip plays %d ms, want about %d", timeline.OutputDuration(c), want)
		}
		end := timeline.EndMs(c)
		if got := clips(t, out)[1].AtMs; got != end {
			t.Fatalf("c2 starts at %d, the ramped clip ends at %d: the ripple must follow the integral", got, end)
		}
	})
	t.Run("a ramp replaces the speed and a speed replaces the ramp", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"set_speed","id":"c1","speed":2}]`)
		out = mustApply(t, out, ramp)
		if c := clips(t, out)[0]; c.Speed != nil || len(c.SpeedRamp) != 3 {
			t.Fatalf("clip: %+v", c)
		}
		out = mustApply(t, out, `[{"op":"set_speed","id":"c1","speed":2}]`)
		if c := clips(t, out)[0]; c.Speed == nil || *c.Speed != 2 || c.SpeedRamp != nil {
			t.Fatalf("clip: %+v", c)
		}
		if got := clipTimes(t, out); got != "c1:0-1500 c2:1500-5500 c3:5500-8500" {
			t.Fatalf("clips: %s", got)
		}
	})
	t.Run("a point past the source range, or out of order", func(t *testing.T) {
		for _, js := range []string{
			`[{"op":"set_speed_ramp","id":"c1","points":[{"at_ms":0,"speed":1},{"at_ms":3001,"speed":2}]}]`,
			`[{"op":"set_speed_ramp","id":"c1","points":[{"at_ms":0,"speed":1},{"at_ms":900,"speed":2},{"at_ms":800,"speed":3}]}]`,
		} {
			out, res := apply(t, base, js)
			if res.OK() || res.Errors()[0].Code != timeline.CodeInvalidSpeedRamp || res.Errors()[0].OpIndex != 0 || !sameSpec(t, out, base) {
				t.Fatalf("%s: %+v", js, res.Issues)
			}
		}
	})
	t.Run("a trim that shortens the source below a point is refused", func(t *testing.T) {
		spec := mustApply(t, base, ramp)
		_, res := apply(t, spec, `[{"op":"trim_item","id":"c1","out_ms":2000}]`)
		if res.OK() || !slices.Contains(errorCodes(res), "invalid_speed_ramp") {
			t.Fatalf("%+v", res.Issues)
		}
		// Trimming the end by less than the last point is fine.
		mustApply(t, spec, `[{"op":"trim_item","id":"c1","out_ms":2300}]`)
	})
	t.Run("a new in point moves the ramp with the footage", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"trim_item","id":"c2","in_ms":1000,"out_ms":5000},
			{"op":"set_speed_ramp","id":"c2","points":[{"at_ms":0,"speed":1},{"at_ms":1000,"speed":3},{"at_ms":2000,"speed":0.5}]}]`)
		pts := func(js string) []timeline.SpeedPoint {
			return clips(t, mustApply(t, spec, js))[1].SpeedRamp
		}
		// An earlier in point: the same footage is 400 ms later in the clip.
		if got := pts(`[{"op":"trim_item","id":"c2","in_ms":600}]`); len(got) != 3 || got[0].AtMs != 400 || got[2].AtMs != 2400 || got[1].Speed != 3 {
			t.Errorf("earlier: %+v", got)
		}
		// A later one by 500: the first point is gone and the ramp starts at the speed it had there, half way from 1 to 3.
		got := pts(`[{"op":"trim_item","id":"c2","in_ms":1500}]`)
		if len(got) != 3 || got[0] != (timeline.SpeedPoint{AtMs: 0, Speed: 2}) || got[1] != (timeline.SpeedPoint{AtMs: 500, Speed: 3}) || got[2].AtMs != 1500 {
			t.Errorf("later: %+v", got)
		}
		// Later by exactly a point: no extra point at 0.
		if got := pts(`[{"op":"trim_item","id":"c2","in_ms":2000}]`); len(got) != 2 || got[0] != (timeline.SpeedPoint{AtMs: 0, Speed: 3}) {
			t.Errorf("on a point: %+v", got)
		}
		// Past the whole ramp: it becomes the plain speed it ended with.
		out := mustApply(t, spec, `[{"op":"trim_item","id":"c2","in_ms":3200,"out_ms":5000}]`)
		if c := clips(t, out)[1]; c.SpeedRamp != nil || c.Speed == nil || *c.Speed != 0.5 {
			t.Errorf("past the ramp: %+v", c)
		}
	})
	t.Run("source audio of a ramped clip is a warning, not an error", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"set_clip_audio","id":"c1","use_source":true}]`)
		out, res := apply(t, spec, ramp)
		if !res.OK() {
			t.Fatalf("%+v", res.Issues)
		}
		var warned bool
		for _, is := range res.Issues {
			if is.Code == timeline.CodeRampAudioMuted && is.Severity == timeline.SeverityWarning && is.OpIndex == 0 {
				warned = true
			}
		}
		if !warned || len(clips(t, out)[0].SpeedRamp) != 3 {
			t.Fatalf("issues: %+v", res.Issues)
		}
	})
	t.Run("add_clip takes a ramp and a motion", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"add_clip","id":"c8","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":2000},
			"speed_ramp":[{"at_ms":0,"speed":1},{"at_ms":1000,"speed":2}],"motion":{"preset":"pull_back","strength":0.3}}]`)
		c := clips(t, out)[3]
		if len(c.SpeedRamp) != 2 || c.Motion == nil || c.Motion.Preset != "pull_back" || c.Speed != nil {
			t.Fatalf("clip: %+v", c)
		}
		// The op's own slices are not shared with the spec.
		pts := []timeline.SpeedPoint{{AtMs: 0, Speed: 1}, {AtMs: 1000, Speed: 2}}
		out2, res := Apply(base, []Op{&AddClip{AssetID: vid60, Source: timeline.Source{OutMs: 2000}, SpeedRamp: pts}}, fixtureCatalog(t))
		if !res.OK() {
			t.Fatal(res.Issues)
		}
		pts[1].Speed = 4
		if clips(t, out2)[3].SpeedRamp[1].Speed != 2 {
			t.Fatal("the clip shares the op's ramp")
		}
	})
	t.Run("a ramped clip with a snap: its cuts stay and are reported", func(t *testing.T) {
		spec := snapSpec(t)
		spec = mustApply(t, spec, `[{"op":"set_speed_ramp","id":"c2","points":[{"at_ms":0,"speed":1},{"at_ms":1000,"speed":1.5}]}]`)
		before := cuts(t, spec)
		out, res := apply(t, spec, `[{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0}}]`)
		if !res.OK() {
			t.Fatalf("%+v", res.Issues)
		}
		if got := cuts(t, out); !slices.Equal(got, before) {
			// Both cuts touch c2, so neither moves.
			t.Fatalf("cuts moved from %v to %v", before, got)
		}
		var warned int
		for _, is := range res.Issues {
			if is.Code == CodeCutNotSnapped && strings.Contains(is.Message, "speed ramp") {
				warned++
			}
		}
		if warned == 0 {
			t.Fatalf("no cut_not_snapped warning that names the ramp: %+v", res.Issues)
		}
	})
}

func TestNewTransitionTypes(t *testing.T) {
	base := baseSpec(t)
	for _, typ := range []string{"whip", "whip_left", "whip_right", "zoom_through", "flash"} {
		out := mustApply(t, base, `[{"op":"set_transition","id":"c2","type":"`+typ+`","duration_ms":300}]`)
		if got := clipTimes(t, out); got != "c1:0-3000 c2:2700-6700 c3:6700-9700" {
			t.Fatalf("%s: clips: %s", typ, got)
		}
		if tr := clips(t, out)[1].TransitionIn; tr == nil || tr.Type != typ || tr.DurationMs != 300 {
			t.Fatalf("%s: transition %+v", typ, tr)
		}
	}
	t.Run("a transition longer than the clip is too long, whatever its type", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"trim_item","id":"c2","out_ms":1500}]`) // plays 500 ms
		for _, typ := range []string{"whip_right", "zoom_through", "flash"} {
			_, res := apply(t, spec, `[{"op":"set_transition","id":"c2","type":"`+typ+`","duration_ms":600}]`)
			if res.OK() || !slices.Contains(errorCodes(res), "transition_too_long") {
				t.Fatalf("%s: %+v", typ, res.Issues)
			}
		}
	})
	t.Run("the first clip loses it", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"set_transition","id":"c2","type":"zoom_through","duration_ms":300}]`)
		out := mustApply(t, spec, `[{"op":"remove_item","id":"c1"}]`)
		if tr := clips(t, out)[0].TransitionIn; tr != nil && tr.Type != "cut" {
			t.Fatalf("the new first clip kept %+v", tr)
		}
	})
	t.Run("a type that does not exist", func(t *testing.T) {
		// The batch parser refuses it, naming the types that exist.
		_, res := ParseBatch([]byte(`[{"op":"set_transition","id":"c2","type":"whip_up","duration_ms":300}]`))
		if res.OK() || !strings.Contains(res.Issues[0].Message, "zoom_through") {
			t.Fatalf("%+v", res.Issues)
		}
		// And a typed op that skipped the parser is refused by the validator of the result.
		out, res := Apply(base, []Op{&SetTransition{ID: "c2", Type: "whip_up", DurationMs: 300}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != timeline.CodeSchema || !sameSpec(t, out, base) {
			t.Fatalf("%+v", res.Issues)
		}
	})
}

func sameOpt[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
