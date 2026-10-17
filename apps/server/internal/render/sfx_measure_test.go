package render

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"math/cmplx"
	"os/exec"
	"strings"
	"testing"
)

// Measuring helpers for the sound design tests: decode an effect to samples with the host's ffmpeg and measure it in Go (levels,
// onset, band energies by an FFT, DC), so that the tests need no tool beyond ffmpeg.

// sfxSamples is a rendered effect: both channels at 48 kHz.
type sfxSamples struct {
	l, r []float64
}

func (s sfxSamples) mono() []float64 {
	out := make([]float64, len(s.l))
	for i := range out {
		out[i] = 0.5 * (s.l[i] + s.r[i])
	}
	return out
}

// rawGraphJitter is the effect as the recipe makes it (stereo, matched level), with the given jitter.
func rawGraphJitter(cat, variant string, j jitter, id string) (graph string, ms int64, ok bool) {
	res, ok := buildSFXJitter("fx0", cat, variant, j, id)
	if !ok {
		return "", 0, false
	}
	st := append([]string{}, res.stmts...)
	st = append(st, chain([]string{res.out}, []string{stereo48}, "aout"))
	return strings.Join(st, ";"), res.ms, true
}

// decodeGraph runs a graph that ends in [aout] and returns the stereo samples.
func decodeGraph(t testing.TB, ffmpeg, graph string) sfxSamples {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", //nolint:gosec // test helper
		"-filter_complex", graph, "-map", "[aout]", "-ac", "2", "-ar", "48000", "-f", "f32le", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, stderr.String())
	}
	data := stdout.Bytes()
	n := len(data) / 8
	s := sfxSamples{l: make([]float64, n), r: make([]float64, n)}
	for i := 0; i < n; i++ {
		s.l[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i*8:])))
		s.r[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i*8+4:])))
	}
	return s
}

func dbfs(x float64) float64 { return 20 * math.Log10(math.Max(x, 1e-9)) }

func peakOf(x []float64) float64 {
	p := 0.0
	for _, v := range x {
		p = math.Max(p, math.Abs(v))
	}
	return p
}

// rmsMaxDB is the loudest 50 ms RMS (hop 10 ms) of x in dBFS: the "level" of an effect, the number the loudness match uses.
func rmsMaxDB(x []float64) float64 {
	win, hop := 2400, 480
	best := 0.0
	for a := 0; a+win <= len(x) || a == 0; a += hop {
		b := min(a+win, len(x))
		sum := 0.0
		for _, v := range x[a:b] {
			sum += v * v
		}
		best = math.Max(best, math.Sqrt(sum/float64(max(b-a, 1))))
		if b == len(x) {
			break
		}
	}
	return dbfs(best)
}

func mean(x []float64) float64 {
	s := 0.0
	for _, v := range x {
		s += v
	}
	return s / float64(max(len(x), 1))
}

// onsetMs is the time of the first sample whose absolute value reaches floor (linear), in ms, or -1.
func onsetMs(x []float64, floor float64) float64 {
	for i, v := range x {
		if math.Abs(v) >= floor {
			return float64(i) / 48
		}
	}
	return -1
}

// fft is an in-place radix-2 FFT of a power-of-two length.
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(length)))
		for i := 0; i < n; i += length {
			wn := complex(1, 0)
			for k := 0; k < length/2; k++ {
				u, v := a[i+k], a[i+k+length/2]*wn
				a[i+k], a[i+k+length/2] = u+v, u-v
				wn *= w
			}
		}
	}
}

// bandEnergies returns the energy (linear power, Hann window) of x in each band [lo, hi) Hz of bands, over the whole signal.
func bandEnergies(x []float64, bands [][2]float64) []float64 {
	n := 1
	for n < len(x) {
		n <<= 1
	}
	a := make([]complex128, n)
	for i, v := range x {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(max(len(x)-1, 1)))
		a[i] = complex(v*w, 0)
	}
	fft(a)
	out := make([]float64, len(bands))
	for k := 1; k < n/2; k++ {
		hz := float64(k) * 48000 / float64(n)
		p := real(a[k])*real(a[k]) + imag(a[k])*imag(a[k])
		for i, b := range bands {
			if hz >= b[0] && hz < b[1] {
				out[i] += p
			}
		}
	}
	return out
}

// bandDB is bandEnergies in dB relative to the total energy of all the bands given.
func bandDB(x []float64, bands [][2]float64) []float64 {
	e := bandEnergies(x, bands)
	total := 0.0
	for _, v := range e {
		total += v
	}
	out := make([]float64, len(e))
	for i, v := range e {
		out[i] = 10 * math.Log10(math.Max(v/math.Max(total, 1e-30), 1e-12))
	}
	return out
}

// The bands of the design: sub, body, low-mid, mid, presence (transient) and air.
var designBands = [][2]float64{{20, 120}, {120, 800}, {800, 2000}, {2000, 6000}, {6000, 16000}}

// energyBetween is the RMS of x between a and b ms in dBFS.
func rmsBetweenDB(x []float64, aMs, bMs float64) float64 {
	a, b := int(aMs*48), min(int(bMs*48), len(x))
	if b <= a {
		return -120
	}
	sum := 0.0
	for _, v := range x[a:b] {
		sum += v * v
	}
	return dbfs(math.Sqrt(sum / float64(b-a)))
}

// tailMs is how long the effect stays within thresholdDB of its loudest 10 ms (RMS), in ms from the start: the length of its tail.
func tailMs(x []float64, thresholdDB float64) float64 {
	hop := 480
	var levels []float64
	best := -200.0
	for a := 0; a+hop <= len(x); a += hop {
		l := rmsBetweenDB(x, float64(a)/48, float64(a+hop)/48)
		levels = append(levels, l)
		best = math.Max(best, l)
	}
	last := 0
	for i, l := range levels {
		if l >= best-thresholdDB {
			last = i
		}
	}
	return float64(last+1) * 10
}
