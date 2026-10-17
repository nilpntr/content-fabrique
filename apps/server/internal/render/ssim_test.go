package render

import (
	"image"
	"image/color"
	"testing"
)

// minSSIM is the acceptance threshold of the golden frames (design note section 7).
const minSSIM = 0.98

// minTextSSIM is the threshold of the frames that show text or captions: libass rasterises and animates a little differently
// between versions and platforms (the committed frames are made with the media-tools image, CI renders with a static ffmpeg), so
// glyph edges and the middle of an animation move by a pixel. A caption on the wrong frame or in the wrong place scores far lower
// (the text tests check timing and position exactly; this checks that the picture is what it should be).
const minTextSSIM = 0.95

// minSSIMFor returns the threshold for a sample: the lower one when text or a caption is on screen in it.
func minSSIMFor(s sample) float64 {
	if s.text {
		return minTextSSIM
	}
	return minSSIM
}

// ssim is the mean structural similarity of two images of the same size: the mean over the red, green and blue planes of
// the SSIM of 8x8 windows taken every 4 pixels, with the usual constants for 8-bit data. It is a pure Go
// implementation, so the comparison does not depend on the ffmpeg that is installed. Images of different sizes score 0.
func ssim(a, b image.Image) float64 {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Dx() != bb.Dx() || ab.Dy() != bb.Dy() || ab.Dx() < 8 || ab.Dy() < 8 {
		return 0
	}
	w, h := ab.Dx(), ab.Dy()
	pa, pb := planes(a), planes(b)
	var sum float64
	for c := range 3 {
		sum += planeSSIM(boxBlur(pa[c], w, h), boxBlur(pb[c], w, h), w, h)
	}
	return sum / 3
}

func planes(img image.Image) [3][]float64 {
	b := img.Bounds()
	var p [3][]float64
	for c := range p {
		p[c] = make([]float64, 0, b.Dx()*b.Dy())
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c, _ := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			p[0] = append(p[0], float64(c.R))
			p[1] = append(p[1], float64(c.G))
			p[2] = append(p[2], float64(c.B))
		}
	}
	return p
}

// boxBlur averages every sample with its 8 neighbours (edges are clamped). Two ffmpeg versions resample by slightly
// different rules, so a hard edge can land half a pixel apart; a 3x3 average keeps that out of the score and leaves the
// structure, which is all an off-by-one frame or a wrong crop needs.
func boxBlur(p []float64, w, h int) []float64 {
	out := make([]float64, len(p))
	for y := range h {
		for x := range w {
			var sum float64
			for dy := -1; dy <= 1; dy++ {
				yy := min(max(y+dy, 0), h-1)
				for dx := -1; dx <= 1; dx++ {
					xx := min(max(x+dx, 0), w-1)
					sum += p[yy*w+xx]
				}
			}
			out[y*w+x] = sum / 9
		}
	}
	return out
}

func planeSSIM(a, b []float64, w, h int) float64 {
	const (
		win    = 8
		stride = 4
		c1     = (0.01 * 255) * (0.01 * 255)
		c2     = (0.03 * 255) * (0.03 * 255)
		n      = win * win
	)
	var total float64
	var count int
	for y := 0; y+win <= h; y += stride {
		for x := 0; x+win <= w; x += stride {
			var sa, sb, saa, sbb, sab float64
			for j := range win {
				row := (y+j)*w + x
				for i := range win {
					va, vb := a[row+i], b[row+i]
					sa += va
					sb += vb
					saa += va * va
					sbb += vb * vb
					sab += va * vb
				}
			}
			ma, mb := sa/n, sb/n
			va, vb := saa/n-ma*ma, sbb/n-mb*mb
			cov := sab/n - ma*mb
			total += ((2*ma*mb + c1) * (2*cov + c2)) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func TestSSIM(t *testing.T) {
	flat := func(c color.NRGBA) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		for y := range 32 {
			for x := range 32 {
				img.SetNRGBA(x, y, c)
			}
		}
		return img
	}
	checker := func(shift int) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		for y := range 32 {
			for x := range 32 {
				v := uint8(0)
				if ((x+shift)/4+y/4)%2 == 0 {
					v = 255
				}
				img.SetNRGBA(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
			}
		}
		return img
	}
	if got := ssim(checker(0), checker(0)); got < 0.9999 {
		t.Errorf("identical images: ssim %v, want 1", got)
	}
	if got := ssim(checker(0), checker(2)); got > 0.5 {
		t.Errorf("shifted checkerboard: ssim %v, want a low score", got)
	}
	if got := ssim(flat(color.NRGBA{R: 200, A: 255}), flat(color.NRGBA{B: 200, A: 255})); got > 0.5 {
		t.Errorf("red against blue: ssim %v, want a low score", got)
	}
	if got := ssim(checker(0), image.NewNRGBA(image.Rect(0, 0, 16, 16))); got != 0 {
		t.Errorf("different sizes: ssim %v, want 0", got)
	}
}
