package style

import "sort"

//go:generate go run ./gen -fonts ../../../../../packages/media-tools/fonts -out metrics_gen.go

// FontTable is the part of a font file that text layout needs, generated from the file by gen (and checked against the file
// by a test, so the table cannot drift from the vendored font): the sizes libass scales by and the advance width of every
// character in the layout character set.
type FontTable struct {
	UnitsPerEm int
	// WinAscent and WinDescent are the OS/2 usWinAscent and usWinDescent. libass sizes the font so that the ASS font size is
	// their sum in pixels: one font unit is size / (WinAscent + WinDescent) pixels wide, and a line is size pixels tall.
	WinAscent, WinDescent int
	// Notdef is the advance of the font's missing-glyph box.
	Notdef uint16
	// Runs lists maximal runs of consecutive characters the font maps; Adv holds their advances, run after run.
	Runs []AdvRun
	Adv  []uint16
}

// AdvRun is a run of consecutive characters in a FontTable.
type AdvRun struct {
	First rune
	Off   int32 // index of the first advance in Adv
	N     int32 // number of characters
}

// layoutRanges is the character set the tables cover: Basic Latin, Latin-1, Latin Extended-A and B, Vietnamese, Cyrillic,
// general punctuation, currency symbols and a few symbols. A character outside it is measured as one full em, which is wider
// than almost any glyph, so a layout stays on the safe side.
var layoutRanges = [][2]rune{
	{0x0020, 0x007E}, {0x00A0, 0x024F}, {0x0300, 0x036F}, {0x0400, 0x04FF}, {0x1E00, 0x1EFF},
	{0x2010, 0x2027}, {0x2030, 0x205E}, {0x20A0, 0x20BF}, {0x2122, 0x2122}, {0x2190, 0x2193}, {0x2212, 0x2212},
}

// InLayoutSet reports whether the tables carry r.
func InLayoutSet(r rune) bool {
	for _, rg := range layoutRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// BuildTable extracts the layout table of a parsed font. It is what gen writes into metrics_gen.go.
func BuildTable(f *FontFile) *FontTable {
	t := &FontTable{UnitsPerEm: f.UnitsPerEm, WinAscent: f.WinAscent, WinDescent: f.WinDescent, Notdef: f.NotdefAdvance}
	if t.WinAscent+t.WinDescent == 0 {
		t.WinAscent, t.WinDescent = f.HheaAscent, -f.HheaDescent
	}
	var prev rune = -2
	for _, r := range f.SortedRunes() {
		if !InLayoutSet(r) {
			continue
		}
		if r != prev+1 || len(t.Runs) == 0 {
			t.Runs = append(t.Runs, AdvRun{First: r, Off: int32(len(t.Adv))}) //nolint:gosec // a few thousand advances at most
		}
		t.Runs[len(t.Runs)-1].N++
		t.Adv = append(t.Adv, f.Advance[r])
		prev = r
	}
	return t
}

// advance returns the advance of r in font units. ok is false when the character is not in the table (the font has no glyph
// for it, or it is outside the layout set).
func (t *FontTable) advance(r rune) (adv uint16, ok bool) {
	i := sort.Search(len(t.Runs), func(i int) bool { return t.Runs[i].First+rune(t.Runs[i].N) > r })
	if i == len(t.Runs) || r < t.Runs[i].First {
		return 0, false
	}
	return t.Adv[int(t.Runs[i].Off)+int(r-t.Runs[i].First)], true
}
