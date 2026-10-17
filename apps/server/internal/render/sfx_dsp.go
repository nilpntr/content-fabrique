package render

import (
	"fmt"
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The building blocks of the sound effects (P3-11). A sound is a handful of mono layers, each made by a source (an aevalsrc
// expression for tones and glides, a seeded anoisesrc for noise), shaped by a sample-accurate envelope (aevalsrc through
// amultiply) and banded, then summed and processed as one: light saturation on the body and the transient (never the sub),
// a bus compressor to glue, a short dark room on the body and the tail only (the sub and the transient stay dry), a high-pass
// at 22 Hz and a loudness match. The design chain and its starting values come from the sound-design notes of the earshot
// project (https://github.com/Leonxlnx/earshot, docs/motion-sfx-techniques.md, MIT licence; Copyright (c) 2026 Leon Lin): the
// layer bands (sub < 120 Hz, body 80-800 Hz, transient > 1-2 kHz), saturation on body and transient only, bus glue of 2:1
// to 4:1 with 10-30 ms attack, the dark room with 10-30 ms pre-delay, the 20-25 Hz high-pass, exponential riser curves and
// decelerating downlifter curves, and the per-category durations. Only numbers and ideas were taken: the code is ours (earshot
// is Python, and none of it is used or ported). See docs/design/audio-v1.md.
//
// Every number written into a filter string is a constant of this package or a Go-formatted float (round6, num); the category
// and variant come from a switch on the enum, and an item's id reaches the graph only as a hash (see jitter).

const sfxRate = AudioSampleRate

// f formats a float for a filter string: at most six decimals, no exponent.
func f(x float64) string { return num(math.Round(x*1e6) / 1e6) }

// f1 is f with one decimal: enough for the centre of a swept filter in Hz, and the sweep commands are most of a graph's length.
func f1(x float64) string { return num(math.Round(x*10) / 10) }

// jitter is the per-item variation: a hash of the item id gives a small, deterministic change of pitch, length, filter centres,
// level and noise seed, so six whooshes on one timeline are six slightly different samples and a re-render of the same spec
// gives the same ones. Earshot's round-robin ranges: pitch +-0.3 to 1 semitone, level +-1 to 2 dB, timing and decay +-10 %.
type jitter struct {
	pitch   float64 // multiplies tone frequencies: +-0.5 semitone
	filter  float64 // multiplies filter centres: +-12 %
	length  float64 // multiplies the natural length, where the category allows: +-6 %
	levelDB float64 // +-1 dB
	seed    int     // 0 to 7, added to the noise seeds
}

func newJitter(id string) jitter {
	h := timeline.SFXHash(id)
	u := func(shift uint) float64 { return float64((h>>shift)&0xFFFF)/65535*2 - 1 }
	return jitter{
		pitch:   math.Exp2(u(0) * 0.5 / 12),
		filter:  math.Exp2(u(16) * 0.12),
		length:  1 + u(32)*0.06,
		levelDB: u(48),
		seed:    int(timeline.SFXHash(id+"#noise") % 8),
	}
}

// builder collects the statements of one sound effect. Every label it makes starts with the effect's prefix, so effects never
// share one.
type sfxBuilder struct {
	prefix string
	id     string
	j      jitter
	bus    string // the compressor of the bus stage, set by glue
	stmts  []string
	n      int
}

func (b *sfxBuilder) label() string {
	b.n++
	return fmt.Sprintf("%s_%d", b.prefix, b.n)
}

// add appends one statement, [in...]chain[out], and returns the output label.
func (b *sfxBuilder) add(in []string, parts ...string) string {
	out := b.label()
	var s strings.Builder
	for _, l := range in {
		s.WriteString("[" + l + "]")
	}
	s.WriteString(strings.Join(parts, ","))
	s.WriteString("[" + out + "]")
	b.stmts = append(b.stmts, s.String())
	return out
}

// tone is a mono signal from an expression of t (seconds), dur seconds long, then the post filters.
func (b *sfxBuilder) tone(expr string, dur float64, post ...string) string {
	return b.add(nil, append([]string{fmt.Sprintf("aevalsrc='%s':s=%d:d=%s", expr, sfxRate, f(dur))}, post...)...)
}

// noise is seeded noise (the seed is the recipe's own plus the item's jitter), dur seconds long, in 10 ms frames so that a
// swept filter (sweep) follows its curve closely.
func (b *sfxBuilder) noise(color string, seed int, dur float64, post ...string) string {
	src := fmt.Sprintf("anoisesrc=color=%s:seed=%d:amplitude=1:sample_rate=%d:duration=%s:nb_samples=480", color, seed+b.j.seed, sfxRate, f(dur))
	return b.add(nil, append([]string{src}, post...)...)
}

// shape multiplies a signal by an envelope given as an expression of t, sample by sample.
func (b *sfxBuilder) shape(sig, env string, dur float64, post ...string) string {
	e := b.tone(env, dur)
	return b.add([]string{sig, e}, append([]string{"amultiply"}, post...)...)
}

// mix sums layers (no normalisation: the levels are the layers' own).
func (b *sfxBuilder) mix(post string, layers ...string) string {
	if len(layers) == 1 {
		if post == "" {
			return layers[0]
		}
		return b.add(layers, post)
	}
	parts := []string{fmt.Sprintf("amix=inputs=%d:duration=longest:normalize=0", len(layers))}
	if post != "" {
		parts = append(parts, post)
	}
	return b.add(layers, parts...)
}

// sweep drives the centre of a band filter along a curve: one command per step, sent by asendcmd just before the filter. name
// is the filter ("bandpass", "lowpass", "highpass") and id its instance name; hz maps the position x in 0..1 to the centre. The
// returned two filters (asendcmd and the filter itself) go at the start of a chain. The commands are Go-formatted numbers.
func (b *sfxBuilder) sweep(name, id string, dur float64, steps int, q float64, hz func(x float64) float64) []string {
	var cmds strings.Builder
	for i := 0; i < steps; i++ {
		// Command i is sent at i/steps of the length and holds until the next one: it carries the value of the middle of its
		// interval, so the curve is followed without running ahead of the envelope it belongs to.
		x := (float64(i) + 0.5) / float64(steps)
		if i > 0 {
			cmds.WriteString(";")
		}
		fmt.Fprintf(&cmds, "%s %s@%s frequency %s", f(dur*float64(i)/float64(steps)), name, id, f1(hz(x)*b.j.filter))
	}
	filter := fmt.Sprintf("%s@%s=f=%s", name, id, f1(hz(0.5/float64(steps))*b.j.filter))
	if name == "bandpass" {
		filter += fmt.Sprintf(":width_type=q:width=%s", f(q))
	}
	return []string{fmt.Sprintf("asendcmd=c='%s'", cmds.String()), filter}
}

// saturate drives a layer into a soft clipper: drive dB of gain, tanh, 2x oversampled so the new harmonics do not fold back.
func saturate(driveDB float64) string {
	return fmt.Sprintf("volume=%sdB,asoftclip=type=tanh:oversample=2", f(driveDB))
}

func hpf(hz float64) string { return "highpass=f=" + f(hz) }
func lpf(hz float64) string { return "lowpass=f=" + f(hz) }
func bpf(hz, q float64) string {
	return fmt.Sprintf("bandpass=f=%s:width_type=q:width=%s", f(hz), f(q))
}
func gain(db float64) string { return "volume=" + f(db) + "dB" }

// glide is the phase, in radians, of a sine whose frequency falls (or rises) exponentially from f0 to f1 with time constant tau
// seconds: f(t) = f1 + (f0 - f1) e^(-t/tau), the integral of which is f1 t + (f0 - f1) tau (1 - e^(-t/tau)). Frequencies are
// multiplied by the item's pitch jitter.
func (b *sfxBuilder) glide(f0, f1, tau float64) string {
	f0, f1 = f0*b.j.pitch, f1*b.j.pitch
	return fmt.Sprintf("2*PI*(%s*t+%s*(1-exp(-t/%s)))", f(f1), f((f0-f1)*tau), f(tau))
}

// decay is exp(-t/tau).
func decay(tau float64) string { return fmt.Sprintf("exp(-t/%s)", f(tau)) }

// attack is a linear ramp from 0 to 1 over ms milliseconds, then 1: min(1, t/attack).
func attack(ms float64) string { return fmt.Sprintf("min(1,t/%s)", f(ms/1000)) }

// room is a short dark room on a layer: a seeded noise impulse response that starts after preMs, decays with time constant tau
// seconds and is low-passed at hz (the return is dark), convolved with the layer (padded by the room's length so that its tail is
// not cut). It returns the layer, passed through untouched (a label can feed only one filter, so the layer is split), and only
// the wet signal; the caller mixes them and sets the wet level. The sub and the transient never go through here. The impulse
// response is made of the room's own constants and a seeded noise, so it is the same on every machine.
func (b *sfxBuilder) room(sig string, lenS, tau, preMs, hz float64) (dry, wet string) {
	dry, send := b.label(), b.label()
	b.stmts = append(b.stmts, fmt.Sprintf("[%s]asplit=2[%s][%s]", sig, dry, send))
	ir := b.noise("white", 21, lenS)
	env := fmt.Sprintf("if(gte(t,%s),exp(-(t-%s)/%s),0)", f(preMs/1000), f(preMs/1000), f(tau))
	ir = b.shape(ir, env, lenS, lpf(hz))
	padded := b.add([]string{send}, "aformat=channel_layouts=mono", fmt.Sprintf("apad=pad_dur=%s", f(lenS)))
	return dry, b.add([]string{padded, ir}, "afir=irnorm=0")
}
