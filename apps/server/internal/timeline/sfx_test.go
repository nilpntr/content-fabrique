package timeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func sfxAt(id, name string, at int64) *SFX {
	return &SFX{ID: id, Sfx: name, AtMs: at, Export: ExportLicensed}
}

func TestSoundEffectValidation(t *testing.T) {
	spec := func(items ...AudioItem) *Spec { return addTrack(baseSpec(), audioTrack(items...)) } // baseSpec is 5 s long
	cases := []struct {
		name string
		spec *Spec
		want []Code
	}{
		{"every effect", spec(sfxAt("s1", SFXWhoosh, 0), sfxAt("s2", SFXImpact, 1000), sfxAt("s3", SFXRiser, 2000), sfxAt("s4", SFXSwish, 3000)), nil},
		{"the last millisecond", spec(sfxAt("s1", SFXImpact, 4999)), nil},
		{"an effect longer than what is left is cut, not refused", spec(sfxAt("s1", SFXRiser, 4800)), nil},
		{"at the end of the timeline", spec(sfxAt("s1", SFXImpact, 5000)), []Code{CodeOutsideTimeline}},
		{"after it", spec(sfxAt("s1", SFXImpact, 9000)), []Code{CodeOutsideTimeline}},
		{"with a gain", spec(&SFX{ID: "s1", Sfx: SFXSwish, AtMs: 10, Export: ExportLicensed, GainDB: ptr(-6.0)}), nil},
		{"next to a cue_only trend sound", spec(trend("s1", 0, 0, nil), sfxAt("s2", SFXWhoosh, 100)), nil},
		{"a duplicate id", spec(sfxAt("s1", SFXWhoosh, 100), sfxAt("s1", SFXImpact, 200)), []Code{CodeDuplicateID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { wantCodes(t, validateSpec(t, tc.spec), tc.want...) })
	}
}

func TestSoundEffectRoundTripAndStrictDecoding(t *testing.T) {
	const doc = `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},` +
		`"tracks":[{"id":"main","type":"video","items":[{"id":"c1","asset_id":"` + assetVideo60 + `","source":{"in_ms":0,"out_ms":3000},"at_ms":0}]},` +
		`{"id":"sound","type":"audio","items":[{"id":"s1","kind":"sfx","sfx":"riser","at_ms":500,"export":"licensed","gain_db":-2}]}],"markers":[]}`
	spec, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal([]byte(doc), &a)
	_ = json.Unmarshal(again, &b)
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Errorf("round trip:\n in %s\nout %s", x, y)
	}
	for _, bad := range []string{`"asset_id":"` + assetVideo60 + `"`, `"license_id":"` + licenseID + `"`, `"trend_id":"` + trendID + `"`} {
		withBad := strings.Replace(doc, `"gain_db":-2`, `"gain_db":-2,`+bad, 1)
		if _, err := Parse([]byte(withBad)); err == nil {
			t.Errorf("an sfx with %s decoded", bad)
		}
	}
	// sfx on another kind is refused.
	other := strings.Replace(doc, `"kind":"sfx","sfx":"riser"`, `"kind":"trend_sound","trend_id":"`+trendID+`","sfx":"riser"`, 1)
	if _, err := Parse([]byte(other)); err == nil {
		t.Error("a trend_sound with an sfx field decoded")
	}
	// The schema is closed: no source offset or duration on an effect.
	for _, extra := range []string{`"source_offset_ms":0`, `"duration_ms":300`} {
		res := Validate([]byte(strings.Replace(doc, `"gain_db":-2`, `"gain_db":-2,`+extra, 1)), unitCatalog)
		if res.OK() {
			t.Errorf("an sfx with %s validated", extra)
		}
	}
}

// The default variants keep the lengths the effects had before variants existed.
func TestSFXDefaultLengths(t *testing.T) {
	for name, want := range map[string]int64{SFXWhoosh: 600, SFXImpact: 800, SFXRiser: 1500, SFXSwish: 300} {
		def, _ := SFXDefaultVariant(name)
		if got, ok := SFXVariantLengthMs(name, def); !ok || got != want {
			t.Errorf("%s/%s: %d %v", name, def, got, ok)
		}
	}
	if _, ok := SFXDefaultVariant("boom"); ok {
		t.Error("boom is not an effect")
	}
}
