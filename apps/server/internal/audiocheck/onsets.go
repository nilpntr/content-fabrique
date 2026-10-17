package audiocheck

import (
	"math"
	"slices"
	"sort"
)

// The onset detector (the "energy flux" of the task): the sound is cut into 10 ms windows every 5 ms, each window's RMS is taken
// in dB, and an onset is a window whose level is at least riseDB above the quietest of the lookback windows before it (30 ms)
// while it is above the noise floor. Dense music has a rise on every drum hit; a quiet bed under speech has none; a synthesised
// effect has one on its first transient. An onset is placed where the rise began (the first window within 24 dB of its peak),
// not where it peaked, because sync is about when the sound starts.
const (
	hopSamples = PCMRate / 200 // 5 ms
	winSamples = PCMRate / 100 // 10 ms
	hopMs      = 5
	lookback   = 6 // 30 ms
	// riseDB is the smallest rise that is an onset; salientDB the smallest that is a hit (earshot counts "salient" hits apart).
	riseDB     = 6.0
	salientDB  = 12.0
	floorDB    = -55.0
	refractory = 12 // 60 ms: two onsets are at least this far apart
	// firstSoundDB is the level at which the sound has started (earshot's first_sound_s).
	firstSoundDB = -50.0
)

// envelopeDB returns the RMS level in dBFS of every 10 ms window, hop 5 ms. Window i covers samples [i*hop, i*hop+win).
func envelopeDB(pcm []float32) []float64 {
	if len(pcm) < winSamples {
		return nil
	}
	n := (len(pcm)-winSamples)/hopSamples + 1
	env := make([]float64, n)
	for i := range env {
		var sum float64
		for _, v := range pcm[i*hopSamples : i*hopSamples+winSamples] {
			sum += float64(v) * float64(v)
		}
		env[i] = 10 * math.Log10(sum/winSamples+1e-12)
	}
	return env
}

// frameMs is the time in ms of the middle of window i.
func frameMs(i int) int64 { return int64(i*hopMs + hopMs) }

// detectOnsets finds the onsets of an envelope, in time order. The sound before the first window is taken to be silence, so a sound
// that starts on the very first sample (an effect on frame 0, a track that opens at once) has an onset at the start.
func detectOnsets(env []float64) []Onset {
	pad := make([]float64, lookback, lookback+len(env))
	for i := range pad {
		pad[i] = -120
	}
	env = append(pad, env...)
	n := len(env)
	flux := make([]float64, n)
	lowAt := make([]int, n)
	for i := lookback; i < n; i++ {
		lo, at := math.Inf(1), i-lookback
		for k := i - lookback; k < i; k++ {
			if env[k] < lo {
				lo, at = env[k], k
			}
		}
		// A rise out of silence is measured from the noise floor, not from -120 dB: a slow fade-in from nothing is not a hit.
		flux[i], lowAt[i] = env[i]-math.Max(lo, floorDB), at
	}
	var out []Onset
	for i := lookback; i < n; i++ {
		if flux[i] < riseDB || env[i] < floorDB {
			continue
		}
		// The biggest rise of its neighbourhood only.
		peak := true
		for k := max(lookback, i-refractory); k <= min(n-1, i+refractory); k++ {
			if k != i && (flux[k] > flux[i] || (flux[k] == flux[i] && k < i)) {
				peak = false
				break
			}
		}
		if !peak {
			continue
		}
		// Where the rise began: the first window from the quietest one on that is within 24 dB of the level the rise reached.
		top := env[i]
		for k := i; k < min(n, i+lookback); k++ {
			top = math.Max(top, env[k])
		}
		start := lowAt[i]
		thr := math.Max(env[start]+0.3*(top-env[start]), top-24)
		j := start
		for j < i && env[j] < thr {
			j++
		}
		out = append(out, Onset{TimeMs: max(frameMs(j-lookback), 0), RiseDB: math.Round(flux[i]*10) / 10})
	}
	return out
}

// nearestOnset returns the onset nearest to t within maxMs (either side), and false when there is none. onsets are in time order.
func nearestOnset(onsets []Onset, t, maxMs int64) (Onset, bool) {
	i := sort.Search(len(onsets), func(k int) bool { return onsets[k].TimeMs >= t })
	best, found := Onset{}, false
	for _, k := range []int{i - 1, i} {
		if k < 0 || k >= len(onsets) {
			continue
		}
		d := onsets[k].TimeMs - t
		if d < 0 {
			d = -d
		}
		if d <= maxMs && (!found || d < abs(best.TimeMs-t)) {
			best, found = onsets[k], true
		}
	}
	return best, found
}

func abs[T ~int | ~int64](x T) T {
	if x < 0 {
		return -x
	}
	return x
}

// longestWithout is the longest stretch of [0, durationMs] with no onset, and where it starts.
func longestWithout(onsets []Onset, durationMs int64) (lengthMs, atMs int64) {
	prev := int64(0)
	for _, o := range onsets {
		if o.TimeMs-prev > lengthMs {
			lengthMs, atMs = o.TimeMs-prev, prev
		}
		prev = o.TimeMs
	}
	if durationMs-prev > lengthMs {
		lengthMs, atMs = durationMs-prev, prev
	}
	return lengthMs, atMs
}

// firstSoundS is when the sound first rises above firstSoundDB, or -1 when it never does.
func firstSoundS(env []float64) float64 {
	for i, v := range env {
		if v >= firstSoundDB {
			return float64(i*hopMs) / 1000
		}
	}
	return -1
}

// waveform returns the min and max of the sound in each of width equal columns.
func waveform(pcm []float32, width int) (lo, hi []float32) {
	lo, hi = make([]float32, width), make([]float32, width)
	if len(pcm) == 0 || width == 0 {
		return lo, hi
	}
	for x := 0; x < width; x++ {
		a, b := x*len(pcm)/width, (x+1)*len(pcm)/width
		if b <= a {
			b = min(a+1, len(pcm))
		}
		seg := pcm[a:b]
		lo[x], hi[x] = slices.Min(seg), slices.Max(seg)
	}
	return lo, hi
}

// silenceGaps finds the silent gaps of an envelope: runs of windows under silenceDB that last at least silenceMinS. A window is
// wholly inside the silence it is part of, so a gap starts at the start of its first window and ends at the end of its last (the
// end of the sound for the last window of the file). audioEndS is the length of the sound.
func silenceGaps(env []float64, audioEndS float64) []Gap {
	var gaps []Gap
	run := -1
	flush := func(last int) {
		if run < 0 {
			return
		}
		start := float64(run*hopMs) / 1000
		end := float64(last*hopMs+winSamples*1000/PCMRate) / 1000
		if last == len(env)-1 {
			end = audioEndS
		}
		if end-start >= silenceMinS {
			gaps = append(gaps, Gap{StartS: start, EndS: end, DurationS: end - start})
		}
		run = -1
	}
	for i, v := range env {
		switch {
		case v < silenceDB && run < 0:
			run = i
		case v >= silenceDB && run >= 0:
			flush(i - 1)
		}
	}
	flush(len(env) - 1)
	return gaps
}
