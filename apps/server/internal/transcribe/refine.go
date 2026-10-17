package transcribe

import (
	"math"
	"slices"
)

// frameMs is the resolution of the energy analysis.
const frameMs = 10

// Refine places the boundaries of the words on the audio.
//
// A speech model's word times come from attention weights: they say which words were spoken and roughly where, but they are
// typically a few hundred milliseconds off a word's real start, which is far too coarse to cut on or to light a caption by. Speech
// itself is more precise than that: between two words there is a dip in energy, often a silence. So each word keeps its anchor (a
// time the provider is confident is inside the word, or the middle of its span) and the boundary between two neighbours is moved to
// the audio between their anchors:
//
//   - the longest stretch of silence there (below an adaptive threshold: the noise floor plus 10 dB, but never closer to the peak
//     than 12 dB nor further than 35 dB): the earlier word ends where it begins and the later one starts where it ends;
//   - failing that, the quietest 10 ms frame between the anchors, shared by both words.
//
// The first word starts where the burst of speech around its anchor begins and the last one ends where it stops. The words keep
// their text, order and confidence; times become monotonic and never overlap. Audio that is too short to analyse is returned
// unchanged.
func Refine(words []Word, pcm []int16) []Word {
	out := slices.Clone(words)
	if len(out) == 0 {
		return out
	}
	db := frameLevels(pcm)
	if len(db) < 4 {
		for i := range out {
			out[i].AnchorMs = 0
		}
		return out
	}
	active, smooth := activity(db)
	n := len(db)

	anchors := make([]int, len(out))
	prev := -1
	for i, w := range out {
		a := w.AnchorMs
		if a <= 0 {
			a = (w.StartMs + w.EndMs) / 2
		}
		f := min(max(int(a/frameMs), 0), n-1)
		f = max(f, prev+1) // anchors are strictly increasing, so each word owns at least a frame
		if f > n-1 {
			f = n - 1
		}
		anchors[i], prev = f, f
	}

	starts := make([]int, len(out))
	ends := make([]int, len(out))

	// The first word starts where the speech around its anchor starts.
	i := anchors[0]
	if !active[i] {
		for i < n && !active[i] {
			i++
		}
		i = min(i, n-1)
	}
	for i > 0 && active[i-1] {
		i--
	}
	starts[0] = i

	for k := 1; k < len(out); k++ {
		lo, hi := anchors[k-1], anchors[k]
		if hi <= lo {
			starts[k], ends[k-1] = lo, lo
			continue
		}
		bestFrom, bestTo := -1, -1
		runFrom := -1
		for f := lo; f <= hi; f++ {
			if f < hi && !active[f] {
				if runFrom < 0 {
					runFrom = f
				}
				continue
			}
			if runFrom >= 0 {
				if bestFrom < 0 || f-runFrom > bestTo-bestFrom {
					bestFrom, bestTo = runFrom, f
				}
				runFrom = -1
			}
		}
		if bestFrom >= 0 {
			ends[k-1], starts[k] = bestFrom, bestTo
			continue
		}
		m := lo
		for f := lo; f <= hi; f++ {
			if smooth[f] < smooth[m] {
				m = f
			}
		}
		ends[k-1], starts[k] = m, m
	}

	// The last word ends where the speech around its anchor stops.
	j := anchors[len(out)-1]
	if !active[j] {
		for j > 0 && !active[j] {
			j--
		}
	}
	for j < n-1 && active[j+1] {
		j++
	}
	ends[len(out)-1] = j + 1

	for k := range out {
		s, e := int64(starts[k])*frameMs, int64(ends[k])*frameMs
		if k > 0 {
			s = max(s, out[k-1].EndMs)
		}
		e = max(e, s+frameMs)
		out[k].StartMs, out[k].EndMs, out[k].AnchorMs = s, e, 0
	}
	return out
}

// frameLevels is the level of each 10 ms frame in dB (relative to one sample step squared, so silence is about -30 dB).
func frameLevels(pcm []int16) []float64 {
	size := SampleRate * frameMs / 1000
	db := make([]float64, 0, len(pcm)/size)
	for i := 0; i+size <= len(pcm); i += size {
		var sum float64
		for _, s := range pcm[i : i+size] {
			sum += float64(s) * float64(s)
		}
		db = append(db, 10*math.Log10(sum/float64(size)+1e-3))
	}
	return db
}

// activity decides which frames carry speech and gives a smoothed level for the valley search. The threshold adapts to the clip:
// 10 dB over the noise floor (the 10th percentile of the levels), but between 12 and 35 dB under the loud end (the 95th
// percentile), so a clip with a loud, steady background still finds its speech and a clean one does not call a breath speech.
func activity(db []float64) (active []bool, smooth []float64) {
	sorted := slices.Clone(db)
	slices.Sort(sorted)
	floor := sorted[len(sorted)/10]
	top := sorted[min(len(sorted)-1, len(sorted)*95/100)]
	thr := math.Min(math.Max(floor+10, top-35), top-12)
	active = make([]bool, len(db))
	smooth = make([]float64, len(db))
	for i, v := range db {
		active[i] = v > thr
		lo, hi := max(i-1, 0), min(i+1, len(db)-1)
		var sum float64
		for _, x := range db[lo : hi+1] {
			sum += x
		}
		smooth[i] = sum / float64(hi-lo+1)
	}
	return active, smooth
}
