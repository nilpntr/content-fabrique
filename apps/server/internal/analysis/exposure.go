package analysis

import (
	"fmt"
	"math"
	"sort"
)

// Percentile exposure (editor v2). The mean brightness of a frame is a poor judge of exposure for low-key footage: a lifter
// lit against a black wall has a mean of 6 to 20 % and still looks right, because the lit part of the picture is where it
// should be. The level of that lit part is the 90th percentile of the frame's brightness, which the series already holds
// (Series.High, per sample), so this works on every stored analysis with no new data and no version bump.

// Exposure is the percentile exposure of a stretch of a clip, both numbers 0 (black) to 1 (white).
type Exposure struct {
	// P90 is the brightness of the lit part of the picture: the median, over the samples of the stretch, of each frame's 90th
	// percentile luma. A median, so one flash of light or one dark frame does not move it.
	P90 float64
	// Mean is the median, over the samples, of each frame's mean luma. It is what a low-key clip has little of.
	Mean float64
}

// ExposureOf computes the exposure of the samples between fromMs and toMs (clamped to the series) from the per-sample mean luma
// and 90th percentile luma (8-bit, limited range like the series). It is false when the stretch holds no sample.
func ExposureOf(luma, high []float64, stepMs, fromMs, toMs int) (Exposure, bool) {
	n := min(len(luma), len(high))
	if n == 0 || stepMs <= 0 {
		return Exposure{}, false
	}
	a := max(fromMs/stepMs, 0)
	b := min((toMs+stepMs-1)/stepMs, n) // a stretch that ends inside a sample includes it
	if b <= a {
		// A stretch shorter than a sample still has the sample it starts in.
		if a >= n {
			return Exposure{}, false
		}
		b = a + 1
	}
	return Exposure{P90: round2(NormLuma(median(high[a:b]))), Mean: round2(NormLuma(median(luma[a:b])))}, true
}

// ExposureBetween is ExposureOf over the series.
func (s Series) ExposureBetween(fromMs, toMs int) (Exposure, bool) {
	return ExposureOf(s.Luma, s.High, s.StepMs, fromMs, toMs)
}

// WholeExposure is the exposure of the whole series.
func (s Series) WholeExposure() (Exposure, bool) {
	return s.ExposureBetween(0, len(s.Luma)*s.StepMs)
}

// ExposureSeries decodes the stored luma and 90th percentile luma series (one uint8 per sample) into a Series that holds only those
// two, which is all ExposureBetween and WholeExposure read.
func ExposureSeries(stepMs int, luma, high []byte) Series {
	return Series{StepMs: stepMs, Luma: unpackUint8(luma), High: unpackUint8(high)}
}

func median(x []float64) float64 {
	c := append([]float64(nil), x...)
	sort.Float64s(c)
	return c[len(c)/2]
}

// The exposure levels the editor is told about. ExposureOK says nothing.
const (
	ExposureOK           = "ok"
	ExposureUnderexposed = "underexposed"
	ExposureLowKey       = "low-key"
	ExposureBright       = "bright"
)

// Thresholds of the levels, on the 0..1 scale of NormLuma. They come from real footage: the founder's gym clips (a lifter in a
// dark gym, a posing wall with one lamp) measure a p90 of 0.10 to 0.33 and a mean of 0.00 to 0.07, and they render correctly
// exposed. So a dark clip is low-key unless even its lit part is near black.
const (
	// lowKeyMean is the mean below which a clip is dark overall.
	lowKeyMean = 0.25
	// blackP90 is the p90 below which even the lit part of the picture is near black (about 8-bit luma 30): nothing in the clip
	// is lit, which is underexposure and not a mood.
	blackP90 = 0.06
	// brightP90 and brightMean are where a clip counts as bright.
	brightP90  = 0.92
	brightMean = 0.65
)

// Level classifies the exposure: underexposed (even the lit part is near black), low-key (a dark picture with a lit part, which is
// how gym footage in a dark room is shot and needs no correction), bright or ok.
func (e Exposure) Level() string {
	switch {
	case e.Mean < lowKeyMean && e.P90 < blackP90:
		return ExposureUnderexposed
	case e.Mean < lowKeyMean:
		return ExposureLowKey
	case e.P90 > brightP90 && e.Mean > brightMean:
		return ExposureBright
	}
	return ExposureOK
}

// Note is the exposure as the editor reads it, "low-key (p90 luma 0.55)", or "" when the exposure is fine. It never suggests a
// gain: a low-key clip that looks right is not a clip to brighten.
func (e Exposure) Note() string {
	lvl := e.Level()
	if lvl == ExposureOK {
		return ""
	}
	return fmt.Sprintf("%s (p90 luma %.2f)", lvl, e.P90)
}

// Jump is how far two exposures are apart in the lit part of the picture (the difference of the p90 values).
func Jump(a, b Exposure) float64 { return round2(math.Abs(a.P90 - b.P90)) }
