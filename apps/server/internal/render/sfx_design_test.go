package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

var sfxCalibrate = flag.Bool("sfx-calibrate", false, "print the loudness match table of the sound effects (sfxLevelDB) instead of checking it")

// sfxVariants lists every category and variant in catalogue order.
func sfxVariants() [][2]string {
	var out [][2]string
	for _, c := range timeline.SFXCategories() {
		vs, _ := timeline.SFXVariants(c)
		for _, v := range vs {
			out = append(out, [2]string{c, v})
		}
	}
	return out
}

// TestSFXLoudnessMatching: every variant of a category is at the category's level (the loudest 50 ms RMS, without jitter), and
// the categories are in the order of a mix: impact and sting loudest, then sub drop and riser, whoosh and downlifter, glitch,
// swish and shimmer, and pop and click as detail. A riser must not blast after a click.
func TestSFXLoudnessMatching(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	var table []string
	for _, cv := range sfxVariants() {
		c, v := cv[0], cv[1]
		g, _, _ := rawGraphJitter(c, v, neutralJitter, "s1")
		got := rmsMaxDB(decodeGraph(t, ffmpeg, g).mono())
		want := sfxTargetDB[c]
		key := sfxVariantKey(c, v)
		table = append(table, fmt.Sprintf("\t%q: %.1f,", key, math.Round((sfxLevelDB[key]+want-got)*10)/10))
		if !*sfxCalibrate && math.Abs(got-want) > 1.0 {
			t.Errorf("%s: level %.1f dBFS, want %.1f (+-1): run the test with -sfx-calibrate and paste the table into sfxLevelDB", key, got, want)
		}
	}
	if *sfxCalibrate {
		sort.Strings(table)
		t.Logf("var sfxLevelDB = map[string]float64{\n%s\n}", strings.Join(table, "\n"))
	}
	// The order of the categories in a mix.
	order := []string{timeline.SFXImpact, timeline.SFXRiser, timeline.SFXWhoosh, timeline.SFXShimmer, timeline.SFXPop, timeline.SFXClick}
	for i := 1; i < len(order); i++ {
		if sfxTargetDB[order[i-1]] < sfxTargetDB[order[i]] {
			t.Errorf("%s (%.0f) must not be quieter than %s (%.0f)", order[i-1], sfxTargetDB[order[i-1]], order[i], sfxTargetDB[order[i]])
		}
	}
}

// segDB is the RMS of x between a and b as fractions of its length, in dBFS.
func segDB(x []float64, a, b float64) float64 {
	n := float64(len(x))
	return rmsBetweenDB(x, a*n/48, b*n/48)
}

// peakFraction is where, as a fraction of the length, the loudest 10 ms of x is.
func peakFraction(x []float64) float64 {
	best, at := -200.0, 0
	for a := 0; a+480 <= len(x); a += 240 {
		l := rmsBetweenDB(x, float64(a)/48, float64(a+480)/48)
		if l > best {
			best, at = l, a
		}
	}
	return float64(at+240) / float64(len(x))
}

// centroid is the spectral centroid of x in Hz.
func centroid(x []float64) float64 {
	n := 1
	for n < len(x) {
		n <<= 1
	}
	a := make([]complex128, n)
	for i, v := range x {
		a[i] = complex(v*(0.5-0.5*math.Cos(2*math.Pi*float64(i)/float64(max(len(x)-1, 1)))), 0)
	}
	fft(a)
	num, den := 0.0, 0.0
	for k := 1; k < n/2; k++ {
		m := math.Hypot(real(a[k]), imag(a[k]))
		hz := float64(k) * 48000 / float64(n)
		num += hz * m
		den += m
	}
	return num / math.Max(den, 1e-12)
}

// dominantHz is the loudest frequency of x between lo and hi Hz.
func dominantHz(x []float64, lo, hi float64) float64 {
	n := 1
	for n < len(x) {
		n <<= 1
	}
	n *= 4 // zero padding: finer bins for the low frequencies
	a := make([]complex128, n)
	for i, v := range x {
		a[i] = complex(v*(0.5-0.5*math.Cos(2*math.Pi*float64(i)/float64(max(len(x)-1, 1)))), 0)
	}
	fft(a)
	best, at := 0.0, 0.0
	for k := 1; k < n/2; k++ {
		hz := float64(k) * 48000 / float64(n)
		if hz < lo || hz > hi {
			continue
		}
		if m := math.Hypot(real(a[k]), imag(a[k])); m > best {
			best, at = m, hz
		}
	}
	return at
}

func addDB(a, b float64) float64 { return 10 * math.Log10(math.Pow(10, a/10)+math.Pow(10, b/10)) }

// TestSFXDesignedShape measures what each variant is, against the design: where it starts, what its layers are (energy in the
// sub, body and transient bands), how long its tail is, and the shape of its motion (a riser builds on an accelerating curve and
// peaks at its end, a downlifter falls along a decelerating one, a whoosh peaks in the middle, a click has an attack under 3 ms).
// The effects are rendered without jitter; TestSFXJitter covers that.
func TestSFXDesignedShape(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	for _, cv := range sfxVariants() {
		c, v := cv[0], cv[1]
		t.Run(c+"_"+v, func(t *testing.T) {
			t.Parallel()
			g, ms, _ := rawGraphJitter(c, v, neutralJitter, "s1")
			s := decodeGraph(t, ffmpeg, g)
			m := s.mono()
			if want, _ := timeline.SFXVariantLengthMs(c, v); ms != want {
				t.Errorf("length %d ms, want %d", ms, want)
			}
			if n := len(m); n < int(ms)*48-48 || n > int(ms)*48+48 {
				t.Errorf("%d samples for %d ms", n, ms)
			}
			bands := bandDB(m, designBands) // sub, body, low-mid, mid, air
			early := bandDB(m[:min(len(m), 48*15)], designBands)
			earlyHF := addDB(early[3], early[4])
			tail := tailMs(m, 30)
			frac := peakFraction(m)
			pk := peakOf(m)
			onset := onsetMs(m, pk*math.Pow(10, -50.0/20))
			sub, body, lowmid, mid, air := bands[0], bands[1], bands[2], bands[3], bands[4]

			// Every effect starts on its time: within a frame (33 ms at 30 fps), in practice within a few ms.
			if onset < 0 || onset > 5 {
				t.Errorf("onset %.1f ms (the first sample within 50 dB of the peak), want within 5 ms", onset)
			}
			if math.Abs(mean(m)) > 0.001*pk+1e-4 {
				t.Errorf("DC offset %.6f", mean(m))
			}
			if got := rmsBetweenDB(m, float64(len(m))/48-3, float64(len(m))/48); got > dbfs(pk)-25 {
				t.Errorf("the last 3 ms are at %.1f dB, %.1f under the peak: it ends on a fade, not a cut", got, dbfs(pk)-got)
			}

			switch c {
			case timeline.SFXWhoosh:
				if frac < 0.4 || frac > 0.75 {
					t.Errorf("the peak is at %.0f%% of the length, want 40-75%% (a pass-by peaks at its middle)", frac*100)
				}
				if tail < float64(ms)*0.85 {
					t.Errorf("tail %.0f ms of %d", tail, ms)
				}
				if v == "heavy" {
					if sub < -6 {
						t.Errorf("the weight of a heavy whoosh: sub share %.1f dB, want at least -6", sub)
					}
				} else if sub > -25 {
					t.Errorf("sub share %.1f dB: an air whoosh is high-passed, want under -25", sub)
				}
				if m := math.Max(lowmid, mid); m < -8 && v != "heavy" {
					t.Errorf("an air band that moves up to 2-4 kHz: low-mid/mid share %.1f dB", m)
				}
				// Movement: the spectrum at the peak is higher than at the start (the band sweeps up and back).
				if early, mid := centroid(m[:len(m)/5]), centroid(m[len(m)*2/5:len(m)*3/5]); mid < early*1.2 {
					t.Errorf("no movement: centroid %.0f Hz at the start, %.0f Hz at the peak", early, mid)
				}
			case timeline.SFXSwish:
				if frac < 0.3 || frac > 0.7 {
					t.Errorf("the peak is at %.0f%% of the length", frac*100)
				}
				if v != "cloth" && addDB(sub, body) > -13 {
					t.Errorf("a swish has no body: sub+body share %.1f dB", addDB(sub, body))
				}
				if ms < 150 || ms > 450 {
					t.Errorf("length %d ms", ms)
				}
			case timeline.SFXImpact:
				if sub < -9 || (v != "metal" && sub < -3) {
					t.Errorf("sub share %.1f dB: the sub carries the weight", sub)
				}
				if body < -35 && v != "crack" {
					t.Errorf("body share %.1f dB: no body layer", body)
				}
				if earlyHF < -20 {
					t.Errorf("transient: the first 15 ms have %.1f dB above 2 kHz, want at least -20", earlyHF)
				}
				if first, later := segDB(m, 0, 0.12), segDB(m, 0.5, 0.62); first < later+6 {
					t.Errorf("no decay: %.1f dB at the start, %.1f dB halfway", first, later)
				}
				lim := map[string][2]float64{"punch": {500, 800}, "deep": {900, 1400}, "crack": {200, 500}, "metal": {800, 1200}}[v]
				if tail < lim[0] || tail > lim[1] {
					t.Errorf("tail %.0f ms, want %.0f-%.0f", tail, lim[0], lim[1])
				}
			case timeline.SFXRiser:
				if ms != map[string]int64{"tension": 1500, "sweep": 1000, "pitch": 2000, "long": 3000}[v] {
					t.Errorf("a riser's length is exact (its peak is its end): %d ms", ms)
				}
				if frac < 0.9 {
					t.Errorf("the peak is at %.0f%% of the length: a riser peaks at its end", frac*100)
				}
				// An accelerating curve: most of the rise lands in the last quarter, not spread evenly.
				if segDB(m, 0.75, 1)-segDB(m, 0.5, 0.75) < 3 {
					t.Errorf("not accelerating: %.1f dB in the third quarter, %.1f dB in the last", segDB(m, 0.5, 0.75), segDB(m, 0.75, 1))
				}
				if segDB(m, 0.8, 1)-segDB(m, 0, 0.2) < 14 {
					t.Errorf("a riser builds: %.1f dB at the start, %.1f at the end", segDB(m, 0, 0.2), segDB(m, 0.8, 1))
				}
				if sub > -20 {
					t.Errorf("sub share %.1f dB: a riser is high-passed at 150 Hz", sub)
				}
				if v != "pitch" {
					if early, late := centroid(m[:len(m)/4]), centroid(m[len(m)*3/4:]); late < early*1.8 {
						t.Errorf("the filter does not open: centroid %.0f Hz to %.0f Hz", early, late)
					}
				}
			case timeline.SFXDownlifter:
				if frac > 0.35 {
					t.Errorf("the peak is at %.0f%%: a downlifter starts loud and falls", frac*100)
				}
				if segDB(m, 0, 0.25)-segDB(m, 0.75, 1) < 10 {
					t.Errorf("no fall: %.1f dB then %.1f dB", segDB(m, 0, 0.25), segDB(m, 0.75, 1))
				}
				// Decelerating: the pitch/filter falls fast at first and slows (an exponential approach), so the centroid
				// drops more in the first half than in the second.
				q := func(a, b float64) float64 { return centroid(m[int(a*float64(len(m))):int(b*float64(len(m)))]) }
				if first, second := q(0, 0.25)-q(0.25, 0.5), q(0.5, 0.75)-q(0.75, 1); first <= second {
					t.Errorf("not decelerating: the centroid falls %.0f Hz early and %.0f Hz late", first, second)
				}
			case timeline.SFXSubDrop:
				if sub < -1 {
					t.Errorf("sub share %.1f dB: a sub drop is a sine under 120 Hz", sub)
				}
				first := dominantHz(m[:len(m)/6], 20, 300)
				last := dominantHz(m[len(m)/2:len(m)*5/6], 20, 300)
				if first < last*1.3 || last < 25 || last > 60 {
					t.Errorf("the pitch falls from %.0f Hz to %.0f Hz: want a drop of 30%% or more that lands at 25-60 Hz", first, last)
				}
			case timeline.SFXPop:
				if ms < 100 || ms > 400 {
					t.Errorf("length %d ms", ms)
				}
				if tail > 250 {
					t.Errorf("tail %.0f ms: a pop is gone in 250 ms", tail)
				}
				if sub+body > -25 && body > -20 {
					t.Errorf("a pop has no low end: sub %.1f dB body %.1f dB", sub, body)
				}
				if hi := bandDB(m, [][2]float64{{20, 150}, {150, 4500}, {4500, 20000}}); hi[0] > -30 || hi[2] > -14 {
					t.Errorf("a pop sits between 150 Hz and 4 kHz: below %.1f dB, above %.1f dB", hi[0], hi[2])
				}
			case timeline.SFXClick:
				if ms > 200 {
					t.Errorf("length %d ms: a click is short", ms)
				}
				// Attack: the peak comes within 3 ms of the start.
				pkAt := 0
				for i, x := range m {
					if math.Abs(x) >= pk*0.7 {
						pkAt = i
						break
					}
				}
				if pkAt > 3*48 {
					t.Errorf("attack %.1f ms: a click reaches its peak in under 3 ms", float64(pkAt)/48)
				}
				if sub+body > -20 && v != "tonal" {
					t.Errorf("a click has no low end: sub %.1f body %.1f", sub, body)
				}
				if hz := bandDB(m, [][2]float64{{20, 120}, {120, 20000}}); hz[0] > -30 {
					t.Errorf("energy under 120 Hz: %.1f dB", hz[0])
				}
			case timeline.SFXGlitch:
				if ms < 200 || ms > 450 {
					t.Errorf("length %d ms", ms)
				}
				if v == "stutter" || v == "chirp" {
					// It is chopped: gaps of at least a few ms where it is silent, in the slices of the glitch.
					gaps := 0
					for a := 0; a+480 <= len(m); a += 240 {
						if rmsBetweenDB(m, float64(a)/48, float64(a+480)/48) < dbfs(pk)-30 {
							gaps++
						}
					}
					if gaps < 2 {
						t.Errorf("a %s glitch has %d silent windows, want at least 2", v, gaps)
					}
				}
			case timeline.SFXShimmer:
				if sub+body > -20 {
					t.Errorf("a shimmer is all top: sub %.1f body %.1f", sub, body)
				}
				if mid < -10 && air < -10 {
					t.Errorf("no sparkle: mid %.1f air %.1f", mid, air)
				}
				if ms < 800 || ms > 1700 {
					t.Errorf("length %d ms", ms)
				}
			case timeline.SFXSting:
				if sub < -3 {
					t.Errorf("sub share %.1f dB", sub)
				}
				if earlyHF < -22 {
					t.Errorf("transient: the first 15 ms have %.1f dB above 2 kHz", earlyHF)
				}
				// A sting rings: 2.0 to 3.6 s in the pro films (earshot, launch-video-sound.md section 6); ours within 30 dB.
				if tail < float64(ms)*0.7 || ms < 2000 || ms > 3600 {
					t.Errorf("tail %.0f ms of %d", tail, ms)
				}
				// The bloom is tonal: between 0.3 and 0.8 s there is a clear line in the chord's range (110-700 Hz) above the hit.
				if hz := dominantHz(m[48*300:48*800], 100, 700); hz < 100 || hz > 700 {
					t.Errorf("no tonal bloom: dominant %.0f Hz", hz)
				}
			}
		})
	}
}

// TestSFXInTheMix renders every effect the way it is mixed (base level, limiter) and checks what the delivery needs: no clipping
// and a peak under the ceiling, no DC, a sound that starts and ends clean, and a mono fold-down that keeps its level.
func TestSFXInTheMix(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	for _, cv := range sfxVariants() {
		c, v := cv[0], cv[1]
		t.Run(c+"_"+v, func(t *testing.T) {
			t.Parallel()
			g, _, err := SFXPreviewGraph(c, v, "s7", 6) // the loudest gain the spec allows
			if err != nil {
				t.Fatal(err)
			}
			s := decodeGraph(t, ffmpeg, g)
			pk := math.Max(peakOf(s.l), peakOf(s.r))
			if dbfs(pk) > sfxLimitDB+0.15 {
				t.Errorf("peak %.2f dBFS, over the ceiling of %.1f", dbfs(pk), sfxLimitDB)
			}
			if dbfs(pk) < -30 {
				t.Errorf("peak %.1f dBFS: inaudible", dbfs(pk))
			}
			// True peak (ebur128, 4x oversampled). The limiter is a sample-peak limiter at -1.5 dBFS: at the default gain the effect
			// stays under -1 dBTP, the web target, before the programme is even added; at the loudest gain the spec allows a bright
			// effect driven into the limiter can overshoot by up to 0.8 dB between the samples (measured: riser/sweep -0.8 dBTP,
			// swish/blade -0.7), so there the check is -0.5 dBTP. The mix of a programme and a +6 dB effect is held to -1 dBTP
			// by TestSoundEffectsLeaveTheProgrammeAlone.
			if tp := truePeakOfGraph(t, ffmpeg, g); tp > -0.5 {
				t.Errorf("true peak %.2f dBTP at +6 dB gain, want at most -0.5", tp)
			}
			if g0, _, err := SFXPreviewGraph(c, v, "s7", 0); err != nil {
				t.Fatal(err)
			} else if tp := truePeakOfGraph(t, ffmpeg, g0); tp > -1.0 {
				t.Errorf("true peak %.2f dBTP at the default gain, want at most -1", tp)
			}
			for name, ch := range map[string][]float64{"left": s.l, "right": s.r} {
				if d := math.Abs(mean(ch)); d > 0.002 {
					t.Errorf("%s DC offset %.5f", name, d)
				}
				if math.Abs(ch[0]) > 0.02 {
					t.Errorf("%s starts at %.4f: a click at the start", name, ch[0])
				}
			}
			// The mono fold-down keeps the level of the loudest channel within 5 dB (phones are mono).
			mono := rmsMaxDB(s.mono())
			loud := math.Max(rmsMaxDB(s.l), rmsMaxDB(s.r))
			// Two decorrelated copies of noise add up 3 dB less than a coherent pair; more than 5 dB would be cancellation.
			if mono < loud-5 {
				t.Errorf("the mono fold-down is %.1f dB, the stereo channel %.1f dB: width cancels in mono", mono, loud)
			}
		})
	}
}

// TestSFXDeterministic: every category and variant renders to the same samples twice (hash of the PCM), a different item id
// gives a different sample, and the same id the same one.
func TestSFXDeterministic(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	hash := func(g string) [32]byte {
		s := decodeGraph(t, ffmpeg, g)
		return sha256.Sum256(append(floatsToBytes(s.l), floatsToBytes(s.r)...))
	}
	for _, cv := range sfxVariants() {
		c, v := cv[0], cv[1]
		t.Run(c+"_"+v, func(t *testing.T) {
			t.Parallel()
			g1, _, _ := SFXPreviewGraph(c, v, "s1", 0)
			g1b, _, _ := SFXPreviewGraph(c, v, "s1", 0)
			g2, _, _ := SFXPreviewGraph(c, v, "s2", 0)
			if g1 != g1b {
				t.Errorf("the graph is not a function of its inputs")
			}
			a, b, other := hash(g1), hash(g1b), hash(g2)
			if a != b {
				t.Errorf("two renders of the same item differ")
			}
			if a == other {
				t.Errorf("two items with different ids are the same sample: no round-robin variation")
			}
		})
	}
}

// TestSFXJitter: the jitter taken from the id is small (pitch within half a semitone, length within 6 %, level within 1 dB), the
// same id always gives the same one, and across many ids it uses its range.
func TestSFXJitter(t *testing.T) {
	var minP, maxP, minL, maxL, minDB, maxDB = 9.0, 0.0, 9.0, 0.0, 9.0, -9.0
	for i := range 400 {
		id := fmt.Sprintf("s%d", i)
		j := newJitter(id)
		if j != newJitter(id) {
			t.Fatalf("%s: jitter is not a function of the id", id)
		}
		if j.pitch < math.Exp2(-0.5/12)-1e-9 || j.pitch > math.Exp2(0.5/12)+1e-9 || j.length < 0.94-1e-9 || j.length > 1.06+1e-9 ||
			math.Abs(j.levelDB) > 1+1e-9 || j.filter < math.Exp2(-0.12)-1e-9 || j.filter > math.Exp2(0.12)+1e-9 || j.seed < 0 || j.seed > 7 {
			t.Fatalf("%s: jitter out of range: %+v", id, j)
		}
		minP, maxP = math.Min(minP, j.pitch), math.Max(maxP, j.pitch)
		minL, maxL = math.Min(minL, j.length), math.Max(maxL, j.length)
		minDB, maxDB = math.Min(minDB, j.levelDB), math.Max(maxDB, j.levelDB)
	}
	if maxP/minP < 1.025 || maxL-minL < 0.1 || maxDB-minDB < 1.6 {
		t.Errorf("the jitter does not use its range: pitch %.3f-%.3f, length %.3f-%.3f, level %.2f-%.2f", minP, maxP, minL, maxL, minDB, maxDB)
	}
	// A riser's length is exact whatever the id, because its peak is its end.
	for i := range 20 {
		res, _ := buildSFX("fx0", timeline.SFXRiser, "tension", fmt.Sprintf("r%d", i))
		if res.ms != 1500 {
			t.Fatalf("riser length %d ms", res.ms)
		}
	}
	// The other lengths move by at most 6 %.
	seen := map[int64]bool{}
	for i := range 20 {
		res, _ := buildSFX("fx0", timeline.SFXWhoosh, "air", fmt.Sprintf("w%d", i))
		if res.ms < 564 || res.ms > 636 {
			t.Fatalf("whoosh length %d ms", res.ms)
		}
		seen[res.ms] = true
	}
	if len(seen) < 5 {
		t.Errorf("only %d different whoosh lengths in 20 items", len(seen))
	}
}

var reTruePeak = regexp.MustCompile(`Peak:\s+(-?[0-9.]+|-inf) dBFS`)

// truePeakOfGraph is the true peak in dBTP that ffmpeg's ebur128 reports for a graph that ends in [aout].
func truePeakOfGraph(t testing.TB, ffmpeg, graph string) float64 {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-nostdin", "-nostats", //nolint:gosec // test helper
		"-filter_complex", graph+";[aout]ebur128=peak=true:framelog=quiet[o]", "-map", "[o]", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ebur128: %v\n%s", err, stderr.String())
	}
	all := reTruePeak.FindAllStringSubmatch(stderr.String(), -1)
	if len(all) == 0 {
		t.Fatalf("no true peak in the log:\n%s", stderr.String())
	}
	v, err := strconv.ParseFloat(all[len(all)-1][1], 64)
	if err != nil {
		return math.Inf(-1)
	}
	return v
}

// The timeline's per-category graph costs (the editor's effect budget) are an upper bound of what a variant adds to the graph.
func TestSFXGraphBytesAreAnUpperBound(t *testing.T) {
	for _, cv := range sfxVariants() {
		for _, id := range []string{"s1", "s2", "s9", "s10", "s11"} {
			g, _, err := SFXPreviewGraph(cv[0], cv[1], id, 0)
			if err != nil {
				t.Fatal(err)
			}
			// The graph of an item in a render adds a stereo stage and a delay: allow a few hundred bytes.
			if n := len(g) + 300; n > timeline.SFXGraphBytes(cv[0]) {
				t.Errorf("%s/%s (%s): %d bytes in a graph, the timeline's cost for %s is %d", cv[0], cv[1], id, n, cv[0], timeline.SFXGraphBytes(cv[0]))
			}
		}
	}
}
