package timeline

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// The graphics track (D-35, docs/design/motion-graphics-v1.md): motion-graphics overlays. An item names a template of the library
// (internal/motion renders it with headless Chrome) and gives it params. The timeline package knows the templates' names and params
// and how each item reads as plain text (AsText): that text, in the item's style, is the item's layout box (so a graphics item is
// placed, kept in the safe zone and kept off the subject exactly like a text item), and it is also the ASS fallback the renderer
// draws when motion graphics are off.

// TrackGraphics is the type of the graphics track.
const TrackGraphics TrackType = "graphics"

// The templates of the library, in the order the schema lists them.
const (
	TemplateTitlePop      = "title_pop"
	TemplateBlurIn        = "blur_in"
	TemplateChapterCard   = "chapter_card"
	TemplateHighlightWord = "highlight_word"
	TemplateCounter       = "counter"
	TemplateLowerLabel    = "lower_label"
	TemplateOutroCard     = "outro_card"
)

// GraphicsTemplates lists the template names.
func GraphicsTemplates() []string {
	return []string{TemplateTitlePop, TemplateBlurIn, TemplateChapterCard, TemplateHighlightWord, TemplateCounter, TemplateLowerLabel, TemplateOutroCard}
}

// The energies of a graphics item.
const (
	EnergyCalm   = "calm"
	EnergyLively = "lively"
	EnergyHype   = "hype"
)

// Limits of a graphics item.
const (
	MinGraphicsMs         int64 = 500
	MaxGraphicsChars            = 60
	MaxGraphicsLines            = 2
	MaxGraphicsAccents          = 16
	MaxHighlightWord            = 12
	MaxCounterAbs               = 999999.0
	DefaultAccentColour         = "#FFD60A"
	DefaultGraphicsEnergy       = EnergyLively
)

// GraphicsTrack holds the motion-graphics overlays.
type GraphicsTrack struct {
	ID    string         `json:"id"`
	Items []GraphicsItem `json:"items"`
}

// TrackID implements Track.
func (t *GraphicsTrack) TrackID() string { return t.ID }

// TrackType implements Track.
func (*GraphicsTrack) TrackType() TrackType { return TrackGraphics }

// MarshalJSON writes the track with its "type".
func (t GraphicsTrack) MarshalJSON() ([]byte, error) {
	type plain GraphicsTrack
	if t.Items == nil {
		t.Items = []GraphicsItem{}
	}
	return withTag("type", string(TrackGraphics), plain(t))
}

// GraphicsItem is one overlay: a template, its time, its place and its params.
type GraphicsItem struct {
	ID           string         `json:"id"`
	Template     string         `json:"template"`
	AtMs         int64          `json:"at_ms"`
	DurationMs   int64          `json:"duration_ms"`
	Anchor       string         `json:"anchor,omitempty"`
	Position     *Point         `json:"position,omitempty"`
	AvoidSubject *bool          `json:"avoid_subject,omitempty"`
	Params       GraphicsParams `json:"params"`
}

// GraphicsParams are the params of every template; the schema says which a template allows (and Validate checks it again for a
// spec built in Go). Optional fields are pointers or omitempty, so a spec round-trips unchanged.
type GraphicsParams struct {
	Text      string   `json:"text"`
	Style     string   `json:"style,omitempty"`
	Fill      string   `json:"fill,omitempty"`
	Accent    string   `json:"accent,omitempty"`
	Scale     *float64 `json:"scale,omitempty"`
	Align     string   `json:"align,omitempty"`
	Shadow    string   `json:"shadow,omitempty"`
	Backing   string   `json:"backing,omitempty"`
	Energy    string   `json:"energy,omitempty"`
	AccentsMs []int64  `json:"accents_ms,omitempty"`
	// chapter_card
	Number *int   `json:"number,omitempty"`
	Layout string `json:"layout,omitempty"`
	// highlight_word: the word that gets the marker, from 1.
	Word *int `json:"word,omitempty"`
	// counter
	From     *float64 `json:"from,omitempty"`
	To       *float64 `json:"to,omitempty"`
	Decimals *int     `json:"decimals,omitempty"`
}

// EndMs is when the item leaves the screen.
func (g GraphicsItem) EndMs() int64 { return g.AtMs + g.DurationMs }

// AvoidsSubject says whether the renderer keeps the item off the subject (the default).
func (g GraphicsItem) AvoidsSubject() bool { return g.AvoidSubject == nil || *g.AvoidSubject }

// EnergyOrDefault is the item's energy, lively when left out.
func (p GraphicsParams) EnergyOrDefault() string {
	if p.Energy == "" {
		return DefaultGraphicsEnergy
	}
	return p.Energy
}

// AccentOrDefault is the accent colour, #FFD60A when left out.
func (p GraphicsParams) AccentOrDefault() string {
	if p.Accent == "" {
		return DefaultAccentColour
	}
	return p.Accent
}

// DecimalsOrDefault is the counter's decimals, 0 when left out.
func (p GraphicsParams) DecimalsOrDefault() int {
	if p.Decimals == nil {
		return 0
	}
	return *p.Decimals
}

// TwoLine says whether a chapter card puts its number on a line of its own.
func (p GraphicsParams) TwoLine() bool { return p.Layout == "two_line" }

// GraphicsDefaultStyle is the style a template uses when the item names none.
func GraphicsDefaultStyle(template string) string {
	switch template {
	case TemplateLowerLabel:
		return "label_box"
	case TemplateCounter:
		return "stat_big"
	}
	return "title_bold"
}

// StyleOrDefault is the item's style id, or its template's default.
func (g GraphicsItem) StyleOrDefault() string {
	if g.Params.Style != "" {
		return g.Params.Style
	}
	return GraphicsDefaultStyle(g.Template)
}

// FallbackAnimation is the ASS animation that stands in for a template when motion graphics are off.
func FallbackAnimation(template string) string {
	switch template {
	case TemplateTitlePop, TemplateCounter:
		return style.AnimPop
	case TemplateChapterCard, TemplateLowerLabel:
		return style.AnimSlideUp
	}
	return style.AnimFade
}

// FallbackExit is the ASS exit that stands in for a template's exit when motion graphics are off.
func FallbackExit(template string) string {
	switch template {
	case TemplateTitlePop, TemplateCounter, TemplateOutroCard:
		return style.ExitPopOut
	case TemplateChapterCard, TemplateLowerLabel:
		return style.ExitSlideDown
	}
	return style.ExitFade
}

// FormatCount writes a counter value with the item's decimals.
func FormatCount(v float64, decimals int) string {
	return strconv.FormatFloat(v, 'f', max(0, min(2, decimals)), 64)
}

// NumberText is the chapter card's number as it is drawn: "1.".
func (p GraphicsParams) NumberText() string {
	if p.Number == nil {
		return ""
	}
	return strconv.Itoa(*p.Number) + "."
}

// CounterSlot is the widest of the counter's two values as written, which is the room the digits get.
func (p GraphicsParams) CounterSlot() string {
	if p.From == nil || p.To == nil {
		return ""
	}
	a, b := FormatCount(*p.From, p.DecimalsOrDefault()), FormatCount(*p.To, p.DecimalsOrDefault())
	if len(b) >= len(a) {
		return b
	}
	return a
}

// DisplayText is the text the item shows once it has entered: the chapter number and title, the counter's final value and label,
// or the text.
func (g GraphicsItem) DisplayText() string {
	p := g.Params
	switch g.Template {
	case TemplateChapterCard:
		if p.TwoLine() {
			return p.NumberText() + "\n" + p.Text
		}
		return p.NumberText() + " " + p.Text
	case TemplateCounter:
		return p.CounterSlot() + " " + p.Text
	}
	return p.Text
}

// AsText is the item as a text item: its text (DisplayText, with the counter's widest value), style, overrides, place and avoidance,
// and the ASS animation that stands in for the template. Its layout box is the overlay's box, and it is what the renderer draws when
// motion graphics are off.
func (g GraphicsItem) AsText() TextItem {
	p := g.Params
	t := TextItem{
		ID: g.ID, AtMs: g.AtMs, DurationMs: g.DurationMs, Text: g.DisplayText(), Style: g.StyleOrDefault(),
		Anchor: g.Anchor, Position: g.Position, Animation: FallbackAnimation(g.Template), Exit: FallbackExit(g.Template),
		Fill: p.Fill, Scale: p.Scale, Shadow: p.Shadow, Align: p.Align, AvoidSubject: g.AvoidSubject,
	}
	return t
}

// Words are the words of the item's display text, in reading order across its lines.
func (g GraphicsItem) Words() []string {
	return strings.Fields(g.DisplayText())
}

// graphicsAllowed lists the params each template allows (besides text, which all take).
var graphicsAllowed = map[string][]string{
	TemplateTitlePop:      {"style", "fill", "accent", "scale", "align", "shadow", "backing", "energy", "accents_ms"},
	TemplateBlurIn:        {"style", "fill", "accent", "scale", "align", "shadow", "backing", "energy", "accents_ms"},
	TemplateOutroCard:     {"style", "fill", "accent", "scale", "align", "shadow", "backing", "energy", "accents_ms"},
	TemplateChapterCard:   {"style", "fill", "accent", "scale", "align", "shadow", "backing", "energy", "accents_ms", "number", "layout"},
	TemplateHighlightWord: {"style", "fill", "accent", "scale", "align", "shadow", "backing", "energy", "accents_ms", "word"},
	TemplateCounter:       {"style", "fill", "accent", "scale", "align", "shadow", "backing", "energy", "accents_ms", "from", "to", "decimals"},
	TemplateLowerLabel:    {"style", "fill", "accent", "scale", "align", "energy", "accents_ms"},
}

// setParams lists the optional params an item sets.
func (p GraphicsParams) setParams() []string {
	var out []string
	add := func(name string, set bool) {
		if set {
			out = append(out, name)
		}
	}
	add("style", p.Style != "")
	add("fill", p.Fill != "")
	add("accent", p.Accent != "")
	add("scale", p.Scale != nil)
	add("align", p.Align != "")
	add("shadow", p.Shadow != "")
	add("backing", p.Backing != "")
	add("energy", p.Energy != "")
	add("accents_ms", p.AccentsMs != nil)
	add("number", p.Number != nil)
	add("layout", p.Layout != "")
	add("word", p.Word != nil)
	add("from", p.From != nil)
	add("to", p.To != nil)
	add("decimals", p.Decimals != nil)
	return out
}

// CheckParams checks what the schema checks about the params and what it cannot (the accents against the item's time, the highlighted
// word against the words), for a spec that did not go through the schema as well. It returns the param at fault and the problem.
func (g GraphicsItem) CheckParams() (field string, err error) {
	allowed, ok := graphicsAllowed[g.Template]
	if !ok {
		return "/template", fmt.Errorf("template %q is not one of %s", g.Template, strings.Join(GraphicsTemplates(), ", "))
	}
	for _, name := range g.Params.setParams() {
		found := false
		for _, a := range allowed {
			found = found || a == name
		}
		if !found {
			return "/params/" + name, fmt.Errorf("a %s item takes no %s", g.Template, name)
		}
	}
	p := g.Params
	if n := len([]rune(p.Text)); n < 1 || n > MaxGraphicsChars || strings.Count(p.Text, "\n") >= MaxGraphicsLines {
		return "/params/text", fmt.Errorf("the text must be 1 to %d characters on at most %d lines", MaxGraphicsChars, MaxGraphicsLines)
	}
	switch p.Energy {
	case "", EnergyCalm, EnergyLively, EnergyHype:
	default:
		return "/params/energy", fmt.Errorf("energy %q is not one of calm, lively, hype", p.Energy)
	}
	switch p.Backing {
	case "", "none", "plate":
	default:
		return "/params/backing", fmt.Errorf("backing %q is not one of none, plate", p.Backing)
	}
	if p.Accent != "" {
		if err := (style.Overrides{Fill: p.Accent}).Validate(); err != nil {
			return "/params/accent", fmt.Errorf("accent: %w", err)
		}
	}
	if len(p.AccentsMs) > MaxGraphicsAccents {
		return "/params/accents_ms", fmt.Errorf("%d accents, at most %d", len(p.AccentsMs), MaxGraphicsAccents)
	}
	for i, a := range p.AccentsMs {
		if a < g.AtMs || a >= g.EndMs() {
			return fmt.Sprintf("/params/accents_ms/%d", i), fmt.Errorf("accent %d at %d ms is outside the item (%d to %d ms); accents are timeline times inside the item", i, a, g.AtMs, g.EndMs())
		}
		if i > 0 && a <= p.AccentsMs[i-1] {
			return fmt.Sprintf("/params/accents_ms/%d", i), fmt.Errorf("accent %d at %d ms is not after the one before it (%d ms); accents are strictly increasing", i, a, p.AccentsMs[i-1])
		}
	}
	switch g.Template {
	case TemplateChapterCard:
		if p.Number == nil || *p.Number < 1 || *p.Number > 99 {
			return "/params/number", fmt.Errorf("a chapter_card needs a number from 1 to 99")
		}
		if p.Layout != "" && p.Layout != "one_line" && p.Layout != "two_line" {
			return "/params/layout", fmt.Errorf("layout %q is not one of one_line, two_line", p.Layout)
		}
		if p.TwoLine() && strings.Contains(p.Text, "\n") {
			return "/params/text", fmt.Errorf("a two_line chapter_card has its number on the first line, so its title must be one line")
		}
	case TemplateHighlightWord:
		words := len(strings.Fields(p.Text))
		if p.Word == nil || *p.Word < 1 || *p.Word > min(words, MaxHighlightWord) {
			return "/params/word", fmt.Errorf("word must name one of the text's %d words, counting from 1", words)
		}
	case TemplateCounter:
		for _, f := range []struct {
			name string
			v    *float64
		}{{"from", p.From}, {"to", p.To}} {
			if f.v == nil || math.IsNaN(*f.v) || math.Abs(*f.v) > MaxCounterAbs {
				return "/params/" + f.name, fmt.Errorf("a counter needs %s, a number from -%g to %g", f.name, MaxCounterAbs, MaxCounterAbs)
			}
		}
		if d := p.DecimalsOrDefault(); d < 0 || d > 2 {
			return "/params/decimals", fmt.Errorf("decimals must be 0, 1 or 2")
		}
		if strings.Contains(p.Text, "\n") {
			return "/params/text", fmt.Errorf("a counter's label is one line")
		}
	}
	return "", nil
}
