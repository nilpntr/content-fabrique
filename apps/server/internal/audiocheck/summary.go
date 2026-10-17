package audiocheck

import (
	"fmt"
	"strings"
)

// summarise writes the plain-language reading of a report: the verdict first, then what to fix, then the numbers a reader (a person
// or the editor model) wants next to the picture. It is plain text, in short lines.
func summarise(r Report) string {
	var b strings.Builder
	pass, warn, fail, _ := counts(r.Checklist)
	switch {
	case !r.HasAudio:
		b.WriteString("This video has no sound track at all. ")
	case r.Loudness.IntegratedLUFS <= -69:
		b.WriteString("The sound track is digital silence: nothing in it can be heard. ")
	case fail == 0 && warn == 0:
		b.WriteString("The sound is in good shape: every check passes. ")
	case fail == 0:
		fmt.Fprintf(&b, "The sound is mostly fine: %d of %d checks pass and %d are worth a look. ", pass, pass+warn, warn)
	default:
		fmt.Fprintf(&b, "The sound needs work: %d checks fail and %d are worth a look (%d pass). ", fail, warn, pass)
	}
	fmt.Fprintf(&b, "Length %.1f s at %.0f fps.\n", r.DurationS, r.FPS)

	if r.HasAudio && r.Loudness.IntegratedLUFS > -69 {
		fmt.Fprintf(&b, "Loudness: %.1f LUFS integrated, true peak %.1f dBTP, range %.1f LU (web target: about -14 LUFS, at most -1 dBTP).\n",
			r.Loudness.IntegratedLUFS, r.Loudness.TruePeakDBTP, r.Loudness.RangeLU)
		fmt.Fprintf(&b, "Start: first sound at %s, body level at %s. End: the last 3 s are %+.1f LU against the programme, %.2f s of silence at the very end.\n",
			secondsOrNever(r.Hook.FirstSoundS), secondsOrNever(r.Hook.TimeToBodyS), r.Loudness.EndLU, r.Silence.EndS)
		if n := len(r.Silence.Gaps); n > 0 {
			fmt.Fprintf(&b, "Silence: %d gap(s) of %.1f s or more, the longest %.2f s.\n", n, silenceMinS, r.Silence.LongestS)
		}
		fmt.Fprintf(&b, "Movement: %d new sounds (%.1f a second, %.1f of them big hits a second); the longest stretch with nothing new is %.1f s at %.1f s.\n",
			r.Onsets.Count, r.Onsets.PerSecond, r.Onsets.SalientPerSecond, r.Onsets.LongestWithoutS, r.Onsets.LongestWithoutAtS)
		if len(r.Sync.Cuts) > 0 {
			fmt.Fprintf(&b, "Sync: %.0f %% of the %d cuts have a new sound within 100 ms (%.0f %% within a frame); moved off the picture the same cuts would score %.0f %%, so the lift is %+.0f points.\n",
				r.Sync.CutsWithin100msShare*100, len(r.Sync.Cuts), r.Sync.CutsWithinFrameShare*100, r.Sync.ChanceWithin100msShare*100,
				(r.Sync.CutsWithin100msShare-r.Sync.ChanceWithin100msShare)*100)
		}
		if r.Sync.SFXPlanned > 0 {
			fmt.Fprintf(&b, "Effects: %d planned (%.1f per 10 s); %d heard near their time, %.0f %% within a frame.\n",
				r.Sync.SFXPlanned, r.Density.SFXPer10s, r.Sync.SFXFound, r.Sync.SFXWithinFrameShare*100)
		}
		var defects []string
		if r.Problems.Clipping {
			defects = append(defects, fmt.Sprintf("clipping (peak %.1f dBFS)", r.Problems.PeakDBFS))
		}
		if r.Problems.DC {
			defects = append(defects, fmt.Sprintf("a DC offset of %.4f", r.Problems.DCOffset))
		}
		if r.Problems.Imbalance {
			defects = append(defects, fmt.Sprintf("channels %.1f dB apart", r.Problems.ImbalanceDB))
		}
		if len(defects) > 0 {
			b.WriteString("Defects: " + strings.Join(defects, ", ") + ".\n")
		}
	}

	var fixes []string
	for _, c := range r.Checklist {
		if (c.Status == StatusFail || c.Status == StatusWarn) && c.Fix != "" {
			tag := "FIX"
			if c.Status == StatusWarn {
				tag = "LOOK"
			}
			est := ""
			if c.Estimate {
				est = " (estimate)"
			}
			fixes = append(fixes, fmt.Sprintf("%s: %s is %s, target %s%s. %s", tag, strings.ToLower(c.Title[:1])+c.Title[1:], c.Value, c.Target, est, c.Fix))
		}
	}
	if len(fixes) > 0 {
		b.WriteString("\n" + strings.Join(fixes, "\n") + "\n")
	}
	b.WriteString("\nThese are measurements, not taste: they say where and how loud, not whether it sounds good. Targets marked estimate are ours; the others are from earshot's measured launch films or web delivery practice (docs/design/audio-v1.md).")
	return b.String()
}
