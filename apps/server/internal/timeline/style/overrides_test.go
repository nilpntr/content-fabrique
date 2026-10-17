package style

import (
	"strings"
	"testing"
)

func TestOverridesValidate(t *testing.T) {
	ok := []Overrides{{}, {Fill: "#F2E9A0"}, {Fill: "#abcdef", Scale: sp(0.6), Shadow: ShadowNone}, {Scale: sp(1.4), Shadow: ShadowStrong}, {Shadow: ShadowSoft}}
	for _, o := range ok {
		if err := o.Validate(); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	bad := map[string]Overrides{
		"fill name":      {Fill: "red"},
		"fill short":     {Fill: "#FFF"},
		"fill alpha":     {Fill: "#F2E9A0FF"},
		"fill injection": {Fill: "#F2E9A0}\\an8"},
		"fill no hash":   {Fill: "F2E9A0"},
		"scale zero":     {Scale: sp(0)},
		"scale low":      {Scale: sp(0.59)},
		"scale high":     {Scale: sp(1.41)},
		"shadow":         {Shadow: "huge"},
	}
	for name, o := range bad {
		if err := o.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
		if _, err := mustStyle(t, "title_series").With(o); err == nil {
			t.Errorf("%s: With accepted it", name)
		}
	}
	for _, a := range []string{"", AlignLeft, AlignCenter, AlignRight} {
		if !ValidAlign(a) {
			t.Errorf("align %q", a)
		}
	}
	if ValidAlign("top") || ValidAlign("Left") {
		t.Error("a bad align was accepted")
	}
}

func sp(v float64) *float64 { return &v }

func mustStyle(t *testing.T, id string) Style {
	t.Helper()
	s, ok := Lookup(id)
	if !ok {
		t.Fatalf("no style %s", id)
	}
	return s
}

func TestWithBuildsADerivedStyle(t *testing.T) {
	base := mustStyle(t, "title_series")
	same, err := base.With(Overrides{})
	if err != nil || same.ID != base.ID || same.EmPct != base.EmPct {
		t.Fatalf("no overrides: %+v %v", same, err)
	}
	if one, _ := base.With(Overrides{Scale: sp(1)}); one.ID != base.ID {
		t.Error("a scale of 1 changed the style")
	}
	d, err := base.With(Overrides{Fill: "#f2e9a0", Scale: sp(0.853), Shadow: ShadowNone})
	if err != nil {
		t.Fatal(err)
	}
	if d.Fill != Opaque(0xF2, 0xE9, 0xA0) || d.ShadowPct != 0 || d.EmPct != base.EmPct*0.85 {
		t.Errorf("derived: %+v", d)
	}
	// The ID is the base and the overrides, with only characters an ASS style name takes and no comma.
	if d.ID != "title_series~fF2E9A0~s85~hn" {
		t.Errorf("id %q", d.ID)
	}
	if strings.ContainsAny(d.ID, ", ") {
		t.Errorf("id %q", d.ID)
	}
	// Two scales that round to the same hundredth are the same style (one ASS style line).
	e, _ := base.With(Overrides{Fill: "#F2E9A0", Scale: sp(0.854), Shadow: ShadowNone})
	if e.ID != d.ID || e.EmPct != d.EmPct {
		t.Errorf("0.853 and 0.854: %q %v / %q %v", d.ID, d.EmPct, e.ID, e.EmPct)
	}
	// Different overrides, different IDs.
	ids := map[string]bool{}
	for _, o := range []Overrides{{Fill: "#111111"}, {Fill: "#222222"}, {Scale: sp(0.7)}, {Scale: sp(0.8)}, {Shadow: ShadowNone}, {Shadow: ShadowSoft}, {Shadow: ShadowStrong}} {
		s, err := base.With(o)
		if err != nil || ids[s.ID] {
			t.Errorf("%+v: %q %v", o, s.ID, err)
		}
		ids[s.ID] = true
	}
	// Shadows: none has no offset, strong is heavier than soft.
	none, _ := base.With(Overrides{Shadow: ShadowNone})
	soft, _ := base.With(Overrides{Shadow: ShadowSoft})
	strong, _ := base.With(Overrides{Shadow: ShadowStrong})
	if none.ShadowPx(100) != 0 || soft.ShadowPx(100) >= strong.ShadowPx(100) || soft.Shadow.A >= strong.Shadow.A {
		t.Errorf("shadows: %d %d %d", none.ShadowPx(100), soft.ShadowPx(100), strong.ShadowPx(100))
	}
	if none.Pad(100) >= soft.Pad(100) {
		t.Error("no shadow does not shrink the padding")
	}
}

func TestParseHexAndCapHeight(t *testing.T) {
	c, ok := ParseHex("#F2E9A0")
	if !ok || c != Opaque(0xF2, 0xE9, 0xA0) || c.Hex() != "#F2E9A0" {
		t.Errorf("%+v %v %s", c, ok, c.Hex())
	}
	if _, ok := ParseHex("F2E9A0"); ok {
		t.Error("no hash")
	}
	// title_series is about 4 % of the frame height in capitals on a 9:16 frame (the series titles of the creator we measured).
	st := mustStyle(t, "title_series")
	if pct := st.CapHeightPct(1080, 1920); pct < 3.6 || pct > 4.4 {
		t.Errorf("title_series cap height %.2f %% of the frame", pct)
	}
	big, _ := st.With(Overrides{Scale: sp(1.4)})
	if big.CapHeightPx(1080) <= st.CapHeightPx(1080)*1.3 {
		t.Error("scale does not scale the capitals")
	}
	if st.CapHeightPct(1080, 0) != 0 {
		t.Error("zero height")
	}
}

func TestShadowName(t *testing.T) {
	base := mustStyle(t, "title_series")
	for o, want := range map[string]string{ShadowNone: ShadowNone, ShadowSoft: ShadowSoft, ShadowStrong: ShadowStrong} {
		d, _ := base.With(Overrides{Shadow: o})
		if d.ShadowName() != want {
			t.Errorf("%s: %s", o, d.ShadowName())
		}
	}
	// The styles as they are: the series title has a light shadow, the plain title a light one, the stat a heavier one is still soft.
	if base.ShadowName() != ShadowSoft || mustStyle(t, "label_box").ShadowName() != ShadowNone {
		t.Errorf("registry shadows: %s %s", base.ShadowName(), mustStyle(t, "label_box").ShadowName())
	}
	// A box style with a shadow override has a shadow.
	if b, _ := mustStyle(t, "label_box").With(Overrides{Shadow: ShadowStrong}); b.ShadowName() != ShadowStrong {
		t.Errorf("box with a strong shadow: %s", b.ShadowName())
	}
}
