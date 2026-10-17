// Package style is the one place that knows what text looks like and where it may go: the style registry (font file, size,
// colours, outline, box, default animation), the safe zones of the four formats (D-25) and the text layout (real font
// advance widths, word wrap) that both timeline.Validate and the renderer use. The validator measures a text item with the
// same functions the renderer places it with, so "inside the safe zone" means the same thing in both. The spec never names a
// font: a style id picks one of the OFL-licensed fonts vendored in packages/media-tools/fonts (D-16).
package style

import (
	"fmt"
	"math"
	"slices"
)

// Font is one vendored font file.
type Font struct {
	ID string
	// File is the file name in packages/media-tools/fonts.
	File string
	// Family is the font name that the ASS Fontname field uses to select this file, and Bold the ASS Bold flag that goes with
	// it (a static font file with a family name that already says its weight, such as "Montserrat ExtraBold", needs Bold off;
	// Inter Bold is the "Inter" family in bold).
	Family string
	Bold   bool
}

// fonts is the allow-list: the only fonts a render can use.
var fonts = []Font{
	{ID: "anton", File: "Anton-Regular.ttf", Family: "Anton"},
	{ID: "inter-bold", File: "Inter-Bold.ttf", Family: "Inter", Bold: true},
	{ID: "montserrat-extrabold", File: "Montserrat-ExtraBold.ttf", Family: "Montserrat ExtraBold"},
}

// Fonts returns the allow-listed fonts.
func Fonts() []Font { return slices.Clone(fonts) }

// FontFiles returns the file names of the allow-listed fonts, in registry order.
func FontFiles() []string {
	out := make([]string, len(fonts))
	for i, f := range fonts {
		out[i] = f.File
	}
	return out
}

// LookupFont returns the font with the given id.
func LookupFont(id string) (Font, bool) {
	for _, f := range fonts {
		if f.ID == id {
			return f, true
		}
	}
	return Font{}, false
}

// Colour is an sRGB colour with an opacity (255 is opaque).
type Colour struct {
	R, G, B, A uint8
}

// Opaque is a fully opaque colour.
func Opaque(r, g, b uint8) Colour { return Colour{r, g, b, 255} }

// ASS formats the colour for an ASS style or override tag: &HAABBGGRR, where AA is the transparency (00 is opaque).
func (c Colour) ASS() string {
	return fmt.Sprintf("&H%02X%02X%02X%02X", 255-c.A, c.B, c.G, c.R)
}

// Role says what a style is for.
type Role int

// The roles. Only RoleText and RoleCaption styles are visible to the spec and the agent; the watermark is the renderer's.
const (
	RoleText Role = iota
	RoleCaption
	RoleWatermark
)

// The text animations (the spec's enum).
const (
	AnimNone       = "none"
	AnimPopIn      = "pop_in"
	AnimFade       = "fade"
	AnimTypewriter = "typewriter"
	AnimSlideUp    = "slide_up"
)

// Style is one entry of the style registry. Sizes are relative to the output height, so one style looks the same on every
// format: SizePct is a percentage of the height, and OutlinePct and ShadowPct are percentages of the font size.
type Style struct {
	ID string
	// Description says what the style is for, in the words the editor agent reads.
	Description string
	Font        string // id of a Font
	SizePct     float64
	Fill        Colour
	// Highlight is the colour of the spoken word in word-highlight captions; the zero Colour means the style has none.
	Highlight  Colour
	Outline    Colour
	Shadow     Colour
	OutlinePct float64
	ShadowPct  float64
	// Box draws the Outline colour as an opaque box behind each line (ASS border style 3) instead of an outline; OutlinePct is
	// then the box's padding.
	Box bool
	// Animation is the default animation of a text item that does not name one.
	Animation string
	Role      Role
}

var white = Opaque(255, 255, 255)
var yellow = Opaque(0xFF, 0xD6, 0x0A)

// registry is the style registry. Order is alphabetical by id, which is the order IDs returns.
var registry = []Style{
	{
		ID: "caption_highlight", Description: "Captions that light up the word being spoken in yellow (the word_highlight caption style); bold white with a black outline.",
		Font: "montserrat-extrabold", SizePct: 3.4, Fill: white, Highlight: yellow, Outline: Opaque(0, 0, 0), Shadow: Colour{0, 0, 0, 140},
		OutlinePct: 8, ShadowPct: 2, Animation: AnimNone, Role: RoleCaption,
	},
	{
		ID: "caption_plain", Description: "Plain captions: bold white with a black outline, no highlight (the chunk caption style); also fine for short on-screen text.",
		Font: "montserrat-extrabold", SizePct: 3.4, Fill: white, Outline: Opaque(0, 0, 0), Shadow: Colour{0, 0, 0, 140},
		OutlinePct: 8, ShadowPct: 2, Animation: AnimNone, Role: RoleCaption,
	},
	{
		ID: "label_box", Description: "A small label on a dark box, for names, exercise names and tags.",
		Font: "inter-bold", SizePct: 2.7, Fill: white, Outline: Colour{0, 0, 0, 205},
		OutlinePct: 24, Box: true, Animation: AnimFade, Role: RoleText,
	},
	{
		ID: "stat_big", Description: "A huge yellow number or short word, for a stat or a rep count; keep it to a few characters.",
		Font: "anton", SizePct: 9.5, Fill: yellow, Outline: Opaque(0, 0, 0), Shadow: Colour{0, 0, 0, 140},
		OutlinePct: 6, ShadowPct: 3, Animation: AnimPopIn, Role: RoleText,
	},
	{
		ID: "subtitle_clean", Description: "Small clean text with a soft shadow, for a second line of explanation under a title.",
		Font: "inter-bold", SizePct: 2.4, Fill: white, Outline: Colour{0, 0, 0, 160}, Shadow: Colour{0, 0, 0, 120},
		OutlinePct: 4, ShadowPct: 5, Animation: AnimFade, Role: RoleText,
	},
	{
		ID: "title_bold", Description: "The default title: bold white with a black outline; for hooks and headlines.",
		Font: "montserrat-extrabold", SizePct: 3.75, Fill: white, Outline: Opaque(0, 0, 0), Shadow: Colour{0, 0, 0, 140},
		OutlinePct: 7, ShadowPct: 3, Animation: AnimPopIn, Role: RoleText,
	},
	{
		ID: "title_outline", Description: "A tall condensed title with a thick black outline; for punchy one-line hooks.",
		Font: "anton", SizePct: 5.2, Fill: white, Outline: Opaque(0, 0, 0),
		OutlinePct: 10, Animation: AnimFade, Role: RoleText,
	},
}

// watermark is the renderer's own style for the PREVIEW mark.
var watermark = Style{
	ID: "watermark", Description: "The preview watermark.",
	Font: "inter-bold", SizePct: 1.7, Fill: Colour{255, 255, 255, 170}, Outline: Colour{0, 0, 0, 120},
	OutlinePct: 6, Animation: AnimNone, Role: RoleWatermark,
}

// Lookup returns the style with the given id. The preview watermark is not a style the spec can use.
func Lookup(id string) (Style, bool) {
	for _, s := range registry {
		if s.ID == id {
			return s, true
		}
	}
	return Style{}, false
}

// IDs returns the ids of the styles a text item may use, in alphabetical order, for messages that tell the agent what it may use.
func IDs() []string {
	out := make([]string, len(registry))
	for i, s := range registry {
		out[i] = s.ID
	}
	return out
}

// All returns the styles a text item may use, in alphabetical order of id.
func All() []Style { return slices.Clone(registry) }

// Watermark returns the style of the preview watermark.
func Watermark() Style { return watermark }

// The caption kinds of the spec's captions track.
const (
	CaptionChunk         = "chunk"
	CaptionWordHighlight = "word_highlight"
)

// CaptionStyle returns the style of a captions track: caption_plain for chunk and caption_highlight for word_highlight.
func CaptionStyle(kind string) (Style, bool) {
	switch kind {
	case CaptionChunk:
		return Lookup("caption_plain")
	case CaptionWordHighlight:
		return Lookup("caption_highlight")
	}
	return Style{}, false
}

// FontOf returns the style's font.
func (s Style) FontOf() Font {
	f, ok := LookupFont(s.Font)
	if !ok {
		panic("style: style " + s.ID + " names the unknown font " + s.Font) // the registry is a literal: a test covers it
	}
	return f
}

// FontSizePx is the font size in pixels at an output height: the percentage of the height, rounded half up, at least 8.
func (s Style) FontSizePx(outH int) int {
	return max(8, int(math.Floor(s.SizePct/100*float64(outH)+0.5)))
}

// OutlinePx is the outline thickness (or the box padding) in pixels for a font size.
func (s Style) OutlinePx(sizePx int) int {
	return int(math.Floor(s.OutlinePct/100*float64(sizePx) + 0.5))
}

// ShadowPx is the shadow offset in pixels for a font size.
func (s Style) ShadowPx(sizePx int) int {
	return int(math.Floor(s.ShadowPct/100*float64(sizePx) + 0.5))
}

// Pad is how far the style's ink reaches beyond the text's line boxes: the outline (or box padding) plus the shadow offset,
// plus one pixel for anti-aliasing. The layout box includes it, so a box inside the safe zone has all its ink inside.
func (s Style) Pad(sizePx int) int {
	return s.OutlinePx(sizePx) + s.ShadowPx(sizePx) + 1
}
