package style

import (
	"math"
	"slices"
)

// Supports reports whether the style's font has a glyph for r, so that the layout measured it: a character in the layout set
// that the font maps, a space or newline, or a zero-width character. A character outside that is drawn by whatever font libass
// falls back to, whose width the layout does not know, so Validate refuses it (unsupported_character) and the renderer drops
// it from transcript words. That is how the safe-zone check stays true for every text that is drawn; the fallback width in
// Measure (one em) is only a last line of defence.
func (s Style) Supports(r rune) bool {
	if r == '\n' || measurable(r) {
		return true
	}
	_, ok := tableOf(s.FontOf()).advance(r)
	return ok
}

// measurable reports whether r is zero-width and needs no glyph.
func measurable(r rune) bool {
	return r == 0x200B || r == 0x200C || r == 0x200D || r == 0x2060 || r == 0xFEFF || (r >= 0xFE00 && r <= 0xFE0F)
}

// FontTableOf returns the layout table of a style's font.
func FontTableOf(s Style) *FontTable { return tableOf(s.FontOf()) }

// Unsupported returns the distinct characters of text that the style's font cannot draw, in order of appearance.
func (s Style) Unsupported(text string) []rune {
	var out []rune
	for _, r := range text {
		if !s.Supports(r) && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// CaptionLines is how many lines of captions the caption band holds: the renderer re-chunks and shrinks a caption until it fits,
// so a caption never leaves the band.
const CaptionLines = 2

// CaptionBand is the rectangle captions are confined to: the zone's centre column, CaptionLines lines of the caption style
// tall, centred at the caption position. Text items stay out of it while captions are on (text_overlaps_captions).
func CaptionBand(z Zone, outW, shortSide int, st Style) Rect {
	size := st.FontSizePx(shortSide)
	h := CaptionLines*size + 2*st.Pad(size)
	col := z.Column(outW)
	cy := z.Inner.Top + int(math.Floor(captionCentre*float64(z.Inner.Height())+0.5))
	top := cy - h/2
	return Rect{Left: col.Left, Top: top, Right: col.Right, Bottom: top + h}
}
