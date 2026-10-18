package styleprofile

import (
	"math"
	"strings"
	"testing"
)

func TestMedianColourAndDeltaE(t *testing.T) {
	// The colours a model reads from pixels differ a little; the channel-wise median is the one they share.
	if got := medianColour([]string{"#FFF0B0", "#F2E9A0", "#FFFAC8"}); got != "#FFF0B0" {
		t.Errorf("median %s", got)
	}
	if got := medianColour([]string{"#F2E9A0", "red", "", "#F4EBA2"}); got != "#F3EAA1" {
		t.Errorf("median with bad values %s", got)
	}
	if medianColour(nil) != "" || medianColour([]string{"nope"}) != "" {
		t.Error("no valid colour")
	}
	// CIE76: identical is 0, black to white is 100, the creator's pale yellow is far from the style's cream and near a one-step read.
	if d, ok := DeltaE("#F2E9A0", "#F2E9A0"); !ok || d != 0 {
		t.Errorf("same: %v %v", d, ok)
	}
	if d, _ := DeltaE("#000000", "#FFFFFF"); math.Abs(d-100) > 0.5 {
		t.Errorf("black to white %.1f", d)
	}
	cream, _ := DeltaE("#F2E9A0", "#F5F0DC")
	near, _ := DeltaE("#F2E9A0", "#F4EBA4")
	if cream < DeltaEMax || near > 4 {
		t.Errorf("pale yellow against cream %.1f, against a near read %.1f", cream, near)
	}
	if _, ok := DeltaE("#F2E9A0", "yellow"); ok {
		t.Error("a bad colour")
	}
}

func TestTheTextProfileOfTheTitles(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	tx := p.Text
	if tx.Colour != "#F2E9A0" || tx.HeightPct != 4 || tx.Shadow != "none" {
		t.Errorf("colour %s, height %.1f, shadow %s", tx.Colour, tx.HeightPct, tx.Shadow)
	}
	if tx.OneLineShare < 0.3 || tx.OneLineShare > 0.34 {
		t.Errorf("one line share %.2f", tx.OneLineShare)
	}
	d := p.Describe()
	for _, want := range []string{"letters about 4.0 % of the frame height", "none shadow"} {
		if !strings.Contains(d, want) {
			t.Errorf("describe lacks %q:\n%s", want, d)
		}
	}
	block := p.CompactBlock()
	if !strings.Contains(block, "title recipe: add_text with") || !strings.Contains(block, `"fill":"#F2E9A0"`) || !strings.Contains(block, `"shadow":"none"`) {
		t.Errorf("block:\n%s", block)
	}
}

func TestTitleRecipe(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	r := p.TitleRecipe()
	if r == nil || r.Style != "title_series" || r.Anchor != "upper_third" || r.Fill != "#F2E9A0" || r.Shadow != "none" || r.Align != "left" && r.Align != "center" {
		t.Fatalf("recipe %+v", r)
	}
	// 4.0 % against the style's 3.95 % is the style's own size: no scale.
	if r.Scale != 0 {
		t.Errorf("scale %v for a size the style already has", r.Scale)
	}
	// A creator with bigger letters gets a scale; one far outside the range is clamped.
	p.Text.HeightPct = 5.0
	if r := p.TitleRecipe(); r.Scale < 1.2 || r.Scale > 1.3 {
		t.Errorf("5 %%: scale %v", r.Scale)
	}
	p.Text.HeightPct = 9
	if r := p.TitleRecipe(); r.Scale != 1.4 {
		t.Errorf("9 %%: scale %v", r.Scale)
	}
	p.Text.HeightPct = 1.2
	if r := p.TitleRecipe(); r.Scale != 0.6 {
		t.Errorf("1.2 %%: scale %v", r.Scale)
	}
	// Lowercase and mixed case pick their styles; position and alignment follow the read; junk is left out.
	p.Text.Case, p.Text.Position, p.Text.Alignment, p.Text.Colour, p.Text.Shadow = "lower", "center", "left", "pale", "glow"
	r = p.TitleRecipe()
	if r.Style != "title_lowercase" || r.Anchor != "center" || r.Align != "left" || r.Fill != "" || r.Shadow != "" {
		t.Errorf("lowercase: %+v", r)
	}
	p.Text.Case = "mixed"
	if r := p.TitleRecipe(); r.Style != "title_bold" {
		t.Errorf("mixed: %+v", r)
	}
	// The arguments hold only fixed words and numbers.
	args := (&TitleRecipe{Style: "title_series", Anchor: "upper_third", Fill: "#F2E9A0", Scale: 0.85, Shadow: "none", Align: "left"}).Args()
	if args != `"style":"title_series","anchor":"upper_third","fill":"#F2E9A0","scale":0.85,"shadow":"none","align":"left"` {
		t.Errorf("args %s", args)
	}
	// No title, no recipe.
	p.Text.TitleShare = 0
	if p.TitleRecipe() != nil {
		t.Error("a recipe without a title")
	}
	var none Profile
	if none.TitleRecipe() != nil {
		t.Error("a recipe without text")
	}
}

func TestCompareTheTitleLook(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	p.Text.OneLineShare = 1
	good := buildingLats()
	good.TitleFill, good.TitleLines, good.TitleHeightPct, good.TitleShadow, good.TitleChars = "#F2E9A0", 1, 4.0, "none", 17
	d := p.Compare(good)
	for _, id := range []string{"title_colour", "title_lines", "title_size", "title_shadow"} {
		found := false
		for _, x := range d.Dimensions {
			if x.ID == id {
				found = true
				if !x.OK {
					t.Errorf("%s out of range: %+v", id, x)
				}
			}
		}
		if !found {
			t.Errorf("no %s dimension", id)
		}
	}
	// What the style has by default: cream, two lines, 5.1 % letters, a strong shadow.
	bad := good
	bad.TitleFill, bad.TitleLines, bad.TitleHeightPct, bad.TitleShadow = "#F5F0DC", 2, 5.6, "strong"
	failed := map[string]string{}
	for _, x := range p.Compare(bad).Failed() {
		failed[x.ID] = x.Fix
	}
	for id, want := range map[string]string{"title_colour": "#F2E9A0", "title_lines": "one line", "title_size": "scale", "title_shadow": "none"} {
		if !strings.Contains(failed[id], want) {
			t.Errorf("%s: fix %q lacks %q", id, failed[id], want)
		}
	}
	// A title in the creator's range except its size, which is half the creator's.
	small := good
	small.TitleHeightPct = 2.0
	if f := p.Compare(small).Failed(); len(f) != 1 || f[0].ID != "title_size" {
		t.Errorf("small: %+v", f)
	}
	// Nothing measured, nothing judged; no title in the edit, no look dimensions.
	plain := buildingLats()
	for _, x := range p.Compare(plain).Dimensions {
		if strings.HasPrefix(x.ID, "title_") && x.ID != "title_case" {
			t.Errorf("unmeasured look judged: %+v", x)
		}
	}
}

// A reference's text height is a share of its own frame height; the profile keeps it as a share of a 9:16 frame's height, so a 16:9 or a
// 1:1 reference does not read 1.78 times too large (and its recipe does not shrink the title).
func TestTextHeightIsNormalisedToAPortraitFrame(t *testing.T) {
	ref := func(w, h int, pct float64) Measures {
		m := referenceMeasures(t)[0]
		m.Width, m.Height = w, h
		m.Text.TextHeightPct = pct
		return m
	}
	// The same capitals, 43 px on a 1080 px short side, read as a share of three different frames.
	cases := map[string]Measures{
		"9:16": ref(1080, 1920, 2.25), // 43 / 1920
		"16:9": ref(1920, 1080, 4.0),  // 43 / 1080
		"1:1":  ref(1080, 1080, 4.0),  // 43 / 1080
		"4:5":  ref(1080, 1350, 3.2),  // 43 / 1350
	}
	for name, m := range cases {
		p := Aggregate([]Measures{m})
		if got := p.Text.HeightPct; math.Abs(got-2.25) > 0.06 {
			t.Errorf("%s: %.2f %% of a 9:16 frame, want 2.25", name, got)
		}
	}
	// Without dimensions nothing is rescaled.
	if p := Aggregate([]Measures{ref(0, 0, 4.0)}); p.Text.HeightPct != 4 {
		t.Errorf("unknown size: %.2f", p.Text.HeightPct)
	}
	if f := portraitFactor(Measures{Width: 1080, Height: 1920}); f != 1 {
		t.Errorf("9:16 factor %v", f)
	}
	// The recipe of a 16:9 reference is the same as the recipe of the 9:16 one with the same capitals.
	a, b := Aggregate([]Measures{cases["9:16"]}).TitleRecipe(), Aggregate([]Measures{cases["16:9"]}).TitleRecipe()
	if a.Scale != b.Scale {
		t.Errorf("scales differ: %v %v", a.Scale, b.Scale)
	}
}
