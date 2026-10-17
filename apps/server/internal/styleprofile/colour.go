package styleprofile

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var hexColour6 = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// rgbOf reads "#RRGGBB".
func rgbOf(hex string) (r, g, b float64, ok bool) {
	if !hexColour6.MatchString(hex) {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(v >> 16 & 0xFF), float64(v >> 8 & 0xFF), float64(v & 0xFF), true
}

// medianColour is the channel-wise median of the valid "#RRGGBB" colours, as "#RRGGBB" (empty for none). The colours are what a
// model estimated from pixels, so they differ a little from read to read; the median of each channel is the colour the references
// share, where a majority of exact strings would pick one of them at random.
func medianColour(colours []string) string {
	var rs, gs, bs []float64
	for _, c := range colours {
		if r, g, b, ok := rgbOf(c); ok {
			rs, gs, bs = append(rs, r), append(gs, g), append(bs, b)
		}
	}
	if len(rs) == 0 {
		return ""
	}
	m := func(v []float64) int { return int(math.Round(RangeOf(v).Median)) }
	return "#" + strings.ToUpper(hex2(m(rs))+hex2(m(gs))+hex2(m(bs)))
}

func hex2(v int) string {
	s := strconv.FormatInt(int64(max(0, min(255, v))), 16)
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

// lab converts an sRGB colour (0 to 255 per channel) to CIE L*a*b* under D65.
func lab(r, g, b float64) (l, a, bb float64) {
	lin := func(c float64) float64 {
		c /= 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	rl, gl, bl := lin(r), lin(g), lin(b)
	x := (0.4124564*rl + 0.3575761*gl + 0.1804375*bl) / 0.95047
	y := 0.2126729*rl + 0.7151522*gl + 0.0721750*bl
	z := (0.0193339*rl + 0.1191920*gl + 0.9503041*bl) / 1.08883
	f := func(t float64) float64 {
		if t > 216.0/24389 {
			return math.Cbrt(t)
		}
		return (24389.0/27*t + 16) / 116
	}
	fx, fy, fz := f(x), f(y), f(z)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

// DeltaE is the CIE76 colour difference between two "#RRGGBB" colours: about 2.3 is just noticeable, 10 a clearly different shade of the
// same colour, 25 or more another colour. ok is false when either is not a valid hex colour.
func DeltaE(a, b string) (de float64, ok bool) {
	r1, g1, b1, ok1 := rgbOf(a)
	r2, g2, b2, ok2 := rgbOf(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	l1, a1, bb1 := lab(r1, g1, b1)
	l2, a2, bb2 := lab(r2, g2, b2)
	return math.Sqrt((l1-l2)*(l1-l2) + (a1-a2)*(a1-a2) + (bb1-bb2)*(bb1-bb2)), true
}
