package ops

import (
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func TestAddClip(t *testing.T) {
	base := baseSpec(t)
	t.Run("appends with the next free id and reports it", func(t *testing.T) {
		out, res := apply(t, base, `[{"op":"add_clip","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":2000}}]`)
		if !res.OK() {
			t.Fatalf("issues: %+v", res.Issues)
		}
		if got := clipTimes(t, out); got != "c1:0-3000 c2:3000-7000 c3:7000-10000 c4:10000-12000" {
			t.Fatalf("clips: %s", got)
		}
		if len(res.Created) != 1 || res.Created[0] != (Created{OpIndex: 0, Kind: "clip", ID: "c4"}) {
			t.Fatalf("created: %+v", res.Created)
		}
	})
	t.Run("inserts at an index and ripples the rest", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"add_clip","id":"c9","index":1,"asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":2000}}]`)
		if got := clipTimes(t, out); got != "c1:0-3000 c9:3000-5000 c2:5000-9000 c3:9000-12000" {
			t.Fatalf("clips: %s", got)
		}
	})
	t.Run("inserts at the start and after a clip", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"add_clip","index":0,"asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":1000}}]`)
		if got := clipTimes(t, out); got != "c4:0-1000 c1:1000-4000 c2:4000-8000 c3:8000-11000" {
			t.Fatalf("clips: %s", got)
		}
		out = mustApply(t, base, `[{"op":"add_clip","after_id":"c2","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":1000}}]`)
		if got := clipTimes(t, out); got != "c1:0-3000 c2:3000-7000 c4:7000-8000 c3:8000-11000" {
			t.Fatalf("clips: %s", got)
		}
	})
	t.Run("carries speed, reframe, look and audio", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"add_clip","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":4000},"speed":2,
			"reframe":{"mode":"cover","focus":{"x":0.2,"y":0.8},"zoom":1.5},"look":{"preset":"warm"},"audio":{"use_source":true,"gain_db":-3},
			"transition_in":{"type":"crossfade","duration_ms":300},"why":"because"}]`)
		c := clips(t, out)[3]
		if c.SpeedOrDefault() != 2 || c.Reframe.Mode != "cover" || *c.Reframe.Zoom != 1.5 || c.Look.Preset != "warm" ||
			!c.Audio.UseSourceOrDefault() || *c.Why != "because" || c.TransitionDurationMs() != 300 {
			t.Fatalf("clip: %+v", c)
		}
		// 4000 ms at 2x is 2000 ms; the crossfade starts it 300 ms early.
		if got := clipTimes(t, out); !strings.HasSuffix(got, "c4:9700-11700") {
			t.Fatalf("clips: %s", got)
		}
	})
	t.Run("does not alias the op's own values", func(t *testing.T) {
		speed := 2.0
		op := &AddClip{AssetID: vid60, Source: timeline.Source{InMs: 0, OutMs: 4000}, Speed: &speed}
		out, res := Apply(base, []Op{op}, fixtureCatalog(t))
		if !res.OK() {
			t.Fatal(res.Issues)
		}
		speed = 4
		if clips(t, out)[3].SpeedOrDefault() != 2 {
			t.Fatal("the new clip shares its speed with the op")
		}
	})
	t.Run("fails with the index of the op", func(t *testing.T) {
		cases := []struct {
			name, js string
			code     string
		}{
			{"id in use", `[{"op":"add_clip","id":"t1","asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":2000}}]`, "id_taken"},
			{"index out of range", `[{"op":"add_clip","index":4,"asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":2000}}]`, "bad_position"},
			{"unknown after_id", `[{"op":"add_clip","after_id":"c9","asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":2000}}]`, "unknown_item"},
			{"after a text item", `[{"op":"add_clip","after_id":"t1","asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":2000}}]`, "wrong_item_kind"},
			{"past the end of the asset", `[{"op":"add_clip","asset_id":"` + vid30 + `","source":{"in_ms":0,"out_ms":31000}}]`, "source_out_of_range"},
			{"unknown asset", `[{"op":"add_clip","asset_id":"ast_01jzzzzzzzzzzzzzzzzzzzzzzz","source":{"in_ms":0,"out_ms":2000}}]`, "unknown_asset"},
			{"audio asset", `[{"op":"add_clip","asset_id":"` + musicMine + `","source":{"in_ms":0,"out_ms":2000}}]`, "wrong_asset_kind"},
			{"too short", `[{"op":"add_clip","asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":200}}]`, "shot_too_short"},
			{"transition on the first clip", `[{"op":"add_clip","index":0,"asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":2000},"transition_in":{"type":"whip","duration_ms":200}}]`, "schema"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				out, res := apply(t, base, tc.js)
				if res.OK() || !slices.Contains(errorCodes(res), tc.code) {
					t.Fatalf("want %s, got %+v", tc.code, res.Issues)
				}
				if res.Errors()[0].OpIndex != 0 {
					t.Fatalf("issue not blamed on op 0: %+v", res.Errors()[0])
				}
				if !sameSpec(t, out, base) {
					t.Fatal("the spec changed")
				}
			})
		}
	})
	t.Run("rejects index together with after_id (typed op)", func(t *testing.T) {
		i := 0
		_, res := Apply(base, []Op{&AddClip{Index: &i, AfterID: "c1", AssetID: vid60, Source: timeline.Source{OutMs: 2000}}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
	})
}

func TestRemoveItem(t *testing.T) {
	base := baseSpec(t)
	out := mustApply(t, base, `[{"op":"remove_item","id":"c2"}]`)
	if got := clipTimes(t, out); got != "c1:0-3000 c3:3000-6000" {
		t.Fatalf("clips: %s", got)
	}
	if len(textItems(out)) != 1 || textItems(out)[0].AtMs != 500 {
		t.Fatal("text moved")
	}
	out = mustApply(t, base, `[{"op":"remove_item","id":"c1"}]`)
	if got := clipTimes(t, out); got != "c2:0-4000 c3:4000-7000" {
		t.Fatalf("clips: %s", got)
	}

	t.Run("the new first clip loses its transition", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"set_transition","id":"c2","type":"crossfade","duration_ms":300}]`)
		out := mustApply(t, spec, `[{"op":"remove_item","id":"c1"}]`)
		if first := clips(t, out)[0]; first.ID != "c2" || first.TransitionIn != nil || first.AtMs != 0 {
			t.Fatalf("first clip: %+v", first)
		}
	})
	t.Run("errors", func(t *testing.T) {
		for js, code := range map[string]string{
			`[{"op":"remove_item","id":"c9"}]`:                                "unknown_item",
			`[{"op":"remove_item","id":"t1"}]`:                                "wrong_item_kind",
			`[{"op":"remove_item","id":"text"}]`:                              "wrong_item_kind",
			`[{"op":"remove_item","id":"s1"}]`:                                "wrong_item_kind",
			`[{"op":"remove_item","id":"c1"},{"op":"remove_item","id":"c1"}]`: "unknown_item",
		} {
			out, res := apply(t, base, js)
			if res.OK() || !slices.Contains(errorCodes(res), code) || !sameSpec(t, out, base) {
				t.Fatalf("%s: want %s, got %+v", js, code, res.Issues)
			}
		}
	})
	t.Run("removing a clip a text item sits past the end of is blamed on the op", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"add_text","at_ms":8500,"duration_ms":1000,"text":"End","style":"title_bold","anchor":"center"}]`)
		_, res := apply(t, spec, `[{"op":"add_text","at_ms":0,"duration_ms":500,"text":"x","style":"title_bold","anchor":"center"},{"op":"remove_item","id":"c3"}]`)
		if res.OK() {
			t.Fatal("expected outside_timeline")
		}
		is := res.Errors()[0]
		if is.Code != timeline.CodeOutsideTimeline || is.OpIndex != 1 || is.Op != "remove_item" {
			t.Fatalf("issue: %+v", is)
		}
	})
}

func TestMoveItem(t *testing.T) {
	base := baseSpec(t)
	out := mustApply(t, base, `[{"op":"move_item","id":"c3","index":0}]`)
	if got := clipTimes(t, out); got != "c3:0-3000 c1:3000-6000 c2:6000-10000" {
		t.Fatalf("clips: %s", got)
	}
	out = mustApply(t, base, `[{"op":"move_item","id":"c1","after_id":"c2"}]`)
	if got := clipTimes(t, out); got != "c2:0-4000 c1:4000-7000 c3:7000-10000" {
		t.Fatalf("clips: %s", got)
	}
	out = mustApply(t, base, `[{"op":"move_item","id":"c1","index":2}]`)
	if got := clipTimes(t, out); got != "c2:0-4000 c3:4000-7000 c1:7000-10000" {
		t.Fatalf("clips: %s", got)
	}
	out = mustApply(t, base, `[{"op":"move_item","id":"t1","at_ms":5000},{"op":"move_item","id":"s1","at_ms":2000}]`)
	if textItems(out)[0].AtMs != 5000 || soundItems(out)[0].Common().AtMs != 2000 {
		t.Fatal("text or sound did not move")
	}

	t.Run("a clip moved to the front loses its transition", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"set_transition","id":"c3","type":"dip_to_black","duration_ms":200}]`)
		out := mustApply(t, spec, `[{"op":"move_item","id":"c3","index":0}]`)
		if clips(t, out)[0].TransitionIn != nil {
			t.Fatal("transition kept on the first clip")
		}
	})
	t.Run("errors", func(t *testing.T) {
		for js, code := range map[string]string{
			`[{"op":"move_item","id":"c1","at_ms":100}]`:     "bad_argument",
			`[{"op":"move_item","id":"t1","index":0}]`:       "bad_argument",
			`[{"op":"move_item","id":"c1","index":3}]`:       "bad_position",
			`[{"op":"move_item","id":"c1","after_id":"c1"}]`: "bad_position",
			`[{"op":"move_item","id":"c1","after_id":"c9"}]`: "unknown_item",
			`[{"op":"move_item","id":"c9","index":0}]`:       "unknown_item",
			`[{"op":"move_item","id":"main","index":0}]`:     "wrong_item_kind",
			`[{"op":"move_item","id":"t1","at_ms":9500}]`:    "outside_timeline",
			`[{"op":"move_item","id":"s1","at_ms":10000}]`:   "outside_timeline",
		} {
			out, res := apply(t, base, js)
			if res.OK() || !slices.Contains(errorCodes(res), code) || !sameSpec(t, out, base) {
				t.Fatalf("%s: want %s, got %+v", js, code, res.Issues)
			}
		}
	})
	t.Run("the typed op needs exactly one target", func(t *testing.T) {
		_, res := Apply(base, []Op{&MoveItem{ID: "c1"}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
	})
}

func TestTrimItem(t *testing.T) {
	base := baseSpec(t)
	out := mustApply(t, base, `[{"op":"trim_item","id":"c2","out_ms":4600}]`)
	if got := clipTimes(t, out); got != "c1:0-3000 c2:3000-6600 c3:6600-9600" {
		t.Fatalf("clips: %s", got)
	}
	out = mustApply(t, base, `[{"op":"trim_item","id":"c2","in_ms":1400}]`)
	if got := clipTimes(t, out); got != "c1:0-3000 c2:3000-6600 c3:6600-9600" {
		t.Fatalf("clips: %s", got)
	}
	out = mustApply(t, base, `[{"op":"trim_item","id":"c1","in_ms":500,"out_ms":5000}]`)
	if got := clipTimes(t, out); got != "c1:0-4500 c2:4500-8500 c3:8500-11500" {
		t.Fatalf("clips: %s", got)
	}
	out = mustApply(t, base, `[{"op":"trim_item","id":"t1","duration_ms":1000},{"op":"trim_item","id":"s1","duration_ms":4000}]`)
	if textItems(out)[0].DurationMs != 1000 || *soundItems(out)[0].Common().DurationMs != 4000 {
		t.Fatal("durations not trimmed")
	}
	t.Run("a speed that does not divide the trim rounds as the spec says", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"set_speed","id":"c2","speed":1.5}]`)
		out := mustApply(t, spec, `[{"op":"trim_item","id":"c2","out_ms":3001}]`)
		// 2001 ms / 1.5 = 1334 exactly
		if got := clipTimes(t, out); !strings.Contains(got, "c2:3000-4334") {
			t.Fatalf("clips: %s", got)
		}
	})
	t.Run("a text item left past the end is an error blamed on the trim", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"add_text","at_ms":8000,"duration_ms":1800,"text":"End","style":"title_bold","anchor":"center"}]`)
		out, res := apply(t, spec, `[{"op":"trim_item","id":"c3","out_ms":1000}]`)
		if res.OK() {
			t.Fatal("expected an error")
		}
		is := res.Errors()[0]
		if is.Code != timeline.CodeOutsideTimeline || is.OpIndex != 0 || !strings.Contains(is.Message, "t2") {
			t.Fatalf("issue: %+v", is)
		}
		if !sameSpec(t, out, spec) {
			t.Fatal("the spec changed")
		}
		// Fixing the text in the same batch works.
		mustApply(t, spec, `[{"op":"trim_item","id":"c3","out_ms":1000},{"op":"update_text","id":"t2","at_ms":7000,"duration_ms":300}]`)
	})
	t.Run("errors", func(t *testing.T) {
		for js, code := range map[string]string{
			`[{"op":"trim_item","id":"c1","duration_ms":1000}]`:                                     "bad_argument",
			`[{"op":"trim_item","id":"t1","in_ms":10}]`:                                             "bad_argument",
			`[{"op":"trim_item","id":"s1","out_ms":10}]`:                                            "bad_argument",
			`[{"op":"trim_item","id":"c9","out_ms":10}]`:                                            "unknown_item",
			`[{"op":"trim_item","id":"c1","out_ms":0}]`:                                             "source_out_of_range",
			`[{"op":"trim_item","id":"c3","out_ms":31000}]`:                                         "source_out_of_range",
			`[{"op":"trim_item","id":"c1","out_ms":100}]`:                                           "shot_too_short",
			`[{"op":"trim_item","id":"t1","duration_ms":100}]`:                                      "schema",
			`[{"op":"trim_item","id":"c1","in_ms":2900},{"op":"trim_item","id":"c2","in_ms":4900}]`: "shot_too_short",
		} {
			out, res := apply(t, base, js)
			if res.OK() || !slices.Contains(errorCodes(res), code) || !sameSpec(t, out, base) {
				t.Fatalf("%s: want %s, got %+v", js, code, res.Issues)
			}
		}
	})
}

func TestReplaceSource(t *testing.T) {
	base := baseSpec(t)
	spec := mustApply(t, base, `[{"op":"set_look","id":"c2","preset":"warm"},{"op":"set_speed","id":"c2","speed":2}]`)
	out := mustApply(t, spec, `[{"op":"replace_source","id":"c2","asset_id":"`+vid30+`","source":{"in_ms":0,"out_ms":4000},"moment_id":"mom_01js9afva5j9pt8dqesz1r8s0p"}]`)
	c2 := clips(t, out)[1]
	if c2.AssetID != vid30 || c2.Look.Preset != "warm" || c2.SpeedOrDefault() != 2 || c2.MomentID == "" {
		t.Fatalf("c2: %+v", c2)
	}
	if got := clipTimes(t, out); got != "c1:0-3000 c2:3000-5000 c3:5000-8000" {
		t.Fatalf("clips: %s", got)
	}
	out = mustApply(t, out, `[{"op":"replace_source","id":"c2","asset_id":"`+vid45+`","source":{"in_ms":0,"out_ms":4000}}]`)
	if clips(t, out)[1].MomentID != "" {
		t.Fatal("the old moment survived")
	}
	_, res := apply(t, base, `[{"op":"replace_source","id":"c2","asset_id":"`+musicMine+`","source":{"in_ms":0,"out_ms":4000}}]`)
	if !slices.Contains(errorCodes(res), "wrong_asset_kind") {
		t.Fatalf("%+v", res.Issues)
	}
}

func TestSetSpeed(t *testing.T) {
	base := baseSpec(t)
	out := mustApply(t, base, `[{"op":"set_speed","id":"c1","speed":2}]`)
	if got := clipTimes(t, out); got != "c1:0-1500 c2:1500-5500 c3:5500-8500" {
		t.Fatalf("clips: %s", got)
	}
	out = mustApply(t, base, `[{"op":"set_speed","id":"c1","speed":0.5}]`)
	if got := clipTimes(t, out); got != "c1:0-6000 c2:6000-10000 c3:10000-13000" {
		t.Fatalf("clips: %s", got)
	}
	// 3001 ms at 2x is 1500.5, which rounds up.
	spec := mustApply(t, base, `[{"op":"trim_item","id":"c1","out_ms":3001}]`)
	out = mustApply(t, spec, `[{"op":"set_speed","id":"c1","speed":2}]`)
	if got := clipTimes(t, out); !strings.HasPrefix(got, "c1:0-1501 c2:1501-") {
		t.Fatalf("clips: %s", got)
	}
	// A typed op can carry a speed the op schema would refuse; the validator of the result still catches it.
	for _, speed := range []float64{0.1, 4.5, 0} {
		out, res := Apply(base, []Op{&SetSpeed{ID: "c1", Speed: speed}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != timeline.CodeSchema || res.Errors()[0].OpIndex != 0 || !sameSpec(t, out, base) {
			t.Fatalf("speed %v: %+v", speed, res.Issues)
		}
	}
}

func TestSetTransition(t *testing.T) {
	base := baseSpec(t)
	out := mustApply(t, base, `[{"op":"set_transition","id":"c2","type":"crossfade","duration_ms":500}]`)
	if got := clipTimes(t, out); got != "c1:0-3000 c2:2500-6500 c3:6500-9500" {
		t.Fatalf("clips: %s", got)
	}
	out = mustApply(t, out, `[{"op":"set_transition","id":"c2","type":"cut"}]`)
	if got := clipTimes(t, out); got != "c1:0-3000 c2:3000-7000 c3:7000-10000" {
		t.Fatalf("clips: %s", got)
	}
	if clips(t, out)[1].TransitionIn == nil || clips(t, out)[1].TransitionIn.Type != "cut" {
		t.Fatal("cut not written")
	}
	for js, code := range map[string]string{
		`[{"op":"set_transition","id":"c1","type":"whip","duration_ms":200}]`:       "schema",
		`[{"op":"set_transition","id":"c2","type":"crossfade","duration_ms":1000}]`: "", // fits: both clips are longer than 1000
		`[{"op":"set_transition","id":"t1","type":"crossfade","duration_ms":300}]`:  "wrong_item_kind",
	} {
		_, res := apply(t, base, js)
		if code == "" {
			if !res.OK() {
				t.Fatalf("%s: %+v", js, res.Issues)
			}
			continue
		}
		if res.OK() || !slices.Contains(errorCodes(res), code) {
			t.Fatalf("%s: want %s, got %+v", js, code, res.Issues)
		}
	}
	t.Run("a transition as long as a clip is too long", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"trim_item","id":"c2","out_ms":1500}]`) // c2 plays 500 ms
		_, res := apply(t, spec, `[{"op":"set_transition","id":"c2","type":"crossfade","duration_ms":600}]`)
		if res.OK() || !slices.Contains(errorCodes(res), "transition_too_long") || res.Errors()[0].OpIndex != 0 {
			t.Fatalf("%+v", res.Issues)
		}
	})
	t.Run("typed cut with a duration", func(t *testing.T) {
		_, res := Apply(base, []Op{&SetTransition{ID: "c2", Type: "cut", DurationMs: 200}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
	})
}

func TestClipSettings(t *testing.T) {
	base := baseSpec(t)
	out := mustApply(t, base, `[
		{"op":"set_reframe","id":"c1","mode":"cover","focus":{"x":0.3,"y":0.7},"zoom":2},
		{"op":"set_reframe","id":"c1","mode":"contain"},
		{"op":"set_look","id":"c2","preset":"bw"},
		{"op":"set_clip_audio","id":"c3","use_source":true},
		{"op":"set_clip_audio","id":"c3","gain_db":-6}]`)
	c := clips(t, out)
	if c[0].Reframe.Mode != "contain" || c[0].Reframe.Focus != nil || c[0].Reframe.Zoom != nil {
		t.Fatalf("set_reframe must replace the reframe: %+v", c[0].Reframe)
	}
	if c[1].Look.Preset != "bw" || c[0].Look != nil {
		t.Fatal("look")
	}
	if !c[2].Audio.UseSourceOrDefault() || c[2].Audio.GainDBOrDefault() != -6 {
		t.Fatalf("audio: %+v", c[2].Audio)
	}
	out = mustApply(t, base, `[{"op":"set_look","preset":"punchy"}]`)
	for _, c := range clips(t, out) {
		if c.Look == nil || c.Look.Preset != "punchy" {
			t.Fatalf("look not set on %s", c.ID)
		}
	}
	for js, code := range map[string]string{
		`[{"op":"set_reframe","id":"t1","mode":"cover"}]`: "wrong_item_kind",
		`[{"op":"set_look","id":"c9","preset":"bw"}]`:     "unknown_item",
		`[{"op":"set_clip_audio","id":"c9","gain_db":1}]`: "unknown_item",
	} {
		if _, res := apply(t, base, js); res.OK() || !slices.Contains(errorCodes(res), code) {
			t.Fatalf("%s: %+v", js, res.Issues)
		}
	}
}

func TestText(t *testing.T) {
	base := baseSpec(t)
	out, res := apply(t, base, `[{"op":"add_text","at_ms":3000,"duration_ms":1500,"text":"Line one\nLine two","style":"caption_plain","position":{"x":0.5,"y":0.45},"animation":"pop_in"},
		{"op":"add_text","id":"t9","at_ms":0,"duration_ms":300,"text":"Hi","style":"title_bold","anchor":"upper_third"}]`)
	if !res.OK() {
		t.Fatalf("%+v", res.Issues)
	}
	ids := []string{}
	for _, it := range textItems(out) {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "t1,t2,t9" {
		t.Fatalf("ids: %v", ids)
	}
	if len(res.Created) != 2 || res.Created[0].ID != "t2" || res.Created[1].ID != "t9" {
		t.Fatalf("created: %+v", res.Created)
	}

	t.Run("creates the text track", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"remove_text","id":"t1"}]`)
		if len(textItems(spec)) != 0 {
			t.Fatal("not removed")
		}
		noText := spec
		noText.Tracks = slices.DeleteFunc(slices.Clone(spec.Tracks), func(tr timeline.Track) bool { return tr.TrackType() == timeline.TrackText })
		out := mustApply(t, noText, `[{"op":"add_text","at_ms":0,"duration_ms":1000,"text":"x","style":"title_bold","anchor":"center"}]`)
		if len(textItems(out)) != 1 || out.Tracks[len(out.Tracks)-1].TrackID() != "text" {
			t.Fatalf("tracks: %+v", out.Tracks)
		}
		// An item named like the track keeps the track out of the way.
		out = mustApply(t, noText, `[{"op":"add_text","id":"text","at_ms":0,"duration_ms":1000,"text":"x","style":"title_bold","anchor":"center"}]`)
		if out.Tracks[len(out.Tracks)-1].TrackID() != "text_2" {
			t.Fatalf("track id: %s", out.Tracks[len(out.Tracks)-1].TrackID())
		}
	})
	t.Run("update_text", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"update_text","id":"t1","text":"Bye","position":{"x":0.5,"y":0.5},"animation":"fade","at_ms":1000,"duration_ms":900,"style":"caption_plain"}]`)
		it := textItems(out)[0]
		if it.Text != "Bye" || it.Anchor != "" || it.Position == nil || it.Animation != "fade" || it.AtMs != 1000 || it.DurationMs != 900 || it.Style != "caption_plain" {
			t.Fatalf("item: %+v", it)
		}
		out = mustApply(t, out, `[{"op":"update_text","id":"t1","anchor":"lower_third"}]`)
		if it := textItems(out)[0]; it.Anchor != "lower_third" || it.Position != nil {
			t.Fatalf("item: %+v", it)
		}
	})
	t.Run("remove_text", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"remove_text","id":"t1"}]`)
		if len(textItems(out)) != 0 {
			t.Fatal("not removed")
		}
	})
	t.Run("errors", func(t *testing.T) {
		for js, code := range map[string]string{
			`[{"op":"add_text","at_ms":9500,"duration_ms":1000,"text":"x","style":"title_bold","anchor":"center"}]`:          "outside_timeline",
			`[{"op":"add_text","at_ms":0,"duration_ms":1000,"text":"x","style":"title_bold","position":{"x":0.5,"y":0.02}}]`: "unsafe_text",
			`[{"op":"add_text","id":"c1","at_ms":0,"duration_ms":1000,"text":"x","style":"title_bold","anchor":"center"}]`:   "id_taken",
			`[{"op":"update_text","id":"c1","text":"x"}]`:                                                                    "wrong_item_kind",
			`[{"op":"update_text","id":"t7","text":"x"}]`:                                                                    "unknown_item",
			`[{"op":"remove_text","id":"c1"}]`:                                                                               "wrong_item_kind",
			`[{"op":"remove_text","id":"t7"}]`:                                                                               "unknown_item",
			`[{"op":"update_text","id":"t1","at_ms":9000}]`:                                                                  "outside_timeline",
		} {
			out, res := apply(t, base, js)
			if res.OK() || !slices.Contains(errorCodes(res), code) || !sameSpec(t, out, base) {
				t.Fatalf("%s: want %s, got %+v", js, code, res.Issues)
			}
		}
		// An unknown style is an error since P3-04 added the style registry: the version is refused and the op is named.
		out, res := apply(t, base, `[{"op":"add_text","at_ms":0,"duration_ms":1000,"text":"x","style":"nope","anchor":"center"}]`)
		if res.OK() || len(res.Issues) != 1 || res.Issues[0].Code != timeline.CodeUnknownStyle || res.Issues[0].OpIndex != 0 || !sameSpec(t, out, base) {
			t.Fatalf("%+v", res.Issues)
		}
	})
	t.Run("typed update_text with nothing to change or both anchor and position", func(t *testing.T) {
		_, res := Apply(base, []Op{&UpdateText{ID: "t1"}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
		_, res = Apply(base, []Op{&UpdateText{ID: "t1", Anchor: "center", Position: &timeline.Point{X: .5, Y: .5}}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
	})
}

func TestSound(t *testing.T) {
	base := baseSpec(t)
	out, res := apply(t, base, `[
		{"op":"set_sound","kind":"licensed_track","asset_id":"`+musicLicensed+`","license_id":"`+licence+`","at_ms":1000,"source_offset_ms":2000,"duration_ms":6000,"gain_db":-12,"fade_in_ms":500,"fade_out_ms":500},
		{"op":"set_sound","kind":"creator_audio","asset_id":"`+musicMine+`","id":"voice"}]`)
	if !res.OK() {
		t.Fatalf("%+v", res.Issues)
	}
	items := soundItems(out)
	if len(items) != 3 || items[1].Common().ID != "s2" || items[2].Common().ID != "voice" {
		t.Fatalf("items: %+v", items)
	}
	if items[1].Common().Export != "licensed" || items[2].Common().Export != "licensed" || items[0].Common().Export != "cue_only" {
		t.Fatal("export not derived from the kind")
	}
	if len(res.Created) != 2 || res.Created[0].Kind != "sound" {
		t.Fatalf("created: %+v", res.Created)
	}

	t.Run("replaces the sound with the same id, in place", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"set_sound","id":"s1","kind":"trend_sound","trend_id":"`+trend+`","at_ms":500,"gain_db":-3}]`)
		items := soundItems(out)
		if len(items) != 1 || items[0].Common().AtMs != 500 || items[0].Common().GainDBOrDefault() != -3 || items[0].Common().SourceOffsetMs != 0 {
			t.Fatalf("items: %+v", items)
		}
	})
	t.Run("remove_sound", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"remove_sound","id":"s1"}]`)
		if len(soundItems(out)) != 0 {
			t.Fatal("not removed")
		}
	})
	t.Run("creates the audio track", func(t *testing.T) {
		noAudio := base
		noAudio.Tracks = slices.DeleteFunc(slices.Clone(base.Tracks), func(tr timeline.Track) bool { return tr.TrackType() == timeline.TrackAudio })
		out := mustApply(t, noAudio, `[{"op":"set_sound","kind":"trend_sound","trend_id":"`+trend+`"}]`)
		if soundItems(out)[0].Common().ID != "s1" || out.Tracks[len(out.Tracks)-1].TrackID() != "sound" {
			t.Fatalf("tracks: %+v", out.Tracks)
		}
	})
	t.Run("errors", func(t *testing.T) {
		for js, code := range map[string]string{
			`[{"op":"set_sound","kind":"licensed_track","asset_id":"` + musicMine + `","license_id":"` + licence + `"}]`:                       "unlicensed_audio",
			`[{"op":"set_sound","kind":"creator_audio","asset_id":"ast_01j5ntfevbyp8fy4vwn751jmjq"}]`:                                          "unlicensed_audio",
			`[{"op":"set_sound","kind":"licensed_track","asset_id":"` + vid60 + `","license_id":"` + licence + `"}]`:                           "wrong_asset_kind",
			`[{"op":"set_sound","kind":"creator_audio","asset_id":"` + musicMine + `","at_ms":10000}]`:                                         "outside_timeline",
			`[{"op":"set_sound","kind":"creator_audio","asset_id":"` + musicMine + `","duration_ms":1000,"fade_in_ms":900,"fade_out_ms":900}]`: "fade_too_long",
			`[{"op":"set_sound","kind":"creator_audio","asset_id":"` + musicMine + `","id":"c1"}]`:                                             "id_taken",
			`[{"op":"remove_sound","id":"s9"}]`: "unknown_item",
			`[{"op":"remove_sound","id":"t1"}]`: "wrong_item_kind",
		} {
			out, res := apply(t, base, js)
			if res.OK() || !slices.Contains(errorCodes(res), code) || !sameSpec(t, out, base) {
				t.Fatalf("%s: want %s, got %+v", js, code, res.Issues)
			}
		}
	})
	t.Run("a typed op with an unknown kind", func(t *testing.T) {
		_, res := Apply(base, []Op{&SetSound{Kind: "radio"}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
	})
}

func TestSetCaptions(t *testing.T) {
	base := baseSpec(t)
	out := mustApply(t, base, `[{"op":"set_captions","style":"chunk","max_words":3}]`)
	ct, ok := out.Tracks[len(out.Tracks)-1].(*timeline.CaptionsTrack)
	if !ok || ct.ID != "captions" || ct.Style != "chunk" || !ct.Enabled || *ct.MaxWords != 3 || ct.Uppercase != nil {
		t.Fatalf("captions: %+v", out.Tracks[len(out.Tracks)-1])
	}
	out = mustApply(t, out, `[{"op":"set_captions","enabled":false,"uppercase":false}]`)
	ct = lastCaptions(t, out)
	if ct.Enabled || ct.Style != "chunk" || *ct.Uppercase || *ct.MaxWords != 3 {
		t.Fatalf("captions: %+v", ct)
	}
	out = mustApply(t, base, `[{"op":"set_captions","enabled":true}]`)
	if ct := lastCaptions(t, out); ct.Style != "word_highlight" {
		t.Fatalf("default style: %+v", ct)
	}
	_, res := Apply(base, []Op{&SetCaptions{}}, fixtureCatalog(t))
	if res.OK() || res.Errors()[0].Code != CodeBadArgument {
		t.Fatalf("%+v", res.Issues)
	}
}

func TestBatchSemantics(t *testing.T) {
	base := baseSpec(t)
	cat := fixtureCatalog(t)

	t.Run("later ops see earlier ones", func(t *testing.T) {
		out, res := apply(t, base, `[
			{"op":"add_clip","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":2000}},
			{"op":"set_speed","id":"c4","speed":2},
			{"op":"move_item","id":"c4","index":0},
			{"op":"add_text","at_ms":0,"duration_ms":600,"text":"First","style":"title_bold","anchor":"center"}]`)
		if !res.OK() {
			t.Fatalf("%+v", res.Issues)
		}
		if got := clipTimes(t, out); got != "c4:0-1000 c1:1000-4000 c2:4000-8000 c3:8000-11000" {
			t.Fatalf("clips: %s", got)
		}
	})
	t.Run("is atomic: a failing op leaves the spec as it was and stops the batch", func(t *testing.T) {
		before := mustJSON(t, base)
		out, res := apply(t, base, `[{"op":"remove_item","id":"c1"},{"op":"remove_item","id":"c9"},{"op":"remove_item","id":"c2"}]`)
		if res.OK() || len(res.Issues) != 1 || res.Issues[0].OpIndex != 1 || res.Issues[0].Op != "remove_item" || res.Issues[0].Code != CodeUnknownItem {
			t.Fatalf("%+v", res.Issues)
		}
		if string(mustJSON(t, out)) != string(before) || string(mustJSON(t, base)) != string(before) {
			t.Fatal("the spec changed")
		}
		if len(res.Created) != 0 {
			t.Fatal("created items reported for a failed batch")
		}
	})
	t.Run("a valid input is not modified by a successful apply", func(t *testing.T) {
		before := mustJSON(t, base)
		out := mustApply(t, base, `[{"op":"remove_item","id":"c1"},{"op":"update_text","id":"t1","text":"changed"}]`)
		if string(mustJSON(t, base)) != string(before) {
			t.Fatal("the input spec was modified")
		}
		if sameSpec(t, out, base) {
			t.Fatal("the output equals the input")
		}
	})
	t.Run("reports every problem of the result at once", func(t *testing.T) {
		_, res := apply(t, base, `[{"op":"trim_item","id":"c1","out_ms":100},{"op":"trim_item","id":"c3","out_ms":31000}]`)
		codes := errorCodes(res)
		if !slices.Contains(codes, "shot_too_short") || !slices.Contains(codes, "source_out_of_range") {
			t.Fatalf("%+v", res.Issues)
		}
		for _, is := range res.Errors() {
			switch is.Code {
			case timeline.CodeShotTooShort:
				if is.OpIndex != 0 {
					t.Fatalf("%+v", is)
				}
			case timeline.CodeSourceOutOfRange:
				if is.OpIndex != 1 {
					t.Fatalf("%+v", is)
				}
			}
		}
	})
	t.Run("ids are never handed out twice in a batch", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"remove_item","id":"c3"},{"op":"add_clip","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":3000}}]`)
		if got := clips(t, out)[2].ID; got != "c4" {
			t.Fatalf("id: %s", got)
		}
		// In a later batch the number is free again.
		spec := mustApply(t, base, `[{"op":"remove_item","id":"c3"}]`)
		out = mustApply(t, spec, `[{"op":"add_clip","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":3000}}]`)
		if got := clips(t, out)[2].ID; got != "c3" {
			t.Fatalf("id: %s", got)
		}
		// An explicit id raises the counter.
		out = mustApply(t, base, `[{"op":"add_clip","id":"c10","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":3000}},{"op":"add_clip","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":3000}}]`)
		if got := clips(t, out)[4].ID; got != "c11" {
			t.Fatalf("id: %s", got)
		}
	})
	t.Run("is deterministic", func(t *testing.T) {
		js := `[{"op":"add_clip","asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":3000}},{"op":"add_text","at_ms":0,"duration_ms":600,"text":"x","style":"title_bold","anchor":"center"}]`
		a, _ := Apply(base, batchOf(t, js), cat)
		b, _ := Apply(base, batchOf(t, js), cat)
		if !sameSpec(t, a, b) {
			t.Fatal("two runs differ")
		}
	})
	t.Run("empty and oversized batches, nil ops", func(t *testing.T) {
		out, res := Apply(base, nil, cat)
		if !res.OK() || !sameSpec(t, out, base) {
			t.Fatal("an empty batch must be a no-op")
		}
		big := make([]Op, MaxBatchOps+1)
		for i := range big {
			big[i] = &SetLook{Preset: "bw"}
		}
		if _, res := Apply(base, big, cat); res.OK() || res.Errors()[0].Code != CodeBatchSize {
			t.Fatalf("%+v", res.Issues)
		}
		if _, res := Apply(base, []Op{nil}, cat); res.OK() || res.Errors()[0].Code != CodeInvalidOp {
			t.Fatalf("%+v", res.Issues)
		}
		var nilAdd *AddClip
		if _, res := Apply(base, []Op{nilAdd}, cat); res.OK() {
			t.Fatal("a typed nil op must fail, not panic")
		}
	})
	t.Run("an unknown style in the result blocks the batch", func(t *testing.T) {
		out, res := apply(t, base, `[{"op":"update_text","id":"t1","style":"mystery"}]`)
		if res.OK() || len(res.Issues) != 1 || res.Issues[0].Severity != timeline.SeverityError || res.Issues[0].Code != timeline.CodeUnknownStyle {
			t.Fatalf("%+v", res.Issues)
		}
		if !sameSpec(t, out, base) {
			t.Fatal("a refused batch must leave the spec as it was")
		}
	})
}

func TestIssueEncoding(t *testing.T) {
	_, res := apply(t, baseSpec(t), `[{"op":"remove_item","id":"c9"}]`)
	got := string(mustJSON(t, res))
	for _, want := range []string{`"op_index":0`, `"op":"remove_item"`, `"code":"unknown_item"`, `"severity":"error"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("%s missing from %s", want, got)
		}
	}
}
