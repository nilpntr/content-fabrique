package ops

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/diff"
)

// outOfOrder is baseSpec with its clips in the array as c2, c1, c3: contiguous by time, so valid.
func outOfOrder(t *testing.T) timeline.Spec {
	t.Helper()
	spec := baseSpec(t)
	main, _, _ := timeline.MainTrack(&spec)
	cp := *main
	cp.Items = []timeline.VideoItem{main.Items[1], main.Items[0], main.Items[2]}
	spec.Tracks = []timeline.Track{&cp, spec.Tracks[1], spec.Tracks[2]}
	if res := timeline.Validate(mustJSON(t, spec), fixtureCatalog(t)); !res.OK() {
		t.Fatalf("setup: %+v", res.Issues)
	}
	return spec
}

func clipIDs(t *testing.T, spec timeline.Spec) string {
	t.Helper()
	var ids []string
	for _, c := range clips(t, spec) {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ",")
}

func TestUnknownTextItemListsTheTextIDs(t *testing.T) {
	base := baseSpec(t)
	for js, want := range map[string]string{
		`[{"op":"update_text","id":"t7","text":"x"}]`: "there are t1",
		`[{"op":"remove_text","id":"zz"}]`:            "there are t1",
		`[{"op":"remove_sound","id":"zz"}]`:           "there are s1",
		`[{"op":"remove_item","id":"zz"}]`:            "there are c1, c2, c3",
	} {
		_, res := apply(t, base, js)
		if res.OK() || res.Errors()[0].Code != CodeUnknownItem || !strings.Contains(res.Errors()[0].Message, want) {
			t.Fatalf("%s: %+v", js, res.Issues)
		}
	}
	_, res := apply(t, base, `[{"op":"update_text","id":"c1","text":"x"}]`)
	if res.Errors()[0].Code != CodeWrongItemKind || !strings.Contains(res.Errors()[0].Message, "not a text item") {
		t.Fatalf("%+v", res.Issues)
	}
	// With no text item at all.
	noText := mustApply(t, base, `[{"op":"remove_text","id":"t1"}]`)
	_, res = apply(t, noText, `[{"op":"update_text","id":"t1","text":"x"}]`)
	if !strings.Contains(res.Errors()[0].Message, "there are none") {
		t.Fatalf("%+v", res.Issues)
	}
}

func TestVersionRoundTripsThroughJSON(t *testing.T) {
	cat := fixtureCatalog(t)
	v1, err := InitialVersion(baseSpec(t), agent)
	if err != nil {
		t.Fatal(err)
	}
	batch := batchOf(t, `[{"op":"trim_item","id":"c3","out_ms":2600},
		{"op":"add_text","at_ms":1000,"duration_ms":900,"text":"Hi","style":"title_bold","position":{"x":0.5,"y":0.2}},
		{"op":"set_sound","kind":"licensed_track","asset_id":"`+musicLicensed+`","license_id":"`+licence+`","gain_db":-9},
		{"op":"snap_cuts_to_beats","grid":{"bpm":120,"first_beat_ms":0},"options":{"max_shift_ms":300,"write_markers":true}},
		{"op":"set_captions","style":"chunk"}]`)
	v2, res := NewVersion(*v1, batch, Author{Kind: AuthorUser, ID: "usr_1"}, cat)
	if v2 == nil {
		t.Fatalf("%+v", res.Issues)
	}
	for _, v := range []*Version{v1, v2} {
		enc, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var back Version
		if err := json.Unmarshal(enc, &back); err != nil {
			t.Fatalf("version %d: %v\n%s", v.Number, err, enc)
		}
		again, err := json.Marshal(&back)
		if err != nil || string(again) != string(enc) {
			t.Fatalf("version %d: marshal -> unmarshal -> marshal differs\n%s\n%s", v.Number, enc, again)
		}
		if back.Number != v.Number || back.Parent != v.Parent || back.AuthorKind != v.AuthorKind || back.AuthorID != v.AuthorID ||
			!reflect.DeepEqual(back.Ops, v.Ops) || !reflect.DeepEqual(back.Summary, v.Summary) || !reflect.DeepEqual(back.Diff, v.Diff) {
			t.Fatalf("version %d differs after the round trip:\n%+v\n%+v", v.Number, v, back)
		}
		if !sameSpec(t, back.Spec, v.Spec) {
			t.Fatalf("version %d: spec differs", v.Number)
		}
	}
	// A stored version with an op the schema refuses does not decode.
	var back Version
	bad := `{"number":2,"parent":1,"spec":` + string(mustJSON(t, v1.Spec)) + `,"ops":[{"op":"set_speed","id":"c1","speed":9}],"diff":[],"author_kind":"agent","author_id":"x","summary":[]}`
	if err := json.Unmarshal([]byte(bad), &back); err == nil {
		t.Fatal("a version with an invalid op decoded")
	}
}

func TestVersionOwnsItsOps(t *testing.T) {
	cat := fixtureCatalog(t)
	v1, _ := InitialVersion(baseSpec(t), agent)
	speed := 2.0
	op := &SetSpeed{ID: "c1", Speed: speed}
	v2, res := NewVersion(*v1, []Op{op}, agent, cat)
	if v2 == nil {
		t.Fatalf("%+v", res.Issues)
	}
	op.Speed = 4
	op.ID = "c2"
	got, ok := v2.Ops[0].(*SetSpeed)
	if !ok || got == op || got.Speed != 2 || got.ID != "c1" {
		t.Fatalf("the version's op is shared with the caller's: %+v", v2.Ops[0])
	}
	// A typed op the op schema refuses cannot become part of a version, even if Apply would take it.
	fast := 17.0
	snap := &SnapCutsToBeats{Grid: BeatGrid{BPM: ptr(120.0), FirstBeatMs: ptr(0.0)}, Options: &SnapOptions{MaxShiftBeats: &fast}}
	if _, res := Apply(baseSpec(t), []Op{snap}, cat); !res.OK() {
		t.Fatalf("setup: %+v", res.Issues)
	}
	if v, res := NewVersion(*v1, []Op{snap}, agent, cat); v != nil || res.OK() {
		t.Fatal("a version was made from an op that does not match the schema")
	}
}

func TestOutOfOrderParent(t *testing.T) {
	cat := fixtureCatalog(t)
	spec := outOfOrder(t)

	// An op that does not look at the order of the clips leaves the array alone.
	out := mustApply(t, spec, `[{"op":"update_text","id":"t1","text":"changed"},{"op":"set_look","preset":"bw"},{"op":"add_text","at_ms":0,"duration_ms":500,"text":"x","style":"title_bold","anchor":"center"}]`)
	if got := clipIDs(t, out); got != "c2,c1,c3" {
		t.Fatalf("order: %s", got)
	}
	patch, err := diff.Specs(spec, out)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range patch {
		if o.Op == diff.OpMove || strings.Contains(o.Path, "at_ms") && strings.HasPrefix(o.Path, "/tracks/0/") {
			t.Fatalf("an unrelated op moved clips: %s", mustJSON(t, patch))
		}
	}

	// A batch that changes nothing gives the no_change warning, not a version full of moves.
	v1 := &Version{Number: 1, Spec: spec}
	v, res := NewVersion(*v1, batchOf(t, `[{"op":"update_text","id":"t1","text":"Hello"},{"op":"move_item","id":"s1","at_ms":0}]`), agent, cat)
	if v != nil || !res.OK() || len(res.Issues) != 1 || res.Issues[0].Code != CodeNoChange {
		t.Fatalf("%+v %+v", v, res.Issues)
	}

	// An op that works on positions or timing puts the track in order first (index 0 is the first clip in time).
	out = mustApply(t, spec, `[{"op":"add_clip","index":1,"asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":1000}}]`)
	if got := clipIDs(t, out); got != "c1,c4,c2,c3" {
		t.Fatalf("order: %s", got)
	}
	out = mustApply(t, spec, `[{"op":"trim_item","id":"c1","out_ms":2500}]`)
	if got := clipTimes(t, out); got != "c1:0-2500 c2:2500-6500 c3:6500-9500" {
		t.Fatalf("clips: %s", got)
	}

	// Normalising once, when the spec enters the chain, puts it in order for good.
	first, err := InitialVersion(spec, agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := clipIDs(t, first.Spec); got != "c1,c2,c3" {
		t.Fatalf("InitialVersion: %s", got)
	}
	if got := clipIDs(t, spec); got != "c2,c1,c3" {
		t.Fatalf("the input was changed: %s", got)
	}
	second, res := NewVersion(*first, batchOf(t, `[{"op":"trim_item","id":"c2","out_ms":4600}]`), agent, cat)
	if second == nil {
		t.Fatalf("%+v", res.Issues)
	}
	for _, o := range second.Diff {
		if o.Op == diff.OpMove {
			t.Fatalf("a trim moved clips: %s", mustJSON(t, second.Diff))
		}
	}
}

func TestCreatedIsWhatExists(t *testing.T) {
	base := baseSpec(t)
	asset := `"asset_id":"` + vid60 + `","source":{"in_ms":0,"out_ms":2000}`
	text := `"at_ms":0,"duration_ms":500,"text":"x","style":"title_bold","anchor":"center"`

	// Added and removed again: not reported.
	_, res := apply(t, base, `[{"op":"add_clip",`+asset+`},{"op":"remove_item","id":"c4"}]`)
	if !res.OK() || len(res.Created) != 0 {
		t.Fatalf("%+v", res)
	}
	// Created, removed and created again with the same id: once, by the last op.
	_, res = apply(t, base, `[{"op":"add_clip",`+asset+`},{"op":"remove_item","id":"c4"},{"op":"add_text",`+text+`},{"op":"add_clip","id":"c4",`+asset+`}]`)
	if !res.OK() {
		t.Fatalf("%+v", res.Issues)
	}
	want := []Created{{OpIndex: 2, Kind: "text", ID: "t2"}, {OpIndex: 3, Kind: "clip", ID: "c4"}}
	if !slices.Equal(res.Created, want) {
		t.Fatalf("created: %+v", res.Created)
	}
	// The id of a removed clip given to a text item: reported as text, by the op that made the text.
	_, res = apply(t, base, `[{"op":"add_clip","id":"x1",`+asset+`},{"op":"remove_item","id":"x1"},{"op":"add_text","id":"x1",`+text+`}]`)
	if !res.OK() || !slices.Equal(res.Created, []Created{{OpIndex: 2, Kind: "text", ID: "x1"}}) {
		t.Fatalf("%+v", res)
	}
}
