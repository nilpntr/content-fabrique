package style

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// FontFile is what the layout needs from a TrueType or OpenType font file: the sizes that libass scales by and the advance
// width of every character the font maps. It is a minimal sfnt reader (head, hhea, hmtx, maxp, OS/2, cmap formats 4 and 12,
// name) on the standard library only; it does not shape text, so kerning and ligatures are not applied. Advances without
// kerning are an upper bound for the fonts of the registry (their kerning is negative for the pairs that matter), which is
// the safe side for a safe-zone check.
type FontFile struct {
	UnitsPerEm int
	// HheaAscent, HheaDescent (negative) and LineGap are the horizontal header's metrics; WinAscent and WinDescent are the
	// OS/2 usWinAscent and usWinDescent. libass sizes a font so that the font size is WinAscent + WinDescent in pixels.
	HheaAscent, HheaDescent, LineGap int
	WinAscent, WinDescent            int
	// Family and Full are the font's family (name ID 1) and full name (name ID 4).
	Family, Full string
	// Advance maps a character to its advance width in font units.
	Advance map[rune]uint16
	// NotdefAdvance is the advance of glyph 0.
	NotdefAdvance uint16
}

// ParseFont reads a font file.
func ParseFont(data []byte) (*FontFile, error) {
	r := reader{b: data}
	switch r.u32(0) {
	case 0x00010000, 0x74727565 /* true */, 0x4F54544F /* OTTO */ :
	default:
		return nil, errors.New("style: not a TrueType or OpenType font (collections are not supported)")
	}
	n := int(r.u16(4))
	tables := make(map[string][]byte, n)
	for i := 0; i < n; i++ {
		rec := 12 + 16*i
		tag := string(r.slice(rec, 4))
		off, length := int(r.u32(rec+8)), int(r.u32(rec+12))
		if r.err != nil {
			return nil, r.err
		}
		if off < 0 || length < 0 || off+length > len(data) {
			return nil, fmt.Errorf("style: font table %q is outside the file", tag)
		}
		tables[tag] = data[off : off+length]
	}
	for _, t := range []string{"head", "hhea", "hmtx", "maxp", "cmap"} {
		if tables[t] == nil {
			return nil, fmt.Errorf("style: font has no %q table", t)
		}
	}

	f := &FontFile{Advance: map[rune]uint16{}}
	head := reader{b: tables["head"]}
	f.UnitsPerEm = int(head.u16(18))
	hhea := reader{b: tables["hhea"]}
	f.HheaAscent, f.HheaDescent, f.LineGap = int(hhea.i16(4)), int(hhea.i16(6)), int(hhea.i16(8))
	numHMetrics := int(hhea.u16(34))
	maxp := reader{b: tables["maxp"]}
	numGlyphs := int(maxp.u16(4))
	if os2 := tables["OS/2"]; os2 != nil {
		o := reader{b: os2}
		f.WinAscent, f.WinDescent = int(o.u16(74)), int(o.u16(76))
	}
	if head.err != nil || hhea.err != nil || maxp.err != nil {
		return nil, errors.New("style: font header tables are truncated")
	}
	if f.UnitsPerEm <= 0 || numHMetrics <= 0 || numGlyphs <= 0 {
		return nil, errors.New("style: font header tables are inconsistent")
	}

	// hmtx: numHMetrics (advance, lsb) pairs; the glyphs after them reuse the last advance.
	hmtx := reader{b: tables["hmtx"]}
	advance := func(gid int) uint16 {
		if gid >= numHMetrics {
			gid = numHMetrics - 1
		}
		return hmtx.u16(4 * gid)
	}
	f.NotdefAdvance = advance(0)
	if hmtx.err != nil {
		return nil, errors.New("style: font hmtx table is truncated")
	}

	cmap, err := readCmap(tables["cmap"])
	if err != nil {
		return nil, err
	}
	for ch, gid := range cmap {
		if gid == 0 || gid >= numGlyphs {
			continue
		}
		f.Advance[ch] = advance(gid)
	}
	if hmtx.err != nil {
		return nil, errors.New("style: font hmtx table is truncated")
	}
	if name := tables["name"]; name != nil {
		f.Family, f.Full = readNames(name)
	}
	return f, nil
}

// SortedRunes returns the characters the font maps, ascending.
func (f *FontFile) SortedRunes() []rune {
	out := make([]rune, 0, len(f.Advance))
	for r := range f.Advance {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// readCmap returns the character to glyph map of the best Unicode subtable (format 12 for the full range, else format 4
// for the Basic Multilingual Plane).
func readCmap(b []byte) (map[rune]int, error) {
	r := reader{b: b}
	n := int(r.u16(2))
	var f4, f12 = -1, -1
	for i := 0; i < n; i++ {
		rec := 4 + 8*i
		platform, encoding, off := r.u16(rec), r.u16(rec+2), int(r.u32(rec+4))
		if r.err != nil || off < 0 || off >= len(b) {
			return nil, errors.New("style: font cmap is truncated")
		}
		unicode := platform == 0 || (platform == 3 && (encoding == 1 || encoding == 10))
		if !unicode {
			continue
		}
		switch r.u16(off) {
		case 4:
			f4 = off
		case 12:
			f12 = off
		}
	}
	out := map[rune]int{}
	if f4 >= 0 {
		readFormat4(b, f4, out)
	}
	if f12 >= 0 {
		readFormat12(b, f12, out)
	}
	if len(out) == 0 {
		return nil, errors.New("style: font has no Unicode cmap (format 4 or 12)")
	}
	return out, nil
}

func readFormat4(b []byte, off int, out map[rune]int) {
	r := reader{b: b}
	segX2 := int(r.u16(off + 6))
	seg := segX2 / 2
	endCodes := off + 14
	startCodes := endCodes + segX2 + 2
	deltas := startCodes + segX2
	rangeOffs := deltas + segX2
	for i := 0; i < seg; i++ {
		end, start := int(r.u16(endCodes+2*i)), int(r.u16(startCodes+2*i))
		delta, ro := int(r.u16(deltas+2*i)), int(r.u16(rangeOffs+2*i))
		if r.err != nil {
			return
		}
		for c := start; c <= end && c < 0xFFFF; c++ {
			gid := 0
			if ro == 0 {
				gid = (c + delta) & 0xFFFF
			} else {
				p := rangeOffs + 2*i + ro + 2*(c-start)
				g := int(r.u16(p))
				if r.err != nil {
					return
				}
				if g != 0 {
					gid = (g + delta) & 0xFFFF
				}
			}
			if gid != 0 {
				out[rune(c)] = gid //nolint:gosec // c is a BMP code point (< 0xFFFF)
			}
		}
	}
}

func readFormat12(b []byte, off int, out map[rune]int) {
	r := reader{b: b}
	groups := int(r.u32(off + 12))
	for i := 0; i < groups; i++ {
		g := off + 16 + 12*i
		start, end, gid := int(r.u32(g)), int(r.u32(g+4)), int(r.u32(g+8))
		if r.err != nil || end-start > 0x110000 {
			return
		}
		for c := start; c <= end; c++ {
			out[rune(c)] = gid + (c - start) //nolint:gosec // c is below 0x110000
		}
	}
}

// readNames returns the family (name ID 1) and full name (name ID 4), preferring the Windows Unicode records.
func readNames(b []byte) (family, full string) {
	r := reader{b: b}
	count, strOff := int(r.u16(2)), int(r.u16(4))
	for i := 0; i < count; i++ {
		rec := 6 + 12*i
		platform, encoding, id := r.u16(rec), r.u16(rec+2), r.u16(rec+6)
		length, off := int(r.u16(rec+8)), int(r.u16(rec+10))
		if r.err != nil {
			return family, full
		}
		if platform != 3 || encoding != 1 || (id != 1 && id != 4) {
			continue
		}
		raw := r.slice(strOff+off, length)
		if r.err != nil {
			return family, full
		}
		runes := make([]rune, 0, len(raw)/2)
		for j := 0; j+1 < len(raw); j += 2 {
			runes = append(runes, rune(binary.BigEndian.Uint16(raw[j:])))
		}
		if id == 1 {
			family = string(runes)
		} else {
			full = string(runes)
		}
	}
	return family, full
}

// reader reads big-endian numbers and remembers the first out-of-range read.
type reader struct {
	b   []byte
	err error
}

func (r *reader) ok(off, n int) bool {
	if off < 0 || off+n > len(r.b) {
		if r.err == nil {
			r.err = errors.New("style: font file is truncated")
		}
		return false
	}
	return true
}

func (r *reader) u16(off int) uint16 {
	if !r.ok(off, 2) {
		return 0
	}
	return binary.BigEndian.Uint16(r.b[off:])
}

func (r *reader) i16(off int) int16 { return int16(r.u16(off)) } //nolint:gosec // a two-complement reinterpretation is the point

func (r *reader) u32(off int) uint32 {
	if !r.ok(off, 4) {
		return 0
	}
	return binary.BigEndian.Uint32(r.b[off:])
}

func (r *reader) slice(off, n int) []byte {
	if !r.ok(off, n) {
		return nil
	}
	return r.b[off : off+n]
}
