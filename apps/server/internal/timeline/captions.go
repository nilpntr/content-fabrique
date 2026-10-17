package timeline

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// captionBand returns the band an enabled captions track draws in, and whether there is one. It also checks that the band
// itself is inside the safe zone (unsafe_text on the captions track): the renderer re-chunks and shrinks every caption until it
// fits the band, so the band being safe is what keeps every caption safe.
func (c *checker) captionBand() (style.Rect, bool) {
	out := c.spec.Output
	zone, ok := out.Zone()
	if !ok {
		return style.Rect{}, false
	}
	for ti, t := range c.spec.Tracks {
		tr, ok := t.(*CaptionsTrack)
		if !ok || !tr.Enabled {
			continue
		}
		st, ok := style.CaptionStyle(tr.Style)
		if !ok {
			continue
		}
		band := style.CaptionBand(zone, out.Width, out.ShortSide(), st)
		if !zone.Safe(band) {
			c.add(CodeUnsafeText, fmt.Sprintf("/tracks/%d", ti),
				"the caption band of the %s captions (%d lines of %s, x %d to %d, y %d to %d px) is not inside the %s safe zone",
				tr.Style, style.CaptionLines, st.ID, band.Left, band.Right, band.Top, band.Bottom, out.Format)
		}
		return band, true
	}
	return style.Rect{}, false
}

// quoteRunes lists characters for a message, with their code points.
func quoteRunes(rs []rune) string {
	parts := make([]string, 0, len(rs))
	for i, r := range rs {
		if i == 8 {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, fmt.Sprintf("%q U+%04X", r, r))
	}
	return strings.Join(parts, ", ")
}
