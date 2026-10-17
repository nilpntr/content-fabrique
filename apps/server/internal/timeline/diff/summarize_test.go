package diff

import (
	"slices"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

const summaryBase = `{
  "schema": "contentfabrique.timeline/v1",
  "output": {"format": "9:16", "width": 1080, "height": 1920, "fps": 30, "background": "#000000"},
  "tracks": [
    {"id": "main", "type": "video", "items": [
      {"id": "c1", "asset_id": "ast_01j3er3kwcgrasdgd4aznmxxe0", "source": {"in_ms": 0, "out_ms": 3000}, "at_ms": 0},
      {"id": "c2", "asset_id": "ast_01jex6xx15ayjx8epfhfjj9s11", "source": {"in_ms": 1000, "out_ms": 5000}, "at_ms": 3000},
      {"id": "c3", "asset_id": "ast_01jfbemsc6way7dnfebdf8tf3g", "source": {"in_ms": 0, "out_ms": 3000}, "at_ms": 7000}
    ]},
    {"id": "text", "type": "text", "items": [
      {"id": "t1", "at_ms": 500, "duration_ms": 2000, "text": "Hello", "style": "title_bold", "anchor": "center"}
    ]},
    {"id": "sound", "type": "audio", "items": [
      {"id": "s1", "kind": "trend_sound", "trend_id": "trd_01jcmevy4tr3fffvjc82eh7rjm", "at_ms": 0, "source_offset_ms": 0, "export": "cue_only"}
    ]}
  ],
  "markers": [{"at_ms": 500, "kind": "beat"}, {"at_ms": 1000, "kind": "beat"}]
}`

func summarize(t *testing.T, patch string) []string {
	t.Helper()
	spec, err := timeline.Parse([]byte(summaryBase))
	if err != nil {
		t.Fatal(err)
	}
	lines, err := Summarize(*spec, mustPatch(t, patch))
	if err != nil {
		t.Fatalf("summarize %s: %v", patch, err)
	}
	return lines
}

func TestSummarize(t *testing.T) {
	cases := []struct {
		name, patch string
		want        []string
	}{
		{"empty", `[]`, []string{}},
		{"trimmed at the end", `[{"op":"replace","path":"/tracks/0/items/2/source/out_ms","value":2600}]`,
			[]string{"trimmed c3 by 400 ms at the end"}},
		{"extended at the end", `[{"op":"replace","path":"/tracks/0/items/1/source/out_ms","value":5400}]`,
			[]string{"extended c2 by 400 ms at the end"}},
		{"trimmed at the start", `[{"op":"replace","path":"/tracks/0/items/1/source/in_ms","value":1250}]`,
			[]string{"trimmed c2 by 250 ms at the start"}},
		{"extended at the start", `[{"op":"replace","path":"/tracks/0/items/1/source/in_ms","value":900}]`,
			[]string{"extended c2 by 100 ms at the start"}},
		{"trim with ripple", `[
			{"op":"replace","path":"/tracks/0/items/1/source/out_ms","value":4600},
			{"op":"replace","path":"/tracks/0/items/2/at_ms","value":6600}]`,
			[]string{"trimmed c2 by 400 ms at the end", "shifted c3 by -400 ms"}},
		{"ripple groups the clips shifted by the same amount", `[
			{"op":"replace","path":"/tracks/0/items/1/at_ms","value":2500},
			{"op":"replace","path":"/tracks/0/items/2/at_ms","value":6500}]`,
			[]string{"shifted c2, c3 by -500 ms"}},
		{"moved text", `[{"op":"replace","path":"/tracks/1/items/0/at_ms","value":2000}]`, []string{"moved t1 to 2.0 s"}},
		{"moved text, fractions of a second", `[{"op":"replace","path":"/tracks/1/items/0/at_ms","value":2150}]`, []string{"moved t1 to 2.15 s"}},
		{"moved sound", `[{"op":"replace","path":"/tracks/2/items/0/at_ms","value":2005}]`, []string{"moved s1 to 2.005 s"}},
		{"trimmed text", `[{"op":"replace","path":"/tracks/1/items/0/duration_ms","value":1500}]`, []string{"trimmed t1 by 500 ms at the end"}},
		{"added clip", `[{"op":"add","path":"/tracks/0/items/3","value":{"id":"c7","asset_id":"ast_01j3er3kwcgrasdgd4aznmxxe0","source":{"in_ms":0,"out_ms":2000},"at_ms":10000}}]`,
			[]string{"added clip c7 (asset nmxxe0, 0.0-2.0 s)"}},
		{"removed clip", `[{"op":"remove","path":"/tracks/0/items/1"},{"op":"replace","path":"/tracks/0/items/1/at_ms","value":3000}]`,
			[]string{"removed clip c2", "shifted c3 by -4000 ms"}},
		{"moved clip", `[{"op":"move","from":"/tracks/0/items/2","path":"/tracks/0/items/0"}]`, []string{"moved c3 to position 1"}},
		{"added text", `[{"op":"add","path":"/tracks/1/items/1","value":{"id":"t2","at_ms":3000,"duration_ms":1000,"text":"Back day","style":"title_bold","anchor":"upper_third"}}]`,
			[]string{`added text t2 "Back day" at 3.0 s`}},
		{"removed text", `[{"op":"remove","path":"/tracks/1/items/0"}]`, []string{"removed text t1"}},
		{"added sound", `[{"op":"add","path":"/tracks/2/items/1","value":{"id":"s2","kind":"licensed_track","asset_id":"ast_01j0h9rew727hk2pt0v5b31h44","license_id":"lic_01jg1bfdc146rw2b0vngmx6p6f","at_ms":0,"source_offset_ms":0,"export":"licensed"}}]`,
			[]string{"added sound s2 (licensed_track asset b31h44)"}},
		{"removed sound", `[{"op":"remove","path":"/tracks/2/items/0"}]`, []string{"removed sound s1"}},
		{"new text track", `[{"op":"add","path":"/tracks/3","value":{"id":"text2","type":"text","items":[{"id":"t5","at_ms":0,"duration_ms":500,"text":"Hi","style":"title_bold","anchor":"center"}]}}]`,
			[]string{"added text track text2", `added text t5 "Hi" at 0.0 s`}},
		{"speed", `[{"op":"add","path":"/tracks/0/items/0/speed","value":1.5}]`, []string{"set the speed of c1 to 1.5x"}},
		{"speed reset", `[{"op":"add","path":"/tracks/0/items/0/speed","value":2},{"op":"remove","path":"/tracks/0/items/0/speed"}]`,
			[]string{"set the speed of c1 to 2x", "reset the speed of c1 to 1x"}},
		{"reframe is described once, from the final state", `[
			{"op":"add","path":"/tracks/0/items/0/reframe","value":{"mode":"cover"}},
			{"op":"add","path":"/tracks/0/items/0/reframe/zoom","value":1.2}]`,
			[]string{"set the reframe of c1 to cover, zoom 1.2"}},
		{"reframe with focus", `[{"op":"add","path":"/tracks/0/items/0/reframe","value":{"mode":"cover","focus":{"x":0.4,"y":0.6}}}]`,
			[]string{"set the reframe of c1 to cover, focus 0.4/0.6"}},
		{"look", `[{"op":"add","path":"/tracks/0/items/1/look","value":{"preset":"warm"}}]`, []string{"set the look of c2 to warm"}},
		{"transition", `[{"op":"add","path":"/tracks/0/items/1/transition_in","value":{"type":"crossfade","duration_ms":300}},{"op":"replace","path":"/tracks/0/items/1/at_ms","value":2700}]`,
			[]string{"set the transition into c2 to crossfade (300 ms)", "shifted c2 by -300 ms"}},
		{"clip audio", `[{"op":"add","path":"/tracks/0/items/2/audio","value":{"use_source":true,"gain_db":-3}}]`,
			[]string{"set the source audio of c3 on at -3 dB"}},
		{"replaced source", `[{"op":"replace","path":"/tracks/0/items/1/asset_id","value":"ast_01jfbemsc6way7dnfebdf8tf3g"},{"op":"replace","path":"/tracks/0/items/1/source/out_ms","value":4000}]`,
			[]string{"replaced the asset of c2 with asset f8tf3g", "trimmed c2 by 1000 ms at the end"}},
		{"text fields", `[
			{"op":"replace","path":"/tracks/1/items/0/text","value":"Bye"},
			{"op":"add","path":"/tracks/1/items/0/animation","value":"fade"},
			{"op":"replace","path":"/tracks/1/items/0/style","value":"caption_plain"}]`,
			[]string{`changed the text of t1 to "Bye"`, "set the animation of t1 to fade", "set the style of t1 to caption_plain"}},
		{"anchor to position", `[{"op":"remove","path":"/tracks/1/items/0/anchor"},{"op":"add","path":"/tracks/1/items/0/position","value":{"x":0.5,"y":0.4}}]`,
			[]string{"moved t1 to position 0.5/0.4"}},
		{"position to anchor", `[{"op":"add","path":"/tracks/1/items/0/anchor","value":"lower_third"}]`, []string{"moved t1 to the lower third"}},
		{"sound fields", `[{"op":"add","path":"/tracks/2/items/0/gain_db","value":-3},{"op":"add","path":"/tracks/2/items/0/fade_in_ms","value":500}]`,
			[]string{"set the gain of s1 to -3 dB", "set the fade in of s1 to 500 ms"}},
		{"captions", `[{"op":"add","path":"/tracks/3","value":{"id":"captions","type":"captions","from":"transcript","style":"chunk","enabled":true}}]`,
			[]string{"added captions (chunk, on)"}},
		{"markers are counted", `[
			{"op":"add","path":"/markers/2","value":{"at_ms":1500,"kind":"beat"}},
			{"op":"add","path":"/markers/3","value":{"at_ms":2000,"kind":"beat"}},
			{"op":"remove","path":"/markers/0"},
			{"op":"replace","path":"/markers/0/at_ms","value":1100}]`,
			[]string{"added 2 markers", "removed 1 marker", "changed 1 marker"}},
		{"output", `[{"op":"replace","path":"/output/fps","value":60}]`, []string{"changed output.fps to 60"}},
		{"a field with no sentence of its own", `[{"op":"add","path":"/tracks/0/items/0/why","value":"best take"}]`, []string{"changed the why of c1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := summarize(t, tc.patch)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestSummarizeCaptionsFields(t *testing.T) {
	spec, err := timeline.Parse([]byte(summaryBase))
	if err != nil {
		t.Fatal(err)
	}
	withCaptions := *spec
	withCaptions.Tracks = append(withCaptions.Tracks, &timeline.CaptionsTrack{ID: "captions", From: "transcript", Style: "chunk", Enabled: true})
	lines, err := Summarize(withCaptions, mustPatch(t, `[
		{"op":"replace","path":"/tracks/3/enabled","value":false},
		{"op":"replace","path":"/tracks/3/style","value":"word_highlight"},
		{"op":"add","path":"/tracks/3/uppercase","value":false},
		{"op":"add","path":"/tracks/3/max_words","value":3}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"turned captions off", "set captions style to word_highlight", "set captions uppercase to false", "set captions max words to 3"}
	if !slices.Equal(lines, want) {
		t.Fatalf("got %q", lines)
	}
}

func TestSummarizeErrors(t *testing.T) {
	spec, err := timeline.Parse([]byte(summaryBase))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Summarize(*spec, mustPatch(t, `[{"op":"remove","path":"/tracks/9"}]`)); err == nil {
		t.Fatal("a patch that does not apply was summarized")
	}
}

func TestSecs(t *testing.T) {
	for ms, want := range map[int64]string{0: "0.0 s", 100: "0.1 s", 2000: "2.0 s", 2150: "2.15 s", 2005: "2.005 s", 59990: "59.99 s", 180000: "180.0 s", -1500: "-1.5 s"} {
		if got := secs(ms); got != want {
			t.Errorf("secs(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestSummarizeNamesAssetsByTitle(t *testing.T) {
	spec, err := timeline.Parse([]byte(summaryBase))
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]string{
		"ast_01j3er3kwcgrasdgd4aznmxxe0": "posing",
		"ast_01jfbemsc6way7dnfebdf8tf3g": "  a very\nlong title that goes on and on and on past forty characters ",
		"ast_01j0h9rew727hk2pt0v5b31h44": "",
	}
	lookup := func(id string) string { return titles[id] }
	cases := []struct{ name, patch, want string }{
		{"added clip", `[{"op":"add","path":"/tracks/0/items/3","value":{"id":"c7","asset_id":"ast_01j3er3kwcgrasdgd4aznmxxe0","source":{"in_ms":5500,"out_ms":8200},"at_ms":10000}}]`,
			"added clip c7 (posing, 5.5-8.2 s)"},
		{"long title is one short line", `[{"op":"add","path":"/tracks/0/items/3","value":{"id":"c7","asset_id":"ast_01jfbemsc6way7dnfebdf8tf3g","source":{"in_ms":0,"out_ms":1250},"at_ms":10000}}]`,
			"added clip c7 (a very long title that goes on and on..., 0.0-1.25 s)"},
		{"no title falls back to the id suffix", `[{"op":"add","path":"/tracks/0/items/3","value":{"id":"c7","asset_id":"ast_01j0h9rew727hk2pt0v5b31h44","source":{"in_ms":0,"out_ms":2000},"at_ms":10000}}]`,
			"added clip c7 (asset b31h44, 0.0-2.0 s)"},
		{"replaced asset", `[{"op":"replace","path":"/tracks/0/items/1/asset_id","value":"ast_01j3er3kwcgrasdgd4aznmxxe0"}]`,
			"replaced the asset of c2 with posing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines, err := SummarizeWith(*spec, mustPatch(t, c.patch), lookup)
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != 1 || lines[0] != c.want {
				t.Errorf("got %q, want %q", lines, c.want)
			}
		})
	}
}
