package style

import (
	"math"
	"strings"
)

// Layout is a text laid out in a style: the lines after word wrap, their widths and the box they take. All sizes are pixels
// of the output (the spec's size, the ASS PlayRes).
type Layout struct {
	Style    Style
	FontSize int
	// Lines are the wrapped lines. Widths are their ink-box widths: the sum of the advance widths of their characters, rounded up.
	Lines  []string
	Widths []int
	// LineHeight is the height of one line (libass makes it equal to the font size) and Pad how far the ink reaches beyond the
	// line boxes on every side.
	LineHeight, Pad int
}

// Box returns the size of the box the text takes, padding included.
func (l Layout) Box() (w, h int) {
	widest := 0
	for _, lw := range l.Widths {
		widest = max(widest, lw)
	}
	return widest + 2*l.Pad, len(l.Lines)*l.LineHeight + 2*l.Pad
}

// Measure lays text out in a style at an output height: it wraps each explicit line (the text's "\n") at maxBoxWidth, the
// widest box the text may take, padding included, and measures what is left. A word wider than a line is broken between
// characters, the rest continuing on the same line as the next word. A text is never wider than maxBoxWidth unless a single
// character is.
func Measure(st Style, outH int, text string, maxBoxWidth int) Layout {
	size := st.FontSizePx(outH)
	l := Layout{Style: st, FontSize: size, LineHeight: size, Pad: st.Pad(size)}
	maxW := float64(max(maxBoxWidth-2*l.Pad, 1))
	m := measurer{table: tableOf(st.FontOf()), size: size}
	for _, explicit := range strings.Split(text, "\n") {
		for _, line := range m.wrap(explicit, maxW) {
			l.Lines = append(l.Lines, line)
			l.Widths = append(l.Widths, int(math.Ceil(m.width(line))))
		}
	}
	return l
}

// measurer measures strings in one font at one size.
type measurer struct {
	table *FontTable
	size  int
}

// tableOf returns the generated layout table of a font. It panics when there is none: a test checks every registry font.
func tableOf(f Font) *FontTable {
	t, ok := generatedTables[f.File]
	if !ok {
		panic("style: no layout table for font " + f.File + "; run go generate ./internal/timeline/style")
	}
	return t
}

// px is the width in pixels of r.
func (m measurer) px(r rune) float64 {
	perUnit := float64(m.size) / float64(m.table.WinAscent+m.table.WinDescent)
	switch {
	case r < 0x20, r == 0x200B, r == 0x200C, r == 0x200D, r == 0x2060, r == 0xFEFF, r >= 0xFE00 && r <= 0xFE0F:
		return 0
	}
	if adv, ok := m.table.advance(r); ok {
		return float64(adv) * perUnit
	}
	return float64(m.table.UnitsPerEm) * perUnit // one em
}

func (m measurer) width(s string) float64 {
	w := 0.0
	for _, r := range s {
		w += m.px(r)
	}
	return w
}

// wrap breaks one explicit line into lines no wider than maxW.
func (m measurer) wrap(line string, maxW float64) []string {
	var out []string
	var cur []rune
	curW := 0.0
	flush := func() {
		out = append(out, strings.TrimRight(string(cur), " "))
		cur, curW = nil, 0
	}
	space := m.px(' ')
	for _, word := range strings.Split(line, " ") {
		if len(cur) > 0 {
			ww := m.width(word)
			first := space // room the word needs on this line to start here: a space and, for a long word, its first character
			if ww > maxW {
				first += m.px([]rune(word + " ")[0])
			} else {
				first += ww
			}
			if curW+first <= maxW {
				cur = append(cur, ' ')
				curW += space
			} else {
				flush()
			}
		}
		for _, r := range word {
			rw := m.px(r)
			if curW+rw > maxW && len(cur) > 0 {
				flush()
			}
			cur = append(cur, r)
			curW += rw
		}
	}
	if len(cur) > 0 || len(out) == 0 {
		flush()
	}
	return out
}

// Anchors of a text item.
const (
	AnchorUpperThird = "upper_third"
	AnchorCenter     = "center"
	AnchorLowerThird = "lower_third"
)

// PlaceAnchor returns the box of a w x h text at an anchor: centred on the vertical centre line, with its top on the top of
// the safe zone (upper_third), its bottom on the bottom of the safe zone (lower_third) or centred on the middle of the
// output (center, moved the shortest way into the zone when a tall text would leave it). It is inside the zone whenever the
// text fits the zone.
func PlaceAnchor(z Zone, anchor string, outW, outH, w, h int) Rect {
	left := (outW - w) / 2
	top := (outH - h) / 2
	switch anchor {
	case AnchorUpperThird:
		top = z.Inner.Top
	case AnchorLowerThird:
		top = z.Inner.Bottom - h
	default:
		if h <= z.Inner.Height() {
			top = max(z.Inner.Top, min(top, z.Inner.Bottom-h))
		}
	}
	return Rect{Left: left, Top: top, Right: left + w, Bottom: top + h}
}

// PlacePosition returns the box of a w x h text centred on the point (x, y) of the output, both in [0, 1], the point rounded
// half up to a pixel. It is not clamped: the validator reports a box that is not safe, and the renderer clamps it with Fit.
func PlacePosition(outW, outH int, x, y float64, w, h int) Rect {
	cx := int(math.Floor(x*float64(outW) + 0.5))
	cy := int(math.Floor(y*float64(outH) + 0.5))
	left, top := cx-w/2, cy-h/2
	return Rect{Left: left, Top: top, Right: left + w, Bottom: top + h}
}

// captionCentre is where the centre of a caption box sits: this share of the way down the safe zone.
const captionCentre = 0.62

// PlaceCaption returns the box of a w x h caption: centred on the vertical centre line at 62 % of the safe zone's height, above
// the lower third where titles sit.
func PlaceCaption(z Zone, outW, w, h int) Rect {
	cy := z.Inner.Top + int(math.Floor(captionCentre*float64(z.Inner.Height())+0.5))
	left, top := (outW-w)/2, cy-h/2
	return Rect{Left: left, Top: top, Right: left + w, Bottom: top + h}
}

// PlaceWatermark returns the box of a w x h watermark: in the top left corner of the safe zone.
func PlaceWatermark(z Zone, w, h int) Rect {
	return Rect{Left: z.Inner.Left, Top: z.Inner.Top, Right: z.Inner.Left + w, Bottom: z.Inner.Top + h}
}

// MaxBoxWidth is the widest box a text may take in a zone: the width of the zone's centre column.
func MaxBoxWidth(z Zone, outW int) int { return z.Column(outW).Width() }
