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

	// Source audio of the clips that ask for it. A clip in full mode is part of the programme; a clip in bed mode is the ambience
	// bed, set to its own level by the evening pass (BedPlan).
	var (
		bedPads []string
		bedPlan = &BedPlan{TargetLUFS: BedTargetLUFS, BelowLU: BedBelowSpeechLU, MaxGainDB: bedMaxGainDB}
		meter   []string
		// An edit with music is music-led (P6-06, D-34): the music is the sound, the clips' ambience bed is left out under it, speech
		// stays and the music is ducked under it.
		musicLed   = timeline.HasMusic(c.spec)
		speechPads []string
		musicPads  []string
	)
	c.plan.MusicLed = musicLed
	for i, cl := range c.clips {
		a := cl.item.Audio
		mode := timeline.ClipAudioMode(a)
		if mode != timeline.AudioModeMute && mode != timeline.AudioModeBed && mode != timeline.AudioModeFull {
			return audioResult{}, fmt.Errorf("%w: clip %s: audio mode %q is not one of mute, bed and full", ErrInvalidSpec, cl.item.ID, mode)
		}
		if mode == timeline.AudioModeMute || !cl.asset.HasAudio || (musicLed && mode == timeline.AudioModeBed) {
			continue
		}
		it := cl.item
		start := timeline.FrameStartMs(cl.startFrame, c.fps)
		dur := timeline.FrameStartMs(cl.endFrame, c.fps) - start
		speed := it.SpeedOrDefault()
		if it.HasRamp() {
			// atempo cannot follow a changing speed, so a speech clip with a speed ramp plays no sound of its own (Validate warns
			// with ramp_audio_muted): it gets no input in the mix either. An ambience bed does not need to follow the ramp: it plays
			// at the clip's average speed, which keeps the bed continuous under the ramped shot.
			if mode != timeline.AudioModeBed || timeline.OutputDuration(it) <= 0 {
				continue
			}
			speed = float64(it.Source.OutMs-it.Source.InMs) / float64(timeline.OutputDuration(it))
		}
		f := []string{
			"atrim=end=" + secs(it.Source.OutMs-it.Source.InMs),
			"asetpts=PTS-STARTPTS",
		}
		f = append(f, atempoChain(speed)...)
		f = append(f, stereo48)
		pre := slices.Clone(f)
		trim := a.GainDBOrDefault()
		bc := BedClip{ItemID: it.ID, Role: "ref", MeterFile: fmt.Sprintf("bedmeter_%d.txt", i)}
		if mode == timeline.AudioModeBed {
			// The nominal gain is the trim alone; the evening pass swaps the measured one in (the filter is named so that it is found).
			bc.Role, bc.TrimDB = "bed", trim
			bc.Filter = fmt.Sprintf("volume@bed%d=%sdB", i, num(trim))
			f = append(f, bc.Filter)
		} else {
			f = append(f, volumeFilter(trim)...)
		}
		meter = append(meter, chain([]string{fmt.Sprintf("%d:a", cl.idx)}, append(pre, "ebur128=framelog=quiet:metadata=1", "ametadata=mode=print:file="+bc.MeterFile), fmt.Sprintf("m%d", i)))
		bedPlan.Clips = append(bedPlan.Clips, bc)
		// Where clips overlap in a transition, the two sound tracks cross over as well.
		fadeIn, fadeOut := cl.overlapFrames > 0, i+1 < len(c.clips) && c.clips[i+1].overlapFrames > 0
		if fadeIn {
			f = append(f, "afade=t=in:st=0:d="+c.frameSecs(cl.overlapFrames))
		}
		if fadeOut {
			d := c.clips[i+1].overlapFrames
			f = append(f, fmt.Sprintf("afade=t=out:st=%s:d=%s", c.frameSecs(cl.endFrame-cl.startFrame-d), c.frameSecs(d)))
		}
		if mode == timeline.AudioModeBed {
			// A bed clip that is cut (not crossfaded) fades for a few ms at its edges, so that the bed does not click at a cut.
			if !fadeIn {
				f = append(f, fmt.Sprintf("afade=t=in:st=0:d=%s", secs(bedFadeMs)))
			}
			if !fadeOut && dur > 2*bedFadeMs {
				f = append(f, fmt.Sprintf("afade=t=out:st=%s:d=%s", secs(dur-bedFadeMs), secs(bedFadeMs)))
			}
		}
		f = append(f, "atrim=end="+secs(dur), "asetpts=PTS-STARTPTS")
		if start > 0 {
			f = append(f, fmt.Sprintf("adelay=%d:all=1", start))
		}
		pad := fmt.Sprintf("as%d", i)
		stmts = append(stmts, chain([]string{fmt.Sprintf("%d:a", cl.idx)}, f, pad))
		if mode == timeline.AudioModeBed {
			bedPads = append(bedPads, pad)
		} else {
			speechPads = append(speechPads, pad)
		}
	}
	hasBed := false
	for _, bc := range bedPlan.Clips {
		hasBed = hasBed || bc.Role == "bed"
	}
	if hasBed {
		c.plan.Bed = bedPlan
		bedPlan.MeterArgs = c.audioOnlyArgs(strings.Join(meter, ";"), func() []string {
			var maps []string
			for i, cl := range c.clips {
				for _, bc := range bedPlan.Clips {
					if bc.ItemID == cl.item.ID {
						maps = append(maps, fmt.Sprintf("[m%d]", i))
					}
				}
			}
			return maps
		}())
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
				continue // generated: sfxPlacements, mixed in after the programme
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
			// Music that plays to the end of the video ends with a short fade unless the item says how (a song cut off on its last frame
			// clicks), and starts with a fade of a few ms so a track entered mid-note does not click either.
			if fadeOut == 0 && com.AtMs+dur >= c.totalMs && dur > 2*MusicEndFadeMs {
				fadeOut = MusicEndFadeMs
			}
			if fadeIn == 0 && dur > 2*bedFadeMs {
				fadeIn = bedFadeMs
			}
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
			musicPads = append(musicPads, pad)
		}
	}
	// Speech over music: the music is ducked under the speech (a sidechain compressor keyed on the speech, a few dB), then both are the
	// programme. Without speech or without music, the pads go to the programme as they are.
	switch {
	case len(speechPads) > 0 && len(musicPads) > 0:
		sp, mu := mixOf(&stmts, speechPads, "aspeech"), mixOf(&stmts, musicPads, "amusic")
		stmts = append(stmts, fmt.Sprintf("[%s]asplit=2[aspmix][aspkey]", sp),
			fmt.Sprintf("[%s][aspkey]sidechaincompress=threshold=%s:ratio=%s:attack=%d:release=%d[amusicd]", mu, num(musicDuckThreshold), num(musicDuckRatio), musicDuckAttackMs, musicDuckReleaseMs))
		mixPads = append(mixPads, "aspmix", "amusicd")
	default:
		mixPads = append(mixPads, speechPads...)
		mixPads = append(mixPads, musicPads...)
	}

	// The programme is the bed plus every source and track item; it is the part that is normalised. The beat guide is a
	// preview aid, not part of the programme: it is mixed in after loudnorm, so that clicks on an otherwise silent
	// timeline are not boosted to -14 LUFS.
	guide := c.beatGuide()
	// The sound effects are the same kind of thing: an accent mixed in after the normalisation, at a fixed level.
	// In a sound-ready edit (a bed and no programme sound) the effects are lifted over the bed, which is not normalised up to them.
	c.sfxBoostDB = 0
	if len(mixPads) == 0 && len(bedPads) > 0 {
		c.sfxBoostDB = SoundReadyEffectGainDB
	}
	places, err := c.sfxPlacements()
	if err != nil {
		return audioResult{}, err
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

	// An edit with no programme sound is a bed and the effects: it is mixed as it is, with no normalisation (soundReadyMix). With a
	// programme (speech, music) the bed clips join it, below the speech.
	if len(mixPads) == 0 && len(bedPads) > 0 {
		return c.soundReadyMix(stmts, bedPads, sfxStmts, sfxPads, guide)
	}
	mixPads = append(mixPads, bedPads...)

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
		// A preview with a beat guide writes an audio stem, which the audio check measures as the export: the stem must have the
		// export's loudness stage (two linear passes), not the preview's dynamic single pass, which levels a different mix.
		res.loudnorm = &LoudnormPlan{Filter: ln, TwoPass: c.profile.LoudnormPasses >= 2 || len(guide) > 0}
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
		// The core is the programme and the effects; the beat guide (preview only) goes on top of it, and the core without the guide
		// is the audio stem the audio check measures (StemLabel).
		core := prog
		if len(sfxPads) > 0 {
			in := "[" + prog + "]"
			for _, p := range sfxPads {
				in += "[" + p + "]"
			}
			core = "acore"
			if len(guide) == 0 {
				core = "aout"
			}
			// A limiter at sfxLimitDB (-1.5 dBFS: headroom under the -1 dBTP target for inter-sample peaks), so the effects cannot
			// push the mix past the peak ceiling. alimiter compensates its own look-ahead delay (latency=1), so the sound stays
			// where it was and keeps its length (see sfx.go).
			stmts = append(stmts, fmt.Sprintf("%samix=inputs=%d:duration=first:normalize=0:dropout_transition=0,%s[%s]", in, len(sfxPads)+1, sfxLimiter, core))
		}
		if len(guide) > 0 {
			stmts = append(stmts, fmt.Sprintf("[%s]asplit=2[astem][acg]", core))
			c.plan.StemLabel = "astem"
			mix := "[acg][abeat]amix=inputs=2:duration=first:normalize=0:dropout_transition=0"
			if len(sfxPads) > 0 {
				mix += "," + sfxLimiter
			}
			stmts = append(stmts, mix+"[aout]")
		}
	case prog == "abed":
		stmts = append(stmts, "[abed]anull[aout]")
	}
	res.statements = stmts
	return res, nil
}

// soundReadyMix mixes an edit whose sound is the bed and the effects. The bed clips were set to their own level by the evening pass
// (BedPlan) and the effects to their category's level plus SoundReadyEffectGainDB (sfxPlacements); they are summed and limited under
// the sound-ready ceiling, and nothing is normalised: the integrated loudness of the export results from the bed and the effects. The
// beat guide (preview only) goes on top, and the mix without it is the audio stem (StemLabel).
func (c *compiler) soundReadyMix(stmts, bedPads, sfxStmts, sfxPads []string, guide []string) (audioResult, error) {
	var res audioResult
	c.plan.SoundReady = true
	in := "[abed]"
	for _, p := range bedPads {
		in += "[" + p + "]"
	}
	stmts = append(stmts, fmt.Sprintf("%samix=inputs=%d:duration=first:normalize=0:dropout_transition=0[abedmix]", in, len(bedPads)+1))
	stmts = append(stmts, sfxStmts...)
	mixIn := "[abedmix]"
	for _, p := range sfxPads {
		mixIn += "[" + p + "]"
	}
	core := "aout"
	if len(guide) > 0 {
		core = "acore"
	}
	stmts = append(stmts, fmt.Sprintf("%samix=inputs=%d:duration=first:normalize=0:dropout_transition=0,%s,%s[%s]", mixIn, len(sfxPads)+1, stereo48, limiterAt(SoundReadyCeilingDB), core))
	if len(guide) > 0 {
		stmts = append(stmts, guide...)
		stmts = append(stmts, "[acore]asplit=2[astem][acg]", "[acg][abeat]amix=inputs=2:duration=first:normalize=0:dropout_transition=0,"+sfxLimiter+"[aout]")
		c.plan.StemLabel = "astem"
	}
	res.statements = stmts
	return res, nil
}

// The music mix (P6-06, D-34). The music of an edit (licensed_track, creator_audio) is the programme with any speech: normalised to the
// web's -14 LUFS with a true peak at -1 dBTP (TargetLUFS, TargetTP). Under speech it is ducked by a sidechain compressor keyed on the
// speech: from about -20 dBFS of speech at 3:1, so speech at its usual -12 dBFS takes the music down about 5 dB (earshot and practice:
// duck 3 to 6 dB under a voice), with a quick attack and a release that does not pump between words. Music that plays to the end of the
// video fades out over its last MusicEndFadeMs.
const (
	MusicEndFadeMs     = 400
	musicDuckThreshold = 0.1
	musicDuckRatio     = 3.0
	musicDuckAttackMs  = 15
	musicDuckReleaseMs = 250
)

// mixOf sums the pads into one (amix, no normalisation) under the label, or returns the one pad there is.
func mixOf(stmts *[]string, pads []string, label string) string {
	if len(pads) == 1 {
		return pads[0]
	}
	*stmts = append(*stmts, fmt.Sprintf("[%s]amix=inputs=%d:duration=longest:normalize=0:dropout_transition=0[%s]", strings.Join(pads, "]["), len(pads), label))
	return label
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
