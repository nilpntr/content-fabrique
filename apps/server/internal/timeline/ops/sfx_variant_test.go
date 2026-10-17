package ops

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// add_sfx takes the new categories and an optional variant (P3-11). The variant is checked against the category here and by the
// schema, and a hostile value never becomes an item.
func TestAddSFXVariants(t *testing.T) {
	base := baseSpec(t)
	t.Run("every category and variant", func(t *testing.T) {
		for _, c := range timeline.SFXCategories() {
			vs, _ := timeline.SFXVariants(c)
			for _, v := range append([]string{"", timeline.SFXVariantAuto}, vs...) {
				op := &AddSFX{Sfx: c, Variant: v, AtMs: 500}
				out, res := Apply(base, []Op{op}, fixtureCatalog(t))
				if !res.OK() {
					t.Fatalf("%s/%q: %+v", c, v, res.Issues)
				}
				items := sounds(t, out)
				got, ok := items[len(items)-1].(*timeline.SFX)
				if !ok || got.Sfx != c || got.Variant != v {
					t.Fatalf("%s/%q: %+v", c, v, items[len(items)-1])
				}
			}
		}
	})
	t.Run("the variant arrives in JSON", func(t *testing.T) {
		out := mustApply(t, base, `[{"op":"add_sfx","id":"fx1","sfx":"impact","variant":"deep","at_ms":100,"gain_db":-2}]`)
		items := sounds(t, out)
		got, ok := items[len(items)-1].(*timeline.SFX)
		if !ok || got.Variant != "deep" || got.ID != "fx1" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a variant of another category, or a hostile one, is refused", func(t *testing.T) {
		for _, op := range []*AddSFX{
			{Sfx: "riser", Variant: "air", AtMs: 10},
			{Sfx: "whoosh", Variant: "air;amovie=/etc/passwd", AtMs: 10},
			{Sfx: "whoosh", Variant: "AIR", AtMs: 10},
			{Sfx: "click", Variant: "auto ", AtMs: 10},
			{Sfx: "whoosh;volume=9", AtMs: 10},
		} {
			_, res := Apply(base, []Op{op}, fixtureCatalog(t))
			if res.OK() || res.Errors()[0].Code != CodeBadArgument {
				t.Errorf("%+v: %+v", op, res.Issues)
			}
		}
	})
}
