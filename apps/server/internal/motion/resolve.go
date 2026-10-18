package motion

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Resolved is a graphics item made concrete for one output: where its box is, what its template draws in it and when.
type Resolved struct {
	Item timeline.GraphicsItem
	// Placement is the item's text placed like a text item (timeline.PlaceTextAvoiding) and Box the box it is drawn in, inside the
	// safe zone. Avoid says what the subject avoidance did.
	Placement timeline.TextPlacement
	Avoid     timeline.Avoidance
	Box       style.Rect
	Timing    Timing
	// Params are the template's page params (what the template's build function reads).
	Params PageParams
	// Font is the font the item uses.
	Font style.Font
}

// PageParams are the params a template receives, all resolved: the laid-out lines, the font and sizes in layout pixels, the
// colours as CSS colours made from numbers, the motion and the times. Nothing in them is the spec's own text except Lines (and
// Count.Label), which the templates set with textContent.
type PageParams struct {
	Lines      []string `json:"lines"`
	Font       string   `json:"font"`
	FontPx     float64  `json:"font_px"`
	LinePx     int      `json:"line_px"`
	Pad        int      `json:"pad"`
	Align      string   `json:"align"`
	Fill       string   `json:"fill"`
	Accent     string   `json:"accent"`
	OnAccent   string   `json:"on_accent"`
	Shadow     *Shadow  `json:"shadow"`
	Outline    *Outline `json:"outline"`
	Plate      *string  `json:"plate"`
	Energy     string   `json:"energy"`
	Motion     Feel     `json:"motion"`
	StaggerMs  int64    `json:"stagger_ms"`
	EnterMs    []int64  `json:"enter_ms"`
	HitsMs     []int64  `json:"hits_ms"`
	ExitMs     int64    `json:"exit_ms"`
	ExitDurMs  int64    `json:"exit_dur_ms"`
	SwipeMs    int64    `json:"swipe_ms,omitempty"`
	NumberWord int      `json:"number_words,omitempty"`
	Highlight  *WordRef `json:"highlight,omitempty"`
	Count      *Count   `json:"count,omitempty"`
}

// Shadow is a text shadow: an offset and a CSS colour.
type Shadow struct {
	X     int    `json:"x"`
	Y     int    `json:"y"`
	Blur  int    `json:"blur"`
	Color string `json:"color"`
}

// Outline is a text outline.
type Outline struct {
	Px    int    `json:"px"`
	Color string `json:"color"`
}

// WordRef names a word: its line and its place in the line, from 0.
type WordRef struct {
	Line int `json:"line"`
	Word int `json:"word"`
}

// Count is what counter counts.
type Count struct {
	From     float64 `json:"from"`
	To       float64 `json:"to"`
	Decimals int     `json:"decimals"`
	Prefix   string  `json:"prefix"`
	Suffix   string  `json:"suffix"`
	SlotPx   int     `json:"slot_px"`
	LandMs   int64   `json:"land_ms"`
}

// ErrNotPlaceable is returned for an item whose box cannot be put inside the safe zone; the renderer leaves it out with a warning,
// as it does a text item.
var ErrNotPlaceable = errors.New("motion: the item's box does not fit the safe zone")

// Resolve makes an item concrete for out, keeping it off the subjects (the obstacles of the clips under it; nil for none).
func Resolve(out timeline.Output, it timeline.GraphicsItem, obstacles []timeline.Subject) (Resolved, error) {
	txt := it.AsText()
	pl, av, err := timeline.PlaceTextAvoiding(out, txt, obstacles)
	if err != nil {
		return Resolved{}, fmt.Errorf("motion: item %s: %w", it.ID, err)
	}
	box, ok := pl.Zone.Fit(pl.Box)
	if !ok {
		return Resolved{}, fmt.Errorf("%w: item %s takes %d x %d px", ErrNotPlaceable, it.ID, pl.Box.Width(), pl.Box.Height())
	}
	lay := pl.Layout
	st := pl.Style
	font := st.FontOf()
	tim := Schedule(it, len(lay.Lines))
	f := FeelOf(it)
	short := out.ShortSide()
	p := PageParams{
		Lines: lay.Lines, Font: font.Family, FontPx: round2(st.EmPx(short)), LinePx: lay.LineHeight, Pad: lay.Pad,
		Align: pl.Align, Fill: css(st.Fill), Accent: it.Params.AccentOrDefault(), Energy: it.Params.EnergyOrDefault(),
		Motion: f, StaggerMs: f.StaggerMs, EnterMs: tim.EnterMs, HitsMs: nonNil(tim.HitsMs),
		ExitMs: tim.ExitMs, ExitDurMs: tim.ExitDurMs, SwipeMs: tim.SwipeMs,
	}
	p.OnAccent = onColour(p.Accent)
	if p.Align == "" {
		p.Align = style.AlignCenter
	}
	size := lay.FontSize
	if st.Box {
		p.Plate = ptr(css(st.Outline))
	} else if px := st.OutlinePx(size); px > 0 && st.Outline.A > 0 {
		p.Outline = &Outline{Px: px, Color: css(st.Outline)}
	}
	if px := st.ShadowPx(size); px > 0 && st.Shadow.A > 0 {
		p.Shadow = &Shadow{X: px, Y: px, Color: css(st.Shadow)}
	}
	if it.Params.Backing == "plate" && p.Plate == nil {
		p.Plate = ptr("rgba(0,0,0,0.55)")
	}
	// Decorations that sit on the letters (highlight_word's marker, outro_card's line) are placed by the template from the font's
	// metrics as Chrome measures them (ctx.metrics in the runtime).
	switch it.Template {
	case timeline.TemplateChapterCard:
		p.NumberWord = 1
	case timeline.TemplateHighlightWord:
		p.Highlight = wordRef(lay.Lines, *it.Params.Word-1)
	case timeline.TemplateCounter:
		slot := it.Params.CounterSlot()
		m := style.Measure(st, short, slot, math.MaxInt32/4)
		p.Count = &Count{
			From: *it.Params.From, To: *it.Params.To, Decimals: it.Params.DecimalsOrDefault(),
			Suffix: " " + it.Params.Text, SlotPx: m.Widths[0], LandMs: tim.LandMs,
		}
	}
	return Resolved{Item: it, Placement: pl, Avoid: av, Box: box, Timing: tim, Params: p, Font: font}, nil
}

// InkTimesMs are the timeline times at which the item must show ink: just after it is fully in, and half-way through its hold.
func (r Resolved) InkTimesMs() []int64 {
	start, end := r.Item.AtMs, r.Item.EndMs()
	in := start + min(r.Timing.FullyInMs, r.Timing.ExitMs)
	hold := start + (min(r.Timing.FullyInMs, r.Timing.ExitMs)+r.Timing.ExitMs)/2
	clamp := func(t int64) int64 { return max(start, min(end-1, t)) }
	a, b := clamp(in), clamp(hold)
	if a == b {
		return []int64{a}
	}
	return []int64{a, b}
}

// wordRef finds the n-th word (from 0) of the laid-out lines.
func wordRef(lines []string, n int) *WordRef {
	for li, l := range lines {
		words := strings.Fields(l)
		if n < len(words) {
			return &WordRef{Line: li, Word: n}
		}
		n -= len(words)
	}
	return &WordRef{}
}

// css writes a colour as #RRGGBB, or rgba() when it is not opaque. The runtime accepts only these two forms.
func css(c style.Colour) string {
	if c.A == 255 {
		return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
	}
	a := math.Round(float64(c.A)/255*1000) / 1000
	return fmt.Sprintf("rgba(%d,%d,%d,%s)", c.R, c.G, c.B, trimFloat(a))
}

// onColour is black or white, whichever has more contrast on the accent (WCAG relative luminance).
func onColour(hex string) string {
	l := Luminance(hex)
	if (l+0.05)/0.05 >= 1.05/(l+0.05) {
		return "#111111"
	}
	return "#FFFFFF"
}

// Luminance is the WCAG relative luminance of a #RRGGBB colour (0 for black, 1 for white).
func Luminance(hex string) float64 {
	var r, g, b uint8
	if _, err := fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &r, &g, &b); err != nil {
		return 0
	}
	lin := func(v uint8) float64 {
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.3f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func ptr[T any](v T) *T { return &v }

func nonNil(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}
