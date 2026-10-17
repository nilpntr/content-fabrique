package styleprofile

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/nilpntr/content-fabrique/internal/audiocheck"
	"github.com/nilpntr/content-fabrique/internal/media"
)

// The sound of a reference, from internal/audiocheck's measurements (P3-11: ebur128 loudness with its momentary curve, the onsets from
// the flux of the energy, silence) and plain Go on top of them: the lift of the music, the shape of the energy, the tempo and where
// the cuts sit against the onsets and the beat. Nothing here is a model; a voice activity detector is not part of it (Speech stays
// "unknown").

// Thresholds of the audio measures.
const (
	// LiftMinLU is the smallest rise of the loudness that counts as a lift of the music.
	LiftMinLU = 1.5
	// liftAfterS and liftBeforeS are the windows the loudness is compared over: the second after a time against the second and a half
	// before it.
	liftAfterS, liftBeforeS = 1.0, 1.5
	// strongOnsetDB is the rise of an onset that counts as a strong one (the drop, a beat that stands out) and hitDB that of an isolated
	// hit (a sound effect lands like this).
	strongOnsetDB = 8.0
	hitDB         = 20.0
	// arcLU is how much the last quarter must differ from the first for the energy to build or fall.
	arcLU = 2.0
	// beatLockedMs is the median distance of the cuts to the beat under which they are called locked to it.
	beatLockedMs = 50
)

// MeasureAudio listens to the file and returns the audio measures; cutsS are the cuts found in the picture.
func MeasureAudio(ctx context.Context, runner media.Runner, path string, cutsS []float64) (AudioStats, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return AudioStats{}, err
	}
	rep, err := audiocheck.Check(ctx, runner, abs, nil)
	if err != nil {
		return AudioStats{}, fmt.Errorf("styleprofile: audio: %w", err)
	}
	var sp spectrum
	if rep.HasAudio {
		if sp, err = dumpSpectrum(ctx, runner, abs); err != nil {
			return AudioStats{}, err
		}
	}
	return AnalyseAudio(rep, sp, cutsS), nil
}

// dumpSpectrum runs the sound dump in the sandbox and analyses it.
func dumpSpectrum(ctx context.Context, runner media.Runner, abs string) (spectrum, error) {
	dir, err := os.MkdirTemp("", "styleaudio-")
	if err != nil {
		return spectrum{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	res, err := runner.Run(ctx, media.Invocation{
		Args: media.StyleAudioArgs(media.InputPath(0)), Inputs: []media.Input{{Host: abs, Container: media.InputPath(0)}}, OutputDir: dir,
		Timeout: 3 * time.Minute,
	})
	if err != nil {
		return spectrum{}, fmt.Errorf("styleprofile: ffmpeg: %w", err)
	}
	if res.ExitCode != 0 {
		return spectrum{}, &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	raw, err := readRegular(filepath.Join(dir, media.StyleAudioFile), maxSpectralSamples*4+4096)
	if err != nil {
		return spectrum{}, err
	}
	return analyseSpectrum(pcmFromF32(raw)), nil
}

// AnalyseAudio turns a listening report into the audio measures of a reference.
// sp may be the zero spectrum (the tempo then comes from the onsets alone, which is poorer for a smooth track).
func AnalyseAudio(rep audiocheck.Report, sp spectrum, cutsS []float64) AudioStats {
	a := AudioStats{Speech: "unknown", Content: ContentUnknown}
	if !rep.HasAudio {
		return a
	}
	a.HasAudio = true
	a.IntegratedLUFS, a.RangeLU, a.EndingLU = round1(rep.Loudness.IntegratedLUFS), round1(rep.Loudness.RangeLU), round1(rep.Loudness.EndLU)
	curve := rep.Loudness.Momentary
	a.IntroS, a.LiftLU = findLift(curve)
	a.QuarterLUFS, a.Arc = arcOf(curve)

	var onsets []audiocheck.Onset
	for _, o := range rep.Onsets.List {
		if o.TimeMs >= 300 { // the start of the sound is an onset of everything
			onsets = append(onsets, o)
		}
	}
	var phaseMs float64
	havePhase := false
	if len(sp.Flux) >= 300 {
		a.TempoBPM, a.TempoConfidence, phaseMs = tempoFromFlux(sp.Flux)
		havePhase = a.TempoBPM > 0
	}
	if a.TempoBPM == 0 {
		a.TempoBPM, a.TempoConfidence = tempoOf(onsets)
	}
	a.Flatness = sp.Flatness
	var strong []int64
	for _, o := range onsets {
		if o.RiseDB >= 6 {
			strong = append(strong, o.TimeMs)
		}
	}
	// Where the cuts sit: the distance to the nearest onset and to the nearest beat of the fitted tempo.
	var beatGrid func(ms int64) int64
	switch {
	case havePhase:
		beatGrid = beatDistanceAt(a.TempoBPM, phaseMs)
	case a.TempoBPM > 0:
		beatGrid = beatDistance(onsets, a.TempoBPM)
	}
	within := 0
	for _, c := range cutsS {
		ms := int64(math.Round(c * 1000))
		if d, ok := nearestDelta(strong, ms); ok {
			a.CutOnsetMs = append(a.CutOnsetMs, d)
			if absI(d) <= 100 {
				within++
			}
		}
		if beatGrid != nil {
			a.CutBeatMs = append(a.CutBeatMs, beatGrid(ms))
		}
	}
	if len(a.CutOnsetMs) > 0 {
		a.CutsWithin100ms = round2(float64(within) / float64(len(a.CutOnsetMs)))
	}
	if len(a.CutOnsetMs) >= 2 {
		abs := make([]float64, len(a.CutOnsetMs))
		for i, d := range a.CutOnsetMs {
			abs[i] = float64(absI(d))
		}
		a.BeatLocked = median(sortedCopy(abs)) <= beatLockedMs && a.CutsWithin100ms >= 0.8
	}
	if len(cutsS) > 0 {
		var big []int64
		for _, o := range onsets {
			if o.RiseDB >= strongOnsetDB {
				big = append(big, o.TimeMs)
			}
		}
		if d, ok := nearestDelta(big, int64(math.Round(cutsS[0]*1000))); ok {
			a.FirstCutOnsetMs = d
		}
		a.FirstCutAtLift = a.LiftLU > 0 && math.Abs(cutsS[0]-a.IntroS) <= 0.6
	}
	// Music: a steady tempo over a sound that is there. Hits: isolated loud onsets away from the start and from the lift.
	dur := math.Max(rep.DurationS, 1)
	a.MusicPresent = a.TempoConfidence >= 0.1 && rep.Loudness.IntegratedLUFS > -45
	hits := 0
	for _, o := range onsets {
		if o.RiseDB >= hitDB && o.TimeMs >= 500 && math.Abs(float64(o.TimeMs)/1000-a.IntroS) > 0.4 {
			hits++
		}
	}
	a.HitsPer10s = round1(float64(hits) / dur * 10)
	switch {
	case !a.MusicPresent:
		a.Content = ContentUnknown
	case a.Flatness > FlatnessAmbience:
		a.Content = ContentMusicEffects
	default:
		a.Content = ContentMusicOnly
	}
	return a
}

func absI(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// nearestDelta is the signed distance (onset minus cut) to the nearest of the times, within 2 s.
func nearestDelta(times []int64, ms int64) (int64, bool) {
	best, found := int64(0), false
	for _, t := range times {
		d := t - ms
		if absI(d) > 2000 {
			continue
		}
		if !found || absI(d) < absI(best) {
			best, found = d, true
		}
	}
	return best, found
}

// mean of the curve between two times (seconds), ignoring points under the silence floor.
func curveMean(c []audiocheck.LoudnessPoint, from, to float64) (float64, bool) {
	var s float64
	n := 0
	for _, p := range c {
		if p.TimeS >= from && p.TimeS < to && p.LUFS > -70 {
			s += p.LUFS
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return s / float64(n), true
}

// findLift is the first big rise of the loudness: the loudness of the second after a time against that of the second and a half
// before it. Of the rises, the biggest sets the scale, and the lift is the first one that reaches LiftMinLU and 70 % of the biggest (a
// quiet intro then the music lifts; a track that keeps building has later rises that are bigger and are not the lift). It returns the
// time of the peak of that rise and its size. A video that never rises by LiftMinLU has no lift (0, 0).
func findLift(c []audiocheck.LoudnessPoint) (atS, liftLU float64) {
	if len(c) < 20 {
		return 0, 0
	}
	end := c[len(c)-1].TimeS
	type pt struct{ t, r float64 }
	var rises []pt
	best := 0.0
	for t := 0.5; t <= end-liftAfterS; t += 0.1 {
		after, ok1 := curveMean(c, t, t+liftAfterS)
		before, ok2 := curveMean(c, math.Max(t-liftBeforeS, 0), t)
		if !ok1 || !ok2 {
			continue
		}
		r := after - before
		rises = append(rises, pt{t, r})
		best = math.Max(best, r)
	}
	if best < LiftMinLU {
		return 0, 0
	}
	need := math.Max(LiftMinLU, 0.7*best)
	for i, p := range rises {
		if p.r < need {
			continue
		}
		// the peak of this run of rises
		peak := p
		for j := i; j < len(rises) && rises[j].r >= need*0.8 && rises[j].t-p.t < 1.5; j++ {
			if rises[j].r > peak.r {
				peak = rises[j]
			}
		}
		return round1(peak.t), round1(peak.r)
	}
	return 0, 0
}

// arcOf is the mean loudness of each quarter and the shape: builds, flat or falls.
func arcOf(c []audiocheck.LoudnessPoint) ([4]float64, string) {
	var q [4]float64
	if len(c) < 8 {
		return q, ArcFlat
	}
	end := c[len(c)-1].TimeS
	for i := range q {
		if m, ok := curveMean(c, end*float64(i)/4, end*float64(i+1)/4+0.001); ok {
			q[i] = round1(m)
		}
	}
	switch d := q[3] - q[0]; {
	case d >= arcLU:
		return q, ArcBuilds
	case d <= -arcLU:
		return q, ArcFalls
	}
	return q, ArcFlat
}

// tempoOf estimates the tempo from the onsets: the autocorrelation of the onset train (each onset smeared over 40 ms and weighted by
// its rise) at the lags of 70 to 180 BPM, each lag scored with its doubles, and the confidence from how far the best lag stands out of
// the others. An estimate is ambiguous by a factor of two (a half-time or a double-time reading), as every tempo estimate is.
func tempoOf(onsets []audiocheck.Onset) (bpm, conf float64) {
	if len(onsets) < 6 {
		return 0, 0
	}
	last := onsets[len(onsets)-1].TimeMs
	n := int(last/10) + 50
	w := make([]float64, n)
	for _, o := range onsets {
		i := int(o.TimeMs / 10)
		for k := -2; k <= 2; k++ {
			if j := i + k; j >= 0 && j < n {
				w[j] += o.RiseDB * (1 - math.Abs(float64(k))/3)
			}
		}
	}
	mean := 0.0
	for _, v := range w {
		mean += v
	}
	mean /= float64(n)
	for i := range w {
		w[i] -= mean
	}
	var energy float64
	for _, v := range w {
		energy += v * v
	}
	if energy == 0 {
		return 0, 0
	}
	ac := func(lag int) float64 {
		var s float64
		for i := 0; i+lag < n; i++ {
			s += w[i] * w[i+lag]
		}
		return s / energy
	}
	type cand struct {
		bpm, score float64
	}
	var cands []cand
	var total float64
	for b := 70; b <= 180; b++ {
		lag := int(math.Round(6000 / float64(b)))
		score := ac(lag) + 0.5*ac(2*lag)
		if 3*lag < n {
			score += 0.25 * ac(3*lag)
		}
		cands = append(cands, cand{float64(b), score})
		total += math.Max(score, 0)
	}
	best := slices.MaxFunc(cands, func(a, b cand) int {
		switch {
		case a.score > b.score:
			return 1
		case a.score < b.score:
			return -1
		}
		return 0
	})
	if best.score <= 0 || total == 0 {
		return 0, 0
	}
	avg := total / float64(len(cands))
	conf = math.Max(0, math.Min(1, (best.score-avg)/(best.score+avg)*1.0))
	return round1(best.bpm), round2(conf)
}

// beatDistance returns a function that gives the signed distance (cut minus the nearest beat, ms) of a time to the beat grid of the
// tempo whose phase best fits the onsets.
func beatDistance(onsets []audiocheck.Onset, bpm float64) func(ms int64) int64 {
	period := 60000 / bpm
	var re, im float64
	for _, o := range onsets {
		ang := 2 * math.Pi * float64(o.TimeMs) / period
		re += o.RiseDB * math.Cos(ang)
		im += o.RiseDB * math.Sin(ang)
	}
	phase := math.Atan2(im, re) / (2 * math.Pi) * period // where the beats are, mod period
	return func(ms int64) int64 {
		x := math.Mod(float64(ms)-phase, period)
		if x < 0 {
			x += period
		}
		if x > period/2 {
			x -= period
		}
		return int64(math.Round(x))
	}
}

// beatDistanceAt is beatDistance for a beat phase that is known (ms of a beat modulo the period).
func beatDistanceAt(bpm, phaseMs float64) func(ms int64) int64 {
	period := 60000 / bpm
	return func(ms int64) int64 {
		x := math.Mod(float64(ms)-phaseMs, period)
		if x < 0 {
			x += period
		}
		if x > period/2 {
			x -= period
		}
		return int64(math.Round(x))
	}
}
