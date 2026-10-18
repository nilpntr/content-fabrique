package style

import (
	"math"
	"strings"
	"unicode"
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

// Measure lays text out in a style on an output whose shorter side is shortSide px: it wraps each explicit line (the text's "\n") at maxBoxWidth, the
// widest box the text may take, padding included, and measures what is left. A word wider than a line is broken between
// characters, the rest continuing on the same line as the next word. A text is never wider than maxBoxWidth unless a single
// character is.
func Measure(st Style, shortSide int, text string, maxBoxWidth int) Layout {
	size := st.FontSizePx(shortSide)
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

// widthSlack widens every measured width by half a percent: libass positions glyphs with hinted and rounded advances, which the
// plain advance widths do not model, and a long line of wide capitals ends up a pixel or two wider than the sum. The ink tests
// fail if the slack is too small.
const widthSlack = 1.005

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
	perUnit := widthSlack * float64(m.size) / float64(m.table.WinAscent+m.table.WinDescent)
	switch {
	case r < 0x20, measurable(r):
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

// PlaceWatermark returns the box of a w x h watermark: in the top right corner of the safe zone, clear of the centred titles and
// of the action column (which starts lower).
func PlaceWatermark(z Zone, w, h int) Rect {
	return Rect{Left: z.Inner.Right - w, Top: z.Inner.Top, Right: z.Inner.Right, Bottom: z.Inner.Top + h}
}

// MaxBoxWidth is the widest box a text may take in a zone: the width of the zone's centre column.
func MaxBoxWidth(z Zone, outW int) int { return z.Column(outW).Width() }

// inkAscender is how far above the capitals the ink of plain Latin text may rise (an ascender, a lowercase "f"), as a share of the cap
// height; inkAccent is the same for text with accented capitals.
const (
	inkAscender = 0.08
	inkAccent   = 0.30
)

// hasDescender says whether a character of the line hangs below the baseline: a descender, or an accented letter (a cedilla, an ogonek).
// Punctuation outside Basic Latin (a curly apostrophe, a dash) does not.
func hasDescender(line string) bool {
	if strings.ContainsAny(line, "gjpqyQ,;()[]{}/|@_$") {
		return true
	}
	for _, r := range line {
		if r > 0x7F && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// hasAccent says whether the line has an accented letter, whose mark rises above the capitals.
func hasAccent(line string) bool {
	for _, r := range line {
		if r > 0x7F && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// InkBox is where the ink of the text can be inside box (the layout box placed on the output, padding included): the same width, and
// vertically from just above the capitals of the first line to the baseline of the last, with room below it only when a character hangs
// below the baseline. A line box is far taller than the capitals in it (Anton's is about twice as tall), so for a title in capitals the
// ink box is about half the layout box: the part of the box a head can be in without touching a letter is the empty space of the line.
// The layout box stays the one that must be inside the safe zone; this is what a head or a body is tested against. libass puts the
// baseline of a line WinAscent font units under its top; a test renders every style and checks the ink against this box.
func (l Layout) InkBox(box Rect) Rect {
	t := tableOf(l.Style.FontOf())
	total := t.WinAscent + t.WinDescent
	if l.Style.Box || total == 0 || len(l.Lines) == 0 {
		return box // a boxed style draws the box itself: the ink is all of the layout box
	}
	k := float64(l.LineHeight) / float64(total)
	capH := float64(t.CapHeight) * k
	if t.CapHeight == 0 {
		capH = 0.7 * float64(t.UnitsPerEm) * k
	}
	up := inkAscender
	if hasAccent(l.Lines[0]) { // only the first line sets the top of the ink
		up = inkAccent
	}
	lineTop := float64(box.Top + l.Pad)
	first := lineTop + float64(t.WinAscent)*k
	last := first + float64(len(l.Lines)-1)*float64(l.LineHeight)
	top := int(math.Floor(first-capH*(1+up))) - l.Pad
	bottom := int(math.Ceil(last)) + l.Pad
	if hasDescender(l.Lines[len(l.Lines)-1]) { // and only the last the bottom
		bottom = int(math.Ceil(last+float64(t.WinDescent)*k)) + l.Pad
	}
	out := Rect{Left: box.Left, Top: max(top, box.Top), Right: box.Right, Bottom: min(bottom, box.Bottom)}
	if out.Bottom <= out.Top {
		return box
	}
	return out
}
