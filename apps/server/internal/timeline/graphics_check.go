package timeline

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// checkGraphics checks the graphics items: the params against the template (invalid_graphic), the style (unknown_style,
// invalid_text_style), the time (outside_timeline), the characters (unsupported_character) and the box, laid out as the item's text
// in its style like a text item (unsafe_text, text_overlaps_captions). The box is the one the renderer gives the template.
func (c *checker) checkGraphics() {
	band, captionsOn := c.captionBand()
	for ti, t := range c.spec.Tracks {
		gt, ok := t.(*GraphicsTrack)
		if !ok {
			continue
		}
		if len(gt.Items) > MaxGraphicsItems {
			c.add(CodeInvalidGraphic, fmt.Sprintf("/tracks/%d/items", ti),
				"the graphics track has %d items, at most %d are allowed; remove %d", len(gt.Items), MaxGraphicsItems, len(gt.Items)-MaxGraphicsItems)
			continue
		}
		for i, it := range gt.Items {
			path := fmt.Sprintf("/tracks/%d/items/%d", ti, i)
			if field, err := it.CheckParams(); err != nil {
				c.add(CodeInvalidGraphic, path+field, "graphics item %s (%s): %v", it.ID, it.Template, err)
				continue
			}
			if c.hasTimeline() {
				if end := it.EndMs(); end > c.total {
					c.add(CodeOutsideTimeline, path,
						"graphics item %s runs from %d to %d ms but the timeline is only %d ms long; shorten it by %d ms or start it earlier",
						it.ID, it.AtMs, end, c.total, end-c.total)
				}
			}
			txt := it.AsText()
			st, ok := style.Lookup(txt.Style)
			if !ok {
				c.add(CodeUnknownStyle, path+"/params/style",
					"graphics item %s uses style %q, which is not in the style registry; use one of: %s", it.ID, txt.Style, strings.Join(style.IDs(), ", "))
				continue
			}
			bad := false
			for _, f := range []struct {
				field string
				err   error
			}{
				{"fill", style.Overrides{Fill: txt.Fill}.Validate()},
				{"scale", style.Overrides{Scale: txt.Scale}.Validate()},
				{"shadow", style.Overrides{Shadow: txt.Shadow}.Validate()},
				{"align", alignErr(txt.Align)},
			} {
				if f.err != nil {
					c.add(CodeInvalidTextStyle, path+"/params/"+f.field, "graphics item %s: %v", it.ID, f.err)
					bad = true
				}
			}
			if bad {
				continue
			}
			if miss := st.Unsupported(txt.Text); len(miss) > 0 {
				c.add(CodeUnsupportedCharacter, path+"/params/text",
					"graphics item %s has characters the font of style %s has no glyph for (%s), so they cannot be drawn or measured; use plain Latin or Cyrillic letters, digits and common punctuation",
					it.ID, txt.Style, quoteRunes(miss))
				continue
			}
			c.checkTextBox(txt, path, band, captionsOn)
		}
	}
}
