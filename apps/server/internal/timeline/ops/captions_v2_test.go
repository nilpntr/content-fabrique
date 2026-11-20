package ops

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func TestSetCaptionsKeepsTheCreatorsLookAndCorrections(t *testing.T) {
	base := baseSpec(t)
	out := mustApply(t, base, `[{"op":"set_captions","style":"keyword_emphasis","fill":"#F5F0DC","accent":"#FFD60A","keywords":["protein"],
		"corrections":[{"asset_id":"`+vid60+`","word":3,"text":"creatine"}]}]`)
	ct := lastCaptions(t, out)
	if ct.Style != "keyword_emphasis" || ct.Fill != "#F5F0DC" || ct.Accent != "#FFD60A" || len(ct.Keywords) != 1 || len(ct.Corrections) != 1 || ct.Corrections[0].Text != "creatine" {
		t.Fatalf("captions: %+v", ct)
	}
	// Other fields change without touching the corrections; the list is replaced as a whole, and [] clears it.
	out = mustApply(t, out, `[{"op":"set_captions","uppercase":false}]`)
	if ct := lastCaptions(t, out); len(ct.Corrections) != 1 || ct.Fill != "#F5F0DC" {
		t.Fatalf("a change of case lost the creator's fixes: %+v", ct)
	}
	out = mustApply(t, out, `[{"op":"set_captions","corrections":[{"asset_id":"`+vid60+`","word":5,"text":"Whey"}]}]`)
	if ct := lastCaptions(t, out); len(ct.Corrections) != 1 || ct.Corrections[0].Word != 5 {
		t.Fatalf("replaced: %+v", ct)
	}
	out = mustApply(t, out, `[{"op":"set_captions","corrections":[]}]`)
	if ct := lastCaptions(t, out); len(ct.Corrections) != 0 {
		t.Fatalf("cleared: %+v", ct)
	}
}

// Hostile corrections are refused by the op itself, typed ops included (a typed op never went through the schema).
func TestSetCaptionsRefusesHostileCorrections(t *testing.T) {
	base := baseSpec(t)
	cat := fixtureCatalog(t)
	fix := func(asset string, word int, text string) *SetCaptions {
		cs := []timeline.CaptionCorrection{{AssetID: asset, Word: word, Text: text}}
		return &SetCaptions{Corrections: &cs}
	}
	for name, op := range map[string]*SetCaptions{
		"override tags": fix(vid60, 1, `{\an8}x`),
		"markup":        fix(vid60, 1, "<script>"),
		"control":       fix(vid60, 1, "a\x00b"),
		"bidi":          fix(vid60, 1, "a\u202eb"),
		"too long":      fix(vid60, 1, strings.Repeat("a", 41)),
		"empty":         fix(vid60, 1, ""),
		"word":          fix(vid60, 100000, "x"),
		"unknown asset": fix("ast_01jz6b8v83t2vr2qa03wpcnhnd", 1, "x"),
		"fill":          {Fill: ptrTo("red")},
		"accent":        {Accent: ptrTo(`#FFFFFF}{\fs9`)},
		"keyword":       {Keywords: &[]string{"{\\an8}"}},
	} {
		_, res := Apply(base, []Op{op}, cat)
		if res.OK() {
			t.Errorf("%s: accepted", name)
		}
	}
	// The same through JSON: the schema refuses them before the op sees them.
	for _, raw := range []string{
		`[{"op":"set_captions","corrections":[{"asset_id":"` + vid60 + `","word":1,"text":"{\\an8}x"}]}]`,
		`[{"op":"set_captions","corrections":[{"asset_id":"` + vid60 + `","word":1,"text":"` + strings.Repeat("a", 41) + `"}]}]`,
		`[{"op":"set_captions","fill":"cream"}]`,
	} {
		if _, res := ParseBatch([]byte(raw)); res.OK() {
			t.Errorf("parsed %s", raw)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }

// A colour is cleared with the empty string, and a correction may end in punctuation ("Dr.", "yes,", "...").
func TestSetCaptionsClearsColoursAndKeepsPunctuation(t *testing.T) {
	out := mustApply(t, baseSpec(t), `[{"op":"set_captions","fill":"#F5F0DC","accent":"#FFD60A"}]`)
	out = mustApply(t, out, `[{"op":"set_captions","fill":"","corrections":[{"asset_id":"`+vid60+`","word":1,"text":"Dr."},{"asset_id":"`+vid60+`","word":2,"text":"yes,","was":"yes"},{"asset_id":"`+vid60+`","word":3,"text":"..."}]}]`)
	ct := lastCaptions(t, out)
	if ct.Fill != "" || ct.Accent != "#FFD60A" || len(ct.Corrections) != 3 || ct.Corrections[1].Was != "yes" {
		t.Fatalf("captions: %+v", ct)
	}
	out = mustApply(t, out, `[{"op":"set_captions","accent":""}]`)
	if ct := lastCaptions(t, out); ct.Accent != "" {
		t.Fatalf("accent not cleared: %+v", ct)
	}
}
