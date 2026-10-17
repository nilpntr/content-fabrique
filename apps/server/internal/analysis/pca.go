package analysis

import (
	"math"

	"github.com/nilpntr/content-fabrique/internal/media"
)

// sqrtLUT maps a grey level to its square root, scaled back to 0..255. A gym is dark and the lifter is a dark shape on a dark
// background: the square root lifts the shadows, so the small movements of a black shirt are not lost beside the lamps.
var sqrtLUT = func() [256]float64 {
	var t [256]float64
	for i := range t {
		t[i] = math.Sqrt(float64(i)/255) * 255
	}
	return t
}()

// window is the thumbnails of the samples [from, to) as a matrix: one row per sample, one column per pixel, in the shadow-lifted
// scale and with every pixel's mean over the window taken out (so what is left is how the picture moves, not what it shows).
func window(raw Raw, from, to int) [][]float64 {
	const px = media.ThumbPixels
	n := to - from
	rows := make([][]float64, n)
	mean := make([]float64, px)
	for i := 0; i < n; i++ {
		t := raw.Thumbs[(from+i)*px : (from+i+1)*px]
		r := make([]float64, px)
		for j, v := range t {
			r[j] = sqrtLUT[v]
			mean[j] += r[j]
		}
		rows[i] = r
	}
	for _, r := range rows {
		for j := range r {
			r[j] -= mean[j] / float64(n)
		}
	}
	return rows
}

// components returns the time courses of the k strongest principal components of the matrix, each of its n rows long, with
// their eigenvalues. It works on the n x n Gram matrix (the eigenvectors of X Xt are the time courses of X's components), so a
// window of a few hundred samples costs a few million operations whatever the thumbnail size, and iterates with deflation: the
// matrices are small and the components few, so power iteration is plenty. Deterministic: the start vectors are fixed.
func components(x [][]float64, k int) (courses [][]float64, eigen []float64) {
	n := len(x)
	if n < 4 {
		return nil, nil
	}
	g := make([][]float64, n)
	for i := range g {
		g[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		for j := i; j < n; j++ {
			s := 0.0
			xi, xj := x[i], x[j]
			for p := range xi {
				s += xi[p] * xj[p]
			}
			g[i][j], g[j][i] = s, s
		}
	}
	for c := 0; c < k; c++ {
		v := make([]float64, n)
		for i := range v { // a fixed, non-degenerate start
			v[i] = 1 + math.Sin(float64(i*(c+3))*0.7)
		}
		normalise(v)
		lambda := 0.0
		for it := 0; it < 80; it++ {
			w := matVec(g, v)
			l := norm(w)
			if l == 0 {
				break
			}
			for i := range w {
				w[i] /= l
			}
			diff := 0.0
			for i := range w {
				diff += math.Abs(w[i] - v[i])
			}
			v, lambda = w, l
			if diff < 1e-9 {
				break
			}
		}
		if lambda <= 0 {
			break
		}
		course := make([]float64, n)
		scale := math.Sqrt(lambda)
		for i := range v {
			course[i] = v[i] * scale
		}
		courses = append(courses, course)
		eigen = append(eigen, lambda)
		// deflate: G -= lambda v vt
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				g[i][j] -= lambda * v[i] * v[j]
			}
		}
	}
	return courses, eigen
}

func matVec(m [][]float64, v []float64) []float64 {
	out := make([]float64, len(m))
	for i, row := range m {
		s := 0.0
		for j, a := range row {
			s += a * v[j]
		}
		out[i] = s
	}
	return out
}

func norm(v []float64) float64 {
	s := 0.0
	for _, a := range v {
		s += a * a
	}
	return math.Sqrt(s)
}

func normalise(v []float64) {
	if n := norm(v); n > 0 {
		for i := range v {
			v[i] /= n
		}
	}
}
