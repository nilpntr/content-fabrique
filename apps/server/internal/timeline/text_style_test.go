package timeline

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// The per-item style overrides (P6-05): placement uses the derived style, so scale and shadow move the box, align does not, and a
// value the renderer cannot take is invalid_text_style.
func TestTextStyleOverridesPlacement(t *testing.T) {
	out := baseSpec().Output
	it := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "BUILDING LATS 101", Style: "title_series", Anchor: AnchorUpperThird}
	base, err := PlaceText(out, it)
	if err != nil {
		t.Fatal(err)
	}
	small := it
	small.Scale = ptr(0.6)
	ps, err := PlaceText(out, small)
	if err != nil || ps.Box.Width() >= base.Box.Width() || ps.Layout.FontSize >= base.Layout.FontSize {
		t.Errorf("a smaller scale: %+v %v", ps.Box, err)
	}
	none := it
	none.Shadow = style.ShadowNone
	pn, _ := PlaceText(out, none)
	if pn.Layout.Pad >= base.Layout.Pad {
		t.Errorf("no shadow: pad %d against %d", pn.Layout.Pad, base.Layout.Pad)
	}
	left := it
	left.Align = style.AlignLeft
	pl, _ := PlaceText(out, left)
	if pl.Box != base.Box || pl.Align != style.AlignLeft || base.Align != style.AlignCenter {
		t.Errorf("align moved the box: %+v %+v (%s, %s)", pl.Box, base.Box, pl.Align, base.Align)
	}
	filled := it
	filled.Fill = "#F2E9A0"
	pf, _ := PlaceText(out, filled)
	if pf.Style.Fill.Hex() != "#F2E9A0" || !strings.HasPrefix(pf.Style.ID, "title_series~") {
		t.Errorf("fill: %+v", pf.Style)
	}
	// An invalid value is an error from PlaceText.
	for name, bad := range map[string]TextItem{
		"fill":   {ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "title_series", Anchor: AnchorUpperThird, Fill: "red"},
		"scale":  {ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "title_series", Anchor: AnchorUpperThird, Scale: ptr(3.0)},
		"shadow": {ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "title_series", Anchor: AnchorUpperThird, Shadow: "big"},
		"align":  {ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "title_series", Anchor: AnchorUpperThird, Align: "top"},
	} {
		if _, err := PlaceText(out, bad); err == nil {
			t.Errorf("%s was placed", name)
		}
	}
}

func TestInvalidTextStyleIsAnIssue(t *testing.T) {
	for name, it := range map[string]TextItem{
		"fill":   {ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "title_series", Anchor: AnchorUpperThird, Fill: "#GGGGGG"},
		"scale":  {ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "title_series", Anchor: AnchorUpperThird, Scale: ptr(0.1)},
		"shadow": {ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "title_series", Anchor: AnchorUpperThird, Shadow: "big"},
		"align":  {ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "title_series", Anchor: AnchorUpperThird, Align: "top"},
	} {
		res := validateSpec(t, addTrack(baseSpec(), textTrack(it)))
		// The JSON Schema refuses each of them first; invalid_text_style is the same check for a spec that did not go through it.
		wantCodes(t, res, CodeSchema)
		if !strings.HasPrefix(res.Issues[0].Path, "/tracks/1/items/0/") {
			t.Errorf("%s: %+v", name, res.Issues[0])
		}
	}
	// Good overrides validate, and a title whose scale makes it fit a smaller zone is safe.
	good := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "BUILDING LATS 101", Style: "title_series", Anchor: AnchorUpperThird,
		Fill: "#F2E9A0", Scale: ptr(0.85), Shadow: style.ShadowNone, Align: style.AlignLeft}
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(good))))
	// The size is checked against the safe zone with the override: a text too wide at 1.4 is unsafe, at 1 it is not.
	wide := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "title_bold", Anchor: AnchorUpperThird}
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(wide))))
}
