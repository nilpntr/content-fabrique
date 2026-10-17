package audiocheck

import (
	"math"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// soundReadyMeasured is a healthy sound-ready file: 10 s of a steady bed at -24 LUFS (momentary every 100 ms) with two accents that
// peak 8 LU over it, a cut at 5 s, integrated loudness at -20.
func soundReadyMeasured() measured {
	pcm := pcmWith(10, 0.5, 2.0, 5.0)
	for i := range pcm { // a low steady bed under the hits
		pcm[i] += float32(0.02 * math.Sin(2*math.Pi*300*float64(i)/PCMRate))
	}
	var mom []LoudnessPoint
	for i := 1; i <= 100; i++ {
		ts := float64(i) / 10
		l := -24.0
		if (ts >= 2.0 && ts <= 2.6) || (ts >= 5.0 && ts <= 5.6) {
			l = -16
		}
		mom = append(mom, LoudnessPoint{TimeS: ts, LUFS: l})
	}
	return measured{
		durationS: 10, fps: 30, hasAudio: true, integrated: -20, lra: 6, truePeak: -2.5, pcm: pcm, momentary: mom,
		channels: []chanStats{{peakDB: -3, rmsDB: -24, peakCount: 1}, {peakDB: -3, rmsDB: -24, peakCount: 1}},
	}
}

func TestSoundReadyProfileHealthyFile(t *testing.T) {
	cfg := config{fps: 30, profile: ProfileSoundReady, sfx: []PlannedSFX{{ID: "s1", Sfx: "impact", AtMs: 2000}, {ID: "s2", Sfx: "whoosh", AtMs: 5000}, {ID: "s3", Sfx: "pop", AtMs: 7000}}}
	r := analyse(soundReadyMeasured(), nil, cfg)
	if r.Profile != ProfileSoundReady || r.Bed == nil {
		t.Fatalf("profile %q bed %+v", r.Profile, r.Bed)
	}
	if r.Bed.LevelLUFS != -24 || r.Bed.Coverage < 0.99 {
		t.Errorf("bed %+v", r.Bed)
	}
	for _, id := range []string{"loudness", "bed_gaps", "bed_present", "bed_level", "bed_steady", "sfx_cut_through", "trend_audible", "true_peak"} {
		if c := find(r, id); c.Status != StatusPass {
			t.Errorf("%s: %s (%s, target %s)", id, c.Status, c.Value, c.Target)
		}
	}
	// The programme checks that do not apply are info; the pop is detail, not an accent.
	for _, id := range []string{"loudness_range", "time_to_body", "dead_air_at_end", "ending_level", "sound_events"} {
		if c := find(r, id); c.Status != StatusInfo {
			t.Errorf("%s should be info in the sound-ready profile, is %s", id, c.Status)
		}
	}
	if find(r, "silent_gap").ID == "silent_gap" {
		t.Error("the programme's silent_gap is still there")
	}
	for _, s := range r.Sync.SFX {
		if s.ID == "s1" && (!s.Accent || s.MarginLU != 8 || s.PeakLUFS != -16) {
			t.Errorf("impact %+v", s)
		}
		if s.ID == "s3" && s.Accent {
			t.Errorf("a pop is not an accent: %+v", s)
		}
	}
	for _, c := range r.Checklist {
		if c.Source == "" || c.Target == "" || c.Value == "" || c.Title == "" {
			t.Errorf("a check without its source, target or value: %+v", c)
		}
		if c.Estimate && !strings.Contains(c.Source, "estimate") {
			t.Errorf("%s: an estimate whose source does not say so: %q", c.ID, c.Source)
		}
	}
	if !strings.Contains(r.Summary, "sound-ready") {
		t.Errorf("summary: %s", r.Summary)
	}
}

func TestSoundReadyProfileFindsTheProblems(t *testing.T) {
	m := soundReadyMeasured()
	// An accent that does not rise over the bed (2 LU) and sits under a trend sound's reach (-22 is under -20).
	for i := range m.momentary {
		if ts := m.momentary[i].TimeS; ts >= 2.0 && ts <= 2.6 {
			m.momentary[i].LUFS = -22
		}
	}
	// The bed drops out for 2 s.
	for i := range m.momentary {
		if ts := m.momentary[i].TimeS; ts >= 7.0 && ts <= 9.0 {
			m.momentary[i].LUFS = -80
		}
	}
	m.integrated = -25
	for i := 7 * PCMRate; i < 9*PCMRate; i++ {
		m.pcm[i] = 0
	}
	cfg := config{fps: 30, profile: ProfileSoundReady, sfx: []PlannedSFX{{ID: "s1", Sfx: "impact", AtMs: 2000}, {ID: "s2", Sfx: "whoosh", AtMs: 5000}}}
	r := analyse(m, nil, cfg)
	for _, id := range []string{"loudness", "bed_gaps", "bed_present", "sfx_cut_through", "trend_audible"} {
		c := find(r, id)
		if c.Status != StatusFail && c.Status != StatusWarn {
			t.Errorf("%s should complain: %s (%s)", id, c.Status, c.Value)
		}
		if c.Fix == "" {
			t.Errorf("%s has no one-line fix", id)
		}
	}
	if c := find(r, "sfx_cut_through"); !strings.Contains(c.Fix, "s1") || strings.Contains(c.Fix, "s2") {
		t.Errorf("the fix should name the weak effect only: %s", c.Fix)
	}
	if !strings.Contains(r.Summary, "FIX") && !strings.Contains(r.Summary, "LOOK") {
		t.Errorf("summary: %s", r.Summary)
	}
}

// Under a bed the nearest onset of a soft effect can be the bed's: a whoosh 83 ms off counts, an impact 3 frames late does not, and a
// riser has no onset to judge.
func TestSoundReadySyncJudgesByAttack(t *testing.T) {
	r := Report{DurationS: 10, Sync: Sync{SFX: []SFXSync{
		{ID: "w", Sfx: "whoosh", AtMs: 1000, OnsetMs: 917, DeltaMs: -83},
		{ID: "i", Sfx: "impact", AtMs: 3000, OnsetMs: 3000, DeltaMs: 0, WithinFrame: true},
		{ID: "r", Sfx: "riser", AtMs: 5000, OnsetMs: -1},
	}}}
	if c := soundReadySync(r); c.Status != StatusPass || c.Value != "100 % (2 of 2)" {
		t.Errorf("%+v", c)
	}
	r.Sync.SFX[1] = SFXSync{ID: "i", Sfx: "impact", AtMs: 3000, OnsetMs: 3100, DeltaMs: 100}
	c := soundReadySync(r)
	if c.Status == StatusPass || !strings.Contains(c.Fix, "i") || strings.Contains(c.Fix, "Move w") {
		t.Errorf("a late impact: %+v", c)
	}
	if c := soundReadySync(Report{DurationS: 10, Sync: Sync{SFX: []SFXSync{{ID: "r", Sfx: "riser", AtMs: 5000, OnsetMs: -1}}}}); c.Status != StatusInfo {
		t.Errorf("a riser alone: %+v", c)
	}
}

// A speech clip with a speed ramp is muted by the renderer, so the edit is still a bed and effects.
func TestProfileFromSpec(t *testing.T) {
	clip := func(id string, mode string, ramp bool) timeline.VideoItem {
		it := timeline.VideoItem{ID: id, Audio: &timeline.VideoAudio{Mode: mode}}
		if ramp {
			it.SpeedRamp = []timeline.SpeedPoint{{AtMs: 0, Speed: 1}, {AtMs: 500, Speed: 2}}
		}
		return it
	}
	spec := func(items ...timeline.VideoItem) *timeline.Spec {
		return &timeline.Spec{Tracks: []timeline.Track{&timeline.VideoTrack{ID: "main", Items: items}}}
	}
	for name, tc := range map[string]struct {
		spec *timeline.Spec
		want string
	}{
		"no spec":                 {nil, ProfileProgramme},
		"beds":                    {spec(clip("a", timeline.AudioModeBed, false), clip("b", timeline.AudioModeMute, false)), ProfileSoundReady},
		"speech":                  {spec(clip("a", timeline.AudioModeBed, false), clip("b", timeline.AudioModeFull, false)), ProfileProgramme},
		"speech under a ramp":     {spec(clip("a", timeline.AudioModeBed, false), clip("b", timeline.AudioModeFull, true)), ProfileSoundReady},
		"nothing but the effects": {spec(clip("a", timeline.AudioModeMute, false)), ProfileSoundReady},
	} {
		if got := ProfileFromSpec(tc.spec); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

// A riser peaks at its end, 1.5 s after it starts: its loudness is read over the stretch it covers, and a window that is mostly the
// hit before it is not counted.
func TestEffectPeakIsReadOverTheEffect(t *testing.T) {
	m := soundReadyMeasured()
	for i := range m.momentary {
		ts := m.momentary[i].TimeS
		switch {
		case ts >= 6.4 && ts <= 6.9: // the riser (5.0 to 6.5 s): its windows near the end
			m.momentary[i].LUFS = -15
		case ts >= 7.0 && ts <= 7.3: // a louder hit just before the next effect starts
			m.momentary[i].LUFS = -8
		}
	}
	cfg := config{fps: 30, profile: ProfileSoundReady, sfx: []PlannedSFX{{ID: "r1", Sfx: "riser", Variant: "tension", AtMs: 5000}, {ID: "p1", Sfx: "pop", AtMs: 7300}}}
	r := analyse(m, nil, cfg)
	var riser, pop SFXSync
	for _, s := range r.Sync.SFX {
		switch s.ID {
		case "r1":
			riser = s
		case "p1":
			pop = s
		}
	}
	if riser.PeakLUFS != -15 || riser.MarginLU != 9 {
		t.Errorf("the riser peaks at its end: %+v", riser)
	}
	if pop.PeakLUFS > -20 {
		t.Errorf("the pop at 7.3 s sees the hit before it (windows ending at 7.0 to 7.3 s hold mostly that): %+v", pop)
	}
}

// A bed at -18 LUFS competes with a trend sound at -14: the check says so; at -24 it is on level.
func TestBedLevelCheck(t *testing.T) {
	for level, want := range map[float64]string{-24: StatusPass, -26: StatusPass, -20: StatusWarn, -18: StatusFail, -31: StatusFail} {
		if got := bedLevelStatus(level); got != want {
			t.Errorf("bed at %.0f LUFS: %s, want %s", level, got, want)
		}
	}
}

func TestProgrammeProfileIsUnchanged(t *testing.T) {
	r := analyse(soundReadyMeasured(), nil, config{fps: 30})
	if r.Profile != ProfileProgramme || r.Bed != nil {
		t.Errorf("profile %q bed %+v", r.Profile, r.Bed)
	}
	if find(r, "silent_gap").ID != "silent_gap" || find(r, "bed_present").ID == "bed_present" {
		t.Error("the programme profile has the sound-ready checks")
	}
}
