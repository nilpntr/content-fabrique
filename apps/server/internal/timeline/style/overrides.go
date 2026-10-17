package style

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Text overrides: a text item may change four things of its style without a style of its own: the fill colour, the size (a scale of
// the style's), the shadow and, in the renderer, the alignment of its lines. The values are a hex colour, a number in a range and
// two enums, all checked here; the renderer writes them into the ASS script only as formatted numbers and fixed words (the colour
// through Colour.ASS, the scale and the shadow through the derived style's own numbers), never as the spec's text.

// The shadows a text item may ask for. The empty string leaves the style's own.
const (
	ShadowNone   = "none"
	ShadowSoft   = "soft"
	ShadowStrong = "strong"
)

// The alignments of the lines of a text item. The empty string is centred.
const (
	AlignLeft   = "left"
	AlignCenter = "center"
	AlignRight  = "right"
)

// The range of the scale of a text item, relative to its style's size, and the precision it is kept to.
const (
	MinScale = 0.6
	MaxScale = 1.4
)

// ShadowPcts are the shadow offsets of the named shadows (percent of the font size) and their opacity (0 to 255).
var (
	softShadow = struct {
		Pct   float64
		Alpha uint8
	}{2, 110}
	strongShadow = struct {
		Pct   float64
		Alpha uint8
	}{6, 200}
)

var hexFill = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// Overrides are the per-item changes of a style. The zero value changes nothing.
type Overrides struct {
	// Fill is "#RRGGBB" or empty.
	Fill string
	// Scale multiplies the style's size; nil means the style's own (a pointer to 0 is a value, and out of range).
	Scale *float64
	// Shadow is ShadowNone, ShadowSoft, ShadowStrong or empty.
	Shadow string
}

// IsZero says the overrides change nothing.
func (o Overrides) IsZero() bool {
	return o.Fill == "" && (o.Scale == nil || *o.Scale == 1) && o.Shadow == ""
}

// Validate checks every field and says what is wrong in one line.
func (o Overrides) Validate() error {
	if o.Fill != "" && !hexFill.MatchString(o.Fill) {
		return fmt.Errorf("fill %q is not a colour like #F2E9A0 (a # and six hex digits)", o.Fill)
	}
	if o.Scale != nil && (math.IsNaN(*o.Scale) || *o.Scale < MinScale || *o.Scale > MaxScale) {
		return fmt.Errorf("scale %v is outside %v to %v", *o.Scale, MinScale, MaxScale)
	}
	switch o.Shadow {
	case "", ShadowNone, ShadowSoft, ShadowStrong:
	default:
		return fmt.Errorf("shadow %q is not one of none, soft, strong", o.Shadow)
	}
	return nil
}

// ValidAlign says whether a is an alignment (or empty).
func ValidAlign(a string) bool {
	switch a {
	case "", AlignLeft, AlignCenter, AlignRight:
		return true
	}
	return false
}

// ParseHex reads "#RRGGBB" as an opaque colour.
func ParseHex(s string) (Colour, bool) {
	if !hexFill.MatchString(s) {
		return Colour{}, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return Colour{}, false
	}
	return Opaque(uint8(v>>16), uint8(v>>8), uint8(v)), true //nolint:gosec // six hex digits
}

// Hex writes the colour as "#RRGGBB" (the opacity is not part of it).
func (c Colour) Hex() string { return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B) }

// With returns the style with the overrides applied, as a style of its own: its ID names the base and the overrides, so the ASS
// script can hold the base and each variant side by side. The scale is kept to a hundredth. An invalid override is an error.
func (s Style) With(o Overrides) (Style, error) {
	if err := o.Validate(); err != nil {
		return Style{}, err
	}
	if o.IsZero() {
		return s, nil
	}
	id := s.ID
	if o.Fill != "" {
		c, _ := ParseHex(o.Fill)
		s.Fill = c
		id += "~f" + strings.ToUpper(o.Fill[1:])
		if s.Highlight != (Colour{}) {
			s.Highlight = c
		}
	}
	if o.Scale != nil && *o.Scale != 1 {
		pct := math.Floor(*o.Scale*100 + 0.5)
		s.EmPct *= pct / 100
		id += "~s" + strconv.Itoa(int(pct))
	}
	switch o.Shadow {
	case ShadowNone:
		s.ShadowPct = 0
		id += "~hn"
	case ShadowSoft:
		s.ShadowPct, s.Shadow = softShadow.Pct, Colour{0, 0, 0, softShadow.Alpha}
		id += "~hs"
	case ShadowStrong:
		s.ShadowPct, s.Shadow = strongShadow.Pct, Colour{0, 0, 0, strongShadow.Alpha}
		id += "~hx"
	}
	s.ID = id
	return s, nil
}

// ShadowName is the shadow of the style as one of the three names: none (no offset), soft (a light one, up to 3 % of the size) or
// strong. It is what the checks and the vision read call a shadow.
func (s Style) ShadowName() string {
	switch {
	case s.ShadowPct <= 0:
		return ShadowNone
	case s.ShadowPct <= 3:
		return ShadowSoft
	}
	return ShadowStrong
}

// CapHeightPx is the height of the capital letters of the style's font on an output whose shorter side is shortSide px, in pixels
// (0 when the font file has no cap height).
func (s Style) CapHeightPx(shortSide int) float64 {
	t := tableOf(s.FontOf())
	if t.CapHeight == 0 || t.WinAscent+t.WinDescent == 0 {
		return 0
	}
	return float64(s.FontSizePx(shortSide)) * float64(t.CapHeight) / float64(t.WinAscent+t.WinDescent)
}

// CapHeightPct is the cap height as a percentage of the output height outH (the way a person reads "the text is 4 % of the frame").
func (s Style) CapHeightPct(shortSide, outH int) float64 {
	if outH <= 0 {
		return 0
	}
	return s.CapHeightPx(shortSide) / float64(outH) * 100
}
