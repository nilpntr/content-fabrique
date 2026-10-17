package timeline

import (
	"math"
	"strings"
	"unicode/utf8"
)

// Rect is a pixel rectangle: Left and Top are inclusive, Right and Bottom are the edges just outside it.
type Rect struct {
	Left, Top, Right, Bottom int
}

func (r Rect) width() int  { return r.Right - r.Left }
func (r Rect) height() int { return r.Bottom - r.Top }

func (r Rect) contains(o Rect) bool {
	return o.Left >= r.Left && o.Right <= r.Right && o.Top >= r.Top && o.Bottom <= r.Bottom
}

func (r Rect) overlaps(o Rect) bool {
	return o.Left < r.Right && o.Right > r.Left && o.Top < r.Bottom && o.Bottom > r.Top
}

// SafeZone is where text may go in one format: inside Inner, and out of every rectangle in Blocked.
type SafeZone struct {
	Inner   Rect
	Blocked []Rect
}

// SafeZoneFor returns the safe zone of a format (design note section 5, D-25):
//
//   - 9:16 follows research 3.4: out of the top 240 px, the bottom 660 px and 120 px on each side, and out of the
//     right-hand action column (x > 900 for y > 840).
//   - 1:1 and 4:5 keep a 5 % margin on every side.
//   - 16:9 keeps a 10 % title-safe margin and the bottom 15 % clear.
//
// Percentages are of the side they cut, rounded half up to a pixel. ok is false for an unknown format.
func SafeZoneFor(f Format) (z SafeZone, ok bool) {
	w, h, ok := f.Size()
	if !ok {
		return SafeZone{}, false
	}
	switch f {
	case Format9x16:
		return SafeZone{
			Inner:   Rect{Left: 120, Top: 240, Right: w - 120, Bottom: h - 660},
			Blocked: []Rect{{Left: 900, Top: 840, Right: w, Bottom: h}},
		}, true
	case Format1x1, Format4x5:
		mx, my := percent(w, 5), percent(h, 5)
		return SafeZone{Inner: Rect{Left: mx, Top: my, Right: w - mx, Bottom: h - my}}, true
	default: // Format16x9
		mx, top, bottom := percent(w, 10), percent(h, 10), percent(h, 15)
		return SafeZone{Inner: Rect{Left: mx, Top: top, Right: w - mx, Bottom: h - bottom}}, true
	}
}

// percent is pct % of v, rounded half up.
func percent(v, pct int) int { return (v*pct + 50) / 100 }

// roundHalfUp is floor(x + 0.5).
func roundHalfUp(x float64) int { return int(math.Floor(x + 0.5)) }

// EstimateTextBox estimates the pixel box a text item takes when it is placed by position: the style's line height
// times the number of lines tall, and 0.6 em per character of the widest line wide (an em is the font size), capped
// at the width of the safe zone, centred on the point (x, y) of the output. P3-04 replaces the width with libass
// font metrics; the issue code stays the same. The three anchors are boxes inside the safe zone by construction, so
// they are not estimated.
func EstimateTextBox(text string, style Style, pos Point, outW, outH int, zone SafeZone) Rect {
	lines := strings.Split(text, "\n")
	widest := 0
	for _, l := range lines {
		widest = max(widest, utf8.RuneCountInString(l))
	}
	w := min(roundHalfUp(0.6*float64(style.FontSizePx)*float64(widest)), zone.Inner.width())
	h := style.LineHeightPx * len(lines)
	cx := roundHalfUp(pos.X * float64(outW))
	cy := roundHalfUp(pos.Y * float64(outH))
	left := cx - w/2
	top := cy - h/2
	return Rect{Left: left, Top: top, Right: left + w, Bottom: top + h}
}
