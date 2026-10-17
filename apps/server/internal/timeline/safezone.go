package timeline

import (
	"fmt"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// TextPlacement is where a text item goes: its style, the text laid out in it and the box it takes, padding included.
// Validate and the renderer both get it from PlaceText, so the box the validator checks is the box the renderer draws.
type TextPlacement struct {
	Style  style.Style
	Layout style.Layout
	Zone   style.Zone
	// Box is the box before any clamping: centred on the item's position, or the box of its anchor.
	Box style.Rect
}

// ShortSide is the shorter side of an output, which text sizes scale with (1080 px on every format).
func (o Output) ShortSide() int { return min(o.Width, o.Height) }

// Zone returns the safe zone of the output's format at its size (design note section 5, D-25; the geometry is in the style
// package, shared with the renderer). ok is false for an unknown format.
func (o Output) Zone() (style.Zone, bool) {
	return style.ZoneFor(string(o.Format), o.Width, o.Height)
}

// PlaceText lays a text item out in its style and places it, for an output. A text is wrapped at the width of the zone's
// centre column (for 9:16 the safe zone narrowed to keep clear of the action column), so centred text never meets it.
// It returns an error for an unknown style or format.
func PlaceText(out Output, it TextItem) (TextPlacement, error) {
	st, ok := style.Lookup(it.Style)
	if !ok {
		return TextPlacement{}, fmt.Errorf("unknown style %q", it.Style)
	}
	zone, ok := out.Zone()
	if !ok {
		return TextPlacement{}, fmt.Errorf("unknown format %q", out.Format)
	}
	lay := style.Measure(st, out.ShortSide(), it.Text, style.MaxBoxWidth(zone, out.Width))
	w, h := lay.Box()
	var box style.Rect
	if it.Position != nil {
		box = style.PlacePosition(out.Width, out.Height, it.Position.X, it.Position.Y, w, h)
	} else {
		box = style.PlaceAnchor(zone, it.Anchor, out.Width, out.Height, w, h)
	}
	return TextPlacement{Style: st, Layout: lay, Zone: zone, Box: box}, nil
}
