package ops

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/diff"
)

var agent = Author{Kind: AuthorAgent, ID: "run_1"}

func TestVersionChain(t *testing.T) {
	cat := fixtureCatalog(t)
	v1, err := InitialVersion(baseSpec(t), agent)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Number != 1 || v1.Parent != 0 || len(v1.Ops) != 0 || len(v1.Diff) != 0 || v1.AuthorKind != AuthorAgent || v1.AuthorID != "run_1" {
		t.Fatalf("v1: %+v", v1)
	}

	batch := batchOf(t, `[{"op":"trim_item","id":"c3","out_ms":2600},{"op":"move_item","id":"t1","at_ms":2000},
		{"op":"add_clip","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":2000}}]`)
	v2, res := NewVersion(*v1, batch, Author{Kind: AuthorUser, ID: "usr_1"}, cat)
	if v2 == nil || !res.OK() {
		t.Fatalf("%+v", res.Issues)
	}
	if v2.Number != 2 || v2.Parent != 1 || v2.AuthorKind != AuthorUser || v2.AuthorID != "usr_1" || len(v2.Ops) != 3 {
		t.Fatalf("v2: %+v", v2)
	}
	wantSummary := []string{
		"added clip c4 (" + vid60 + ")",
		"trimmed c3 by 400 ms at the end",
		"moved t1 to 2.0 s",
	}
	if !slices.Equal(v2.Summary, wantSummary) {
		t.Fatalf("summary:\n got %q\nwant %q", v2.Summary, wantSummary)
	}
	// The diff replays to the child.
	got, err := diff.ApplySpec(v1.Spec, v2.Diff)
	if err != nil || !sameSpec(t, got, v2.Spec) {
		t.Fatalf("the diff does not replay: %v", err)
	}
	// A version encodes to JSON with its ops in the wire format.
	enc := string(mustJSON(t, v2))
	for _, want := range []string{`"number":2`, `"parent":1`, `"author_kind":"user"`, `"op":"trim_item"`, `"diff":[`} {
		if !contains(enc, want) {
			t.Fatalf("%q missing from %s", want, enc)
		}
	}

	// Versions are immutable: the parent did not change, and neither does a child when the next one is made.
	if !sameSpec(t, v1.Spec, baseSpec(t)) {
		t.Fatal("the parent version changed")
	}
	before := mustJSON(t, v2)
	v3, res := NewVersion(*v2, batchOf(t, `[{"op":"remove_item","id":"c1"}]`), agent, cat)
	if v3 == nil || !res.OK() || v3.Number != 3 || v3.Parent != 2 {
		t.Fatalf("v3: %+v %+v", v3, res.Issues)
	}
	if string(mustJSON(t, v2)) != string(before) {
		t.Fatal("the version changed when its child was made")
	}
	// And the whole history replays from the first spec.
	replay := v1.Spec
	for _, v := range []*Version{v2, v3} {
		if replay, err = diff.ApplySpec(replay, v.Diff); err != nil {
			t.Fatal(err)
		}
		if !sameSpec(t, replay, v.Spec) {
			t.Fatalf("replay differs at version %d", v.Number)
		}
	}
}

func TestVersionRefusals(t *testing.T) {
	cat := fixtureCatalog(t)
	v1, _ := InitialVersion(baseSpec(t), agent)

	// A failed batch gives no version, and says why.
	v, res := NewVersion(*v1, batchOf(t, `[{"op":"remove_item","id":"zzz"}]`), agent, cat)
	if v != nil || res.OK() || res.Errors()[0].OpIndex != 0 {
		t.Fatalf("%+v %+v", v, res.Issues)
	}
	// A batch that changes nothing gives no version, and a warning.
	v, res = NewVersion(*v1, batchOf(t, `[{"op":"update_text","id":"t1","text":"Hello"}]`), agent, cat)
	if v != nil || !res.OK() || len(res.Issues) != 1 || res.Issues[0].Code != CodeNoChange || res.Issues[0].Severity != timeline.SeverityWarning {
		t.Fatalf("%+v %+v", v, res.Issues)
	}
	// An unknown author kind is refused, by both constructors.
	if _, err := InitialVersion(baseSpec(t), Author{Kind: "robot"}); err == nil {
		t.Fatal("InitialVersion accepted an unknown author")
	}
	if v, res := NewVersion(*v1, batchOf(t, `[{"op":"set_look","preset":"bw"}]`), Author{Kind: "robot"}, cat); v != nil || res.OK() {
		t.Fatal("NewVersion accepted an unknown author")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The summaries of typical batches, end to end: ops, ripple and diff together.
func TestSummaryGoldens(t *testing.T) {
	cat := fixtureCatalog(t)
	cases := []struct {
		name  string
		batch string
		want  []string
	}{
		{"trim the end of a clip", `[{"op":"trim_item","id":"c2","out_ms":4600}]`,
			[]string{"trimmed c2 by 400 ms at the end", "shifted c3 by -400 ms"}},
		{"trim the start of a clip", `[{"op":"trim_item","id":"c2","in_ms":1400}]`,
			[]string{"trimmed c2 by 400 ms at the start", "shifted c3 by -400 ms"}},
		{"extend a clip", `[{"op":"trim_item","id":"c3","out_ms":3500}]`, []string{"extended c3 by 500 ms at the end"}},
		{"move text", `[{"op":"move_item","id":"t1","at_ms":2000}]`, []string{"moved t1 to 2.0 s"}},
		{"add a clip", `[{"op":"add_clip","asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":2000}}]`,
			[]string{"added clip c4 (" + vid60 + ")"}},
		{"insert a clip", `[{"op":"add_clip","index":1,"asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":2000}}]`,
			[]string{"added clip c4 (" + vid60 + ")", "shifted c2, c3 by +2000 ms"}},
		{"remove a clip", `[{"op":"remove_item","id":"c2"}]`, []string{"removed clip c2", "shifted c3 by -4000 ms"}},
		{"reorder clips", `[{"op":"move_item","id":"c3","index":0}]`,
			[]string{"moved c3 to position 1", "shifted c1, c2 by +3000 ms"}},
		{"speed up a clip", `[{"op":"set_speed","id":"c1","speed":2}]`,
			[]string{"set the speed of c1 to 2x", "shifted c2, c3 by -1500 ms"}},
		{"crossfade", `[{"op":"set_transition","id":"c2","type":"crossfade","duration_ms":500}]`,
			[]string{"set the transition into c2 to crossfade (500 ms)", "shifted c2, c3 by -500 ms"}},
		{"look and reframe", `[{"op":"set_look","preset":"punchy"},{"op":"set_reframe","id":"c1","mode":"contain"}]`,
			[]string{"set the look of c1 to punchy", "set the reframe of c1 to contain", "set the look of c2 to punchy", "set the look of c3 to punchy"}},
		{"text", `[{"op":"add_text","at_ms":3000,"duration_ms":1000,"text":"Back day","style":"title_bold","anchor":"upper_third"},{"op":"remove_text","id":"t1"}]`,
			[]string{"removed text t1", `added text t2 "Back day" at 3.0 s`}},
		{"sound", `[{"op":"set_sound","kind":"creator_audio","asset_id":"` + musicMine + `","gain_db":-6},{"op":"remove_sound","id":"s1"}]`,
			[]string{"removed sound s1", "added sound s2 (creator_audio " + musicMine + ")"}},
		{"captions", `[{"op":"set_captions","style":"chunk"}]`, []string{"added captions (chunk, on)"}},
	}
	base := baseSpec(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, res := apply(t, base, tc.batch)
			if !res.OK() {
				t.Fatalf("%+v", res.Issues)
			}
			patch, err := diff.Specs(base, out)
			if err != nil {
				t.Fatal(err)
			}
			got, err := diff.Summarize(base, patch)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("\n got %q\nwant %q\npatch %s", got, tc.want, mustJSON(t, patch))
			}
		})
	}
	_ = cat
}

func TestDiffIsCompactForTypicalEdits(t *testing.T) {
	base := baseSpec(t)
	// A trim is a source change and the ripple: no whole-array rewrites.
	out := mustApply(t, base, `[{"op":"trim_item","id":"c2","out_ms":4600}]`)
	patch, _ := diff.Specs(base, out)
	b, _ := json.Marshal(patch)
	if string(b) != `[{"op":"replace","path":"/tracks/0/items/1/source/out_ms","value":4600},{"op":"replace","path":"/tracks/0/items/2/at_ms","value":6600}]` {
		t.Fatalf("patch: %s", b)
	}
	// A reorder is one move plus the new times.
	out = mustApply(t, base, `[{"op":"move_item","id":"c3","index":0}]`)
	patch, _ = diff.Specs(base, out)
	moves := 0
	for _, o := range patch {
		if o.Op == diff.OpMove {
			moves++
		}
	}
	if moves != 1 || len(patch) != 4 {
		b, _ := json.Marshal(patch)
		t.Fatalf("patch: %s", b)
	}
}
