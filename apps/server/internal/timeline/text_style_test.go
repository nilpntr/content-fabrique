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
		// Through Validate the JSON Schema refuses each of them first, at the field.
		res := validateSpec(t, addTrack(baseSpec(), textTrack(it)))
		wantCodes(t, res, CodeSchema)
		if !strings.HasPrefix(res.Issues[0].Path, "/tracks/1/items/0/") {
			t.Errorf("%s: %+v", name, res.Issues[0])
		}
		// A Go-built spec does not pass the schema: the semantic checker called directly reports invalid_text_style at the field's own
		// path, and does not go on to measure a text it cannot place.
		issues := checkSemantics(addTrack(baseSpec(), textTrack(it)), MemoryCatalog(nil))
		var found []Issue
		for _, is := range issues {
			if is.Code == CodeInvalidTextStyle {
				found = append(found, is)
			}
			if is.Code == CodeUnsafeText {
				t.Errorf("%s: a text that cannot be placed was measured: %+v", name, is)
			}
		}
		if len(found) != 1 || found[0].Path != "/tracks/1/items/0/"+name || !strings.Contains(found[0].Message, "t1") {
			t.Errorf("%s: invalid_text_style issues %+v", name, found)
		}
	}
	// A bad override is reported whether or not the style is known.
	unknown := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "A", Style: "no_such_style", Anchor: AnchorUpperThird, Fill: "red"}
	codes := map[Code]bool{}
	for _, is := range checkSemantics(addTrack(baseSpec(), textTrack(unknown)), MemoryCatalog(nil)) {
		codes[is.Code] = true
	}
	if !codes[CodeInvalidTextStyle] || !codes[CodeUnknownStyle] {
		t.Errorf("unknown style with a bad fill: %v", codes)
	}
	// Good overrides validate, and a title whose scale makes it fit a smaller zone is safe.
	good := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "BUILDING LATS 101", Style: "title_series", Anchor: AnchorUpperThird,
		Fill: "#F2E9A0", Scale: ptr(0.85), Shadow: style.ShadowNone, Align: style.AlignLeft}
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(good))))
}

// The size override is part of the safe-zone check: a title low in the zone fits at the style's size and runs out of the bottom of the zone
// at a scale of 1.4, so the same item is safe at 1 and unsafe_text at 1.4.
func TestScaleCountsInTheSafeZone(t *testing.T) {
	low := func(scale *float64) TextItem {
		return TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "BUILDING LATS 101", Style: "title_series", Position: &Point{X: 0.5, Y: 0.605}, Scale: scale}
	}
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(low(nil)))))
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(low(ptr(1.0))))))
	res := validateSpec(t, addTrack(baseSpec(), textTrack(low(ptr(1.4)))))
	wantCodes(t, res, CodeUnsafeText)
	if !strings.Contains(res.Issues[0].Message, "bottom edge") {
		t.Errorf("message: %s", res.Issues[0].Message)
	}
}
