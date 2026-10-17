// Package analysis is the deterministic clip analysis (P2-02, P2-03 and the deterministic slice of P2-09, D-10): cheap signals
// from one ffmpeg pass over the proxy (motion, exposure, sharpness, scene-change scores), shots from the scene scores, and
// reps, holds and dead time from the periodicity of the motion. No model is involved; the same bytes always give the same
// answer, which is what lets the editor trust the numbers and the tests pin them.
//
// The pure parts (this file, shots.go, reps.go) work on plain slices and are tested on synthetic signals. Service (service.go)
// runs the ffmpeg pass in the sandbox, stores the result and is the job's worker.
package analysis

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/media"
)

// StepMs is the spacing of the samples of every series.
const StepMs = media.AnalysisStepMs

// Raw is what the ffmpeg pass produced, parsed.
type Raw struct {
	// Thumbs holds N thumbnails of media.ThumbPixels grey bytes each, one per sample.
	Thumbs []byte
	// Sharp is the edge energy per sample, Scene the scene-change score per sample; both have N entries.
	Sharp, Scene []float64
}

// N is the number of samples.
func (r Raw) N() int { return len(r.Thumbs) / media.ThumbPixels }

// ParseRaw checks the three files of the pass against each other and returns them. The files are the sandbox's output, so
// they are checked: the thumbnail file must be a whole number of thumbnails, within the bound, and the two text series must
// not be longer than it (a short one is padded with its last value: the filters may drop the last frame).
func ParseRaw(thumbs, sharp, scene []byte) (Raw, error) {
	if len(thumbs) == 0 || len(thumbs)%media.ThumbPixels != 0 {
		return Raw{}, fmt.Errorf("analysis: the thumbnail file is %d bytes, not a whole number of %d-byte thumbnails", len(thumbs), media.ThumbPixels)
	}
	n := len(thumbs) / media.ThumbPixels
	if n > media.MaxAnalysisSamples {
		return Raw{}, fmt.Errorf("analysis: %d samples is over the limit of %d", n, media.MaxAnalysisSamples)
	}
	sh, err := parseMetadataSeries(sharp, "lavfi.signalstats.YAVG", n)
	if err != nil {
		return Raw{}, fmt.Errorf("analysis: sharpness: %w", err)
	}
	sc, err := parseMetadataSeries(scene, "lavfi.scd.score", n)
	if err != nil {
		return Raw{}, fmt.Errorf("analysis: scene scores: %w", err)
	}
	return Raw{Thumbs: thumbs, Sharp: sh, Scene: sc}, nil
}

// parseMetadataSeries reads ffmpeg's `metadata=mode=print` text:
//
//	frame:12   pts:12      pts_time:1.2
//	lavfi.scd.score=0.296
//
// into n values indexed by frame number. A frame without the key keeps the previous value; frames past n are an error.
func parseMetadataSeries(b []byte, key string, n int) ([]float64, error) {
	out := make([]float64, n)
	seen := make([]bool, n)
	frame := -1
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "frame:"):
			f := strings.Fields(strings.TrimPrefix(line, "frame:"))
			if len(f) == 0 {
				return nil, fmt.Errorf("bad frame line %q", line)
			}
			v, err := strconv.Atoi(f[0])
			if err != nil || v < 0 {
				return nil, fmt.Errorf("bad frame line %q", line)
			}
			frame = v
		case strings.HasPrefix(line, key+"="):
			if frame < 0 {
				return nil, fmt.Errorf("value before any frame line")
			}
			v, err := strconv.ParseFloat(strings.TrimPrefix(line, key+"="), 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("bad value %q", line)
			}
			if frame >= n {
				continue // one frame more than the thumbnails: harmless
			}
			out[frame], seen[frame] = v, true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	last := 0.0
	for i := range out {
		if seen[i] {
			last = out[i]
		} else {
			out[i] = last
		}
	}
	return out, nil
}

// Series are the per-sample signals the analysis works on and the job stores. All have the same length.
type Series struct {
	StepMs int `json:"step_ms"`
	// Motion is the mean absolute change of the (brightness-corrected) thumbnail from the previous sample, 0 to 255.
	Motion []float64 `json:"motion"`
	// Luma is the mean grey level of the thumbnail (0 to 255); Contrast is its 10th-to-90th percentile spread.
	Luma     []float64 `json:"luma"`
	Contrast []float64 `json:"contrast"`
	// High is the 90th percentile brightness: the level of the lit part of the picture, which a dark background does not drag down.
	High []float64 `json:"high"`
	// Sharp is the edge energy of the frame, Scene the scene-change score.
	Sharp []float64 `json:"sharp"`
	Scene []float64 `json:"scene"`
}

// Len is the number of samples.
func (s Series) Len() int { return len(s.Motion) }

// DurationMs is the length the series covers.
func (s Series) DurationMs() int { return s.Len() * s.StepMs }

// BuildSeries derives the series from the raw output.
func BuildSeries(r Raw) Series {
	n := r.N()
	s := Series{
		StepMs: StepMs, Motion: make([]float64, n), Luma: make([]float64, n), Contrast: make([]float64, n), High: make([]float64, n),
		Sharp: append([]float64(nil), r.Sharp...), Scene: append([]float64(nil), r.Scene...),
	}
	const px = media.ThumbPixels
	prev := make([]float64, px)
	cur := make([]float64, px)
	var hist [256]int
	for i := 0; i < n; i++ {
		t := r.Thumbs[i*px : (i+1)*px]
		hist = [256]int{}
		sum := 0.0
		for _, v := range t {
			sum += float64(v)
			hist[v]++
		}
		mean := sum / px
		s.Luma[i] = mean
		hi := percentile(&hist, px, 0.9)
		s.High[i] = float64(hi)
		s.Contrast[i] = float64(hi - percentile(&hist, px, 0.1))
		for j, v := range t {
			cur[j] = float64(v) - mean
		}
		if i > 0 {
			d := 0.0
			for j := range cur {
				d += math.Abs(cur[j] - prev[j])
			}
			s.Motion[i] = d / px
		}
		prev, cur = cur, prev
	}
	if n > 1 {
		s.Motion[0] = s.Motion[1]
	}
	return s
}

func percentile(hist *[256]int, total int, q float64) int {
	target := int(q * float64(total))
	acc := 0
	for v, c := range hist {
		acc += c
		if acc > target {
			return v
		}
	}
	return 255
}

// ---- small numeric helpers ---------------------------------------------------------------------------------------------

// smooth is a centred moving average over w samples (shorter at the ends).
func smooth(x []float64, w int) []float64 {
	if w <= 1 || len(x) == 0 {
		return append([]float64(nil), x...)
	}
	out := make([]float64, len(x))
	half := w / 2
	pre := make([]float64, len(x)+1)
	for i, v := range x {
		pre[i+1] = pre[i] + v
	}
	for i := range x {
		lo, hi := max(i-half, 0), min(i+half+1, len(x))
		out[i] = (pre[hi] - pre[lo]) / float64(hi-lo)
	}
	return out
}

func mean(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

func stddev(x []float64) float64 {
	if len(x) < 2 {
		return 0
	}
	m := mean(x)
	s := 0.0
	for _, v := range x {
		s += (v - m) * (v - m)
	}
	return math.Sqrt(s / float64(len(x)))
}

// quantile returns the q quantile (0..1) of x by nearest rank; 0 for an empty slice.
func quantile(x []float64, q float64) float64 {
	if len(x) == 0 {
		return 0
	}
	c := append([]float64(nil), x...)
	sort.Float64s(c)
	i := int(math.Round(q * float64(len(c)-1)))
	return c[max(0, min(i, len(c)-1))]
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
