package timeline

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The catalogue: eleven categories, three or four variants each, and every name a plain lower-case word (the renderer maps
// them to constants with a switch, and they must never carry anything into a filter string).
func TestSFXCatalogue(t *testing.T) {
	cats := SFXCategories()
	if len(cats) != 11 {
		t.Fatalf("%d categories: %v", len(cats), cats)
	}
	plain := regexp.MustCompile(`^[a-z][a-z_]*$`)
	for _, c := range cats {
		vs, ok := SFXVariants(c)
		if !ok || len(vs) < 3 || len(vs) > 4 {
			t.Errorf("%s: variants %v", c, vs)
		}
		if !plain.MatchString(c) {
			t.Errorf("category %q is not a plain word", c)
		}
		def, _ := SFXDefaultVariant(c)
		if def != vs[0] {
			t.Errorf("%s: default %q, first %q", c, def, vs[0])
		}
		seen := map[string]bool{}
		for _, v := range vs {
			if !plain.MatchString(v) || v == SFXVariantAuto || seen[v] {
				t.Errorf("%s: variant %q", c, v)
			}
			seen[v] = true
			if ms, ok := SFXVariantLengthMs(c, v); !ok || ms < 50 || ms > 4000 {
				t.Errorf("%s/%s: length %d", c, v, ms)
			}
			if got, ok := ResolveSFXVariant(c, v, "x"); !ok || got != v {
				t.Errorf("%s/%s resolves to %q %v", c, v, got, ok)
			}
		}
		// An empty variant is the default (the old spelling), and a variant of another category is refused.
		if got, ok := ResolveSFXVariant(c, "", "x"); !ok || got != vs[0] {
			t.Errorf("%s: empty variant resolves to %q", c, got)
		}
		for _, other := range cats {
			if other == c {
				continue
			}
			ovs, _ := SFXVariants(other)
			for _, v := range ovs {
				if _, ok := ResolveSFXVariant(c, v, "x"); ok != slices.Contains(vs, v) {
					t.Errorf("%s accepts %q: %v", c, v, ok)
				}
			}
		}
	}
	for _, bad := range []string{"Air", "air;x", "air,volume=9", "auto2", " air", "auto "} {
		if _, ok := ResolveSFXVariant(SFXWhoosh, bad, "x"); ok {
			t.Errorf("variant %q was accepted", bad)
		}
	}
	if _, ok := ResolveSFXVariant("boom", "", "x"); ok {
		t.Error("boom is not a category")
	}
}

func TestSFXAutoVariantIsRoundRobinByID(t *testing.T) {
	// The same id gives the same variant, and 40 ids reach every variant of a category.
	for _, c := range SFXCategories() {
		vs, _ := SFXVariants(c)
		seen := map[string]int{}
		for i := range 40 {
			id := "s" + strconv.Itoa(i+1)
			a, _ := ResolveSFXVariant(c, SFXVariantAuto, id)
			b, _ := ResolveSFXVariant(c, SFXVariantAuto, id)
			if a != b {
				t.Fatalf("%s %s: %q then %q", c, id, a, b)
			}
			seen[a]++
		}
		if len(seen) != len(vs) {
			t.Errorf("%s: auto reached %v of %v", c, seen, vs)
		}
	}
	// The hash is a fixed function of the id (a SHA-256 prefix), so a stored spec keeps its sounds across releases.
	// It is pinned here, so a change of the function is a failing test and not a silent re-roll of every stored spec.
	if got := SFXHash("s1"); got != 0xe8bc163c82eee187 {
		t.Errorf("SFXHash(s1) = %#x", got)
	}
}

func TestSoundEffectVariantValidation(t *testing.T) {
	doc := func(sfx, variant string) []byte {
		v := ""
		if variant != "" {
			v = `,"variant":"` + variant + `"`
		}
		return []byte(`{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},` +
			`"tracks":[{"id":"main","type":"video","items":[{"id":"c1","asset_id":"` + assetVideo60 + `","source":{"in_ms":0,"out_ms":3000},"at_ms":0}]},` +
			`{"id":"sound","type":"audio","items":[{"id":"s1","kind":"sfx","sfx":"` + sfx + `"` + v + `,"at_ms":500,"export":"licensed"}]}],"markers":[]}`)
	}
	for _, c := range SFXCategories() {
		vs, _ := SFXVariants(c)
		for _, v := range append([]string{"", SFXVariantAuto}, vs...) {
			if res := Validate(doc(c, v), unitCatalog); !res.OK() {
				t.Errorf("%s/%q: %+v", c, v, res.Issues)
			}
		}
	}
	// Hostile and misplaced values fail the schema: nothing from the spec reaches a filter string as text.
	for _, tc := range [][2]string{
		{"whoosh", "punch"}, {"whoosh", "air;amovie=/etc/passwd"}, {"whoosh", "air,volume=99"}, {"whoosh", "AIR"}, {"whoosh", "auto "},
		{"impact", "air"}, {"riser", "punch"}, {"sting", "boom"}, {"click", "../../x"}, {"whoosh;x", "air"}, {"boom", ""},
	} {
		if res := Validate(doc(tc[0], tc[1]), unitCatalog); res.OK() {
			t.Errorf("%s/%q validated", tc[0], tc[1])
		}
	}
	// A variant on another kind of sound is refused by the decoder.
	other := strings.Replace(string(doc("whoosh", "air")), `"kind":"sfx","sfx":"whoosh"`, `"kind":"trend_sound","trend_id":"`+trendID+`"`, 1)
	if _, err := Parse([]byte(other)); err == nil {
		t.Error("a trend_sound with a variant decoded")
	}
	// The variant round-trips.
	spec, err := Parse(doc("riser", "pitch"))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(spec)
	if !strings.Contains(string(out), `"variant":"pitch"`) {
		t.Errorf("variant lost: %s", out)
	}
}
