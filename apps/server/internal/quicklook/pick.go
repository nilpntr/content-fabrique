package quicklook

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
)

// Frame selection. The quick look shows the model four pictures spread over the clip, and wants them sharp: a frame caught in
// a swing or a hard pan tells the model little. The proxy is cut into more candidates than needed (CandidatesPerFrame for each
// of the Frames windows the clip is divided into) at evenly spaced times, each candidate is scored for sharpness, and the
// sharpest of each window wins. A window whose candidates cannot be scored, or are all equally flat (a black fade, a blank
// wall), falls back to its middle candidate, so a clip always gives Frames pictures spread over its length.

const (
	// Frames is how many pictures the model sees.
	Frames = 4
	// CandidatesPerFrame is how many candidates each window gets.
	CandidatesPerFrame = 3
	// LongSide is the long side of a picture in pixels (JPEG).
	LongSide = 512
)

// CandidateTimes returns the times, in milliseconds, at which to cut candidates for a clip of the given length:
// Frames * CandidatesPerFrame times, evenly spaced and centred in equal slices, so the first and last are never the very
// first or last frame (usually a fade or a black frame). A clip too short for that many distinct times gets fewer, but
// always at least Frames when it has any length at all.
func CandidateTimes(durationMs int64) []int64 {
	if durationMs <= 0 {
		return nil
	}
	n := Frames * CandidatesPerFrame
	times := make([]int64, 0, n)
	var last int64 = -1
	for i := range n {
		t := (durationMs*int64(2*i+1) + int64(n)) / int64(2*n) // the middle of slice i
		t = min(t, max(durationMs-40, 0))                      // a frame must exist at that time: stay clear of the very end
		if t == last {
			continue
		}
		times = append(times, t)
		last = t
	}
	return times
}

// Candidate is a picture cut from the clip, with its sharpness when it could be measured.
type Candidate struct {
	AtMs int64
	JPEG []byte
	// Sharpness is the variance of the picture's Laplacian, or a negative number when it could not be measured.
	Sharpness float64
}

// Pick chooses up to Frames candidates spread over the clip: the candidates (in time order) are divided into Frames
// consecutive windows and each window gives its sharpest one. When fewer candidates than Frames exist, all are used. The
// result is in time order.
func Pick(cands []Candidate) []Candidate {
	if len(cands) <= Frames {
		return append([]Candidate(nil), cands...)
	}
	out := make([]Candidate, 0, Frames)
	for w := range Frames {
		lo, hi := w*len(cands)/Frames, (w+1)*len(cands)/Frames
		out = append(out, bestOf(cands[lo:hi]))
	}
	return out
}

// bestOf is the sharpest of a window, or its middle candidate when none is sharper than another.
func bestOf(window []Candidate) Candidate {
	middle := window[len(window)/2]
	best, found := middle, false
	for _, c := range window {
		if c.Sharpness < 0 {
			continue
		}
		if !found || c.Sharpness > best.Sharpness {
			best, found = c, true
		}
	}
	// All flat (or all unmeasured): the middle one is as good as any and keeps the spread.
	if !found || best.Sharpness <= 0 {
		return middle
	}
	return best
}

// Sharpness decodes a JPEG and returns the variance of its Laplacian: the usual cheap measure of focus, high for a picture
// with crisp edges and low for a blurred or flat one. It works on the luma of the picture as it is (the quick look's pictures
// are already small), so it is a few milliseconds of Go per picture.
func Sharpness(jpegBytes []byte) (float64, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		return -1, fmt.Errorf("decode: %w", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 3 || h < 3 {
		return -1, errors.New("picture too small to measure")
	}
	luma := make([]float64, w*h)
	switch g := img.(type) {
	case *image.Gray:
		for y := range h {
			for x := range w {
				luma[y*w+x] = float64(g.GrayAt(b.Min.X+x, b.Min.Y+y).Y)
			}
		}
	default:
		for y := range h {
			for x := range w {
				r, gr, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				luma[y*w+x] = (0.299*float64(r) + 0.587*float64(gr) + 0.114*float64(bl)) / 257
			}
		}
	}
	var sum, sumSq float64
	n := 0
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			l := luma[(y-1)*w+x] + luma[(y+1)*w+x] + luma[y*w+x-1] + luma[y*w+x+1] - 4*luma[y*w+x]
			sum += l
			sumSq += l * l
			n++
		}
	}
	mean := sum / float64(n)
	v := sumSq/float64(n) - mean*mean
	if math.IsNaN(v) || v < 0 {
		return -1, errors.New("could not measure")
	}
	return v, nil
}
