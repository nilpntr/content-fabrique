package editor

import (
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// screenText is a text the edit puts on screen: a text item, or a graphics item (D-35) as the text it draws (GraphicsItem.AsText: its
// words, style, look and place). The checks about texts and titles read these, so a hook title made a title_pop is still the hook title.
type screenText struct {
	timeline.TextItem
	// Graphic is the graphics item it comes from, nil for a text item.
	Graphic *timeline.GraphicsItem
}

// IsTitle says whether the text is a title: a text item in a title_ style, or a title graphic (title_pop, blur_in, chapter_card).
func (t screenText) IsTitle() bool {
	if t.Graphic != nil {
		return t.Graphic.IsTitle()
	}
	return strings.HasPrefix(t.Style, "title_")
}

// Kind says what it is, for a message: "text" or the graphic's template.
func (t screenText) Kind() string {
	if t.Graphic != nil {
		return t.Graphic.Template
	}
	return "text"
}

// screenTexts are the texts of a spec: the text items, then the graphics items, each track in its order.
func screenTexts(spec *timeline.Spec) []screenText {
	var texts, graphics []screenText
	for _, t := range spec.Tracks {
		switch t := t.(type) {
		case *timeline.TextTrack:
			for _, it := range t.Items {
				texts = append(texts, screenText{TextItem: it})
			}
		case *timeline.GraphicsTrack:
			for i := range t.Items {
				g := t.Items[i]
				graphics = append(graphics, screenText{TextItem: g.AsText(), Graphic: &g})
			}
		}
	}
	return append(texts, graphics...)
}
