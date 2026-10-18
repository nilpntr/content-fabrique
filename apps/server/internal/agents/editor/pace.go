package editor

import (
	"fmt"
	"math"

	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// The pace of an edit: how long its shots run. Nothing here is about one creator. The numbers come from the creator's style profile (the
// shot lengths measured in their own finished videos), scaled by the vibe of the edit and by the creator's own pace setting, and only
// without a profile from the general defaults below. plan_edit tells the editor the targets, and check_edit holds the edit to them
// (shot_pacing, long_shot, held_ending).
//
//   - The median shot aims at the creator's measured median shot (every shot of their videos that have a cut), within ±40 %.
//   - No shot but the hook and the ending runs longer than the creator's measured 90th percentile shot.
//   - The ending is held no longer than the creator's measured ending (the last shot of their videos), or up to 1.6 times that when it
//     moves: a push with a punch-in on a beat.
//
// The vibe scales all three: calm 1.3, lively 1.0, hype 0.7. Lively is "like the creator's own videos". The factors are a beat either
// way on the shots this pace is about: a shot of 3 to 4 beats gets about one beat more at calm (x1.3) and one beat less at hype (x0.7),
// which is the difference the playbook describes between a held moment and a hype cut (2 to 4 beats against 1 to 2), without leaving the
// creator's own range (their quartiles run about 0.5 to 1.6 times their median). At calm a creator whose videos are long takes keeps
// their long takes: the targets do not apply, and the long-take thresholds of the profile do (applyStyle).

// The factors of the vibes and of the creator's pace setting.
var (
	vibePace = map[string]float64{VibeCalm: 1.3, VibeLively: 1.0, VibeHype: 0.7}
	// PaceSettings are the creator's own pace overrides ("Your style": slower, like my videos, faster): a quarter longer or a fifth
	// shorter, the same step as the vibes and never as far as a vibe apart. "" is no setting.
	PaceSettings = map[string]float64{"": 1, styleprofile.PaceLikeMine: 1, styleprofile.PaceSlower: 1.25, styleprofile.PaceFaster: 0.8}
)

// paceSetting is the pace setting of the effective style ("" without a style). "like_mine", the default, is a factor of 1.
func paceSetting(set *styleprofile.Resolved) string {
	if set == nil {
		return ""
	}
	return set.Pace
}

// The bounds of the targets.
const (
	// paceBand is how far the median shot may be from the target: ±40 %.
	paceBand = 0.4
	// endingMoving is how much longer a moving ending (a push and a punch-in on a beat) may be held than a still one.
	endingMoving = 1.6
	// p90OverMedian and endingOverMedian stand in for a profile made before the 90th percentile and the ending were measured: the ratios of
	// the defaults below.
	p90OverMedian    = 7.0 / 4.0
	endingOverMedian = 5.0 / 4.0
)

// paceDefaults are the targets without a profile, in beats of the default grid (124 BPM, a beat of 484 ms), from the generic playbook: a
// strong moment of 4 beats is the typical shot (1.94 s), a full rep of 7 beats the longest that is worth it (3.39 s), and the loop's
// ending a held 5 beats (2.42 s). Talking and tutorial videos follow the speech, not a pace: they have none.
var paceDefaults = map[string]struct{ median, p90, ending float64 }{
	TypeWorkout:  {4, 7, 5},
	TypeUnboxing: {4, 7, 5},
	TypeVlog:     {4, 8, 5},
	TypeGeneral:  {4, 7, 5},
}

// defaultBeatMs is the beat of the default grid the defaults are counted in.
const defaultBeatMs = 60000.0 / 124

// PaceTargets are the shot lengths of an edit, in output milliseconds.
type PaceTargets struct {
	// Applies is false when there is no pace to hold the edit to (a talking or tutorial video, a long-take creator at calm).
	Applies bool
	// FromProfile says the numbers are the creator's measured ones (else the defaults).
	FromProfile bool
	// MedianMs is the target median shot, MedianLoMs to MedianHiMs the range it may be in.
	MedianMs, MedianLoMs, MedianHiMs int64
	// MaxShotMs is the longest a shot may be, the hook and the ending left out.
	MaxShotMs int64
	// EndingMs is the longest a still ending may be held, EndingMovingMs one with a push and a punch-in on a beat.
	EndingMs, EndingMovingMs int64
	// Factor is the vibe's factor times the setting's; Vibe and Setting what they were.
	Factor        float64
	Vibe, Setting string
}

// Pace computes the targets of an edit from the creator's profile (nil, or one too weak to be a rule, gives the defaults), the content
// type, the vibe and the creator's pace setting ("" for none).
func Pace(p *styleprofile.Profile, contentType, vibe, setting string) PaceTargets {
	vibe = VibeOr(vibe)
	f, ok := PaceSettings[setting]
	if !ok {
		f, setting = 1, ""
	}
	t := PaceTargets{Factor: vibePace[vibe] * f, Vibe: vibe, Setting: setting}
	def, ok := paceDefaults[contentType]
	if !ok {
		return t
	}
	median, p90, ending := def.median*defaultBeatMs, def.p90*defaultBeatMs, def.ending*defaultBeatMs
	if p.Firm() {
		pc := p.Pacing
		m := pc.ShotS.Median
		if m <= 0 {
			m = pc.ShotMedianS.Median
		}
		if vibe == VibeCalm && pc.Style == styleprofile.PaceLongTake {
			return t // calm keeps a long-take creator's long takes: the profile's long-take thresholds hold
		}
		if m > 0 {
			t.FromProfile = true
			median = m * 1000
			p90, ending = median*p90OverMedian, median*endingOverMedian
			if pc.ShotP90S > 0 {
				p90 = pc.ShotP90S * 1000
			}
			if pc.EndingS.Median > 0 {
				ending = pc.EndingS.Median * 1000
			}
		}
	}
	ms := func(v float64) int64 { return int64(math.Round(v * t.Factor)) }
	t.Applies = true
	t.MedianMs = ms(median)
	t.MedianLoMs, t.MedianHiMs = int64(math.Round(float64(t.MedianMs)*(1-paceBand))), int64(math.Round(float64(t.MedianMs)*(1+paceBand)))
	t.MaxShotMs = max(ms(p90), t.MedianHiMs)
	t.EndingMs = ms(ending)
	t.EndingMovingMs = int64(math.Round(float64(t.EndingMs) * endingMoving))
	return t
}

// source says where the numbers come from, for the editor's notes.
func (t PaceTargets) source() string {
	from := "the general defaults (no creator style profile)"
	if t.FromProfile {
		from = "the creator's measured shot lengths"
	}
	s := fmt.Sprintf("%s at the %s vibe (x%.2g)", from, t.Vibe, vibePace[t.Vibe])
	if t.Setting != "" && t.Setting != styleprofile.PaceLikeMine {
		s += fmt.Sprintf(", and the creator's pace setting %q (x%.2g)", t.Setting, PaceSettings[t.Setting])
	}
	return s
}

// Describe is the targets as plan_edit tells them.
func (t PaceTargets) Describe() string {
	if !t.Applies {
		return ""
	}
	return fmt.Sprintf("Pace targets, from %s: a median shot of about %s (%s to %s); no shot but the hook and the ending over %s; the ending held at most %s, or %s with a push and a punch-in on a beat. Shots of a slow lift that do not fit: cut at the rep's turning point (half a rep on the beat), or switch to another angle of the same exercise at the turning point.",
		t.source(), secs2(t.MedianMs), secs2(t.MedianLoMs), secs2(t.MedianHiMs), secs2(t.MaxShotMs), secs2(t.EndingMs), secs2(t.EndingMovingMs))
}
