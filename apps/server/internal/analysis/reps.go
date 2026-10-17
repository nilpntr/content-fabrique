package analysis

import (
	"fmt"
	"io"
	"math"
	"strings"
)

// Rep segmentation (the deterministic slice of P2-09).
//
// A set of reps is a body moving back and forth: the picture changes along one direction (a few principal components of the
// thumbnails' movement) and comes back, again and again, with a period of one and a half to six seconds. The frame difference
// alone cannot cut reps: it is a speed, it peaks in the middle of the movement and has two humps per rep. The principal
// component is a position, so one rep is one cycle of it.
//
// Steps, per shot:
//
//  1. Scan. Slide a window (15 s, every 2.5 s) over the shot; in each, take the top three principal components of the thumbnails'
//     movement and look for an autocorrelation peak between 1.2 and 6 s. A window whose best component repeats with
//     strength >= MinStrength is periodic.
//  2. Runs. Consecutive periodic windows (one bad window between two good ones is bridged) form a run. The windows' components
//     are chained into one signal: each window contributes its middle and is aligned (sign and offset) with the previous one on
//     their overlap, choosing the component that correlates best there.
//  3. Reps. The signal's tops and bottoms are found with a zigzag; the kind the lifter pauses at (less motion there) are the
//     rep boundaries; reps at the edges that are far smaller or of another length than the rest (getting into position, putting
//     the weight down) are dropped.

// Params are the tunable numbers of the analysis. DefaultParams are what the product uses; tests change single fields.
type Params struct {
	// MinPeriodMs and MaxPeriodMs bound the rep duration: a rep is 1.2 to 6 seconds (a 1.0 s period would be the proxy's own
	// keyframe interval, which leaves a pulse in the thumbnails).
	MinPeriodMs, MaxPeriodMs int
	// WindowMs and HopMs are the window the scan for periodic movement slides over the shot.
	WindowMs, HopMs int
	// MinStrength is the autocorrelation a window needs to count as periodic (0..1).
	MinStrength float64
	// MinReps is the fewest reps a stretch of periodic movement must hold to be a set.
	MinReps int
	// ZigzagFraction is how far (as a fraction of the movement's range) the signal must turn back to confirm a top or a bottom.
	ZigzagFraction float64
	// SmoothMs smooths a component before its tops and bottoms are looked for.
	SmoothMs int
	// StillMult is the motion below which a sample is still, as a multiple of the shot's noise floor, with StillFloor as the
	// smallest absolute value that counts.
	StillMult, StillFloor float64
	// MinStillMs is the shortest still stretch that is a hold; MinDeadMs the shortest stretch outside the sets that is reported as
	// dead time.
	MinStillMs, MinDeadMs int
}

// DefaultParams returns the product's parameters.
func DefaultParams() Params {
	return Params{
		MinPeriodMs: 1200, MaxPeriodMs: 6000,
		WindowMs: 20000, HopMs: 2500,
		MinStrength: 0.5, MinReps: 3,
		ZigzagFraction: 0.3, SmoothMs: 400,
		StillMult: 2.5, StillFloor: 0.25,
		MinStillMs: 1500, MinDeadMs: 2000,
	}
}

func (p Params) samples(ms int) int { return max(ms/StepMs, 1) }

var debugOn = false

var debugf = func(string, ...any) {}

func dbgSpark(x []float64, per int) string {
	lo, hi := quantile(x, 0), quantile(x, 1)
	bars := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for i := 0; i < len(x); i += per {
		m := mean(x[i:min(i+per, len(x))])
		k := int((m - lo) / math.Max(hi-lo, 1e-9) * 7.99)
		b.WriteRune(bars[max(0, min(k, 7))])
	}
	return b.String()
}

// Rep is one repetition: from one turning point to the next of the same kind.
type Rep struct {
	// StartIdx, PeakIdx and EndIdx are sample indexes: the rep starts and ends at a turning point where the movement pauses
	// (the position it returns to), PeakIdx is the opposite turning point between them.
	StartIdx, PeakIdx, EndIdx int
	// Amplitude is the size of the movement (in the units of the principal component), Range the same relative to the
	// median rep of the set.
	Amplitude, Range float64
	// Regularity is 1 for a rep as long as the median one, falling to 0 at 40 % longer or shorter.
	Regularity float64
}

// Set is a stretch of periodic movement: a set of reps.
type Set struct {
	StartIdx, EndIdx int
	// PeriodMs is the median rep duration, Strength the mean autocorrelation of the windows it was found in.
	PeriodMs float64
	Strength float64
	Reps     []Rep
}

// RepsResult is what FindReps found in a range of the series.
type RepsResult struct {
	From, To int
	Windows  []WindowInfo
	Sets     []Set
}

// WindowInfo is what the scan saw in one window (for the debug output and the tests).
type WindowInfo struct {
	From, To int
	Strength float64
	Period   float64 // samples
}

// FindReps looks for sets of reps in the whole series.
func FindReps(raw Raw, s Series, p Params) RepsResult { return FindRepsIn(raw, s, 0, s.Len(), p) }

// FindRepsIn looks for sets of reps in samples [from, to) of the series (a shot).
func FindRepsIn(raw Raw, s Series, from, to int, p Params) RepsResult {
	res := RepsResult{From: from, To: to}
	minLag, maxLag := p.samples(p.MinPeriodMs), p.samples(p.MaxPeriodMs)
	win := min(p.samples(p.WindowMs), to-from)
	hop := p.samples(p.HopMs)
	if win < max(3*minLag, 2*maxLag/3) || win < 2*minLag+4 {
		return res
	}
	maxLagWin := min(maxLag, win/2)

	// 1. Scan.
	var ws []windowCourses
	for a := from; ; a += hop {
		b := min(a+win, to)
		a = b - win // the last window ends at the end of the shot
		wc := scanWindow(raw, a, b, minLag, maxLagWin, p)
		ws = append(ws, wc)
		res.Windows = append(res.Windows, WindowInfo{From: a, To: b, Strength: wc.strength, Period: wc.lag})
		if b == to {
			break
		}
	}

	// 2. Runs of periodic windows: a run starts at a window that is clearly periodic and reaches over the neighbours that still
	// are somewhat (hysteresis), so a window that straddles the end of a set does not cut the set short.
	ok := make([]bool, len(ws))
	weak := p.MinStrength * 0.6
	for i, w := range ws {
		ok[i] = w.strength >= p.MinStrength
	}
	for changed := true; changed; {
		changed = false
		for i := range ws {
			if ok[i] || ws[i].strength < weak {
				continue
			}
			if (i > 0 && ok[i-1]) || (i+1 < len(ws) && ok[i+1]) {
				ok[i], changed = true, true
			}
		}
	}
	for i := 0; i < len(ws); {
		if !ok[i] {
			i++
			continue
		}
		j := i
		for j < len(ok) && ok[j] {
			j++
		}
		for _, run := range stitchRun(ws[i:j], hop) {
			if set, good := repsOf(s, run.x, run.from, run.period(), p); good {
				set.Strength = run.strength
				// Windows overlap, so two runs can see the same reps: the later run only keeps what comes after the earlier set.
				if n := len(res.Sets); n > 0 {
					prevEnd := res.Sets[n-1].EndIdx
					kept := set.Reps[:0:0]
					for _, r := range set.Reps {
						if r.StartIdx >= prevEnd-1 {
							kept = append(kept, r)
						}
					}
					if len(kept) < p.MinReps {
						continue
					}
					set.Reps, set.StartIdx = kept, kept[0].StartIdx
				}
				res.Sets = append(res.Sets, set)
			}
		}
		i = j
	}
	return res
}

// windowCourses is one window of the scan: its smoothed components and how periodic its best one is.
type windowCourses struct {
	from, to int
	courses  [][]float64 // per component, smoothed, in the units of pixel movement (RMS)
	best     int
	lag      float64 // samples
	strength float64
}

func scanWindow(raw Raw, a, b, minLag, maxLag int, p Params) windowCourses {
	wc := windowCourses{from: a, to: b, best: -1}
	courses, _ := components(window(raw, a, b), 3)
	scale := 1 / math.Sqrt(float64(b-a))
	for c, course := range courses {
		x := smooth(course, p.samples(p.SmoothMs))
		for i := range x {
			x[i] *= scale
		}
		wc.courses = append(wc.courses, x)
		lag, r := dominantPeriod(x, minLag, maxLag)
		if r > wc.strength {
			wc.best, wc.lag, wc.strength = c, lag, r
		}
	}
	return wc
}

// signal is a chained component: x[i] is sample from+i of the series.
type signal struct {
	from     int
	x        []float64
	strength float64
	lags     []float64 // the period (samples) of each window in it
}

// period is the median of the windows' periods, in samples.
func (g signal) period() float64 { return quantile(g.lags, 0.5) }

// stitchRun chains the windows of a run of periodic windows into signals. Each window contributes its middle hop samples (the
// first its beginning, the last its end); it is aligned with the signal so far on the overlap of the two windows, using the
// component that correlates best there. When no component correlates (the movement changed), the signal ends and a new one
// starts.
func stitchRun(ws []windowCourses, hop int) []signal {
	var out []signal
	var cur *signal
	var prev []float64 // the previous window's chosen course, aligned, over prevFrom..
	var prevFrom int
	flush := func() {
		if cur != nil && len(cur.x) > 0 {
			out = append(out, *cur)
		}
		cur = nil
	}
	for k, w := range ws {
		if w.best < 0 {
			flush()
			continue
		}
		var aligned []float64
		if cur == nil || prev == nil {
			flush()
			aligned = append([]float64(nil), w.courses[w.best]...)
			cur = &signal{from: w.from}
			prevFrom = w.from
		} else {
			// the overlap of this window with the previous one
			lo, hi := w.from, prevFrom+len(prev)
			bestC, bestCorr := -1, 0.0
			var bestSign float64
			for c, course := range w.courses {
				corr := correlate(prev[lo-prevFrom:hi-prevFrom], course[:hi-lo])
				if math.Abs(corr) > math.Abs(bestCorr) {
					bestC, bestCorr = c, corr
				}
			}
			if bestC < 0 || math.Abs(bestCorr) < 0.5 {
				flush()
				k--
				_ = k
				aligned = append([]float64(nil), w.courses[w.best]...)
				cur = &signal{from: w.from}
				prevFrom = w.from
			} else {
				bestSign = math.Copysign(1, bestCorr)
				course := w.courses[bestC]
				off := mean(prev[lo-prevFrom:hi-prevFrom]) - bestSign*mean(course[:hi-lo])
				aligned = make([]float64, len(course))
				for i, v := range course {
					aligned[i] = bestSign*v + off
				}
			}
		}
		// which samples does this window contribute?
		from := w.from + (w.to-w.from-hop)/2
		to := from + hop
		if cur.from == w.from && len(cur.x) == 0 { // the first of the signal: from its beginning
			from = w.from
		}
		if k == len(ws)-1 {
			to = w.to
		}
		if from < cur.from+len(cur.x) {
			from = cur.from + len(cur.x)
		}
		for i := from; i < to; i++ {
			if i-w.from < len(aligned) {
				cur.x = append(cur.x, aligned[i-w.from])
			}
		}
		prev, prevFrom = aligned, w.from
		cur.strength = math.Max(cur.strength, w.strength)
		cur.lags = append(cur.lags, w.lag)
	}
	flush()
	return out
}

// correlate is the Pearson correlation of two equal-length slices.
func correlate(a, b []float64) float64 {
	n := min(len(a), len(b))
	if n < 3 {
		return 0
	}
	ma, mb := mean(a[:n]), mean(b[:n])
	var sab, saa, sbb float64
	for i := 0; i < n; i++ {
		da, db := a[i]-ma, b[i]-mb
		sab += da * db
		saa += da * da
		sbb += db * db
	}
	if saa == 0 || sbb == 0 {
		return 0
	}
	return sab / math.Sqrt(saa*sbb)
}

// dominantPeriod returns the lag (in samples, interpolated) of the strongest autocorrelation peak of x between minLag and
// maxLag, and the autocorrelation there. When a shorter lag has nearly as strong a peak, that is the fundamental and wins (a
// signal repeating every T also repeats every 2T). A signal with no peak returns strength 0.
func dominantPeriod(x []float64, minLag, maxLag int) (float64, float64) {
	n := len(x)
	if n < 2*minLag+2 || maxLag <= minLag {
		return 0, 0
	}
	x = detrend(x)
	r := make([]float64, maxLag+2)
	for l := 0; l <= maxLag+1 && l < n-2; l++ {
		var sxy, sxx, syy float64
		for i := 0; i+l < n; i++ {
			sxy += x[i] * x[i+l]
			sxx += x[i] * x[i]
			syy += x[i+l] * x[i+l]
		}
		if sxx > 0 && syy > 0 {
			r[l] = sxy / math.Sqrt(sxx*syy)
		}
	}
	// the local maxima of r after it first dropped below zero (the lobe around lag 0 is not a period)
	start := 1
	for start < len(r) && r[start] > 0 {
		start++
	}
	var peaks []int
	for l := max(start, minLag); l <= maxLag && l+1 < len(r); l++ {
		if r[l] > r[l-1] && r[l] >= r[l+1] && r[l] > 0 {
			peaks = append(peaks, l)
		}
	}
	if len(peaks) == 0 {
		return 0, 0
	}
	top := 0.0
	for _, l := range peaks {
		top = math.Max(top, r[l])
	}
	for _, l := range peaks {
		if r[l] >= 0.8*top { // the shortest of the strong peaks is the period
			return parabolic(r, l), r[l]
		}
	}
	return 0, 0
}

// parabolic refines a peak at index l of r to a fraction of a sample.
func parabolic(r []float64, l int) float64 {
	if l <= 0 || l+1 >= len(r) {
		return float64(l)
	}
	a, b, c := r[l-1], r[l], r[l+1]
	den := a - 2*b + c
	if den == 0 {
		return float64(l)
	}
	return float64(l) + 0.5*(a-c)/den
}

// detrend takes out the mean and the straight line through the points.
func detrend(x []float64) []float64 {
	n := float64(len(x))
	var sx, sy, sxy, sxx float64
	for i, v := range x {
		fi := float64(i)
		sx += fi
		sy += v
		sxy += fi * v
		sxx += fi * fi
	}
	den := n*sxx - sx*sx
	slope := 0.0
	if den != 0 {
		slope = (n*sxy - sx*sy) / den
	}
	icpt := (sy - slope*sx) / n
	out := make([]float64, len(x))
	for i, v := range x {
		out[i] = v - (icpt + slope*float64(i))
	}
	return out
}

// repsOf finds the turning points of x (whose first sample is sample a of the series), picks the kind (tops or bottoms) that
// the movement pauses at, and cuts the reps between them.
func repsOf(s Series, x []float64, a int, period float64, p Params) (Set, bool) {
	tp := turningPoints(x, p.ZigzagFraction)
	debugf("signal %.1f-%.1fs: %d turning points, period %.1fs\n", float64(a)/10, float64(a+len(x))/10, len(tp), period/10)
	if debugOn {
		var parts []string
		for _, t := range tp {
			parts = append(parts, fmt.Sprintf("%.1f:%.2f", float64(a+t)/10, x[t]))
		}
		debugf("  tp %s\n  x: %s\n", strings.Join(parts, " "), dbgSpark(x, 5))
	}
	if len(tp) < p.MinReps+1 || period <= 0 {
		return Set{}, false
	}
	// Tops and bottoms alternate. The boundary kind is the one with less motion at it (the lifter pauses there), and when
	// both are about equally still the one whose spacing is the more regular.
	motionAt := func(i int) float64 {
		lo, hi := max(a+i-2, 0), min(a+i+3, s.Len())
		return mean(s.Motion[lo:hi])
	}
	var kinds [2][]int
	for i, t := range tp {
		kinds[i%2] = append(kinds[i%2], t)
	}
	// A rep has one top and one bottom; a second wiggle within a period (the lifter's grip shifting, a bounce) is not a rep.
	// Keep, greedily from the most extreme, the turning points that are at least 0.6 of a period from a kept one.
	kinds[0], kinds[1] = spread(x, kinds[0], 0.6*period), spread(x, kinds[1], 0.6*period)
	score := func(ts []int) (still, irregular float64) {
		if len(ts) < 2 {
			return math.Inf(1), math.Inf(1)
		}
		m := make([]float64, len(ts))
		for i, t := range ts {
			m[i] = motionAt(t)
		}
		gaps := make([]float64, len(ts)-1)
		for i := range gaps {
			gaps[i] = float64(ts[i+1] - ts[i])
		}
		return mean(m), stddev(gaps) / math.Max(quantile(gaps, 0.5), 1)
	}
	s0, i0 := score(kinds[0])
	s1, i1 := score(kinds[1])
	pick := 0
	switch {
	case s0 < 0.85*s1:
		pick = 0
	case s1 < 0.85*s0:
		pick = 1
	case i1 < i0:
		pick = 1
	}
	bounds := kinds[pick]
	debugf("  tops/bottoms: kinds %d/%d, motion %.2f/%.2f, irregularity %.2f/%.2f, pick %d\n", len(kinds[0]), len(kinds[1]), s0, s1, i0, i1, pick)
	if len(bounds) < p.MinReps+1 {
		return Set{}, false
	}
	var reps []Rep
	for i := 0; i+1 < len(bounds); i++ {
		b0, b1 := bounds[i], bounds[i+1]
		peak, amp := b0, 0.0
		base := (x[b0] + x[b1]) / 2
		for j := b0; j <= b1; j++ {
			if d := math.Abs(x[j] - base); d > amp {
				amp, peak = d, j
			}
		}
		reps = append(reps, Rep{StartIdx: a + b0, PeakIdx: a + peak, EndIdx: a + b1, Amplitude: amp})
	}
	amps := make([]float64, len(reps))
	durs := make([]float64, len(reps))
	for i, r := range reps {
		amps[i], durs[i] = r.Amplitude, float64(r.EndIdx-r.StartIdx)
	}
	medAmp, medDur := quantile(amps, 0.5), quantile(durs, 0.5)
	for i := range reps {
		reps[i].Range = reps[i].Amplitude / math.Max(medAmp, 1e-9)
		reps[i].Regularity = clamp01(1 - math.Abs(durs[i]-medDur)/math.Max(medDur, 1)/0.4)
	}
	// A rep at an edge of the set that is much smaller, or of another length, is the lifter getting into position or putting
	// the weight down: strip edges while they are off.
	off := func(r Rep) bool {
		d := float64(r.EndIdx - r.StartIdx)
		return r.Range < 0.4 || d < 0.5*medDur || d > 1.8*medDur
	}
	for len(reps) > 0 && off(reps[0]) {
		reps = reps[1:]
	}
	for len(reps) > 0 && off(reps[len(reps)-1]) {
		reps = reps[:len(reps)-1]
	}
	if len(reps) < p.MinReps {
		return Set{}, false
	}
	return Set{
		StartIdx: reps[0].StartIdx, EndIdx: reps[len(reps)-1].EndIdx,
		PeriodMs: medDur * float64(StepMs), Reps: reps,
	}, true
}

// spread keeps, of turning points of one kind (all tops or all bottoms of x), the ones that are at least dist samples from a
// more extreme one that is kept. Which kind they are is decided by the sign of their value against the neighbours, so the
// caller does not say: the most extreme is the highest of tops and the lowest of bottoms.
func spread(x []float64, pts []int, dist float64) []int {
	if len(pts) < 2 {
		return pts
	}
	// tops sit above the mean of the points of the kind's neighbours; compare with the overall mean of the kind
	m := 0.0
	for _, i := range pts {
		m += x[i]
	}
	m /= float64(len(pts))
	overall := mean(x)
	sign := 1.0
	if m < overall {
		sign = -1 // bottoms: the most extreme is the lowest
	}
	order := append([]int(nil), pts...)
	// insertion sort by extremeness, ties by position: deterministic
	for i := 1; i < len(order); i++ {
		for j := i; j > 0; j-- {
			a, b := sign*x[order[j]], sign*x[order[j-1]]
			if a > b || (a == b && order[j] < order[j-1]) {
				order[j], order[j-1] = order[j-1], order[j]
			} else {
				break
			}
		}
	}
	var kept []int
	for _, i := range order {
		ok := true
		for _, k := range kept {
			if math.Abs(float64(i-k)) < dist {
				ok = false
				break
			}
		}
		if ok {
			kept = append(kept, i)
		}
	}
	sortInts(kept)
	return kept
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// turningPoints returns the indexes of the tops and bottoms of x, alternating, found with a zigzag: a reversal counts when the
// signal has turned back by at least fraction of its 5th-to-95th percentile range.
func turningPoints(x []float64, fraction float64) []int {
	if len(x) < 3 {
		return nil
	}
	delta := fraction * (quantile(x, 0.95) - quantile(x, 0.05))
	if delta <= 0 {
		return nil
	}
	var out []int
	ext, extI := x[0], 0
	dir := 0 // +1 rising (ext is a candidate top), -1 falling, 0 not decided yet
	for i := 1; i < len(x); i++ {
		v := x[i]
		switch dir {
		case 0:
			switch {
			case v-ext >= delta:
				dir, ext, extI = 1, v, i
			case ext-v >= delta:
				dir, ext, extI = -1, v, i
			}
		case 1:
			if v > ext {
				ext, extI = v, i
			} else if ext-v >= delta {
				out = append(out, extI)
				dir, ext, extI = -1, v, i
			}
		case -1:
			if v < ext {
				ext, extI = v, i
			} else if v-ext >= delta {
				out = append(out, extI)
				dir, ext, extI = 1, v, i
			}
		}
	}
	return out
}

// Dump prints the result for a person to read (the debug test and cfctl).
func (r RepsResult) Dump(w io.Writer) {
	for _, wi := range r.Windows {
		fmt.Fprintf(w, "  window %5.1f-%5.1fs strength %.2f period %.2fs\n", float64(wi.From)/10, float64(wi.To)/10, wi.Strength, wi.Period/10)
	}
	for i, set := range r.Sets {
		fmt.Fprintf(w, "set %d: %.1f-%.1fs, %d reps, period %.2fs, strength %.2f\n", i+1,
			float64(set.StartIdx)/10, float64(set.EndIdx)/10, len(set.Reps), set.PeriodMs/1000, set.Strength)
		for j, rp := range set.Reps {
			fmt.Fprintf(w, "   rep %2d: %5.1f -> %5.1f (peak %5.1f) range %.2f reg %.2f\n", j+1,
				float64(rp.StartIdx)/10, float64(rp.EndIdx)/10, float64(rp.PeakIdx)/10, rp.Range, rp.Regularity)
		}
	}
}
