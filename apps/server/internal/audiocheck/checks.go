package audiocheck

import "fmt"

// Where the targets come from (shown in the report, so that a reader can weigh them).
const (
	srcEarshot  = "earshot: measured on 7 launch films"
	srcPractice = "practice: web delivery"
	srcEstimate = "estimate"
)

// checklist builds the pass/fail list. A check that does not apply to the edit (no cuts, no effects) is info, never a failure.
func checklist(r Report) []CheckItem {
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
			Fix:    "The sound track is silent: mix in the clips' own sound (audio.use_source), a licensed track or sound effects. A speed ramp mutes a clip's own sound.",
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
		add(CheckItem{
			ID: "cut_sync", Title: "Cuts with an onset within 100 ms", Status: st(share >= cutSyncMinShare, share >= cutSyncMinShare-0.2),
			Value:  fmt.Sprintf("%.0f %% (%.0f %% within a frame; %.0f %% by chance, lift %+.0f points)", share*100, r.Sync.CutsWithinFrameShare*100, r.Sync.ChanceWithin100msShare*100, lift*100),
			Target: fmt.Sprintf("at least %.0f %%", cutSyncMinShare*100),
			Source: srcEstimate + " (earshot measured a median of 81 % but 86 % by chance: dense music covers every cut, so look at the lift)", Estimate: true,
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
