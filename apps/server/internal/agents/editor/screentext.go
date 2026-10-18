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

// IsChapter says whether the text is a numbered chapter title: a chapter_card, or a text (or another graphic) whose words are "1. ...".
// A chapter title is neither the video's title nor a label: the creator's chapter-titles setting governs it (chapter_titles).
func (t screenText) IsChapter() bool {
	if t.Graphic != nil && t.Graphic.Template == timeline.TemplateChapterCard {
		return true
	}
	return IsChapterTitle(t.Text)
}

// IsTitle says whether the text is the video's title (the hook or series title): a text item in a title_ style or a title_pop or blur_in
// graphic, that is not a chapter title.
func (t screenText) IsTitle() bool {
	if t.IsChapter() {
		return false
	}
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
