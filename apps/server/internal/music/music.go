// Package music analyses a creator's music track once (P6-06, D-34): the tempo and a beat grid with its downbeats, the strong onsets, the
// drop (where the energy jumps, the moment a hook should end on), the phrase boundaries and the loudness. It is model-free: the
// measurements of package audiocheck (ebur128 loudness with its momentary curve, the onsets) and the spectral flux of a mono 8 kHz dump,
// analysed in plain Go. The editor cuts to it (snap_cuts_to_beats with the beat list, the hook ending on the drop) and check_edit holds
// the cuts to it.
//
// The track is an upload of the creator's (an audio file, or the sound of one of their videos): untrusted media. It is only ever an input of
// the sandbox job, what the job writes is read as regular files under a size cap, and the result is numbers only.
package music

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/audiocheck"
	"github.com/nilpntr/content-fabrique/internal/media"
)

// Version is the version of the analysis: a stored analysis of an older version is made again.
const Version = 1

// Bounds of the analysis.
const (
	// MinBPM and MaxBPM are the tempo range a beat grid is written in: a reading under MinBPM is taken at double time and one over
	// MaxBPM at half time (every tempo estimate is ambiguous by a factor of two, and cuts want a beat of 0.35 to 0.7 s).
	MinBPM, MaxBPM = 85.0, 175.0
	// strongOnsetDB is the rise of an onset that counts as a strong one, a hit worth cutting on.
	strongOnsetDB = 6.0
	// maxOnsets is the most strong onsets kept (the loudest first, then in time order).
	maxOnsets = 400
	// dropMinLU is the smallest jump of the loudness (the second after against the second before) that is a drop; dropFromS and dropToS
	// are where the drop of a short video's track is looked for.
	dropMinLU          = 2.0
	dropFromS, dropToS = 1.0, 12.0
	// phraseBars is the length of a phrase in bars of four beats.
	phraseBars = 4
	// curveSettleS is how long the momentary curve takes to mean something (its window is 400 ms).
	curveSettleS = 0.5
	// MaxSeconds is the longest track analysed.
	MaxSeconds = 600
)

// Analysis is the music analysis of a track. Times are milliseconds from the start of the track (the source time of a creator_audio
// item: a sound placed with source_offset_ms plays the track from there).
type Analysis struct {
	Version    int   `json:"version"`
	DurationMs int64 `json:"duration_ms"`
	// Music says the sound has a steady beat over a level that is there; a sound with none (speech, a room) has no grid.
	Music bool `json:"music"`
	// TempoBPM is the tempo of the grid, within MinBPM..MaxBPM, and Confidence how clearly the beat stands out (0 to 1).
	TempoBPM   float64 `json:"tempo_bpm"`
	Confidence float64 `json:"confidence"`
	// BeatsMs is the beat grid (a constant tempo with its fitted phase), DownbeatsMs every fourth beat (the strongest of the four
	// phases), PhrasesMs the starts of phrases of four bars, aligned on the drop when there is one.
	BeatsMs     []int64 `json:"beats_ms"`
	DownbeatsMs []int64 `json:"downbeats_ms"`
	PhrasesMs   []int64 `json:"phrases_ms"`
	// OnsetsMs are the strong onsets (a rise of 6 dB or more), the hits a cut can land on.
	OnsetsMs []int64 `json:"onsets_ms"`
	// DropMs is the drop: the biggest jump of the energy early in the track (DropLU over the second before), snapped to the onset and the
	// beat it starts on; 0 when there is none.
	DropMs int64   `json:"drop_ms"`
	DropLU float64 `json:"drop_lu"`
	// The loudness of the track: integrated, true peak and range.
	LUFS       float64 `json:"lufs"`
	TruePeakDB float64 `json:"true_peak_dbtp"`
	RangeLU    float64 `json:"range_lu"`
}

// BeatMs is the length of a beat, 0 without a grid.
func (a Analysis) BeatMs() float64 {
	if a.TempoBPM <= 0 {
		return 0
	}
	return 60000 / a.TempoBPM
}

// Line is the one-line summary the editor reads in the footage list.
func (a Analysis) Line() string {
	if !a.Music || len(a.BeatsMs) == 0 {
		return fmt.Sprintf("sound with no steady beat (not music the cuts can follow), %s long, %.1f LUFS", secs(a.DurationMs), a.LUFS)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "music: %s BPM (a beat every %d ms, confidence %.2f), %s long, %.1f LUFS", trimF(a.TempoBPM), int64(math.Round(a.BeatMs())), a.Confidence, secs(a.DurationMs), a.LUFS)
	if len(a.DownbeatsMs) > 0 {
		fmt.Fprintf(&b, "; first downbeat at %s", secs2(a.DownbeatsMs[0]))
	}
	if a.DropMs > 0 {
		fmt.Fprintf(&b, "; drop at %s (+%.1f LU)", secs2(a.DropMs), a.DropLU)
	} else {
		b.WriteString("; no clear drop")
	}
	if len(a.PhrasesMs) > 1 {
		fmt.Fprintf(&b, "; phrases every %s", secs(a.PhrasesMs[1]-a.PhrasesMs[0]))
	}
	fmt.Fprintf(&b, "; %d beats, %d strong onsets (get_music lists them)", len(a.BeatsMs), len(a.OnsetsMs))
	return b.String()
}

// Between returns the times of list that are inside [from, to).
func Between(list []int64, from, to int64) []int64 {
	i, _ := slices.BinarySearch(list, from)
	j, _ := slices.BinarySearch(list, to)
	return list[i:j]
}

// Nearest is the distance from t to the nearest time of a sorted list (and that time), false for an empty list.
func Nearest(list []int64, t int64) (at, dist int64, ok bool) {
	if len(list) == 0 {
		return 0, 0, false
	}
	i, _ := slices.BinarySearch(list, t)
	best, bestD := int64(0), int64(math.MaxInt64)
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(list) {
			if d := abs(list[j] - t); d < bestD {
				best, bestD = list[j], d
			}
		}
	}
	return best, bestD, true
}

// Measure listens to the file at path in the sandbox and analyses it. workRoot is the host directory the job directories are made in (it
// must be inside the sandbox policy's allowed roots); "" is the system's temporary directory.
func Measure(ctx context.Context, runner media.Runner, path, workRoot string) (Analysis, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Analysis{}, err
	}
	dir, err := os.MkdirTemp(workRoot, "music-")
	if err != nil {
		return Analysis{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	listen := filepath.Join(dir, "listen")
	if err := os.Mkdir(listen, 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return Analysis{}, err
	}
	rep, err := audiocheck.Check(ctx, runner, abs, nil, audiocheck.WithWorkDir(listen))
	if err != nil {
		return Analysis{}, fmt.Errorf("music: listen: %w", err)
	}
	if rep.DurationS > MaxSeconds {
		return Analysis{}, fmt.Errorf("music: the track is %.0f s long, at most %d s are analysed", rep.DurationS, MaxSeconds)
	}
	var sp Spectrum
	if rep.HasAudio {
		if sp, err = DumpSpectrum(ctx, runner, abs, rep.DurationS, dir); err != nil {
			return Analysis{}, err
		}
	}
	return Analyse(rep, sp), nil
}

// DumpSpectrum runs the mono 8 kHz dump of the sound in the sandbox and analyses its spectrum. workRoot is as for Measure.
func DumpSpectrum(ctx context.Context, runner media.Runner, abs string, durationS float64, workRoot string) (Spectrum, error) {
	dir, err := os.MkdirTemp(workRoot, "spectrum-")
	if err != nil {
		return Spectrum{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	res, err := runner.Run(ctx, media.Invocation{
		Args: media.StyleAudioArgs(media.InputPath(0)), Inputs: []media.Input{{Host: abs, Container: media.InputPath(0)}}, OutputDir: dir,
		Timeout: 2*time.Minute + time.Duration(durationS*float64(time.Second)),
	})
	if err != nil {
		return Spectrum{}, fmt.Errorf("music: ffmpeg: %w", err)
	}
	if res.ExitCode != 0 {
		return Spectrum{}, &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	path := filepath.Join(dir, media.StyleAudioFile)
	fi, err := os.Lstat(path)
	if err != nil {
		return Spectrum{}, fmt.Errorf("music: ffmpeg wrote no %s", media.StyleAudioFile)
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxSpectralSamples*4+4096 {
		return Spectrum{}, fmt.Errorf("music: %s is not a regular file of a plausible size (%d bytes)", media.StyleAudioFile, fi.Size())
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- a file this process just checked, in its own job dir
	if err != nil {
		return Spectrum{}, err
	}
	return AnalyseSpectrum(PCMFromF32(raw)), nil
}

// Analyse turns a listening report and the spectrum of a track into its analysis.
func Analyse(rep audiocheck.Report, sp Spectrum) Analysis {
	a := Analysis{Version: Version, DurationMs: int64(math.Round(rep.DurationS * 1000)), BeatsMs: []int64{}, DownbeatsMs: []int64{}, PhrasesMs: []int64{}, OnsetsMs: []int64{}}
	if !rep.HasAudio {
		return a
	}
	a.LUFS, a.TruePeakDB, a.RangeLU = round1(rep.Loudness.IntegratedLUFS), round1(rep.Loudness.TruePeakDBTP), round1(rep.Loudness.RangeLU)
	if a.DurationMs <= 0 && len(sp.Flux) > 0 {
		a.DurationMs = int64(len(sp.Flux)) * 10
	}
	// The strong onsets, the loudest maxOnsets of them, in time order.
	var strong []audiocheck.Onset
	for _, o := range rep.Onsets.List {
		if o.RiseDB >= strongOnsetDB {
			strong = append(strong, o)
		}
	}
	if len(strong) > maxOnsets {
		slices.SortStableFunc(strong, func(x, y audiocheck.Onset) int { return cmpF(y.RiseDB, x.RiseDB) })
		strong = strong[:maxOnsets]
	}
	for _, o := range strong {
		a.OnsetsMs = append(a.OnsetsMs, o.TimeMs)
	}
	slices.Sort(a.OnsetsMs)
	a.OnsetsMs = slices.Compact(a.OnsetsMs)

	// The tempo and the phase of the beat, from the flux.
	var bpm, conf, phase float64
	if len(sp.Flux) >= 300 {
		bpm, conf, phase = TempoFromFlux(sp.Flux)
	}
	a.Music = bpm > 0 && conf >= 0.1 && rep.Loudness.IntegratedLUFS > -45
	if bpm > 0 {
		for bpm < MinBPM {
			bpm *= 2
		}
		for bpm > MaxBPM {
			bpm /= 2
		}
		a.TempoBPM, a.Confidence = round1(bpm), conf
	}
	// The drop is found whether or not there is a grid: a jump of the energy is a moment to cut on in any sound.
	a.DropMs, a.DropLU = findDrop(rep.Loudness.Momentary, a.OnsetsMs, float64(a.DurationMs)/1000)
	if !a.Music {
		return a
	}
	// The autocorrelation reads the tempo to a whole 10 ms lag (124 and 125 BPM are one lag): refine it, and its phase, by the comb that
	// fits the flux best within 2 BPM, in steps of 0.02 BPM.
	if ref, ph, ok := refineTempo(sp.Flux, a.TempoBPM); ok {
		a.TempoBPM, phase = round2(ref), ph
		if r := math.Round(ref); math.Abs(ref-r) <= 0.06 {
			a.TempoBPM = r // a whole tempo read a hair off is the whole tempo (tracks are produced at one)
		}
	}
	period := 60000 / a.TempoBPM
	first := math.Mod(phase, period)
	if first < 0 {
		first += period
	}
	for t := first; t < float64(a.DurationMs); t += period {
		a.BeatsMs = append(a.BeatsMs, int64(math.Round(t)))
	}
	if a.DropMs > 0 {
		// The drop lands on the beat when one is near (the momentary curve and the onset are a little late or early).
		if at, d, ok := Nearest(a.BeatsMs, a.DropMs); ok && float64(d) <= 0.3*period {
			a.DropMs = at
		}
	}
	// The downbeats: of the four phases of the bar, the one whose beats carry the most flux, or the one the drop is on.
	best, bestScore := 0, -1.0
	for o := range 4 {
		var s float64
		for i := o; i < len(a.BeatsMs); i += 4 {
			s += fluxAt(sp.Flux, a.BeatsMs[i])
		}
		if s > bestScore {
			best, bestScore = o, s
		}
	}
	if a.DropMs > 0 {
		if i := slices.Index(a.BeatsMs, a.DropMs); i >= 0 {
			best = i % 4
		}
	}
	for i := best; i < len(a.BeatsMs); i += 4 {
		a.DownbeatsMs = append(a.DownbeatsMs, a.BeatsMs[i])
	}
	// Phrases of four bars, counted from the drop when there is one (a track's sections start on it), else from the first downbeat.
	anchor := 0
	if a.DropMs > 0 {
		anchor = max(slices.Index(a.DownbeatsMs, a.DropMs), 0)
	}
	for i := anchor % phraseBars; i < len(a.DownbeatsMs); i += phraseBars {
		a.PhrasesMs = append(a.PhrasesMs, a.DownbeatsMs[i])
	}
	return a
}

// refineTempo fits a comb to the onset curve (the flux with its local mean taken out, 10 ms a value): for each tempo within 2 BPM of bpm,
// the size of the sum of the flux turned around the circle of one beat (a tempo whose beats fall on the pulses adds them up in one
// direction). It returns the best tempo and the phase of its beats in ms.
func refineTempo(flux []float64, bpm float64) (float64, float64, bool) {
	n := len(flux)
	if n < 300 || bpm <= 0 {
		return 0, 0, false
	}
	w := make([]float64, n)
	for i := range w {
		lo, hi := max(i-50, 0), min(i+50, n)
		var m float64
		for _, v := range flux[lo:hi] {
			m += v
		}
		w[i] = math.Max(flux[i]-m/float64(hi-lo), 0)
	}
	bestB, bestS, bestPh := 0.0, -1.0, 0.0
	for b := bpm - 2; b <= bpm+2+1e-9; b += 0.02 {
		period := 6000 / b // in 10 ms steps
		var re, im float64
		for i, v := range w {
			if v == 0 {
				continue
			}
			ang := 2 * math.Pi * float64(i) / period
			re += v * math.Cos(ang)
			im += v * math.Sin(ang)
		}
		if s := re*re + im*im; s > bestS {
			bestB, bestS = b, s
			bestPh = math.Atan2(im, re) / (2 * math.Pi) * period * 10
		}
	}
	if bestS <= 0 {
		return 0, 0, false
	}
	return bestB, bestPh, true
}

// fluxAt is the largest flux value within 30 ms of a time.
func fluxAt(flux []float64, ms int64) float64 {
	i := int(ms / 10)
	var m float64
	for j := i - 3; j <= i+3; j++ {
		if j >= 0 && j < len(flux) {
			m = math.Max(m, flux[j])
		}
	}
	return m
}

// findDrop is the biggest jump of the momentary loudness between dropFromS and dropToS: the mean of the second after a time against the
// second before it, at least dropMinLU. The jump's time is moved to the strong onset that starts it (within 250 ms; the momentary window
// is 400 ms long, so the curve rises a little before and after the hit).
func findDrop(c []audiocheck.LoudnessPoint, onsets []int64, durS float64) (int64, float64) {
	if len(c) < 20 {
		return 0, 0
	}
	bestT, bestR := 0.0, 0.0
	for t := dropFromS; t <= math.Min(dropToS, durS-1); t += 0.05 {
		after, ok1 := curveMean(c, t, t+1)
		// The first half second of the curve is the 400 ms window filling up, not the music: it is not part of "before".
		before, ok2 := curveMean(c, math.Max(t-1, curveSettleS), t)
		if !ok1 || !ok2 {
			continue
		}
		if r := after - before; r > bestR {
			bestT, bestR = t, r
		}
	}
	if bestR < dropMinLU {
		return 0, 0
	}
	ms := int64(math.Round(bestT * 1000))
	if at, d, ok := Nearest(onsets, ms); ok && d <= 250 {
		ms = at
	}
	return ms, round1(bestR)
}

// curveMean is the mean of the curve between two times (seconds), ignoring points under the silence floor (taken at -70 LUFS: a silent
// stretch before a drop counts as very quiet, not as missing).
func curveMean(c []audiocheck.LoudnessPoint, from, to float64) (float64, bool) {
	var s float64
	n := 0
	for _, p := range c {
		if p.TimeS >= from && p.TimeS < to {
			s += math.Max(p.LUFS, -70)
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return s / float64(n), true
}

// PCM helpers and small numbers.

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func cmpF(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func trimF(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func secs(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + " s" }

func secs2(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 2, 64) + " s" }

