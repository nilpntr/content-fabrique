package render

import (
	"fmt"
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The recipes of the sound effects: one function per category, one parameter row per variant. See sfx_dsp.go for the chain and
// docs/design/audio-v1.md for the design. All frequencies are Hz, times seconds and levels dB (relative to the layer's own
// full scale) unless a name says otherwise; the numbers are starting values from earshot's motion-sfx notes (MIT, see
// sfx_dsp.go), tuned by measuring the result (the tests in sfx_design_test.go pin the shape of every variant).

// sfxTargetDB is the level every variant of a category is matched to, before the base level (sfxBaseDB) and the item's gain: the
// loudest 50 ms RMS of the effect (neutral jitter) in dBFS. The categories differ on purpose: an impact or a sting is the loudest
// accent, a riser builds to nearly as much, a whoosh sits under the picture, and a click or a pop is detail, 10 to 15 dB under a
// hit (earshot, launch-video-sound.md section 5 and motion-sfx-techniques.md section 13). Within a category every variant is the
// same level, so a riser does not blast after a click and a deep impact is not quieter than a punch.
var sfxTargetDB = map[string]float64{
	timeline.SFXImpact: -11, timeline.SFXSting: -11, timeline.SFXSubDrop: -10, timeline.SFXRiser: -11, timeline.SFXDownlifter: -12,
	timeline.SFXWhoosh: -12, timeline.SFXGlitch: -13, timeline.SFXShimmer: -15, timeline.SFXSwish: -14, timeline.SFXPop: -17, timeline.SFXClick: -21,
}

// sfxVariantKey is the key of a variant in the tables.
func sfxVariantKey(cat, variant string) string { return cat + "/" + variant }

// sfxResult is a recipe's output: the statements, the label of its mono or stereo result (matched level, cut to its length and
// faded) and the length in ms.
type sfxResult struct {
	stmts []string
	out   string
	ms    int64
}

// buildSFX builds one effect's statements. prefix makes the labels unique in the graph; id drives the jitter. It returns false
// for a category or variant that does not exist.
func buildSFX(prefix, cat, variant, id string) (sfxResult, bool) {
	return buildSFXJitter(prefix, cat, variant, newJitter(id), id)
}

// neutralJitter is no jitter at all: what the loudness match is measured with.
var neutralJitter = jitter{pitch: 1, filter: 1, length: 1}

// buildSFXJitter is buildSFX with the jitter given (the tests use neutralJitter). id still seeds the shimmer's pings.
func buildSFXJitter(prefix, cat, variant string, j jitter, id string) (sfxResult, bool) {
	if _, ok := timeline.SFXVariantLengthMs(cat, variant); !ok {
		return sfxResult{}, false
	}
	b := &sfxBuilder{prefix: prefix, j: j, id: id}
	var out string
	var d float64
	switch cat {
	case timeline.SFXWhoosh:
		out, d = b.whoosh(variant)
	case timeline.SFXSwish:
		out, d = b.swish(variant)
	case timeline.SFXImpact:
		out, d = b.impact(variant)
	case timeline.SFXRiser:
		out, d = b.riser(variant)
	case timeline.SFXDownlifter:
		out, d = b.downlifter(variant)
	case timeline.SFXSubDrop:
		out, d = b.subDrop(variant)
	case timeline.SFXPop:
		out, d = b.pop(variant)
	case timeline.SFXClick:
		out, d = b.click(variant)
	case timeline.SFXGlitch:
		out, d = b.glitch(variant)
	case timeline.SFXShimmer:
		out, d = b.shimmer(variant)
	case timeline.SFXSting:
		out, d = b.sting(variant)
	default:
		return sfxResult{}, false
	}
	// The loudness match and the item's level jitter, then the cut: the effect is exactly its length and ends on a short fade.
	level := sfxLevelDB[sfxVariantKey(cat, variant)] + b.j.levelDB
	fade := math.Min(0.02, d/4)
	tail := []string{gain(level), b.bus, "highpass=f=22", "atrim=end=" + f(d), "asetpts=PTS-STARTPTS", fmt.Sprintf("afade=t=out:st=%s:d=%s", f(d-fade), f(fade))}
	out = b.add([]string{out}, tail...)
	return sfxResult{stmts: b.stmts, out: out, ms: int64(math.Round(d * 1000))}, true
}

// length is the natural length of a variant in seconds, varied by the item's jitter where the sound has a free tail (a riser
// peaks where it ends, so it is exact).
func (b *sfxBuilder) length(cat, variant string, jittered bool) float64 {
	ms, _ := timeline.SFXVariantLengthMs(cat, variant)
	d := float64(ms) / 1000
	if jittered {
		d *= b.j.length
	}
	return math.Round(d*1000) / 1000
}

// glue records the bus stage of the effect and returns the sum unchanged: a compressor that ties the layers together (2:1 to 4:1,
// 10-30 ms attack so that the transient gets through, 60-150 ms release, a few dB of gain reduction), then the high-pass at 22 Hz
// that keeps energy below hearing out of the headroom. buildSFXJitter adds it after the loudness match, so that the compressor
// sees the level the effect is mixed at and its threshold means something.
func (b *sfxBuilder) glue(sum string, ratio, attackMs, releaseMs float64) string {
	return b.glueAt(sum, 0.3, ratio, attackMs, releaseMs)
}

// glueAt is glue with the threshold (linear) given: a riser's build is the effect, so its bus compressor only takes the top.
func (b *sfxBuilder) glueAt(sum string, threshold, ratio, attackMs, releaseMs float64) string {
	b.bus = fmt.Sprintf("acompressor=threshold=%s:ratio=%s:attack=%s:release=%s:makeup=1:knee=2.8", f(threshold), f(ratio), f(attackMs), f(releaseMs))
	return sum
}

// stereo makes a layer stereo by centring it (a mono layer upmixed), so that stereo layers (the wide ones) mix with it.
func (b *sfxBuilder) stereo(sig string) string {
	return b.add([]string{sig}, "aformat=channel_layouts=stereo")
}

// wide gives a layer width with a Haas delay: the right channel is the left one delayed by ms (2 to 50 ms fuses into one
// sound, not an echo). Only air and tail layers get it; the transient and the sub stay centred, so the mono fold-down keeps them.
func (b *sfxBuilder) wide(sig string, ms float64) string {
	l, r, r2, out := b.label(), b.label(), b.label(), b.label()
	b.stmts = append(b.stmts,
		fmt.Sprintf("[%s]asplit=2[%s][%s]", sig, l, r),
		fmt.Sprintf("[%s]adelay=%s:all=1[%s]", r, f(ms), r2),
		fmt.Sprintf("[%s][%s]join=inputs=2:channel_layout=stereo[%s]", l, r2, out))
	return out
}

// bell is the position-in-time curve of a pass-by: 0 at the start, 1 at the peak (at fraction peak of the length), 0 at the end.
// It rises along an accelerating curve and falls faster, like the speed of an object going past: sin(pi x^k)^2 with
// x^k = 0.5 at the peak.
func bell(x, peak float64) float64 {
	k := math.Log(0.5) / math.Log(peak)
	s := math.Sin(math.Pi * math.Pow(x, k))
	return s * s
}

// bellExpr is bell as an expression of t, for an envelope over d seconds.
func bellExpr(d, peak float64) string {
	k := math.Log(0.5) / math.Log(peak)
	return fmt.Sprintf("pow(sin(PI*pow(t/%s,%s)),2)", f(d), f(k))
}

// ---- whoosh and swish -----------------------------------------------------------------------------------------------------

// passFloor is the linear level of a pass at its start relative to its peak (-30 dB).
const passFloor = 0.03

// passSpec describes an air pass: two noise bands that move together, an optional low weight, a short room on the air.
type passSpec struct {
	d, peak    float64 // length and where the peak is, as a fraction of it
	lo, hi     float64 // the band centre goes from lo up to hi at the peak and back
	q          float64
	color      string
	second     float64 // the second band is at second x the first (0: none), 4 dB down
	weight     float64 // a sine under it that swells with the peak (0: none), Hz at the peak
	weightDB   float64
	roomTau    float64 // 0: no room
	roomDB     float64
	drive      float64 // saturation of the air in dB (0: none)
	whistle    float64 // a narrow band at this centre (0: none), for a swish
	whistleDB  float64
	widenMs    float64
	glueRatio  float64
	airDB      float64
	hpAir      float64
	sweepSteps int
}

func (b *sfxBuilder) pass(cat, variant string, s passSpec, jittered bool) (string, float64) {
	d := b.length(cat, variant, jittered)
	s.d = d
	// The envelope starts at -30 dB below the peak, not at silence: the effect is audible from its first milliseconds (a hush that
	// swells into the pass), so that its onset is where at_ms says (within a frame) and not 40 ms later.
	env := fmt.Sprintf("%s*(%s+%s*%s)*%s", f(0.9), f(passFloor), f(1-passFloor), bellExpr(d, s.peak), attack(3))
	var layers []string
	band := func(id string, ratio, db float64, seed int) string {
		sw := b.sweep("bandpass", id, d, s.sweepSteps, s.q, func(x float64) float64 {
			return s.lo * math.Pow(s.hi/s.lo, bell(x, s.peak)) * ratio
		})
		n := b.noise(s.color, seed, d, append(sw, gain(db))...)
		return b.shape(n, env, d)
	}
	air := band(b.prefix+"a", 1, s.airDB, 11)
	if s.second > 0 {
		air2 := band(b.prefix+"b", s.second, s.airDB-4, 12)
		air = b.mix("", air, air2)
	}
	post := []string{hpf(s.hpAir)}
	if s.drive > 0 {
		post = append(post, saturate(s.drive))
	}
	air = b.add([]string{air}, post...)
	dry := air
	if s.whistle > 0 {
		w := b.noise("white", 13, d, bpf(s.whistle*b.j.filter, 25), gain(s.whistleDB))
		dry = b.mix("", dry, b.shape(w, env, d))
	}
	var wet string
	if s.roomTau > 0 {
		dry, wet = b.room(dry, s.roomTau*2.5, s.roomTau, 12, 3500)
		wet = b.add([]string{wet}, gain(s.roomDB))
	}
	air = dry
	if s.widenMs > 0 {
		air = b.wide(air, s.widenMs)
		if wet != "" {
			wet = b.wide(wet, s.widenMs*1.6)
		}
	} else {
		air = b.stereo(air)
		if wet != "" {
			wet = b.stereo(wet)
		}
	}
	layers = append(layers, air)
	if wet != "" {
		layers = append(layers, wet)
	}
	if s.weight > 0 {
		hum := b.tone(fmt.Sprintf("%s*sin(2*PI*(%s*t+%s*t*t/%s))*%s", f(math.Pow(10, s.weightDB/20)), f(s.weight*0.7*b.j.pitch), f(s.weight*0.15*b.j.pitch), f(d), bellExpr(d, s.peak)), d, lpf(160))
		layers = append(layers, b.stereo(hum))
	}
	sum := b.mix("", layers...)
	return b.glue(sum, s.glueRatio, 15, 100), d
}

func (b *sfxBuilder) whoosh(variant string) (string, float64) {
	s := map[string]passSpec{
		"air":   {peak: 0.55, lo: 450, hi: 3800, q: 1.4, color: "pink", second: 1.6, roomTau: 0.12, roomDB: -12, widenMs: 9, glueRatio: 2, airDB: 16, hpAir: 250, sweepSteps: 30},
		"fast":  {peak: 0.6, lo: 700, hi: 5500, q: 1.8, color: "pink", second: 1.6, roomTau: 0.08, roomDB: -14, widenMs: 7, glueRatio: 2, airDB: 18, hpAir: 350, sweepSteps: 24},
		"heavy": {peak: 0.5, lo: 200, hi: 2200, q: 1.0, color: "pink", second: 1.6, weight: 70, weightDB: -9, roomTau: 0.2, roomDB: -9, widenMs: 12, glueRatio: 3, airDB: 4, hpAir: 120, sweepSteps: 40},
	}[variant]
	return b.pass(timeline.SFXWhoosh, variant, s, true)
}

func (b *sfxBuilder) swish(variant string) (string, float64) {
	s := map[string]passSpec{
		"thin":  {peak: 0.55, lo: 2200, hi: 7000, q: 1.2, color: "white", second: 1.5, whistle: 3100, whistleDB: -14, roomTau: 0, widenMs: 5, glueRatio: 2, airDB: 0, hpAir: 1200, sweepSteps: 20},
		"blade": {peak: 0.5, lo: 3500, hi: 9000, q: 1.5, color: "white", second: 1.4, whistle: 5200, whistleDB: -9, roomTau: 0, widenMs: 4, glueRatio: 2, airDB: 0, hpAir: 2000, sweepSteps: 16},
		"cloth": {peak: 0.55, lo: 900, hi: 3600, q: 0.9, color: "pink", second: 1.7, roomTau: 0.05, roomDB: -16, widenMs: 6, glueRatio: 2, airDB: 12, hpAir: 500, sweepSteps: 24},
	}[variant]
	return b.pass(timeline.SFXSwish, variant, s, true)
}

// ---- impact, sub drop and the hit of a sting -------------------------------------------------------------------------------

// hitSpec describes a hit: a transient, a body (a pitched sine, a noise band or a metallic cloud), a sub with a pitch drop and a
// short dark room on the body.
type hitSpec struct {
	d float64 // set by the caller from the variant

	trHP, trTau, trDB, trDrive float64 // the transient: noise burst above trHP, decaying with trTau, drive in dB
	trBand                     float64 // a second burst band centre (0: none), for a crack

	bodyF0, bodyF1, bodyTau, bodyDecay, bodyDB, bodyDrive float64 // the body sine (bodyDB < -40: none)
	noiseBand, noiseQ, noiseTau, noiseDB                  float64 // the body's noise band (noiseDB < -40: none)
	metal                                                 float64 // f0 of a cloud of inharmonic partials replacing the sine (0: none)

	subF0, subF1, subTau, subDecay, subDB float64 // the sub: clean, never saturated (subDB < -40: none)
	subHold                               float64 // flat for this long before it decays

	roomTau, roomDB, roomPreMs float64 // the room on the body (roomTau 0: none)
	glueRatio                  float64
}

const none = -60.0

// hit builds the layers of a hit of length hs.d and returns the unfinished sum (stereo) and the dry layers it needs the caller to
// know nothing about.
func (b *sfxBuilder) hit(hs hitSpec) string {
	d := hs.d
	var layers []string

	// The transient: a noise burst, its tail shortened (env to a power), above 1-2 kHz. It carries the timing.
	if hs.trHP > 0 {
		td := math.Min(d, 0.3)
		n := b.noise("white", 7, td, hpf(hs.trHP*b.j.filter))
		trPost := []string{gain(hs.trDB)}
		if hs.trDrive > 0 {
			trPost = append([]string{saturate(hs.trDrive)}, trPost...)
		}
		tr := b.shape(n, fmt.Sprintf("%s*%s", attack(0.3), decay(hs.trTau)), td, trPost...)
		if hs.trBand > 0 {
			n2 := b.noise("white", 8, td, bpf(hs.trBand*b.j.filter, 1.4))
			tr = b.mix("", tr, b.shape(n2, fmt.Sprintf("%s*%s", attack(0.3), decay(hs.trTau*1.6)), td, gain(hs.trDB-3)))
		}
		layers = append(layers, b.stereo(tr))
	}

	// The body: weight and character; saturated lightly, banded 70 Hz to 900 Hz (a metal body is open above).
	var body []string
	if hs.bodyF0 > 0 || hs.metal > 0 {
		var tone string
		if hs.metal > 0 {
			var terms []string
			for i, p := range []struct{ r, a float64 }{{1, 1}, {1.59, 0.7}, {2.14, 0.5}, {2.30, 0.5}, {2.65, 0.35}, {3.51, 0.25}} {
				terms = append(terms, fmt.Sprintf("%s*sin(2*PI*%s*t)*exp(-t/%s)", f(p.a), f(hs.metal*p.r*b.j.pitch), f(hs.bodyDecay/(1+0.5*float64(i)))))
			}
			tone = b.tone(fmt.Sprintf("0.9*%s*(%s)", attack(0.5), strings.Join(terms, "+")), d, hpf(90), lpf(5000))
		} else {
			tone = b.tone(fmt.Sprintf("%s*sin(%s)*%s", attack(0.5), b.glide(hs.bodyF0, hs.bodyF1, hs.bodyTau), decay(hs.bodyDecay)), d, hpf(80), lpf(1200))
		}
		body = append(body, b.add([]string{tone}, saturate(hs.bodyDrive), gain(hs.bodyDB)))
	}
	if hs.noiseBand > 0 {
		n := b.noise("pink", 9, d, bpf(hs.noiseBand*b.j.filter, hs.noiseQ))
		body = append(body, b.shape(n, fmt.Sprintf("%s*%s", attack(1), decay(hs.noiseTau)), d, gain(hs.noiseDB)))
	}
	if len(body) > 0 {
		bodyMix := b.mix("", body...)
		if hs.roomTau > 0 {
			dryBody, wet := b.room(bodyMix, hs.roomTau*2.5, hs.roomTau, hs.roomPreMs, 3500)
			bodyMix = dryBody
			layers = append(layers, b.wide(b.add([]string{wet}, gain(hs.roomDB)), 11))
		}
		layers = append(layers, b.stereo(bodyMix))
	}

	// The sub: a clean sine that drops from subF0 to subF1; no saturation, no room, centred. A 3 ms ramp starts it at a zero
	// crossing, and the decay is exponential after the hold.
	if hs.subF0 > 0 {
		env := fmt.Sprintf("%s*%s", attack(3), decay(hs.subDecay))
		if hs.subHold > 0 {
			env = fmt.Sprintf("%s*exp(-max(0,t-%s)/%s)", attack(3), f(hs.subHold), f(hs.subDecay))
		}
		sub := b.tone(fmt.Sprintf("%s*sin(%s)", env, b.glide(hs.subF0, hs.subF1, hs.subTau)), d, lpf(160), gain(hs.subDB))
		layers = append(layers, b.stereo(sub))
	}
	sum := b.mix("", layers...)
	return b.glue(sum, hs.glueRatio, 20, 100)
}

func (b *sfxBuilder) impact(variant string) (string, float64) {
	d := b.length(timeline.SFXImpact, variant, true)
	hs := map[string]hitSpec{
		"punch": {trHP: 1800, trTau: 0.006, trDB: 0, trDrive: 4, bodyF0: 180, bodyF1: 98, bodyTau: 0.045, bodyDecay: 0.14, bodyDB: -3, bodyDrive: 6,
			noiseBand: 260, noiseQ: 0.8, noiseTau: 0.09, noiseDB: -9,
			subF0: 95, subF1: 42, subTau: 0.11, subDecay: 0.26, subDB: -3, subHold: 0.03, roomTau: 0.18, roomDB: -11, roomPreMs: 14, glueRatio: 3},
		"deep": {trHP: 1200, trTau: 0.008, trDB: -6, trDrive: 3, bodyF0: 140, bodyF1: 84, bodyTau: 0.07, bodyDecay: 0.22, bodyDB: -3, bodyDrive: 6,
			noiseBand: 200, noiseQ: 0.8, noiseTau: 0.14, noiseDB: -9,
			subF0: 75, subF1: 34, subTau: 0.16, subDecay: 0.5, subDB: -1, subHold: 0.08, roomTau: 0.3, roomDB: -10, roomPreMs: 18, glueRatio: 3},
		"crack": {trHP: 2500, trTau: 0.012, trDB: 0, trDrive: 5, trBand: 1400, bodyF0: 260, bodyF1: 130, bodyTau: 0.03, bodyDecay: 0.08, bodyDB: -7, bodyDrive: 5,
			noiseBand: 500, noiseQ: 0.9, noiseTau: 0.06, noiseDB: -10,
			subF0: 80, subF1: 50, subTau: 0.05, subDecay: 0.12, subDB: -9, roomTau: 0.1, roomDB: -13, roomPreMs: 10, glueRatio: 2.5},
		"metal": {trHP: 2000, trTau: 0.008, trDB: -3, trDrive: 3, metal: 190, bodyDecay: 0.45, bodyDB: -3, bodyDrive: 3,
			noiseBand: 1500, noiseQ: 0.8, noiseTau: 0.05, noiseDB: -14,
			subF0: 80, subF1: 44, subTau: 0.1, subDecay: 0.3, subDB: -5, subHold: 0.02, roomTau: 0.3, roomDB: -9, roomPreMs: 16, glueRatio: 3},
	}[variant]
	hs.d = d
	return b.hit(hs), d
}

func (b *sfxBuilder) subDrop(variant string) (string, float64) {
	d := b.length(timeline.SFXSubDrop, variant, true)
	hs := map[string]hitSpec{
		// A sub drop is mostly sub: a clean sine gliding down (the glide a fraction of a second to a few seconds), a saturated
		// copy of its octave above so that a phone can hear it, and a tiny tick so that the onset is exact.
		"boom":  {trHP: 3500, trTau: 0.003, trDB: -18, trDrive: 0, bodyF0: 220, bodyF1: 76, bodyTau: 0.25, bodyDecay: 0.45, bodyDB: -10, bodyDrive: 9, subF0: 110, subF1: 38, subTau: 0.25, subDecay: 0.42, subDB: 0, subHold: 0.25, glueRatio: 2},
		"quick": {trHP: 3500, trTau: 0.003, trDB: -16, trDrive: 0, bodyF0: 180, bodyF1: 90, bodyTau: 0.08, bodyDecay: 0.18, bodyDB: -10, bodyDrive: 9, subF0: 90, subF1: 45, subTau: 0.08, subDecay: 0.16, subDB: 0, glueRatio: 2},
		"long":  {trHP: 3500, trTau: 0.003, trDB: -20, trDrive: 0, bodyF0: 160, bodyF1: 64, bodyTau: 0.5, bodyDecay: 0.8, bodyDB: -10, bodyDrive: 9, subF0: 80, subF1: 32, subTau: 0.5, subDecay: 0.75, subDB: 0, subHold: 0.5, glueRatio: 2},
	}[variant]
	hs.d = d
	return b.hit(hs), d
}

// ---- riser and downlifter -----------------------------------------------------------------------------------------------

// riserSpec describes a rise: a noise band whose centre climbs along an exponential curve, a tonal rise, an accelerating
// tremolo, and a level that grows on the same curve.
type riserSpec struct {
	lo, hi, q  float64 // the band centre goes lo -> hi along lo (hi/lo)^(x^power)
	power      float64 // 2 to 3: most of the change lands in the last third
	noiseDB    float64
	tonal      float64 // start Hz of the tonal rise (0: none)
	semitones  float64 // how far it rises, along x^power
	toneDB     float64
	voices     int // detuned voices
	trem0      float64
	trem1      float64 // tremolo rate in Hz at the start and at the end (0: none)
	tremDepth  float64
	roomTau    float64
	roomDB     float64
	widenMs    float64
	levelFloor float64 // the level at the start, linear (0.1 is -20 dB)
	hpNoise    float64
}

func (b *sfxBuilder) riserBody(d float64, s riserSpec) string {
	// The level follows the curve too, in dB: from levelFloor (-20 dB for 0.1) up to 0 dB along x^power, so that it accelerates
	// as it is heard (a linear amplitude that grows as x^p flattens out in dB: most of the rise would be in the middle).
	lev := fmt.Sprintf("exp(-%s*(1-pow(t/%s,%s)))", f(-math.Log(s.levelFloor)), f(d), f(s.power))
	var layers []string
	if s.noiseDB > none {
		sw := b.sweep("bandpass", b.prefix+"r", d, int(math.Max(24, d*30)), s.q, func(x float64) float64 {
			return s.lo * math.Pow(s.hi/s.lo, math.Pow(x, s.power))
		})
		n := b.noise("pink", 5, d, append(sw, hpf(s.hpNoise), gain(s.noiseDB))...)
		env := lev
		if s.trem1 > 0 {
			// Accelerating tremolo: the rate climbs from trem0 to trem1 as x^2, the phase is its integral, and the depth grows with
			// the rise. The phase is offset so that the tremolo is at a crest where the riser ends: the peak is at the end.
			cycles := s.trem0*d + (s.trem1-s.trem0)*d/3
			offset := -math.Pi/2 - 2*math.Pi*cycles
			phase := fmt.Sprintf("2*PI*(%s*t+%s*t*t*t/(%s*%s))+%s", f(s.trem0), f((s.trem1-s.trem0)/3), f(d), f(d), f(offset))
			env = fmt.Sprintf("%s*(1-%s*t/%s*(0.5+0.5*sin(%s)))", lev, f(s.tremDepth), f(d), phase)
		}
		n = b.shape(n, env, d)
		layers = append(layers, n)
	}
	if s.tonal > 0 {
		var voices []string
		for v := 0; v < s.voices; v++ {
			cents := (float64(v) - float64(s.voices-1)/2) * 12 // detune 12 cents apart
			f0 := s.tonal * math.Pow(2, cents/1200) * b.j.pitch
			// f(t) = f0 * 2^(semitones/12 * x^power): the phase is the integral, which pitchPhase writes as a series.
			voices = append(voices, fmt.Sprintf("sin(%s)", pitchPhase(f0, s.semitones, d, s.power)))
		}
		tone := b.tone(fmt.Sprintf("%s*(%s)*%s", f(1/float64(s.voices)), strings.Join(voices, "+"), lev), d, hpf(150), gain(s.toneDB))
		layers = append(layers, tone)
	}
	var dry string
	if len(layers) == 1 {
		dry = layers[0]
	} else {
		dry = b.mix("", layers...)
	}
	var out []string
	if s.roomTau > 0 {
		var wet string
		dry, wet = b.room(dry, s.roomTau*2.5, s.roomTau, 16, 3500)
		out = append(out, b.wide(b.add([]string{wet}, gain(s.roomDB)), 14))
	}
	if s.widenMs > 0 {
		out = append(out, b.wide(dry, s.widenMs))
	} else {
		out = append(out, b.stereo(dry))
	}
	return b.mix("", out...)
}

// pitchPhase is the phase in radians of a sine whose frequency rises from f0 by `semitones` along x^power, x = t/d:
// f(t) = f0 * 2^(semitones/12 * x^power). The integral has no closed form for a power other than 1, so the exponent
// is expanded: 2^(a x^p) = sum of (a ln2 x^p)^k / k!, which is exact enough (four terms) for a up to 2 octaves, and
// integrates term by term.
func pitchPhase(f0, semitones, d, power float64) string {
	a := semitones / 12 * math.Ln2 // 2^(semitones/12 x^p) = e^(a x^p)
	// integral over 0..t of e^(a (s/d)^p) ds = d * sum_k a^k/k! * (t/d)^(kp+1) / (kp+1)
	var terms []string
	fact := 1.0
	for k := 0; k <= 5; k++ {
		if k > 0 {
			fact *= float64(k)
		}
		c := math.Pow(a, float64(k)) / fact / (float64(k)*power + 1)
		terms = append(terms, fmt.Sprintf("%s*pow(t/%s,%s)", f(c), f(d), f(float64(k)*power+1)))
	}
	return fmt.Sprintf("2*PI*%s*%s*(%s)", f(f0), f(d), strings.Join(terms, "+"))
}

func (b *sfxBuilder) riser(variant string) (string, float64) {
	d := b.length(timeline.SFXRiser, variant, false)
	s := map[string]riserSpec{
		"tension": {lo: 160, hi: 9000, q: 1.6, power: 2.4, noiseDB: 0, tonal: 220, semitones: 24, toneDB: -6, voices: 3, trem0: 1, trem1: 8, tremDepth: 0.35, roomTau: 0.25, roomDB: -12, widenMs: 10, levelFloor: 0.1, hpNoise: 150},
		"sweep":   {lo: 200, hi: 10000, q: 2.2, power: 2.0, noiseDB: 24, trem0: 2, trem1: 12, tremDepth: 0.5, roomTau: 0.15, roomDB: -14, widenMs: 8, levelFloor: 0.08, hpNoise: 150},
		"pitch":   {lo: 400, hi: 6000, q: 1.2, power: 2.6, noiseDB: -12, tonal: 196, semitones: 24, toneDB: 0, voices: 4, trem0: 0, trem1: 0, roomTau: 0.3, roomDB: -11, widenMs: 12, levelFloor: 0.12, hpNoise: 200},
		"long":    {lo: 140, hi: 8000, q: 1.5, power: 3.0, noiseDB: 0, tonal: 165, semitones: 24, toneDB: -7, voices: 3, trem0: 0.8, trem1: 7, tremDepth: 0.4, roomTau: 0.4, roomDB: -11, widenMs: 14, levelFloor: 0.06, hpNoise: 150},
	}[variant]
	return b.glueAt(b.riserBody(d, s), 0.9, 1.5, 25, 150), d
}

func (b *sfxBuilder) downlifter(variant string) (string, float64) {
	d := b.length(timeline.SFXDownlifter, variant, true)
	// The mirror of a riser: fast at first, slowing at the end (an exponential decay with a time constant of a third of the
	// length), the filter closing from a bright top to a dark floor and the pitch falling; a wetter room as the dry part dies.
	type dl struct {
		hi, lo, q        float64
		noiseDB          float64
		tonal, semitones float64
		toneDB           float64
		roomTau, roomDB  float64
		tape             bool
	}
	s := map[string]dl{
		"fall":  {hi: 10000, lo: 300, q: 1.4, noiseDB: 8, tonal: 880, semitones: -24, toneDB: -1, roomTau: 0.25, roomDB: -9},
		"short": {hi: 9000, lo: 500, q: 1.6, noiseDB: 10, tonal: 1320, semitones: -19, toneDB: 0, roomTau: 0.12, roomDB: -12},
		"tape":  {noiseDB: none, tonal: 600, semitones: -36, toneDB: 0, roomTau: 0.15, roomDB: -12, tape: true},
	}[variant]
	tau := d / 3
	lev := fmt.Sprintf("exp(-t/%s)", f(tau))
	var layers []string
	if s.noiseDB > none {
		sw := b.sweep("bandpass", b.prefix+"d", d, int(math.Max(24, d*30)), s.q, func(x float64) float64 {
			// decelerating: the centre follows e^(-3x) from hi to lo
			return s.lo + (s.hi-s.lo)*math.Exp(-3*x)
		})
		n := b.noise("pink", 6, d, append(sw, hpf(120), gain(s.noiseDB))...)
		layers = append(layers, b.shape(n, fmt.Sprintf("%s*%s", attack(2), lev), d))
	}
	if s.tonal > 0 {
		var phase string
		if s.tape {
			// Tape stop: the playback rate glides to zero: f(t) = f0 (1 - x)^1.6 over d, the phase its integral.
			f0 := s.tonal * b.j.pitch
			phase = fmt.Sprintf("2*PI*%s*%s*(1-pow(1-t/%s,2.6))/2.6", f(f0), f(d), f(d))
		} else {
			// The pitch falls along the decelerating curve (see decelPhase).
			phase = decelPhase(s.tonal*b.j.pitch, s.semitones, d)
		}
		env := fmt.Sprintf("%s*%s", attack(2), lev)
		if s.tape {
			env = fmt.Sprintf("%s*pow(1-t/%s,0.6)", attack(2), f(d))
		}
		tone := b.tone(fmt.Sprintf("%s*(sin(%s)+0.35*sin(2*(%s)))", env, phase, phase), d, hpf(100), saturate(4), gain(s.toneDB))
		layers = append(layers, tone)
	}
	dry := b.mix("", layers...)
	dry, wet := b.room(dry, s.roomTau*2.5, s.roomTau, 14, 3000)
	out := []string{b.wide(dry, 9), b.wide(b.add([]string{wet}, gain(s.roomDB)), 13)}
	return b.glue(b.mix("", out...), 2.5, 20, 120), d
}

// decelPhase is the phase of a sine whose frequency falls from f0 by -semitones (semitones < 0) along the decelerating curve
// 1 - e^(-3 x), x = t/d: f(t) = f0 2^(semitones/12 (1 - e^(-3x))). With a = semitones ln2 / 12, f = f0 e^(a) e^(-a e^(-3x)),
// expanded in powers of e^(-3x) (six terms): integrated term by term.
func decelPhase(f0, semitones, d float64) string {
	a := semitones / 12 * math.Ln2
	// f(t) = f0 * exp(a) * sum_k (-a)^k / k! * e^(-3 k x); integral over 0..t = f0 exp(a) d * [x + sum_{k>=1} (-a)^k/k! (1 - e^(-3 k x)) / (3 k)]
	terms := []string{"t/" + f(d)}
	fact := 1.0
	for k := 1; k <= 6; k++ {
		fact *= float64(k)
		c := math.Pow(-a, float64(k)) / fact / (3 * float64(k))
		terms = append(terms, fmt.Sprintf("%s*(1-exp(-%s*t/%s))", f(c), f(3*float64(k)), f(d)))
	}
	return fmt.Sprintf("2*PI*%s*%s*(%s)", f(f0*math.Exp(a)), f(d), strings.Join(terms, "+"))
}

// ---- pop and click ----------------------------------------------------------------------------------------------------------

func (b *sfxBuilder) pop(variant string) (string, float64) {
	d := b.length(timeline.SFXPop, variant, true)
	type pp struct {
		f0, f1, tau float64 // the bubble's pitch rises from f0 to f1 with time constant tau
		decay       float64
		tick        float64 // the noise tick's centre
		tickDB      float64
		daughter    bool
	}
	s := map[string]pp{
		"bubble": {f0: 420, f1: 1150, tau: 0.022, decay: 0.05, tick: 3000, tickDB: -9},
		"tick":   {f0: 900, f1: 2300, tau: 0.012, decay: 0.022, tick: 3800, tickDB: -8},
		"double": {f0: 380, f1: 1050, tau: 0.02, decay: 0.05, tick: 2800, tickDB: -9, daughter: true},
	}[variant]
	// A bubble: an upward-gliding sine (f(t) = f1 - (f1 - f0) e^(-t/tau): glide with f0 and f1 swapped), 0.7 ms attack (a perfect
	// step reads as a digital tick), an exponential decay, a 1-2 ms band-passed tick on the onset.
	bub := fmt.Sprintf("%s*sin(%s)*%s", attack(0.7), b.glide(s.f0, s.f1, s.tau), decay(s.decay))
	if s.daughter {
		// A smaller, higher ring 9 ms later: a daughter bubble.
		bub += fmt.Sprintf("+0.45*if(gte(t,0.009),min(1,(t-0.009)/0.0007)*sin(%s)*exp(-(t-0.009)/%s),0)", b.glide(s.f0*1.5, s.f1*1.5, s.tau), f(s.decay*0.8))
	}
	tone := b.tone(bub, d, hpf(150), lpf(4500))
	n := b.noise("white", 3, 0.01, bpf(s.tick*b.j.filter, 2))
	tick := b.shape(n, fmt.Sprintf("%s*exp(-t/0.0012)", attack(0.2)), 0.01, gain(s.tickDB))
	sum := b.mix("", b.stereo(tone), b.stereo(tick))
	return b.glue(sum, 2, 5, 60), d
}

// clickMakeupDB lifts the noise clicks, which a narrow band-pass leaves very quiet, to a level where they can be balanced against
// the tonal blip.
const clickMakeupDB = 20

func (b *sfxBuilder) click(variant string) (string, float64) {
	d := b.length(timeline.SFXClick, variant, false)
	// Farnell's click (earshot, section 6): white noise through a band-pass, an envelope that rises in 1 ms and falls in about
	// 20 ms, squared before it multiplies the noise. A push button is several clicks; a tonal blip is a short sine with a
	// tactile click under it.
	clickAt := func(at, hz, q, tau float64, db float64) string {
		db += clickMakeupDB
		n := b.noise("white", 14+int(at*1000)%7, d, bpf(hz*b.j.filter, q))
		env := fmt.Sprintf("if(gte(t,%s),pow(min(1,(t-%s)/0.001),1)*pow(exp(-(t-%s)/%s),2),0)", f(at), f(at), f(at), f(tau))
		return b.shape(n, env, d, gain(db))
	}
	var layers []string
	switch variant {
	case "ui":
		layers = []string{clickAt(0, 5000, 12, 0.007, 0), clickAt(0.028, 3200, 6, 0.01, -14)}
	case "soft":
		layers = []string{clickAt(0, 2600, 3, 0.012, -2)}
	case "tonal":
		blip := b.tone(fmt.Sprintf("%s*sin(%s)*%s", attack(1), b.glide(1500, 1200, 0.015), decay(0.03)), d, hpf(300), gain(-3))
		layers = []string{blip, clickAt(0, 4500, 4, 0.004, -7)}
	case "mech":
		// The push-button composite: clicks at 0 (4 kHz), 30 ms (3 kHz), 140 ms (4 kHz) and 150 ms (7 kHz).
		layers = []string{clickAt(0, 4000, 8, 0.01, 0), clickAt(0.03, 3000, 8, 0.012, -4), clickAt(0.14, 4000, 8, 0.01, -10), clickAt(0.15, 7000, 8, 0.006, -12)}
	}
	sum := b.mix("", layers...)
	b.bus = "acompressor=threshold=0.5:ratio=2:attack=2:release=40:makeup=1"
	return b.add([]string{b.stereo(sum)}, hpf(120)), d
}

// ---- glitch ---------------------------------------------------------------------------------------------------------------

func (b *sfxBuilder) glitch(variant string) (string, float64) {
	d := b.length(timeline.SFXGlitch, variant, true)
	// Deterministic by construction: no random(): a hash of the slice number made of sin() steps the pitch, and the noise is
	// seeded. One slice is one video frame at 30 fps (33 ms), so that the picture and the sound stutter together.
	slice := 1.0 / 30
	var sum string
	switch variant {
	case "stutter":
		// A tone burst chopped into slices that repeat, each with its own pitch, gated on and off: a buffer repeat.
		pitch := fmt.Sprintf("(700+1900*mod(abs(sin(floor(t/%s)*12.9898)*43758.5453),1))", f(slice))
		gate := fmt.Sprintf("lt(mod(t,%s),%s)", f(slice), f(slice*0.55))
		tone := b.tone(fmt.Sprintf("%s*%s*sin(2*PI*%s*t*%s)*(1-t/%s)", gate, "0.8", pitch, f(b.j.pitch), f(d)), d, hpf(300), "acrusher=bits=8:mode=lin:mix=0.5")
		n := b.noise("white", 15, d, bpf(3000*b.j.filter, 1))
		nz := b.shape(n, fmt.Sprintf("%s*%s*%s*0.35", attack(0.5), gate, decay(0.12)), d)
		sum = b.mix("", b.stereo(tone), b.stereo(nz))
	case "crush":
		// Noise crushed to a few bits at a low sample rate, falling fast: a digital crunch.
		n := b.noise("white", 16, d, bpf(1800*b.j.filter, 0.7), "acrusher=bits=5:mode=log:aa=0:samples=6:mix=1")
		c := b.shape(n, fmt.Sprintf("%s*%s", attack(0.5), decay(0.06)), d)
		tone := b.tone(fmt.Sprintf("%s*sin(2*PI*%s*t)*%s*0.4", attack(0.5), f(240*b.j.pitch), decay(0.05)), d, saturate(8), "acrusher=bits=6:mode=lin:samples=3:mix=1")
		sum = b.mix("", b.stereo(c), b.stereo(tone))
	default: // chirp
		// R2D2-style data chirps: square bursts of 20 ms at 1-4 kHz, a new pitch for each.
		step := 0.02
		pitch := fmt.Sprintf("(1000+3000*mod(abs(sin(floor(t/%s)*78.233)*43758.5453),1))", f(step))
		gate := fmt.Sprintf("lt(mod(t,%s),%s)", f(step), f(step*0.8))
		tone := b.tone(fmt.Sprintf("%s*0.6*sin(2*PI*%s*t*%s+3*sin(2*PI*%s*t))*(1-t/%s)", gate, pitch, f(b.j.pitch), pitch, f(d)), d, hpf(500), lpf(7000), "acrusher=bits=10:mode=lin:mix=0.4")
		sum = b.stereo(tone)
	}
	return b.glue(sum, 2, 5, 60), d
}

// ---- shimmer --------------------------------------------------------------------------------------------------------------

// lcg is a small deterministic generator for the placement of a shimmer's pings (the seed is the item's hash, so two items
// sparkle differently and the same item always the same). It is written out here, not math/rand, so that it is the same in every
// release of Go.
type lcg uint64

func (l *lcg) next() float64 {
	*l = *l*6364136223846793005 + 1442695040888963407
	return float64(uint64(*l)>>11) / float64(1<<53)
}

func (b *sfxBuilder) shimmer(variant string) (string, float64) {
	d := b.length(timeline.SFXShimmer, variant, true)
	// Ping cloud: short sine pings on a pentatonic scale (C major pentatonic from C7), 2 to 8 kHz, 0.5-1 ms attack, 30-200 ms
	// exponential decay, +-6 dB at random; the density follows an envelope that swells with the motion and thins out.
	scale := []float64{2093, 2349, 2637, 3136, 3520, 4186, 4699, 5274, 6272, 7040}
	rng := lcg(timeline.SFXHash(b.id+"#"+variant) ^ 0x9E3779B97F4A7C15)
	n, span := 26, 0.78
	switch variant {
	case "wash":
		n, span = 8, 0.6
	case "rise":
		n, span = 18, 0.62
	}
	var terms []string
	for i := 0; i < n; i++ {
		x := rng.next()
		// The times follow a swell: dense in the middle, thin at both ends. The first ping is at 0: the effect starts on its
		// time, not a few tens of ms after it.
		t0 := d * span * (0.5 - 0.5*math.Cos(math.Pi*x))
		if i == 0 {
			t0 = 0
		}
		if variant == "rise" {
			t0 = d * span * math.Pow(float64(i)/float64(n), 0.8)
		}
		idx := int(rng.next() * float64(len(scale)))
		if variant == "rise" {
			idx = min(len(scale)-1, i*len(scale)/n)
		}
		hz := scale[min(idx, len(scale)-1)] * b.j.pitch
		tau := 0.03 + rng.next()*0.15
		amp := math.Pow(10, (rng.next()*12-6)/20) * 0.5
		terms = append(terms, fmt.Sprintf("%s*if(gte(t,%s),min(1,(t-%s)/0.0008)*sin(2*PI*%s*(t-%s))*exp(-(t-%s)/%s),0)", f(amp), f(t0), f(t0), f(hz), f(t0), f(t0), f(tau)))
	}
	pings := b.tone(strings.Join(terms, "+"), d, hpf(1500), lpf(9000))
	var layers []string
	layers = append(layers, b.wide(pings, 8))
	// The wash: noise above 6 kHz that swells and fades, well under the pings (about 18 dB for a sparkle, only 8 for a wash).
	washDB := -18.0
	if variant == "wash" {
		washDB = -6
	}
	wn := b.noise("white", 17, d, hpf(6500*b.j.filter), lpf(12000), gain(washDB))
	layers = append(layers, b.wide(b.shape(wn, fmt.Sprintf("(%s+%s*%s)*%s", f(passFloor), f(1-passFloor), bellExpr(d, 0.5), attack(3)), d), 12))
	dry, wet := b.room(b.mix("", layers...), 0.7, 0.3, 20, 6000)
	layers = []string{dry, b.wide(b.add([]string{wet}, gain(-8)), 17)}
	return b.glue(b.mix("", layers...), 2, 10, 120), d
}

// ---- sting ----------------------------------------------------------------------------------------------------------------

func (b *sfxBuilder) sting(variant string) (string, float64) {
	d := b.length(timeline.SFXSting, variant, true)
	// A logo or reveal hit: an impact with a long sub, then a tonal bloom that enters 20 ms after the hit with a 150 ms attack
	// and a low-pass that opens from 1 kHz to 7 kHz, in a room of its own. The chord is a root, a fifth and an octave (no third,
	// so that it does not fight the music's key); bright adds a major ninth, dark a lower root and a minor colour.
	hs := hitSpec{d: d, trHP: 1800, trTau: 0.007, trDB: -2, trDrive: 4, bodyF0: 150, bodyF1: 58, bodyTau: 0.06, bodyDecay: 0.2, bodyDB: -3, bodyDrive: 6,
		noiseBand: 240, noiseQ: 0.8, noiseTau: 0.12, noiseDB: -9,
		subF0: 90, subF1: 40, subTau: 0.14, subDecay: 0.55, subDB: -2, subHold: 0.12, roomTau: 0.3, roomDB: -11, roomPreMs: 16, glueRatio: 3}
	var chord []float64
	var bloomLP, bloomDecay, bloomDB float64
	switch variant {
	case "hit":
		chord, bloomLP, bloomDecay, bloomDB = []float64{220, 330, 440}, 7000, 0.9, -9
	case "bright":
		chord, bloomLP, bloomDecay, bloomDB = []float64{261.63, 392, 523.25, 587.33}, 9000, 0.7, -8
		hs.trDB, hs.subDB = 0, -4
	default: // dark
		chord, bloomLP, bloomDecay, bloomDB = []float64{110, 164.81, 220, 261.63}, 3500, 1.1, -8
		hs.subF0, hs.subF1, hs.subDecay = 70, 32, 0.8
	}
	hit := b.hit(hs)
	var voices []string
	for _, hz := range chord {
		// Two slightly detuned sines with a quiet second harmonic: a pad-like note.
		voices = append(voices, fmt.Sprintf("sin(2*PI*%s*t)+sin(2*PI*%s*t)+0.25*sin(2*PI*%s*t)", f(hz*b.j.pitch), f(hz*b.j.pitch*1.003), f(hz*b.j.pitch*2)))
	}
	env := fmt.Sprintf("if(gte(t,0.02),min(1,(t-0.02)/0.15)*exp(-(t-0.02)/%s),0)", f(bloomDecay))
	tone := b.tone(fmt.Sprintf("%s*(%s)/%s", env, strings.Join(voices, "+"), f(float64(len(chord))*2)), d)
	sw := b.sweep("lowpass", b.prefix+"s", 0.5, 12, 0, func(x float64) float64 { return 1000 + (bloomLP-1000)*math.Sqrt(x) })
	tone = b.add([]string{tone}, append(sw, hpf(120), gain(bloomDB))...)
	tone, wet := b.room(tone, 1.2, 0.5, 20, 5000)
	bloom := b.mix("", b.wide(tone, 10), b.wide(b.add([]string{wet}, gain(-5)), 16))
	return b.glue(b.mix("", hit, bloom), 2.5, 25, 150), d
}
