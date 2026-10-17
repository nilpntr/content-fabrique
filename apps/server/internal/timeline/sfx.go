package timeline

import (
	"crypto/sha256"
	"encoding/binary"
)

// The catalogue of generated sound effects (P3-10 part B, redesigned in P3-11). Eleven categories, each with three or four named
// variants; the first variant of a category is its default, and the old spec values (`sfx: "whoosh"` with no variant) mean the
// default variant, so nothing written before P3-11 changes meaning. The renderer (internal/render/sfx*.go) synthesises each variant
// from layers; this file is the part of the catalogue the spec, the validator, the ops and the editor need: the names, the natural
// length of every variant, and the rule for the `auto` variant.

// The sound effect categories (the `sfx` field).
const (
	SFXWhoosh     = "whoosh"
	SFXImpact     = "impact"
	SFXRiser      = "riser"
	SFXSwish      = "swish"
	SFXDownlifter = "downlifter"
	SFXSubDrop    = "sub_drop"
	SFXPop        = "pop"
	SFXClick      = "click"
	SFXGlitch     = "glitch"
	SFXShimmer    = "shimmer"
	SFXSting      = "sting"
)

// SFXVariantAuto is the round-robin variant: the renderer picks one of the category's variants from a hash of the item id, so
// that six whooshes on one timeline are not six copies of one sample, and the same item always gets the same one.
const SFXVariantAuto = "auto"

// sfxVariant is one named variant and its natural length.
type sfxVariant struct {
	name     string
	lengthMs int64
}

// sfxCatalogue lists the categories in documentation order; the first variant of each is the default.
var sfxCatalogue = []struct {
	name     string
	variants []sfxVariant
}{
	{SFXWhoosh, []sfxVariant{{"air", 600}, {"fast", 350}, {"heavy", 1000}}},
	{SFXSwish, []sfxVariant{{"thin", 300}, {"blade", 200}, {"cloth", 400}}},
	{SFXImpact, []sfxVariant{{"punch", 800}, {"deep", 1400}, {"crack", 500}, {"metal", 1200}}},
	{SFXRiser, []sfxVariant{{"tension", 1500}, {"sweep", 1000}, {"pitch", 2000}, {"long", 3000}}},
	{SFXDownlifter, []sfxVariant{{"fall", 1200}, {"short", 600}, {"tape", 800}}},
	{SFXSubDrop, []sfxVariant{{"boom", 1000}, {"quick", 500}, {"long", 2000}}},
	{SFXPop, []sfxVariant{{"bubble", 300}, {"tick", 150}, {"double", 350}}},
	{SFXClick, []sfxVariant{{"ui", 80}, {"soft", 100}, {"tonal", 120}, {"mech", 180}}},
	{SFXGlitch, []sfxVariant{{"stutter", 300}, {"crush", 250}, {"chirp", 400}}},
	{SFXShimmer, []sfxVariant{{"sparkle", 1200}, {"wash", 1500}, {"rise", 1000}}},
	{SFXSting, []sfxVariant{{"hit", 2500}, {"bright", 2000}, {"dark", 3000}}},
}

// SFXCategories lists the sound effect categories, in catalogue order.
func SFXCategories() []string {
	out := make([]string, len(sfxCatalogue))
	for i, c := range sfxCatalogue {
		out[i] = c.name
	}
	return out
}

func sfxCategory(name string) ([]sfxVariant, bool) {
	for _, c := range sfxCatalogue {
		if c.name == name {
			return c.variants, true
		}
	}
	return nil, false
}

// SFXVariants lists the variants of a category, default first, and false for a name that is not a category.
func SFXVariants(name string) ([]string, bool) {
	vs, ok := sfxCategory(name)
	if !ok {
		return nil, false
	}
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.name
	}
	return out, true
}

// SFXDefaultVariant is the variant an item with no `variant` gets.
func SFXDefaultVariant(name string) (string, bool) {
	vs, ok := sfxCategory(name)
	if !ok {
		return "", false
	}
	return vs[0].name, true
}

// SFXHash is the 64-bit hash of an item id that drives the `auto` variant and the renderer's jitter (pitch, length, filter and
// noise seed). It is a SHA-256 prefix, so it is the same on every machine and every release.
func SFXHash(id string) uint64 {
	sum := sha256.Sum256([]byte(id))
	return binary.BigEndian.Uint64(sum[:8])
}

// ResolveSFXVariant turns what an item says (a category, a variant that may be empty or "auto", and its id) into the concrete
// variant the renderer builds. It returns false for a category that does not exist and for a variant the category does not have.
func ResolveSFXVariant(name, variant, id string) (string, bool) {
	vs, ok := sfxCategory(name)
	if !ok {
		return "", false
	}
	switch variant {
	case "":
		return vs[0].name, true
	case SFXVariantAuto:
		return vs[SFXHash(id)%uint64(len(vs))].name, true
	}
	for _, v := range vs {
		if v.name == variant {
			return variant, true
		}
	}
	return "", false
}

// SFXVariantLengthMs is the natural length of a concrete variant (as ResolveSFXVariant returns it). The renderer may vary the
// length of some categories by a few percent per item (see docs/design/audio-v1.md); a riser's length is exact, because its peak
// is where it ends.
func SFXVariantLengthMs(name, variant string) (int64, bool) {
	vs, ok := sfxCategory(name)
	if !ok {
		return 0, false
	}
	for _, v := range vs {
		if v.name == variant {
			return v.lengthMs, true
		}
	}
	return 0, false
}

// The sound effects' budget (P3-11). Every effect is part of the render's filter graph, which is one argument of the ffmpeg process
// (the renderer refuses a graph over render.MaxFilterGraphBytes, 120,000 bytes, because Linux limits an argument to 128 KiB). An
// effect is 0.7 to 6 kB of it, so a spec may carry only so many: SFXGraphBytes is an upper bound for a category (its largest
// variant, rounded up; render's TestSFXGraphBytesAreAnUpperBound keeps it honest) and SFXBudgetBytes what a spec's effects may add
// up to, which leaves the rest of the 120,000 bytes for the clips, the text and the mix. That is about 30 effects of a middle size,
// far more than the edit style asks for (at most 4 per 10 s).
const SFXBudgetBytes = 100_000

var sfxGraphBytes = map[string]int{
	SFXWhoosh: 5500, SFXSwish: 4000, SFXImpact: 2800, SFXRiser: 6000, SFXDownlifter: 3900, SFXSubDrop: 1500,
	SFXPop: 1400, SFXClick: 2100, SFXGlitch: 1400, SFXShimmer: 5200, SFXSting: 4200,
}

// SFXGraphBytes is the most a sound effect of the category adds to the filter graph, in bytes (0 for a name that is not one).
func SFXGraphBytes(name string) int { return sfxGraphBytes[name] }

// SFXIsAccent says whether a category is an accent (the loud, designed hits: impact, sting, sub_drop, riser, downlifter, whoosh, glitch)
// and not detail (swish, shimmer, pop, click). Accents duck speech under them (render) and are the ones the audio check expects to cut
// through the bed and to be heard under a trend sound.
func SFXIsAccent(name string) bool {
	switch name {
	case SFXImpact, SFXSting, SFXSubDrop, SFXRiser, SFXDownlifter, SFXWhoosh, SFXGlitch:
		return true
	}
	return false
}
