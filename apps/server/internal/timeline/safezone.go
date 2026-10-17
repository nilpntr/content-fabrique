package timeline

import (
	"fmt"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Rect is a pixel rectangle: Left and Top are inclusive, Right and Bottom are the edges just outside it.
type Rect = style.Rect

// SafeZone is where text may go in one format: inside Inner, and out of every rectangle in Blocked.
type SafeZone = style.Zone

// SafeZoneFor returns the safe zone of a format at the format's size (design note section 5, D-25), from the shared
// geometry in the style package:
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
	return style.ZoneFor(string(f), w, h)
}

// TextPlacement is where a text item goes: its style, the text laid out in it and the box it takes, padding included.
// Validate and the renderer both get it from PlaceText, so the box the validator checks is the box the renderer draws.
type TextPlacement struct {
	Style  style.Style
	Layout style.Layout
	Zone   SafeZone
	// Box is the box before any clamping: centred on the item's position, or the box of its anchor.
	Box Rect
}

// PlaceText lays a text item out in its style and places it, for an output. A text is wrapped at the width of the zone's
// centre column (for 9:16 the safe zone narrowed to keep clear of the action column), so centred text never meets it.
// It returns an error for an unknown style or format.
func PlaceText(out Output, it TextItem) (TextPlacement, error) {
	st, ok := style.Lookup(it.Style)
	if !ok {
		return TextPlacement{}, fmt.Errorf("unknown style %q", it.Style)
	}
	zone, ok := style.ZoneFor(string(out.Format), out.Width, out.Height)
	if !ok {
		return TextPlacement{}, fmt.Errorf("unknown format %q", out.Format)
	}
	lay := style.Measure(st, out.Height, it.Text, style.MaxBoxWidth(zone, out.Width))
	w, h := lay.Box()
	var box Rect
	if it.Position != nil {
		box = style.PlacePosition(out.Width, out.Height, it.Position.X, it.Position.Y, w, h)
	} else {
		box = style.PlaceAnchor(zone, it.Anchor, out.Width, out.Height, w, h)
	}
	return TextPlacement{Style: st, Layout: lay, Zone: zone, Box: box}, nil
}
