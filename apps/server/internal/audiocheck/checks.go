package audiocheck

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Where the targets come from (shown in the report, so that a reader can weigh them).
const (
	srcEarshot  = "earshot: measured on 7 launch films"
	srcPractice = "practice: web delivery"
	srcEstimate = "estimate"
)

// checklist builds the pass/fail list. A check that does not apply to the edit (no cuts, no effects) is info, never a failure.
func checklist(r Report) []CheckItem {
	cs := programmeChecklist(r)
	if r.Profile == ProfileSoundReady {
		cs = soundReadyChecklist(r, cs)
	}
	return cs
}

func programmeChecklist(r Report) []CheckItem {
	var cs []CheckItem
	add := func(c CheckItem) { cs = append(cs, c) }
	st := func(ok, warn bool) string {
		switch {
		case ok:
			return StatusPass
		case warn:
			return StatusWarn
		}
		return StatusFail
	}

	add(CheckItem{
		ID: "has_audio", Title: "The video has a sound track", Status: st(r.HasAudio, false),
		Value: yesNo(r.HasAudio), Target: "yes", Source: srcPractice,
		Fix: "Mix in the clips' own sound, a licensed track or sound effects: a silent video autoplays to nothing.",
	})
	if !r.HasAudio {
		return cs
	}
	silent := r.Loudness.IntegratedLUFS <= -69
	if silent {
		// Digital silence: nothing else can be measured, and a list of failures that are all the same fact would bury it.
		add(CheckItem{
			ID: "loudness", Title: "Integrated loudness", Status: StatusFail, Value: "digital silence",
			Target: fmt.Sprintf("%.0f to %.0f LUFS (about %.0f)", lufsWindowLo, lufsWindowHi, targetLUFS),
			Source: srcEarshot + "; " + srcPractice,
			Fix:    "The sound track is silent: give the clips a sound mode (audio.mode bed for ambience, full for speech; a clip left in mute, or an asset with no sound track, is silent), or add a licensed track or sound effects. A speed ramp mutes the speech of a full clip, not the bed of a bed clip.",
		})
		return cs
	}

	// Loudness.
	li := r.Loudness.IntegratedLUFS
	add(CheckItem{
		ID: "loudness", Title: "Integrated loudness", Status: st(li >= lufsWindowLo && li <= lufsWindowHi, li >= lufsWindowLo-lufsWarnMargin && li <= lufsWindowHi+lufsWarnMargin),
		Value: fmt.Sprintf("%.1f LUFS", li), Target: fmt.Sprintf("%.0f to %.0f LUFS (about %.0f)", lufsWindowLo, lufsWindowHi, targetLUFS),
		Source: srcEarshot + " (median -14.8; web window -16..-9); practice: about -14 for YouTube and the web",
		Fix:    loudnessFix(li),
	})
	tp := r.Loudness.TruePeakDBTP
	tpStatus := st(tp <= truePeakLimit, tp <= truePeakDecoded)
	add(CheckItem{
		ID: "true_peak", Title: "True peak", Status: tpStatus,
		Value: fmt.Sprintf("%.1f dBTP", tp), Target: fmt.Sprintf("at most %.0f dBTP (a decoded file: at most %.0f)", truePeakLimit, truePeakDecoded),
		Source: srcPractice + " (encoding adds up to about 1 dB); earshot: 6 of 7 films were above 0 dBTP, a flaw not to copy",
		Fix:    "Lower the master or the sound effects' gain: the render limits at -1.5 dBFS, so a higher true peak comes from the programme itself.",
	})
	add(CheckItem{
		ID: "loudness_range", Title: "Loudness range", Status: st(r.Loudness.RangeLU <= rangeMaxLU, r.Loudness.RangeLU <= rangeWarnMaxLU),
		Value: fmt.Sprintf("%.1f LU", r.Loudness.RangeLU), Target: fmt.Sprintf("at most %.1f LU", rangeMaxLU),
		Source: srcEarshot + " (1.7 to 12.2 LU, median 5.5)", Estimate: false,
		Fix: "A wide range means loud and quiet stretches: even out the clips' gain, or duck the music under speech.",
	})

	// The hook.
	fs := r.Hook.FirstSoundS
	add(CheckItem{
		ID: "first_sound", Title: "First sound", Status: st(fs >= 0 && fs <= firstSoundMaxS, fs >= 0 && fs <= firstSoundWarnS),
		Value: secondsOrNever(fs), Target: fmt.Sprintf("within %.1f s", firstSoundMaxS),
		Source: srcEarshot + " (0.04 to 0.48 s, median 0.13 s): no silent cold open",
		Fix:    "Start sound at frame 0: put the music, the clip's own sound or a hit under the first shot.",
	})
	tb := r.Hook.TimeToBodyS
	add(CheckItem{
		ID: "time_to_body", Title: "Time to body level", Status: st(tb >= 0 && tb <= bodyMaxS, tb >= 0 && tb <= bodyWarnS),
		Value: secondsOrNever(tb), Target: fmt.Sprintf("within %.1f s (momentary loudness within %.0f LU of the programme)", bodyMaxS, bodyWithinLU),
		Source: srcEstimate + " (earshot: 6 of 7 films open within -1.8..+2.2 LU of their level; the edit style wants the hook in 2 s)", Estimate: true,
		Fix: "The sound builds too slowly: start the music or the loud clip earlier, or cut the quiet lead-in.",
	})

	// Silence.
	lg := r.Silence.LongestS
	add(CheckItem{
		ID: "silent_gap", Title: "Longest silent gap", Status: st(lg <= gapMaxS, lg <= gapWarnS),
		Value: fmt.Sprintf("%.2f s", lg), Target: fmt.Sprintf("at most %.1f s (a deliberate stop-down: up to %.1f s)", gapMaxS, gapWarnS),
		Source: srcEarshot + " (no silent hole over 0.04 s except one film's deliberate 1.66 s)",
		Fix:    "Fill the gap with sound or shorten the cut; keep a silence only before a hit you mean.",
	})
	es := r.Silence.EndS
	add(CheckItem{
		ID: "dead_air_at_end", Title: "Dead air at the end", Status: st(es <= endSilenceMaxS, es <= endSilenceWarnS),
		Value: fmt.Sprintf("%.2f s", es), Target: fmt.Sprintf("at most %.1f s", endSilenceMaxS),
		Source: srcEarshot + " (median 0.04 s); practice: finish within about 0.5 s of the last frame",
		Fix:    "End on a sting or let the music resolve into the last frame; trim the silent tail.",
	})
	el := r.Loudness.EndLU
	add(CheckItem{
		ID: "ending_level", Title: "The last 3 s reach body level", Status: st(el >= endLUMin && el <= endLUMax, el >= endLUMin-3 && el <= endLUMax+3),
		Value: fmt.Sprintf("%+.1f LU vs the programme", el), Target: fmt.Sprintf("%+.0f to %+.0f LU", endLUMin, endLUMax),
		Source: srcEarshot + " (-2.2 to +5.6 LU, median +0.2): the ending lands, it does not fade away",
		Fix:    "Avoid a long fade-out: end on a hit or let the music finish at its level.",
	})

	// Sync.
	if len(r.Sync.Cuts) == 0 {
		add(CheckItem{ID: "cut_sync", Title: "Sound on the cuts", Status: StatusInfo, Value: "no cuts", Target: "-", Source: srcEstimate, Estimate: true})
	} else {
		share := r.Sync.CutsWithin100msShare
		lift := share - r.Sync.ChanceWithin100msShare
		// Both: a share over the minimum and a lift over chance. With dense sound every cut has an onset near it, whatever the sync,
		// so a high share with no lift is "covered by the music", which is a fine thing to be but not a sign of sync: it passes the
		// share and is reported as info when the chance level is itself over the target.
		status := st(share >= cutSyncMinShare && lift >= cutSyncMinLift, share >= cutSyncMinShare-0.2 && lift >= 0)
		if share >= cutSyncMinShare && r.Sync.ChanceWithin100msShare >= cutSyncMinShare {
			// Chance alone would pass: the sound is dense enough to cover every cut. Say so, and require the lift only when it can show.
			status = StatusInfo
			if lift >= cutSyncMinLift {
				status = StatusPass
			}
		}
		add(CheckItem{
			ID: "cut_sync", Title: "Cuts with an onset within 100 ms", Status: status,
			Value:  fmt.Sprintf("%.0f %% (%.0f %% within a frame; %.0f %% by chance, lift %+.0f points)", share*100, r.Sync.CutsWithinFrameShare*100, r.Sync.ChanceWithin100msShare*100, lift*100),
			Target: fmt.Sprintf("at least %.0f %% and %.0f points over chance", cutSyncMinShare*100, cutSyncMinLift*100),
			Source: srcEstimate + " (earshot measured a median of 81 % but 86 % by chance: dense music covers every cut, so the lift over chance is what shows sync)", Estimate: true,
			Fix: "Put a whoosh, a hit or a music accent on the cuts that matter (a sparse sound track needs sound on every cut).",
		})
	}
	if r.Sync.SFXPlanned == 0 {
		add(CheckItem{ID: "sfx_sync", Title: "Sound effects on their frame", Status: StatusInfo, Value: "no sound effects", Target: "-", Source: srcEstimate, Estimate: true})
	} else {
		share := r.Sync.SFXWithinFrameShare
		add(CheckItem{
			ID: "sfx_sync", Title: "Sound effects heard within a frame of their at_ms", Status: st(share >= sfxFrameMinShare, share >= sfxFrameMinShare-0.2),
			Value:  fmt.Sprintf("%.0f %% (%d of %d heard at all, %.0f %% within 100 ms)", share*100, r.Sync.SFXFound, r.Sync.SFXPlanned, r.Sync.SFXWithin100msShare*100),
			Target: fmt.Sprintf("at least %.0f %%", sfxFrameMinShare*100),
			Source: srcEstimate + " (our effects start on their first sample; a quiet one under loud music cannot be separated: earshot's LIMITS.md)", Estimate: true,
			Fix: "An effect that is not heard is masked by the programme: raise its gain_db or move it away from a loud moment.",
		})
	}

	// Density.
	if r.Density.SFXCount == 0 {
		add(CheckItem{ID: "sfx_density", Title: "Sound effects per 10 s", Status: StatusInfo, Value: "0", Target: "-", Source: srcEstimate, Estimate: true})
	} else {
		d := r.Density.SFXPer10s
		add(CheckItem{
			ID: "sfx_density", Title: "Sound effects per 10 s", Status: st(d <= sfxPer10sMax, d <= sfxPer10sWarnMax),
			Value: fmt.Sprintf("%.1f", d), Target: fmt.Sprintf("at most %.0f", sfxPer10sMax),
			Source: srcEstimate + " (edit style: about one effect per 3-4 cuts; earshot: a cluttered effect layer on busy music turns into noise)", Estimate: true,
			Fix: "Keep the effects for the moments that carry meaning (the reveal, the key action, the section change) and let the music carry the rest.",
		})
	}
	q := r.Density.LongestQuietS
	add(CheckItem{
		ID: "sound_events", Title: "Longest stretch with no new sound", Status: st(q <= quietMaxS, q <= quietWarnMaxS),
		Value: fmt.Sprintf("%.1f s (at %.1f s), %.1f onsets a second", q, r.Density.LongestQuietAt, r.Onsets.PerSecond), Target: fmt.Sprintf("at most %.1f s", quietMaxS),
		Source: srcEstimate + " (earshot: 2.5 to 8.2 onsets a second in music-led films; a speech edit is sparser)", Estimate: true,
		Fix: "A long stretch with nothing new feels dead: add a cue, a whoosh on a transition or a music change.",
	})

	// Problems.
	add(CheckItem{
		ID: "clipping", Title: "No clipping", Status: st(!r.Problems.Clipping, false),
		Value: fmt.Sprintf("peak %.1f dBFS, %d samples at the peak", r.Problems.PeakDBFS, r.Problems.PeakCount), Target: "under full scale",
		Source: srcPractice, Fix: "Lower the gain of the loudest source; the render's limiter cannot undo a clipped source.",
	})
	add(CheckItem{
		ID: "dc_offset", Title: "No DC offset", Status: st(!r.Problems.DC, false),
		Value: fmt.Sprintf("%.4f", r.Problems.DCOffset), Target: fmt.Sprintf("under %.3f", dcMax), Source: srcEstimate, Estimate: true,
		Fix: "Re-encode the source with a high-pass at 20 Hz.",
	})
	if len(r.Problems.ChannelRMSDB) >= 2 {
		add(CheckItem{
			ID: "channel_balance", Title: "Stereo channels balanced", Status: st(!r.Problems.Imbalance, false),
			Value: fmt.Sprintf("%.1f dB apart", r.Problems.ImbalanceDB), Target: fmt.Sprintf("within %.0f dB", imbalanceMaxDB), Source: srcEstimate, Estimate: true,
			Fix: "One channel is much quieter: check the sources for a mono or one-sided file.",
		})
	}
	return cs
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func secondsOrNever(s float64) string {
	if s < 0 {
		return "never"
	}
	return fmt.Sprintf("%.2f s", s)
}

func loudnessFix(lufs float64) string {
	if lufs < lufsWindowLo {
		return "Too quiet: the render normalises the programme to -14 LUFS, so the sources are being lost: check the clips' gain and that the sounds are not muted (a speed ramp mutes a clip's own sound)."
	}
	return "Too loud for the web: lower the programme; the render normalises to -14 LUFS."
}

// counts returns how many checks of each status there are.
func counts(cs []CheckItem) (pass, warn, fail, info int) {
	for _, c := range cs {
		switch c.Status {
		case StatusPass:
			pass++
		case StatusWarn:
			warn++
		case StatusFail:
			fail++
		default:
			info++
		}
	}
	return pass, warn, fail, info
}

// soundReadyChecklist turns the programme checklist into the sound-ready one (P3-12). The export is a bed and effects to which the creator
// adds a trend sound in TikTok, so the web's programme loudness, the long-silence and the ending checks do not apply (they stay as info);
// what matters is that the bed is there and steady, that the effects cut through it, land on their frame and would still be heard under
// a trend sound, and that the export is in the sound-ready loudness range with headroom. Every target here is an estimate of ours.
func soundReadyChecklist(r Report, cs []CheckItem) []CheckItem {
	if !r.HasAudio || r.Loudness.IntegratedLUFS <= -69 {
		for i := range cs {
			if cs[i].ID == "loudness" {
				cs[i].Fix = "The export is silent: put the clips' own sound in as a bed (set_clip_audio with mode bed for every clip) and add a few sound effects."
			}
		}
		return cs
	}
	st := func(ok, warn bool) string {
		switch {
		case ok:
			return StatusPass
		case warn:
			return StatusWarn
		}
		return StatusFail
	}
	info := func(c *CheckItem) {
		c.Status = StatusInfo
		c.Source += " (info in the sound-ready profile: the export is a bed and effects)"
	}
	var out []CheckItem
	for _, c := range cs {
		switch c.ID {
		case "loudness":
			li := r.Loudness.IntegratedLUFS
			out = append(out, CheckItem{
				ID: "loudness", Title: "Integrated loudness (sound-ready range)", Status: st(li >= soundReadyLo && li <= soundReadyHi, li >= soundReadyLo-soundReadyWarn && li <= soundReadyHi+soundReadyWarn),
				Value: fmt.Sprintf("%.1f LUFS", li), Target: fmt.Sprintf("%.0f to %.0f LUFS (about -20 for few effects)", soundReadyLo, soundReadyHi),
				Source: srcEstimate + " (D-33: sound-ready: the creator's trend sound brings the programme loudness; a bed at -24 and a few effects integrate at about -20, and the export is not normalised to the web's -14)", Estimate: true,
				Fix: "A lower reading means the bed is almost silent (check the clips have sound: set_clip_audio mode bed), a higher one that there are many effects or a clip that is far too loud.",
			})
		case "silent_gap":
			g := r.Silence.LongestS
			out = append(out, CheckItem{
				ID: "bed_gaps", Title: "The bed has no gap", Status: st(g <= bedGapMaxS, g <= bedGapWarnS),
				Value: fmt.Sprintf("longest silent gap %.2f s", g), Target: fmt.Sprintf("at most %.1f s", bedGapMaxS),
				Source: srcEstimate + " (a cut fades for 20 ms; a longer hole is a clip with no sound or one left in mute)", Estimate: true,
				Fix: "Set every clip's audio to bed (set_clip_audio with mode bed, no id) so there is sound under every shot; a clip with no sound of its own needs a sound effect on it.",
			})
		case "sfx_sync":
			out = append(out, soundReadySync(r))
		case "loudness_range", "time_to_body", "dead_air_at_end", "ending_level", "sound_events":
			info(&c)
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}

	bed := r.Bed
	if bed == nil {
		bed = &BedStats{LevelLUFS: silenceFloor, P10LUFS: silenceFloor}
	}
	out = append(out,
		CheckItem{
			ID: "bed_present", Title: "The bed is there", Status: st(bed.Coverage >= bedCoverageMin, bed.Coverage >= bedCoverageWarn),
			Value:  fmt.Sprintf("sound over %.0f LUFS for %.0f %% of the time, bed at %.1f LUFS", bedPresentLUFS, bed.Coverage*100, bed.LevelLUFS),
			Target: fmt.Sprintf("at least %.0f %% of the time", bedCoverageMin*100), Source: srcEstimate + " (D-33: an export that is silent until a sound is added feels broken)", Estimate: true,
			Fix: "Put the clips' own sound in as a bed: set_clip_audio with mode bed (no id) for the footage without speech.",
		},
		CheckItem{
			ID: "bed_level", Title: "The bed is a low ambience", Status: bedLevelStatus(bed.LevelLUFS),
			Value: fmt.Sprintf("%.1f LUFS (median)", bed.LevelLUFS), Target: fmt.Sprintf("%.0f LUFS, within %.0f LU", bedLevelLUFS, bedLevelTolLU),
			Source: srcEstimate + " (D-33: a trend sound layered in TikTok is at about -14 LUFS and must dominate; a bed within 10 LU of it competes with it)", Estimate: true,
			Fix: "The bed is louder or quieter than an ambience should be: lower or raise it with gain_db on the clip that sticks out (set_clip_audio), or mute a clip whose sound is not an ambience.",
		},
		CheckItem{
			ID: "bed_steady", Title: "The bed is steady", Status: st(bed.LevelLUFS-bed.P10LUFS <= bedSteadyDipLU, bed.LevelLUFS-bed.P10LUFS <= bedSteadyWarnLU),
			Value: fmt.Sprintf("the quietest tenth is %.1f LU under the median", bed.LevelLUFS-bed.P10LUFS), Target: fmt.Sprintf("at most %.0f LU", bedSteadyDipLU),
			Source: srcEstimate + " (the renderer evens the clips out to one level; a dip is a clip with little sound)", Estimate: true,
			Fix: "A shot has much less sound than the rest: replace it, or lay a sound effect over it.",
		},
	)
	var accents, through, audible int
	var weak, quiet []string
	for _, s := range r.Sync.SFX {
		if !s.Accent || s.AtMs >= int64(r.DurationS*1000) || s.PeakLUFS <= -69 {
			continue
		}
		accents++
		if s.MarginLU >= cutThroughMinLU {
			through++
		} else {
			weak = append(weak, s.ID)
		}
		if s.PeakLUFS >= trendLUFS-trendMarginLU {
			audible++
		} else {
			quiet = append(quiet, s.ID)
		}
	}
	if accents == 0 {
		for _, id := range []string{"sfx_cut_through", "trend_audible"} {
			out = append(out, CheckItem{ID: id, Title: map[string]string{"sfx_cut_through": "Effects cut through the bed", "trend_audible": "Effects audible under a trend sound"}[id],
				Status: StatusInfo, Value: "no accent effects", Target: "-", Source: srcEstimate, Estimate: true})
		}
		return out
	}
	share := func(n int) float64 { return float64(n) / float64(accents) }
	out = append(out,
		CheckItem{
			ID: "sfx_cut_through", Title: "Effects cut through the bed", Status: st(through == accents, share(through) >= 0.5),
			Value: fmt.Sprintf("%d of %d accents are at least %.0f LU over the bed", through, accents, cutThroughMinLU), Target: "all of them",
			Source: srcEstimate + " (a hit that is not clearly over the bed is not heard)", Estimate: true,
			Fix: "Raise the gain_db of " + strings.Join(weak, ", ") + " (a few dB), or use a heavier variant (impact deep, whoosh heavy): they are lost in the bed.",
		},
		CheckItem{
			ID: "trend_audible", Title: "Effects audible under a trend sound", Status: st(audible == accents, share(audible) >= 0.5),
			Value: fmt.Sprintf("%d of %d accents peak within %.0f LU of %.0f LUFS", audible, accents, trendMarginLU, trendLUFS), Target: fmt.Sprintf("peak (momentary) at least %.0f LUFS", trendLUFS-trendMarginLU),
			Source: srcEstimate + " (each hit's peak momentary loudness against a trend sound at -14 LUFS, with a 6 LU margin: a transient 6 dB under music is still heard; an estimate, not a listening test)", Estimate: true,
			Fix: "Raise the gain_db of " + strings.Join(quiet, ", ") + ", or use a stronger effect: under a trend sound at -14 LUFS they will not be heard.",
		},
	)
	return out
}

// soundReadySync judges the effects on their frame under a bed. A bed has onsets of its own (a rep, a plate), so the nearest onset of a
// soft effect can be the bed's; and a riser, a downlifter or a shimmer have no onset at all. So a hard attack (impact, sting, sub_drop,
// click, pop, glitch) must be heard within a frame, a whoosh or a swish within 100 ms, and the others are left to the loudness checks.
func soundReadySync(r Report) CheckItem {
	hard := map[string]bool{timeline.SFXImpact: true, timeline.SFXSting: true, timeline.SFXSubDrop: true, timeline.SFXClick: true, timeline.SFXPop: true, timeline.SFXGlitch: true}
	soft := map[string]bool{timeline.SFXWhoosh: true, timeline.SFXSwish: true}
	counted, ok := 0, 0
	var late []string
	for _, s := range r.Sync.SFX {
		if s.AtMs >= int64(r.DurationS*1000) {
			continue
		}
		heard := s.OnsetMs >= 0
		switch {
		case hard[s.Sfx]:
			counted++
			if heard && s.WithinFrame {
				ok++
			} else {
				late = append(late, s.ID)
			}
		case soft[s.Sfx]:
			counted++
			if heard && s.DeltaMs >= -100 && s.DeltaMs <= 100 {
				ok++
			} else {
				late = append(late, s.ID)
			}
		}
	}
	c := CheckItem{
		ID: "sfx_sync", Title: "Sound effects land on their frame", Target: fmt.Sprintf("at least %.0f %%", sfxFrameMinShare*100),
		Source: srcEstimate + " (a hard attack within a frame, a whoosh or swish within 100 ms because the bed has onsets of its own; risers, downlifters and shimmers have no onset to measure)", Estimate: true,
	}
	if counted == 0 {
		c.Status, c.Value, c.Target = StatusInfo, "no effect with a measurable onset", "-"
		return c
	}
	share := float64(ok) / float64(counted)
	c.Value = fmt.Sprintf("%.0f %% (%d of %d)", share*100, ok, counted)
	switch {
	case share >= sfxFrameMinShare:
		c.Status = StatusPass
	case share >= sfxFrameMinShare-0.2:
		c.Status = StatusWarn
	default:
		c.Status = StatusFail
	}
	c.Fix = "Move " + strings.Join(late, ", ") + " to the frame of the cut or hit (at_ms), or raise its gain_db: it is not heard on its frame, so the bed or another sound covers it."
	return c
}

// bedLevelStatus judges the median short-term loudness of the bed against bedLevelLUFS.
func bedLevelStatus(level float64) string {
	d := level - bedLevelLUFS
	if d < 0 {
		d = -d
	}
	switch {
	case d <= bedLevelTolLU:
		return StatusPass
	case d <= bedLevelWarnLU:
		return StatusWarn
	}
	return StatusFail
}
