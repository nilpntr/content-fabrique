package ops

import (
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func sounds(t *testing.T, spec timeline.Spec) []timeline.AudioItem {
	t.Helper()
	for _, tr := range spec.Tracks {
		if at, ok := tr.(*timeline.AudioTrack); ok {
			return at.Items
		}
	}
	return nil
}

func TestAddSFX(t *testing.T) {
	base := baseSpec(t)
	t.Run("adds an effect with the next free id and reports it", func(t *testing.T) {
		out, res := apply(t, base, `[{"op":"add_sfx","sfx":"whoosh","at_ms":2800,"gain_db":-3}]`)
		if !res.OK() {
			t.Fatalf("%+v", res.Issues)
		}
		items := sounds(t, out)
		sfx, ok := items[len(items)-1].(*timeline.SFX)
		if !ok || sfx.ID != "s2" || sfx.Sfx != "whoosh" || sfx.AtMs != 2800 || sfx.Export != timeline.ExportLicensed || sfx.GainDB == nil || *sfx.GainDB != -3 {
			t.Fatalf("effect: %+v", items[len(items)-1])
		}
		if len(res.Created) != 1 || res.Created[0] != (Created{OpIndex: 0, Kind: "sound", ID: "s2"}) {
			t.Fatalf("created: %+v", res.Created)
		}
	})
	t.Run("creates the audio track when there is none", func(t *testing.T) {
		noSound := mustApply(t, base, `[{"op":"remove_sound","id":"s1"}]`)
		out := mustApply(t, noSound, `[{"op":"add_sfx","id":"fx1","sfx":"impact","at_ms":100}]`)
		if len(sounds(t, out)) != 1 {
			t.Fatalf("sounds: %+v", sounds(t, out))
		}
	})
	t.Run("an effect past the end of the timeline is refused, naming the op", func(t *testing.T) {
		out, res := apply(t, base, `[{"op":"add_sfx","sfx":"impact","at_ms":10000}]`)
		if res.OK() || res.Errors()[0].Code != timeline.CodeOutsideTimeline || res.Errors()[0].OpIndex != 0 || !sameSpec(t, out, base) {
			t.Fatalf("%+v", res.Issues)
		}
	})
	t.Run("an effect whose natural length runs past the end is fine: it is cut", func(t *testing.T) {
		mustApply(t, base, `[{"op":"add_sfx","sfx":"riser","at_ms":9900}]`)
	})
	t.Run("id taken, and a typed unknown effect", func(t *testing.T) {
		_, res := apply(t, base, `[{"op":"add_sfx","id":"c1","sfx":"swish","at_ms":10}]`)
		if res.OK() || res.Errors()[0].Code != CodeIDTaken {
			t.Fatalf("%+v", res.Issues)
		}
		_, res = Apply(base, []Op{&AddSFX{Sfx: "boom", AtMs: 10}}, fixtureCatalog(t))
		if res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
	})
	t.Run("move_item and remove_sound work on it, trim_item says it has a fixed length", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"add_sfx","id":"fx1","sfx":"swish","at_ms":1000}]`)
		moved := mustApply(t, spec, `[{"op":"move_item","id":"fx1","at_ms":2500}]`)
		var at int64
		for _, it := range sounds(t, moved) {
			if sfx, ok := it.(*timeline.SFX); ok {
				at = sfx.AtMs
			}
		}
		if at != 2500 {
			t.Fatalf("moved to %d", at)
		}
		_, res := apply(t, spec, `[{"op":"trim_item","id":"fx1","duration_ms":200}]`)
		if res.OK() || res.Errors()[0].Code != CodeBadArgument || !strings.Contains(res.Errors()[0].Message, "sound effect") {
			t.Fatalf("%+v", res.Issues)
		}
		gone := mustApply(t, spec, `[{"op":"remove_sound","id":"fx1"}]`)
		if len(sounds(t, gone)) != 1 {
			t.Fatalf("sounds after remove: %+v", sounds(t, gone))
		}
	})
	t.Run("set_sound with the id of an effect replaces it", func(t *testing.T) {
		spec := mustApply(t, base, `[{"op":"add_sfx","id":"fx1","sfx":"swish","at_ms":1000}]`)
		out := mustApply(t, spec, `[{"op":"set_sound","id":"fx1","kind":"trend_sound","trend_id":"`+trend+`","at_ms":0,"source_offset_ms":0}]`)
		if _, still := sounds(t, out)[1].(*timeline.SFX); still {
			t.Fatal("the effect was not replaced")
		}
	})
}

func TestSetLookMatchAndExposure(t *testing.T) {
	base := baseSpec(t)
	t.Run("new presets and match", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"set_look","id":"c1","preset":"gym_moody","match":true},{"op":"set_look","preset":"auto"}]`)
		for i, c := range clips(t, out) {
			if c.Look.Preset != "auto" {
				t.Fatalf("clip %d: %+v", i, c.Look)
			}
		}
		// c1 keeps its match when a later op leaves it out; the others never had one.
		if c := clips(t, out); c[0].Look.Match == nil || !*c[0].Look.Match || c[1].Look.Match != nil {
			t.Fatalf("match: %+v %+v", c[0].Look, c[1].Look)
		}
		out = mustApply(t, out, `[{"op":"set_look","id":"c1","preset":"warm","match":false}]`)
		if m := clips(t, out)[0].Look.Match; m == nil || *m {
			t.Fatalf("match: %v", m)
		}
	})
	t.Run("exposure_ev is the hint of one clip", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"set_look","id":"c2","preset":"auto","match":true,"exposure_ev":-0.8}]`)
		c := clips(t, out)
		if c[1].ExposureEv == nil || *c[1].ExposureEv != -0.8 || c[0].ExposureEv != nil {
			t.Fatalf("hints: %v %v", c[0].ExposureEv, c[1].ExposureEv)
		}
		// Left out, the hint stays.
		out = mustApply(t, out, `[{"op":"set_look","id":"c2","preset":"none"}]`)
		if clips(t, out)[1].ExposureEv == nil {
			t.Fatal("a later set_look dropped the hint")
		}
		_, res := apply(t, base, `[{"op":"set_look","preset":"auto","exposure_ev":1}]`)
		if res.OK() || res.Errors()[0].Code != CodeBadArgument {
			t.Fatalf("%+v", res.Issues)
		}
	})
	t.Run("add_clip takes a look with match and a hint, copied", func(t *testing.T) {
		on := true
		ev := 0.7
		op := &AddClip{AssetID: vid60, Source: timeline.Source{OutMs: 2000}, Look: &timeline.Look{Preset: "auto", Match: &on}, ExposureEv: &ev}
		out, res := Apply(base, []Op{op}, fixtureCatalog(t))
		if !res.OK() {
			t.Fatal(res.Issues)
		}
		on, ev = false, 3
		c := clips(t, out)[3]
		if !c.Look.MatchOrDefault() || *c.ExposureEv != 0.7 {
			t.Fatalf("the clip shares the op's values: %+v %v", c.Look, *c.ExposureEv)
		}
	})
	t.Run("a hint outside -3 to 3 is refused by the validator of the result", func(t *testing.T) {
		ev := 1.5
		_, res := Apply(base, []Op{&SetLook{ID: "c1", Preset: "auto", ExposureEv: &ev}}, fixtureCatalog(t))
		if res.OK() || !slices.Contains(errorCodes(res), "schema") {
			t.Fatalf("%+v", res.Issues)
		}
	})
}
