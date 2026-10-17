package timeline

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// zone is the safe zone of a format at its size.
func (f Format) zone() (style.Zone, bool) {
	w, h, ok := f.Size()
	if !ok {
		return style.Zone{}, false
	}
	return style.ZoneFor(string(f), w, h)
}

func TestSafeZones(t *testing.T) {
	cases := []struct {
		format Format
		inner  style.Rect
		blocks int
	}{
		{Format9x16, style.Rect{Left: 120, Top: 240, Right: 960, Bottom: 1260}, 1},
		{Format1x1, style.Rect{Left: 54, Top: 54, Right: 1026, Bottom: 1026}, 0},
		{Format4x5, style.Rect{Left: 54, Top: 68, Right: 1026, Bottom: 1282}, 0},
		{Format16x9, style.Rect{Left: 192, Top: 108, Right: 1728, Bottom: 918}, 0},
	}
	for _, tc := range cases {
		z, ok := tc.format.zone()
		if !ok || z.Inner != tc.inner || len(z.Blocked) != tc.blocks {
			t.Errorf("%s: got %+v %v, want inner %+v and %d blocked areas", tc.format, z, ok, tc.inner, tc.blocks)
		}
	}
	if z, _ := Format9x16.zone(); z.Blocked[0] != (style.Rect{Left: 900, Top: 840, Right: 1080, Bottom: 1920}) {
		t.Errorf("9:16 action column: %+v", z.Blocked[0])
	}
	if _, ok := Format("2:1").zone(); ok {
		t.Error("unknown format")
	}
}

// at returns the normalised position whose pixel centre is (cx, cy).
func at(cx, cy int, f Format) *Point {
	w, h, _ := f.Size()
	return &Point{X: float64(cx) / float64(w), Y: float64(cy) / float64(h)}
}

// boxSize is the size of the box a text takes in a style on a format, from the shared layout.
func boxSize(t *testing.T, f Format, styleID, text string) (w, h int) {
	t.Helper()
	w2, h2, _ := f.Size()
	zone, _ := style.ZoneFor(string(f), w2, h2)
	st, ok := style.Lookup(styleID)
	if !ok {
		t.Fatalf("no style %s", styleID)
	}
	return style.Measure(st, min(w2, h2), text, style.MaxBoxWidth(zone, w2)).Box()
}

// TestUnsafeTextEdges puts a box exactly on each edge of each format's safe zone (safe) and one pixel beyond (unsafe).
func TestUnsafeTextEdges(t *testing.T) {
	const text, sty = "Hi", "title_bold"
	for _, f := range []Format{Format9x16, Format1x1, Format4x5, Format16x9} {
		w, h := boxSize(t, f, sty, text)
		zone, _ := f.zone()
		in := zone.Inner
		mid := func(lo, hi int) int { return (lo + hi) / 2 }
		cx0, cy0 := mid(in.Left, in.Right), mid(in.Top, in.Bottom)
		if f == Format9x16 {
			cy0 = 700 // above the action column's top, so only the zone edges matter below
		}
		edges := []struct {
			name   string
			cx, cy int // box exactly on the edge
			dx, dy int // one pixel further out
			reason string
		}{
			{"left", in.Left + w/2, cy0, -1, 0, "left edge"},
			{"right", in.Right - w + w/2, cy0, 1, 0, "right edge"},
			{"top", cx0, in.Top + h/2, 0, -1, "top edge"},
			{"bottom", cx0, in.Bottom - h + h/2, 0, 1, "bottom edge"},
		}
		if f == Format9x16 {
			// The action column: x above 900 for y above 840. Left of it and above it are safe.
			edges = append(edges,
				struct {
					name           string
					cx, cy, dx, dy int
					reason         string
				}{"action column, right edge at 900", 900 - w + w/2, 1000, 1, 0, "action column"},
				struct {
					name           string
					cx, cy, dx, dy int
					reason         string
				}{"action column, bottom edge at 840", 800 + w/2, 840 - h + h/2, 0, 1, "action column"},
			)
		}
		for _, e := range edges {
			t.Run(fmt.Sprintf("%s %s", f, e.name), func(t *testing.T) {
				mk := func(cx, cy int) Result {
					item := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: text, Style: sty, Position: at(cx, cy, f)}
					return validateSpec(t, addTrack(withFormat(baseSpec(), f), textTrack(item)))
				}
				wantCodes(t, mk(e.cx, e.cy))
				res := mk(e.cx+e.dx, e.cy+e.dy)
				wantCodes(t, res, CodeUnsafeText)
				if !strings.Contains(res.Issues[0].Message, e.reason) {
					t.Fatalf("message %q lacks %q", res.Issues[0].Message, e.reason)
				}
				if res.Issues[0].Path != "/tracks/1/items/0/position" {
					t.Fatalf("path %q", res.Issues[0].Path)
				}
			})
		}
	}
}

func TestTextLayout(t *testing.T) {
	w1, h1 := boxSize(t, Format9x16, "title_bold", "Free position")
	w3, h3 := boxSize(t, Format9x16, "title_bold", "Free position\nin three\nlines")
	if w3 != w1 || h3 != 3*(h1-2*padOf(t, "title_bold", 1080))+2*padOf(t, "title_bold", 1080) {
		t.Errorf("three lines: %dx%d, one line %dx%d", w3, h3, w1, h1)
	}
	// An empty line still takes a line.
	_, hEmpty := boxSize(t, Format9x16, "title_bold", "Hi\n\nHi")
	_, hTwo := boxSize(t, Format9x16, "title_bold", "Hi\nHi")
	if lh := h1 - 2*padOf(t, "title_bold", 1080); hEmpty != hTwo+lh {
		t.Errorf("empty line: %d vs %d", hEmpty, hTwo)
	}
	// Characters count as runes: the same number of the same letter is the same width, in any encoding length.
	wA, _ := boxSize(t, Format9x16, "title_bold", "aaa")
	wB, _ := boxSize(t, Format9x16, "title_bold", "ééé")
	if d := wA - wB; d < -6 || d > 6 { // é is as wide as a in this font
		t.Errorf("aaa %d wide, ééé %d wide", wA, wB)
	}
	// A line wider than the safe column wraps at it and the box grows taller instead of wider.
	zone, _ := Format9x16.zone()
	col := style.MaxBoxWidth(zone, 1080)
	if col != 720 {
		t.Errorf("9:16 text column is %d px wide, want 720 (the safe zone narrowed to keep out of the action column)", col)
	}
	long := strings.Repeat("W", 20)
	w, h := boxSize(t, Format9x16, "title_bold", long)
	lh := h1 - 2*padOf(t, "title_bold", 1080)
	if lines := (h - 2*padOf(t, "title_bold", 1080)) / lh; w > col || lines < 2 || h != lines*lh+2*padOf(t, "title_bold", 1080) {
		t.Errorf("20 W: %dx%d, column %d, one line %d tall: want a few whole lines", w, h, col, h1)
	}
	// More real widths than the old 0.6 em estimate: narrow letters are narrower, wide ones wider.
	wi, _ := boxSize(t, Format9x16, "title_bold", strings.Repeat("i", 10))
	ww, _ := boxSize(t, Format9x16, "title_bold", strings.Repeat("W", 10))
	if wi >= ww/2 {
		t.Errorf("10 i is %d wide, 10 W is %d wide: the layout should use real advance widths", wi, ww)
	}
}

func padOf(t *testing.T, styleID string, _ int) int {
	t.Helper()
	st, _ := style.Lookup(styleID)
	return st.Pad(st.FontSizePx(1080))
}

// The wrapped height decides the verdict: a position that fits one line of text can run into the action column when the
// text wraps.
func TestWrappedTextGrowsTaller(t *testing.T) {
	_, hBox := boxSize(t, Format9x16, "title_bold", strings.Repeat("W", 20))
	item := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: strings.Repeat("W", 20), Style: "title_bold"}
	// The box's bottom on y = 840 (safe) and one pixel lower (into the action column? the box is at most 720 wide, centred, so
	// it ends at x = 900 at most: no overlap). The wrap shows in the vertical zone instead.
	_, zoneBottom := 0, 1260
	item.Position = at(540, zoneBottom-hBox+hBox/2, Format9x16)
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(item))))
	item.Position = at(540, zoneBottom-hBox+hBox/2+1, Format9x16)
	res := validateSpec(t, addTrack(baseSpec(), textTrack(item)))
	wantCodes(t, res, CodeUnsafeText)
	if !strings.Contains(res.Issues[0].Message, "bottom edge") {
		t.Fatalf("message %q", res.Issues[0].Message)
	}
	// 40 characters of W take a tall box (about 750 px of the 1020 px between the margins): too high or too low is unsafe,
	// and the middle is fine. An anchor moves the box into the zone by itself.
	item.Text = strings.Repeat("W", 40)
	for y, safe := range map[float64]bool{0.2: false, 0.4: true, 0.55: false} {
		item.Position = &Point{X: 0.5, Y: y}
		res := validateSpec(t, addTrack(baseSpec(), textTrack(item)))
		if safe {
			wantCodes(t, res)
		} else {
			wantCodes(t, res, CodeUnsafeText)
		}
	}
	item.Position, item.Anchor = nil, AnchorCenter
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), textTrack(item))))
	// A text taller than the zone cannot be placed by any anchor: that is unsafe_text on the anchor.
	item.Style = "stat_big"
	item.Text = strings.Repeat("WWWW ", 14)
	res = validateSpec(t, addTrack(baseSpec(), textTrack(item)))
	wantCodes(t, res, CodeUnsafeText)
	if res.Issues[0].Path != "/tracks/1/items/0/anchor" {
		t.Fatalf("path %q", res.Issues[0].Path)
	}
}

func TestAnchorsFitNormalText(t *testing.T) {
	long := strings.Repeat("W", 10) + "\n" + strings.Repeat("W", 10) + "\n" + strings.Repeat("W", 10)
	for _, f := range []Format{Format9x16, Format1x1, Format4x5, Format16x9} {
		for _, a := range []string{AnchorUpperThird, AnchorCenter, AnchorLowerThird} {
			item := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: long, Style: "title_bold", Anchor: a}
			wantCodes(t, validateSpec(t, addTrack(withFormat(baseSpec(), f), textTrack(item))))
		}
	}
}

// Every style's every anchor is a box inside the zone on every format for a short text.
func TestEveryStyleEveryAnchorIsSafe(t *testing.T) {
	for _, sty := range style.IDs() {
		for _, f := range []Format{Format9x16, Format1x1, Format4x5, Format16x9} {
			for _, a := range []string{AnchorUpperThird, AnchorCenter, AnchorLowerThird} {
				text := "Short hook\nsecond line"
				if sty == "stat_big" {
					text = "5x5" // a style for a few characters
				}
				item := TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: text, Style: sty, Anchor: a}
				wantCodes(t, validateSpec(t, addTrack(withFormat(baseSpec(), f), textTrack(item))))
			}
		}
	}
}
