package render

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// stereo48 puts a branch of the audio graph in the one format the mix works in, so amix never has to guess.
var stereo48 = fmt.Sprintf("aresample=%d,aformat=sample_fmts=fltp:channel_layouts=stereo", AudioSampleRate)

// Beat guide clicks (design note): 30 ms sine clicks, 1 kHz for a beat and 1.5 kHz for a downbeat; a drop gets a longer
// accent (150 ms at 2 kHz). The guide is mixed in at -12 dB. The clicks are gated sines: they start and stop at once.
const (
	beatClickMs  = 30
	dropClickMs  = 150
	beatFreqHz   = 1000
	downFreqHz   = 1500
	dropFreqHz   = 2000
	beatGuideGdB = -12
)

// audioResult is what audioGraph hands back to Compile.
type audioResult struct {
	statements []string
	// loudnorm is nil when the mix is silent.
	loudnorm *LoudnormPlan
	// pass1Graph is the audio statements with the measuring loudnorm, for the two-pass profile.
	pass1Graph string
}

// loudnormFilter is the single-pass filter.
func loudnormFilter() string {
	return fmt.Sprintf("loudnorm=I=%d:TP=%d:LRA=%d", TargetLUFS, TargetTP, TargetLRA)
}

// atempoChain returns the atempo filters that give the speed, each within 0.5 to 2 (older ffmpeg builds stop at that
// range, and chaining is exact), or nothing for speed 1.
func atempoChain(speed float64) []string {
	var out []string
	for speed > 2 {
		out = append(out, "atempo=2")
		speed /= 2
	}
	for speed < 0.5 {
		out = append(out, "atempo=0.5")
		speed /= 0.5
	}
	if speed != 1 {
		out = append(out, "atempo="+num(round6(speed)))
	}
	return out
}

func volumeFilter(gainDB float64) []string {
	if gainDB == 0 {
		return nil
	}
	return []string{"volume=" + num(gainDB) + "dB"}
}

// audioGraph builds the audio statements, appends the audio track items to the plan's inputs and collects the sound
// cues. Its output pad is [aout].
func (c *compiler) audioGraph(resolver AssetResolver) (audioResult, error) {
	var (
		stmts   []string
		mixPads []string
	)
	c.plan.SoundCues = []SoundCue{}

	// Source audio of the clips that ask for it.
	for i, cl := range c.clips {
		a := cl.item.Audio
		if a == nil || !a.UseSourceOrDefault() || !cl.asset.HasAudio {
			continue
		}
		if cl.item.HasRamp() {
			// atempo cannot follow a changing speed, so a clip with a speed ramp plays no sound of its own (Validate warns
			// with ramp_audio_muted). It gets no input in the mix either.
			continue
		}
		it := cl.item
		start := timeline.FrameStartMs(cl.startFrame, c.fps)
		dur := timeline.FrameStartMs(cl.endFrame, c.fps) - start
		f := []string{
			"atrim=end=" + secs(it.Source.OutMs-it.Source.InMs),
			"asetpts=PTS-STARTPTS",
		}
		f = append(f, atempoChain(it.SpeedOrDefault())...)
		f = append(f, stereo48)
		f = append(f, volumeFilter(a.GainDBOrDefault())...)
		// Where clips overlap in a transition, the two sound tracks cross over as well.
		if cl.overlapFrames > 0 {
			f = append(f, "afade=t=in:st=0:d="+c.frameSecs(cl.overlapFrames))
		}
		if i+1 < len(c.clips) && c.clips[i+1].overlapFrames > 0 {
			d := c.clips[i+1].overlapFrames
			f = append(f, fmt.Sprintf("afade=t=out:st=%s:d=%s", c.frameSecs(cl.endFrame-cl.startFrame-d), c.frameSecs(d)))
		}
		f = append(f, "atrim=end="+secs(dur), "asetpts=PTS-STARTPTS")
		if start > 0 {
			f = append(f, fmt.Sprintf("adelay=%d:all=1", start))
		}
		pad := fmt.Sprintf("as%d", i)
		stmts = append(stmts, chain([]string{fmt.Sprintf("%d:a", cl.idx)}, f, pad))
		mixPads = append(mixPads, pad)
	}

	// Audio track items. A cue_only item is a cue and nothing else, whatever the profile (D-03): it is not resolved, it
	// gets no input and no filter, and nothing of it reaches the argument list.
	resolved := map[string]ResolvedAsset{}
	for _, t := range c.spec.Tracks {
		tr, ok := t.(*timeline.AudioTrack)
		if !ok {
			continue
		}
		for _, item := range tr.Items {
			if _, isSFX := item.(*timeline.SFX); isSFX {
				continue // generated: sfxGraph, mixed in after the programme
			}
			com := item.Common()
			trend, isTrend := item.(*timeline.TrendSound)
			if isTrend || com.Export == timeline.ExportCueOnly {
				if !isTrend || com.Export != timeline.ExportCueOnly {
					return audioResult{}, fmt.Errorf("%w: audio item %s: export %q does not fit kind %q", ErrInvalidSpec, com.ID, com.Export, item.AudioKind())
				}
				c.plan.SoundCues = append(c.plan.SoundCues, SoundCue{
					ItemID:         com.ID,
					TrendID:        trend.TrendID,
					AtMs:           com.AtMs,
					SourceOffsetMs: com.SourceOffsetMs,
					DurationMs:     com.EffectiveDurationMs(c.totalMs),
					GainDB:         com.GainDBOrDefault(),
				})
				continue
			}
			assetID, _ := item.AssetRef()
			dur := min(com.EffectiveDurationMs(c.totalMs), c.totalMs-com.AtMs)
			if dur <= 0 {
				continue
			}
			ra, seen := resolved[assetID]
			if !seen {
				var err error
				ra, err = resolver.Resolve(assetID, c.profile.Source)
				if err != nil {
					return audioResult{}, fmt.Errorf("render: resolve asset %s for audio item %s: %w", assetID, com.ID, err)
				}
				resolved[assetID] = ra
			}
			if !ra.HasAudio {
				return audioResult{}, fmt.Errorf("%w: audio item %s: asset %s has no audio stream", ErrInvalidSpec, com.ID, assetID)
			}
			k := len(c.inputs)
			c.inputs = append(c.inputs, Input{
				Index: k, Role: RoleAudio, ItemID: com.ID, AssetID: assetID, Location: ra.Location,
				SeekMs: com.SourceOffsetMs, DurationMs: dur,
			})
			f := []string{"atrim=end=" + secs(dur), "asetpts=PTS-STARTPTS", stereo48}
			fadeIn, fadeOut := com.FadeMs()
			if fadeIn > 0 {
				f = append(f, "afade=t=in:st=0:d="+secs(fadeIn))
			}
			if fadeOut > 0 {
				f = append(f, fmt.Sprintf("afade=t=out:st=%s:d=%s", secs(max(0, dur-fadeOut)), secs(fadeOut)))
			}
			if com.AtMs > 0 {
				f = append(f, fmt.Sprintf("adelay=%d:all=1", com.AtMs))
			}
			f = append(f, volumeFilter(com.GainDBOrDefault())...)
			pad := fmt.Sprintf("au%d", k)
			stmts = append(stmts, chain([]string{fmt.Sprintf("%d:a", k)}, f, pad))
			mixPads = append(mixPads, pad)
		}
	}

	// The programme is the bed plus every source and track item; it is the part that is normalised. The beat guide is a
	// preview aid, not part of the programme: it is mixed in after loudnorm, so that clicks on an otherwise silent
	// timeline are not boosted to -14 LUFS.
	guide := c.beatGuide()
	// The sound effects are the same kind of thing: an accent mixed in after the normalisation, at a fixed level.
	places := c.sfxPlacements()
	if c.sfxErr != nil {
		return audioResult{}, c.sfxErr
	}
	var sfxStmts, sfxPads []string
	for _, p := range places {
		sfxStmts = append(sfxStmts, p.stmts...)
		sfxPads = append(sfxPads, p.pad)
	}
	// Where an effect overlaps speech from a clip's own sound, the programme is ducked a few dB under it (sfx.go, duck). Only a
	// programme that has sound to duck is: a timeline whose only sound is effects has nothing under them.
	var duckStmts []string
	duckedProg := ""
	if len(mixPads) > 0 {
		duckStmts, duckedProg, sfxPads = c.duck("aprog", places, sfxPads)
	}
	post := slices.Clone(sfxPads)
	if len(guide) > 0 {
		post = append(post, "abeat")
	}

	// The bed is silence of exactly the output's length: the mix always has an audio stream, and amix runs for exactly
	// as long as the video.
	bed := fmt.Sprintf("anullsrc=r=%d:cl=stereo:d=%s[abed]", AudioSampleRate, secs(c.totalMs))
	stmts = append([]string{bed}, stmts...)

	progOut := "aout"
	if len(post) > 0 {
		progOut = "aprog"
	}
	var res audioResult
	prog := "abed"
	if len(mixPads) > 0 {
		var in strings.Builder
		in.WriteString("[abed]")
		for _, p := range mixPads {
			in.WriteString("[" + p + "]")
		}
		mix := fmt.Sprintf("%samix=inputs=%d:duration=first:normalize=0:dropout_transition=0", in.String(), len(mixPads)+1)
		ln := loudnormFilter()
		// loudnorm resamples to 192 kHz internally, so the rate and layout are set again after it.
		stmts = append(stmts, mix+","+ln+","+stereo48+"["+progOut+"]")
		prog = progOut
		res.loudnorm = &LoudnormPlan{Filter: ln, TwoPass: c.profile.LoudnormPasses >= 2}
		if res.loudnorm.TwoPass {
			measure := append(slices.Clone(stmts[:len(stmts)-1]), mix+","+ln+":print_format=json[aout]")
			res.pass1Graph = strings.Join(measure, ";")
		}
	}
	switch {
	case len(post) > 0:
		stmts = append(stmts, guide...)
		stmts = append(stmts, sfxStmts...)
		if len(duckStmts) > 0 {
			stmts = append(stmts, duckStmts...)
			prog = duckedProg
		}
		in := "[" + prog + "]"
		for _, p := range post {
			in += "[" + p + "]"
		}
		mix := fmt.Sprintf("%samix=inputs=%d:duration=first:normalize=0:dropout_transition=0", in, len(post)+1)
		if len(sfxPads) > 0 {
			// A limiter at sfxLimitDB (-1.5 dBFS: headroom under the -1 dBTP target for inter-sample peaks), so the effects cannot
			// push the mix past the peak ceiling. alimiter compensates its own look-ahead delay (latency=1), so the sound stays
			// where it was and keeps its length (see sfx.go).
			mix += "," + sfxLimiter
		}
		stmts = append(stmts, mix+"[aout]")
	case prog == "abed":
		stmts = append(stmts, "[abed]anull[aout]")
	}
	res.statements = stmts
	return res, nil
}

// beatGuide returns the statements of the generated click track, ending in [abeat], or nil when the profile has none or
// the spec has no beat, downbeat or drop markers. Each kind of marker is a sine source gated by one volume expression
// that is evaluated per 1 ms frame, not per sample: an aevalsrc expression with a term per marker costs seconds on a 30 s
// timeline, this costs nothing. The sine source has an amplitude of 1/8, so the window is worth 8: a click is at 0 dBFS
// before the -12 dB stage. The guide needs no input.
func (c *compiler) beatGuide() []string {
	if !c.profile.BeatGuide {
		return nil
	}
	var beat, down, drop []string
	for _, m := range c.spec.Markers {
		if m.AtMs >= c.totalMs {
			continue
		}
		switch m.Kind {
		case timeline.MarkerBeat:
			beat = append(beat, click(m.AtMs, beatClickMs))
		case timeline.MarkerDownbeat:
			down = append(down, click(m.AtMs, beatClickMs))
		case timeline.MarkerDrop:
			drop = append(drop, click(m.AtMs, dropClickMs))
		}
	}
	var stmts, pads []string
	for _, g := range []struct {
		name    string
		windows []string
		freq    int
	}{{"abeatb", beat, beatFreqHz}, {"abeatd", down, downFreqHz}, {"abeatp", drop, dropFreqHz}} {
		if len(g.windows) == 0 {
			continue
		}
		stmts = append(stmts, fmt.Sprintf("sine=frequency=%d:sample_rate=%d:duration=%s:samples_per_frame=%d,volume='8*(%s)':eval=frame:precision=float,aformat=sample_fmts=fltp:channel_layouts=stereo[%s]",
			g.freq, AudioSampleRate, secs(c.totalMs), AudioSampleRate/1000, strings.Join(g.windows, "+"), g.name))
		pads = append(pads, g.name)
	}
	if len(pads) == 0 {
		return nil
	}
	mix := fmt.Sprintf("volume=%ddB[abeat]", beatGuideGdB)
	if len(pads) == 1 {
		// The one source goes straight to the level stage.
		stmts = append(stmts, fmt.Sprintf("[%s]%s", pads[0], mix))
	} else {
		in := "[" + strings.Join(pads, "][") + "]"
		stmts = append(stmts, fmt.Sprintf("%samix=inputs=%d:duration=first:normalize=0:dropout_transition=0,%s", in, len(pads), mix))
	}
	return stmts
}

// click is the window of one click, for the volume expression: 1 from at_ms for lenMs.
func click(atMs, lenMs int64) string {
	return fmt.Sprintf("between(t,%s,%s)", num(float64(atMs)/1000), num(round6(float64(atMs+lenMs)/1000)))
}
