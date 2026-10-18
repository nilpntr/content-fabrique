// Package camshift measures how much the camera itself moves in a clip (P6-06): a tripod shot whose subject moves is static, a hand-held
// shot drifts and follows. It is plain Go over the small grey thumbnails the clip analysis and the style pass already make: for frames one
// second apart, the sub-pixel translation of the whole picture that best matches one to the other (the background dominates the match,
// so a person moving in front of a fixed camera reads as no shift). The median of those shifts, as a share of the frame per second, is the
// camera motion. Separating it from applied effects is what lets a style profile say "no effects" without meaning "no motion".
package camshift

import (
	"math"
	"slices"
)

// Thresholds of the verdict, in percent of the frame width per second, calibrated on the founder's five tripod clips (0 %/s) and the
// creator's ten references (eight read 0 %/s; the hand-held lifestyle video reads 4.5 %/s): docs/design/style-profile-v1.md section 3.
const (
	// HandheldPctPerS is the median shift from which a camera is hand-held (or moving): a breathing, drifting picture.
	HandheldPctPerS = 0.8
	// cutDiff is the mean frame difference (0 to 255) over which a pair straddles a cut and is not measured.
	cutDiff = 40
	// border is how many pixels at each edge are left out of the match (they leave the picture as it moves).
	border = 4
	// maxShift is the largest shift searched, in thumbnail pixels.
	maxShift = 3.0
	// rejectShare: a best shift whose difference is not under this share of no shift is no shift (a flat or noisy picture).
	rejectShare = 0.995
)

// The verdicts.
const (
	Static   = "static"
	Handheld = "handheld"
)

// Motion is the camera motion of a clip.
type Motion struct {
	// ShiftPctPerS is the median shift of the picture, in percent of the frame width per second; P75 its 75th percentile.
	ShiftPctPerS float64 `json:"shift_pct_per_s"`
	P75PctPerS   float64 `json:"p75_pct_per_s"`
	// Pairs is how many pairs of frames were measured (0: too short to say).
	Pairs int `json:"pairs"`
}

// Verdict is static or handheld; "" when nothing was measured.
func (m Motion) Verdict() string {
	switch {
	case m.Pairs == 0:
		return ""
	case m.ShiftPctPerS >= HandheldPctPerS:
		return Handheld
	}
	return Static
}

// Measure measures the camera motion of frames: grey thumbnails of size x size pixels, fps a second.
func Measure(frames [][]byte, size, fps int) Motion {
	lag := max(fps, 1)
	lagS := float64(lag) / float64(max(fps, 1))
	var shifts []float64
	for i := 0; i+lag < len(frames); i += max(lag/2, 1) {
		a, b := frames[i], frames[i+lag]
		if len(a) != size*size || len(b) != size*size || meanDiff(a, b) > cutDiff {
			continue
		}
		dx, dy := bestShift(a, b, size)
		shifts = append(shifts, math.Hypot(dx, dy)/float64(size)*100/lagS)
	}
	if len(shifts) == 0 {
		return Motion{}
	}
	slices.Sort(shifts)
	return Motion{ShiftPctPerS: round2(pct(shifts, 0.5)), P75PctPerS: round2(pct(shifts, 0.75)), Pairs: len(shifts)}
}

func meanDiff(a, b []byte) float64 {
	var d int
	for i := range a {
		x := int(a[i]) - int(b[i])
		if x < 0 {
			x = -x
		}
		d += x
	}
	return float64(d) / float64(len(a))
}

// bestShift is the translation (dx, dy) of b that best matches a over the middle of the picture: whole pixels first, then quarter pixels
// around the best whole one. A shift that is not clearly better than none is none (a flat or a noisy picture).
func bestShift(a, b []byte, size int) (float64, float64) {
	sad := func(dx, dy float64) float64 {
		var sum float64
		for y := border; y < size-border; y++ {
			for x := border; x < size-border; x++ {
				sx, sy := float64(x)+dx, float64(y)+dy
				ix, iy := int(math.Floor(sx)), int(math.Floor(sy))
				if ix < 0 || iy < 0 || ix >= size-1 || iy >= size-1 {
					sum += 255
					continue
				}
				fx, fy := sx-float64(ix), sy-float64(iy)
				v := float64(b[iy*size+ix])*(1-fx)*(1-fy) + float64(b[iy*size+ix+1])*fx*(1-fy) +
					float64(b[(iy+1)*size+ix])*(1-fx)*fy + float64(b[(iy+1)*size+ix+1])*fx*fy
				sum += math.Abs(float64(a[y*size+x]) - v)
			}
		}
		return sum
	}
	base := sad(0, 0)
	bx, by, best := 0.0, 0.0, base
	for dy := -maxShift; dy <= maxShift; dy++ {
		for dx := -maxShift; dx <= maxShift; dx++ {
			if v := sad(dx, dy); v < best {
				bx, by, best = dx, dy, v
			}
		}
	}
	cx, cy := bx, by
	for dy := -0.75; dy <= 0.75; dy += 0.25 {
		for dx := -0.75; dx <= 0.75; dx += 0.25 {
			if v := sad(cx+dx, cy+dy); v < best {
				bx, by, best = cx+dx, cy+dy, v
			}
		}
	}
	if best > rejectShare*base {
		return 0, 0
	}
	return bx, by
}

func pct(sorted []float64, q float64) float64 {
	i := int(math.Round(q * float64(len(sorted)-1)))
	return sorted[i]
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
