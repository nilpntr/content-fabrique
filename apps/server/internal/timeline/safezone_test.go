package timeline

import (
	"strings"
	"testing"
)

func TestSafeZones(t *testing.T) {
	cases := []struct {
		format Format
		inner  Rect
		blocks int
	}{
		{Format9x16, Rect{120, 240, 960, 1260}, 1},
		{Format1x1, Rect{54, 54, 1026, 1026}, 0},
		{Format4x5, Rect{54, 68, 1026, 1282}, 0},
		{Format16x9, Rect{192, 108, 1728, 918}, 0},
	}
	for _, tc := range cases {
		z, ok := SafeZoneFor(tc.format)
		if !ok || z.Inner != tc.inner || len(z.Blocked) != tc.blocks {
			t.Errorf("%s: got %+v %v, want inner %+v and %d blocked areas", tc.format, z, ok, tc.inner, tc.blocks)
		}
	}
	if z, _ := SafeZoneFor(Format9x16); z.Blocked[0] != (Rect{900, 840, 1080, 1920}) {
		t.Errorf("9:16 action column: %+v", z.Blocked[0])
	}
	if _, ok := SafeZoneFor("2:1"); ok {
		t.Error("unknown format")
	}
}

// pos converts a pixel centre to the normalised position the spec stores.
func pos(cx, cy float64, f Format) *Point {
	w, h, _ := f.Size()
	return &Point{X: cx / float64(w), Y: cy / float64(h)}
}

// "Hi" in title_bold is 86 px wide (2 x 0.6 x 72, rounded) and 86 px tall (one line): half of it is 43 px.
func TestUnsafeTextEdges(t *testing.T) {
	cases := []struct {
		name   string
		format Format
		cx, cy float64
		safe   bool
		reason string // part of the message when unsafe
	}{
		{"9:16 centre", Format9x16, 540, 960, true, ""},
		{"9:16 top edge on the line", Format9x16, 540, 283, true, ""},
		{"9:16 top edge 1 px above", Format9x16, 540, 282, false, "top edge"},
		{"9:16 bottom edge on the line", Format9x16, 540, 1217, true, ""},
		{"9:16 bottom edge 1 px below", Format9x16, 540, 1218, false, "bottom edge"},
		{"9:16 left edge on the line", Format9x16, 163, 960, true, ""},
		{"9:16 left edge 1 px outside", Format9x16, 162, 960, false, "left edge"},
		{"9:16 right edge on the line, high", Format9x16, 917, 700, true, ""},
		{"9:16 right edge 1 px outside", Format9x16, 918, 700, false, "right edge"},
		{"9:16 action column: right edge at 900", Format9x16, 857, 960, true, ""},
		{"9:16 action column: right edge at 901", Format9x16, 858, 960, false, "action column"},
		{"9:16 action column: bottom at 840", Format9x16, 900, 797, true, ""},
		{"9:16 action column: bottom at 841", Format9x16, 900, 798, false, "action column"},
		{"1:1 top on the line", Format1x1, 540, 97, true, ""},
		{"1:1 top 1 px above", Format1x1, 540, 96, false, "top edge"},
		{"1:1 bottom on the line", Format1x1, 540, 983, true, ""},
		{"1:1 bottom 1 px below", Format1x1, 540, 984, false, "bottom edge"},
		{"1:1 left on the line", Format1x1, 97, 540, true, ""},
		{"1:1 left 1 px outside", Format1x1, 96, 540, false, "left edge"},
		{"1:1 right on the line", Format1x1, 983, 540, true, ""},
		{"1:1 right 1 px outside", Format1x1, 984, 540, false, "right edge"},
		{"1:1 has no action column", Format1x1, 983, 983 - 42, true, ""},
		{"4:5 top on the line", Format4x5, 540, 111, true, ""},
		{"4:5 top 1 px above", Format4x5, 540, 110, false, "top edge"},
		{"4:5 bottom on the line", Format4x5, 540, 1239, true, ""},
		{"4:5 bottom 1 px below", Format4x5, 540, 1240, false, "bottom edge"},
		{"16:9 top on the line", Format16x9, 960, 151, true, ""},
		{"16:9 top 1 px above", Format16x9, 960, 150, false, "top edge"},
		{"16:9 bottom 15 % clear: on the line", Format16x9, 960, 875, true, ""},
		{"16:9 bottom 15 % clear: 1 px below", Format16x9, 960, 876, false, "bottom edge"},
		{"16:9 side margin 10 %: on the line", Format16x9, 235, 540, true, ""},
		{"16:9 side margin 10 %: 1 px outside", Format16x9, 234, 540, false, "left edge"},
		{"16:9 right side margin on the line", Format16x9, 1685, 540, true, ""},
		{"16:9 right side margin 1 px outside", Format16x9, 1686, 540, false, "right edge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "Hi", Style: "title_bold", Position: pos(tc.cx, tc.cy, tc.format)}
			s := withFormat(baseSpec(), tc.format)
			res := validateSpec(t, addTrack(s, textTrack(item)))
			if tc.safe {
				wantCodes(t, res)
				return
			}
			wantCodes(t, res, CodeUnsafeText)
			if !strings.Contains(res.Issues[0].Message, tc.reason) {
				t.Fatalf("message %q lacks %q", res.Issues[0].Message, tc.reason)
			}
			if res.Issues[0].Path != "/tracks/1/items/0/position" {
				t.Fatalf("path %q", res.Issues[0].Path)
			}
		})
	}
}

func TestTextBoxEstimate(t *testing.T) {
	zone, _ := SafeZoneFor(Format9x16)
	style, _ := LookupStyle("title_bold")
	// Three lines, the widest has 13 characters: 13 x 0.6 x 72 = 561.6 -> 562 wide, 3 x 86 = 258 tall, centred.
	box := EstimateTextBox("Free position\nin three\nlines", style, Point{X: 0.5, Y: 0.35}, 1080, 1920, zone)
	if box.width() != 562 || box.height() != 258 || box.Left != 540-281 || box.Top != 672-129 {
		t.Fatalf("got %+v", box)
	}
	// Characters count as runes, not bytes.
	if got := EstimateTextBox("ééé", style, Point{X: 0.5, Y: 0.5}, 1080, 1920, zone).width(); got != roundHalfUp(0.6*72*3) {
		t.Fatalf("width %d", got)
	}
	// A line wider than the zone wraps at the zone's width (840 px on 9:16) and the box grows taller instead:
	// 20 W are 864 px wide, so two lines of 86 px.
	mid := Point{X: 0.5, Y: 0.5}
	if b := EstimateTextBox(strings.Repeat("W", 20), style, mid, 1080, 1920, zone); b.width() != 840 || b.height() != 172 {
		t.Fatalf("20 characters: %+v", b)
	}
	// 120 W are 5184 px wide: ceil(5184 / 840) = 7 lines, 602 px.
	long := strings.Repeat("W", 120)
	if b := EstimateTextBox(long, style, mid, 1080, 1920, zone); b.width() != 840 || b.height() != 7*86 {
		t.Fatalf("120 characters: %+v", b)
	}
	// Explicit lines add up: one line that wraps in two plus one short line is three lines.
	if b := EstimateTextBox(strings.Repeat("W", 20)+"\nHi", style, mid, 1080, 1920, zone); b.height() != 3*86 {
		t.Fatalf("wrapped plus explicit line: %+v", b)
	}
	// An empty line still takes a line.
	if b := EstimateTextBox("Hi\n\nHi", style, mid, 1080, 1920, zone); b.height() != 3*86 {
		t.Fatalf("empty line: %+v", b)
	}
}

// The wrapped height decides the verdict: 20 W at y = 754 px end at 840 px, just above the action column's top edge,
// and one pixel lower they run into it. Without wrapping (one 86 px line) both would fit.
func TestWrappedTextGrowsTaller(t *testing.T) {
	item := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: strings.Repeat("W", 20), Style: "title_bold"}
	item.Position = pos(540, 754, Format9x16)
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(item))))
	item.Position = pos(540, 755, Format9x16)
	res := validateSpec(t, addTrack(baseSpec(), textTrack(item)))
	wantCodes(t, res, CodeUnsafeText)
	if !strings.Contains(res.Issues[0].Message, "action column") {
		t.Fatalf("message %q", res.Issues[0].Message)
	}
	// 120 characters need seven lines (602 px) of the 600 px between the top margin and the action column: nowhere
	// to put them by position, whatever the position.
	item.Text = strings.Repeat("W", 120)
	for _, y := range []float64{0.2, 0.3, 0.4, 0.5} {
		item.Position = &Point{X: 0.5, Y: y}
		wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(item))), CodeUnsafeText)
	}
	// An anchor takes the same text.
	item.Position, item.Anchor = nil, AnchorCenter
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(item))))
}

func TestAnchorsAreNeverUnsafe(t *testing.T) {
	long := strings.Repeat("W", 39) + "\n" + strings.Repeat("W", 39) + "\n" + strings.Repeat("W", 39)
	for _, f := range []Format{Format9x16, Format1x1, Format4x5, Format16x9} {
		for _, a := range []string{AnchorUpperThird, AnchorCenter, AnchorLowerThird} {
			item := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: long, Style: "title_bold", Anchor: a}
			wantCodes(t, validateSpec(t, addTrack(withFormat(baseSpec(), f), textTrack(item))))
		}
	}
}
