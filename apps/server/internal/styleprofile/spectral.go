package styleprofile

import (
	"encoding/binary"
	"math"
	"math/cmplx"
	"slices"
)

// The spectral side of the sound: an onset curve from the spectral flux (a smooth track such as a pad or a bass line has few energy
// onsets, but its beats still move the spectrum), the tempo from it, and the spectral flatness (a tonal, musical sound has a low
// flatness, noise such as a room or a crowd a high one). Plain Go over a mono 8 kHz dump of the sound.

const (
	spectralRate = 8000
	fftSize      = 256
	hopSamples   = 80 // 10 ms at 8 kHz
	bandCount    = 16
	// maxSpectralSamples bounds the dump: MaxStyleSeconds of sound.
	maxSpectralSamples = 600 * spectralRate
)

// pcmFromF32 decodes little-endian float32 samples.
func pcmFromF32(raw []byte) []float32 {
	n := min(len(raw)/4, maxSpectralSamples)
	out := make([]float32, n)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out
}

// fft is an in-place radix-2 transform of a power-of-two length.
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

// spectrum is the result of the analysis of a dump: the onset curve (one value per 10 ms) and the spectral flatness median.
type spectrum struct {
	Flux     []float64
	Flatness float64
}

// analyseSpectrum computes the flux and the flatness of the dump.
func analyseSpectrum(pcm []float32) spectrum {
	var s spectrum
	if len(pcm) < fftSize*2 {
		return s
	}
	window := make([]float64, fftSize)
	for i := range window {
		window[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1))
	}
	// Band edges, log-spaced from 60 Hz to 3.8 kHz, in bins of rate/fftSize = 31.25 Hz.
	binHz := float64(spectralRate) / fftSize
	edges := make([]int, bandCount+1)
	for i := range edges {
		f := 60 * math.Pow(3800.0/60, float64(i)/bandCount)
		edges[i] = max(int(f/binHz), i+1)
		if i > 0 && edges[i] <= edges[i-1] {
			edges[i] = edges[i-1] + 1
		}
	}
	buf := make([]complex128, fftSize)
	prev := make([]float64, bandCount)
	var flat []float64
	for start := 0; start+fftSize <= len(pcm); start += hopSamples {
		for i := range buf {
			buf[i] = complex(float64(pcm[start+i])*window[i], 0)
		}
		fft(buf)
		var flux float64
		var logSum, linSum float64
		nb := 0
		for b := 0; b < bandCount; b++ {
			var e float64
			for k := edges[b]; k < edges[b+1] && k < fftSize/2; k++ {
				re, im := real(buf[k]), imag(buf[k])
				e += re*re + im*im
			}
			l := math.Log1p(e * 1e3)
			if d := l - prev[b]; d > 0 && start > 0 {
				flux += d
			}
			prev[b] = l
		}
		s.Flux = append(s.Flux, flux)
		// The flatness over the 100 Hz to 3.5 kHz bins of this frame: the geometric over the arithmetic mean of the power.
		for k := 3; k < 112; k++ {
			p := real(buf[k])*real(buf[k]) + imag(buf[k])*imag(buf[k]) + 1e-12
			logSum += math.Log(p)
			linSum += p
			nb++
		}
		if linSum > 1e-6 {
			flat = append(flat, math.Exp(logSum/float64(nb))/(linSum/float64(nb)))
		}
	}
	if len(flat) > 0 {
		slices.Sort(flat)
		s.Flatness = round2(median(flat))
	}
	return s
}

// tempoFromFlux estimates the tempo from the onset curve: the autocorrelation of the flux (with its local mean taken out) at the lags of
// 70 to 180 BPM, each scored with its doubles, and the confidence from how far the best lag stands out of the others. The estimate is
// ambiguous by a factor of two, as every tempo estimate is. The second result is the confidence, the third the beat phase in ms.
func tempoFromFlux(flux []float64) (bpm, conf, phaseMs float64) {
	n := len(flux)
	if n < 300 {
		return 0, 0, 0
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
	var energy float64
	for _, v := range w {
		energy += v * v
	}
	if energy == 0 {
		return 0, 0, 0
	}
	ac := func(lag int) float64 {
		var s float64
		for i := 0; i+lag < n; i++ {
			s += w[i] * w[i+lag]
		}
		return s / energy
	}
	type cand struct{ bpm, score float64 }
	var cands []cand
	var total float64
	for b := 70; b <= 180; b++ {
		lag := int(math.Round(6000 / float64(b)))
		score := ac(lag) + 0.5*ac(2*lag)
		if 4*lag < n {
			score += 0.25 * ac(4*lag)
		}
		cands = append(cands, cand{float64(b), score})
		total += math.Max(score, 0)
	}
	best := cands[0]
	for _, c := range cands {
		if c.score > best.score {
			best = c
		}
	}
	if best.score <= 0 || total == 0 {
		return 0, 0, 0
	}
	avg := total / float64(len(cands))
	conf = math.Max(0, math.Min(1, (best.score-avg)/(best.score+avg)))
	// The phase: where the beats sit modulo the period, from the flux weighted by a cosine of the period.
	period := 6000 / best.bpm // in 10 ms steps
	var re, im float64
	for i, v := range w {
		ang := 2 * math.Pi * float64(i) / period
		re += v * math.Cos(ang)
		im += v * math.Sin(ang)
	}
	phase := math.Atan2(im, re) / (2 * math.Pi) * period * 10
	return round1(best.bpm), round2(conf), phase
}
