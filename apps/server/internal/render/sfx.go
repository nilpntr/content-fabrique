package render

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Generated sound effects (P3-10 part B): whoosh, impact, riser and swish are synthesised in ffmpeg from aevalsrc and a
// seeded anoisesrc, with filters and envelopes. There are no asset files, the seeds are fixed, so the samples are the same on
// every render and every machine (to the precision of the float maths of ffmpeg's expression evaluator), and they are ours:
// the export is "licensed" and nothing is cue-only about them (D-03 is about the trend sounds we do not own).
//
// Every number written here is a constant of this file or a number the compiler formatted (the start and the gain of an item);
// the effect itself is chosen by a switch on the enum, so nothing from the spec is written into the graph as text.
//
// Level: each effect is built with a peak of about 0.9 to 1.4 before the stage that sets its level, then goes through
// sfxBaseDB and the item's gain_db. The effects are mixed in after the loudness normalisation of the programme (the way the
// preview's beat guide is), at that fixed level, followed by a limiter at -1 dBFS: a lone impact on a silent timeline is not
// boosted to -14 LUFS, and an effect on top of a normalised programme cannot push the mix over its true-peak target.
const (
	sfxBaseDB   = -6
	sfxLimitLin = "0.891251" // -1 dBFS
	// sfxLimiterDelay is the latency of alimiter at its default 5 ms attack and 48 kHz, in samples (an impulse comes out 239
	// samples late, in ffmpeg 7.1 and 8.1 alike). TestSoundEffectsLeaveTheProgrammeAlone fails if another ffmpeg changes it.
	sfxLimiterDelay = 239
)

// sfxSource returns the statements that make the raw effect (mono) and end in the filter chain tail (a comma-separated list
// of filters, possibly empty), into pad out. It returns false for a name that is not an effect.
func sfxSource(name, tail, out string) ([]string, bool) {
	finish := func(in string, extra string) string {
		f := extra
		if tail != "" {
			f += "," + tail
		}
		return fmt.Sprintf("%s%s[%s]", in, f, out)
	}
	noise := func(color string, seed int, seconds string) string {
		return fmt.Sprintf("anoisesrc=color=%s:seed=%d:amplitude=1:sample_rate=%d:duration=%s", color, seed, AudioSampleRate, seconds)
	}
	switch name {
	case timeline.SFXWhoosh:
		// Pink noise through a wide band around 900 Hz under a bell-shaped envelope: air passing.
		return []string{noise("pink", 11, "0.6") +
			",bandpass=f=900:width_type=h:width=1400,volume='0.9*pow(sin(PI*t/0.6),2)':eval=frame:precision=float,afade=t=out:st=0.57:d=0.03," + stereo48 +
			finishTail(tail, out)}, true
	case timeline.SFXSwish:
		// A short, higher air pass.
		return []string{noise("white", 3, "0.3") +
			",bandpass=f=3500:width_type=h:width=3000,volume='0.8*pow(sin(PI*t/0.3),2)':eval=frame:precision=float,afade=t=out:st=0.285:d=0.015," + stereo48 +
			finishTail(tail, out)}, true
	case timeline.SFXImpact:
		// A sine that falls from 133 Hz to 38 Hz (phase: the integral of 38 + 95 e^(-14 t)) under a fast decay, and a crack of
		// high-passed noise that dies in 60 ms.
		return []string{
			"aevalsrc='0.9*sin(2*PI*(38*t+6.785714*(1-exp(-14*t))))*exp(-5*t)':s=" + fmt.Sprint(AudioSampleRate) + ":d=0.8[ia]",
			noise("white", 7, "0.8") + ",highpass=f=1500,volume='0.5*exp(-45*t)':eval=frame:precision=float[ib]",
			finish("[ia][ib]amix=inputs=2:duration=longest:normalize=0,afade=t=out:st=0.77:d=0.03,"+stereo48, ""),
		}, true
	case timeline.SFXRiser:
		// A sine that rises from 220 Hz to 2020 Hz in 1.5 s (phase: 220 t + 600 t^2) and white noise above 900 Hz, both growing
		// with the square of the time: tension that peaks at the end.
		return []string{
			"aevalsrc='0.55*sin(2*PI*(220*t+600*t*t))*pow(t/1.5,2)':s=" + fmt.Sprint(AudioSampleRate) + ":d=1.5[ra]",
			noise("white", 5, "1.5") + ",highpass=f=900,volume='0.4*pow(t/1.5,2)':eval=frame:precision=float[rb]",
			finish("[ra][rb]amix=inputs=2:duration=longest:normalize=0,afade=t=out:st=1.47:d=0.03,"+stereo48, ""),
		}, true
	}
	return nil, false
}

// finishTail is the end of a single-chain statement: the optional tail filters and the output pad.
func finishTail(tail, out string) string {
	if tail == "" {
		return "[" + out + "]"
	}
	return "," + tail + "[" + out + "]"
}

// sfxGraph returns the statements of the sound effects and the pads they end in, to be mixed after the programme. An effect
// that starts at or after the end of the timeline plays nothing and is left out.
func (c *compiler) sfxGraph() (stmts, pads []string, err error) {
	n := 0
	for _, t := range c.spec.Tracks {
		tr, ok := t.(*timeline.AudioTrack)
		if !ok {
			continue
		}
		for _, item := range tr.Items {
			sfx, ok := item.(*timeline.SFX)
			if !ok {
				continue
			}
			if sfx.Export != timeline.ExportLicensed {
				return nil, nil, fmt.Errorf("%w: audio item %s: export %q does not fit kind %q", ErrInvalidSpec, sfx.ID, sfx.Export, sfx.AudioKind())
			}
			natural, known := timeline.SFXLengthMs(sfx.Sfx)
			if !known {
				return nil, nil, fmt.Errorf("%w: audio item %s: %q is not a sound effect", ErrInvalidSpec, sfx.ID, sfx.Sfx)
			}
			gain := sfx.GainDB
			if gain != nil && (*gain < -30 || *gain > 6 || *gain != *gain) {
				return nil, nil, fmt.Errorf("%w: audio item %s: gain %s dB is outside -30 to 6", ErrInvalidSpec, sfx.ID, num(*gain))
			}
			if sfx.AtMs < 0 || sfx.AtMs >= c.totalMs {
				continue
			}
			dur := min(natural, c.totalMs-sfx.AtMs)
			db := float64(sfxBaseDB)
			if gain != nil {
				db += *gain
			}
			tail := []string{"volume=" + num(db) + "dB", "atrim=end=" + secs(dur), "asetpts=PTS-STARTPTS"}
			if dur < natural { // cut by the end of the timeline: a short fade instead of a click
				fade := min(dur, 10)
				tail = append(tail, fmt.Sprintf("afade=t=out:st=%s:d=%s", secs(dur-fade), secs(fade)))
			}
			if sfx.AtMs > 0 {
				tail = append(tail, fmt.Sprintf("adelay=%d:all=1", sfx.AtMs))
			}
			pad := fmt.Sprintf("fx%d", n)
			n++
			st, _ := sfxSource(sfx.Sfx, strings.Join(tail, ","), pad)
			stmts = append(stmts, st...)
			pads = append(pads, pad)
		}
	}
	return stmts, pads, nil
}
