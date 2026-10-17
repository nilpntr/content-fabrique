package timeline

import "slices"

// Style is what validation needs to know about a text style: how big its text is, to estimate the box a text item
// takes. This is the initial stub of the style registry (design note section 5). P3-04 replaces it with the real
// registry in render/styles (font file, size, colours, outline, animation defaults, libass metrics) and keeps
// LookupStyle's contract, so the validator does not change.
type Style struct {
	ID string
	// FontSizePx is the font size in pixels at the output's own resolution (1080 px wide for the vertical formats).
	FontSizePx int
	// LineHeightPx is the height of one line of text, in pixels.
	LineHeightPx int
}

// styleRegistry is the stub's content: the styles the fixtures and the first prompts use.
var styleRegistry = map[string]Style{
	"title_bold":    {ID: "title_bold", FontSizePx: 72, LineHeightPx: 86},
	"caption_plain": {ID: "caption_plain", FontSizePx: 56, LineHeightPx: 67},
}

// LookupStyle returns the style with the given id, and false if the registry does not know it.
func LookupStyle(id string) (Style, bool) {
	s, ok := styleRegistry[id]
	return s, ok
}

// StyleIDs returns the ids of the known styles in alphabetical order, for messages that tell the agent what it may
// use.
func StyleIDs() []string {
	ids := make([]string, 0, len(styleRegistry))
	for id := range styleRegistry {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
