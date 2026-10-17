package styleprofile

import (
	"fmt"
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// TitleRecipe is how the creator's title is written in our timeline: the style, and the per-item overrides (fill, scale, shadow, align)
// that bring it to the creator's colour, size, shadow and alignment, the anchor that keeps it clear of the subject, and whether it is
// one line. It is made from the profile's text style and is what the editor applies with add_text.
type TitleRecipe struct {
	// Style is title_series (uppercase), title_lowercase (lowercase) or title_bold (mixed case).
	Style string
	// Fill is "#RRGGBB" or empty (the style's own); Scale is 0 for the style's own size, else 0.6 to 1.4; Shadow is none, soft, strong or
	// empty (the style's own); Align is left, center or right.
	Fill   string
	Scale  float64
	Shadow string
	Align  string
	// Anchor is upper_third (the top of the safe zone: above the subject's head), center or lower_third.
	Anchor string
	// OneLine says the creator's titles are one line, so a title should fit one line: short, or at a smaller scale.
	OneLine bool
	// CapHeightPct is the creator's capital height as a percentage of the frame height (0: not read).
	CapHeightPct float64
}

// ScaleSnap is how close to 1 a scale may be before it is left out: a style that is already the creator's size gets no scale.
const ScaleSnap = 0.05

// TitleRecipe derives the recipe from the text profile, or nil when the creator has no title.
func (p *Profile) TitleRecipe() *TitleRecipe {
	t := p.Text
	if t == nil || t.TitleShare <= 0 {
		return nil
	}
	r := &TitleRecipe{Anchor: style.AnchorUpperThird, Align: style.AlignCenter, CapHeightPct: t.HeightPct, OneLine: t.OneLineShare >= 0.5}
	switch t.Case {
	case "lower":
		r.Style = "title_lowercase"
	case "mixed":
		r.Style = "title_bold"
	default:
		r.Style = "title_series"
	}
	if hexColour6.MatchString(t.Colour) {
		r.Fill = strings.ToUpper(t.Colour)
	}
	if st, ok := style.Lookup(r.Style); ok && t.HeightPct > 0 {
		if base := st.CapHeightPct(1080, 1920); base > 0 {
			s := math.Min(math.Max(t.HeightPct/base, style.MinScale), style.MaxScale)
			if math.Abs(s-1) >= ScaleSnap {
				r.Scale = math.Round(s*100) / 100
			}
		}
	}
	switch t.Shadow {
	case style.ShadowNone, style.ShadowSoft, style.ShadowStrong:
		r.Shadow = t.Shadow
	}
	switch t.Alignment {
	case style.AlignLeft, style.AlignRight, style.AlignCenter:
		r.Align = t.Alignment
	}
	switch t.Position {
	case "center":
		r.Anchor = style.AnchorCenter
	case "lower_third":
		r.Anchor = style.AnchorLowerThird
	}
	return r
}

// Args is the recipe as add_text arguments, for the editor to read: only the fields that differ from the style's own.
func (r *TitleRecipe) Args() string {
	parts := []string{fmt.Sprintf(`"style":%q`, r.Style), fmt.Sprintf(`"anchor":%q`, r.Anchor)}
	if r.Fill != "" {
		parts = append(parts, fmt.Sprintf(`"fill":%q`, r.Fill))
	}
	if r.Scale != 0 {
		parts = append(parts, fmt.Sprintf(`"scale":%g`, r.Scale))
	}
	if r.Shadow != "" {
		parts = append(parts, fmt.Sprintf(`"shadow":%q`, r.Shadow))
	}
	if r.Align != style.AlignCenter {
		parts = append(parts, fmt.Sprintf(`"align":%q`, r.Align))
	}
	return strings.Join(parts, ",")
}

// Describe is the recipe in one line for the editor.
func (r *TitleRecipe) Describe() string {
	one := ""
	if r.OneLine {
		one = " Keep the title to one line: a short text, or a smaller scale if it would wrap."
	}
	return fmt.Sprintf("title recipe: add_text with %s.%s", r.Args(), one)
}
