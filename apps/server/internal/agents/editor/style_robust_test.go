package editor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// montageMeasures are n references of a fast creator: 13 one-second shots in 13 s, a cut a second, effects at fx a minute.
func montageMeasures(t *testing.T, n int, fx float64) []styleprofile.Measures {
	t.Helper()
	base := ginoMeasures(t)
	var out []styleprofile.Measures
	for i := 0; i < n; i++ {
		m := base[i%len(base)]
		m.Name = fmt.Sprintf("montage-%d.mp4", i)
		m.DurationS = 13
		m.CutsS = []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
		m.ShotsS = []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
		m.Video.EffectsPerMinute = fx
		out = append(out, m)
	}
	return out
}

// eight shots of 1 s that do not speed up, with no effects and no title: a flat edit of a montage.
func flatMontage(t *testing.T, firstMs int64) []shot {
	t.Helper()
	shots := []shot{{asset: assetA, in: 0, out: firstMs}}
	for i := 0; i < 7; i++ {
		shots = append(shots, shot{asset: assetA, in: 0, out: 1000 + int64(i)*100}) // growing: the pace does not build
	}
	return shots
}

// One odd reference does not open the gates for a montage creator: a 120 s single take and a first cut at 30 s leave the hook, the
// median shot and the length tight, so a slow edit still fails.
func TestAnOutlierDoesNotOpenTheChecksForAMontageCreator(t *testing.T) {
	ms := montageMeasures(t, 5, 9)
	ms[3].DurationS, ms[3].CutsS, ms[3].ShotsS = 120, nil, []float64{120}
	ms[4].CutsS = []float64{30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41}
	ms[4].ShotsS = []float64{30, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	ms[4].DurationS = 42
	p := styleprofile.Aggregate(ms)
	if !p.Firm() {
		t.Fatal("not firm")
	}
	slow := specOf(t, []shot{{asset: assetA, in: 0, out: 8000}, {asset: assetA, in: 0, out: 10000}, {asset: assetA, in: 0, out: 10000}, {asset: assetA, in: 0, out: 12000}}, title(0), nil)
	r := RunChecks(CheckInput{Spec: slow, Plan: &Plan{ContentType: TypeWorkout, TargetLengthS: 40, TempoBPM: 124}, Style: p})
	for _, id := range []string{CheckHookLength, CheckShotPacing} {
		if byID(r)[id].Status != CheckFail {
			t.Errorf("%s did not fail for a slow edit of a montage creator: %+v", id, byID(r)[id])
		}
	}
	if c := byID(r)[CheckHookLength]; !strings.Contains(c.Detail, "by 4.00 s") && !strings.Contains(c.Detail, "by 3.") && !strings.Contains(c.Detail, "by 2.") {
		t.Errorf("hook limit: %s", c.Detail)
	}
}

// The minimum effect rate and the building pace are loosened only for a creator of long takes or of few effects. A montage creator
// with busy effects keeps both; one with light effects loses them; the long-take creator loses them.
func TestStyleKeepsTheFastEditMinimumsWhereTheyApply(t *testing.T) {
	spec := specOf(t, flatMontage(t, 1000), title(0), nil)
	plan := &Plan{ContentType: TypeWorkout, TargetLengthS: 10, TempoBPM: 124}
	// Busy effects, montage: both keep their defaults.
	busy := styleprofile.Aggregate(montageMeasures(t, 5, 9))
	if busy.Effects.Verdict != styleprofile.EffectsBusy || busy.Pacing.Style != styleprofile.PaceMontage {
		t.Fatalf("busy profile: %s %s", busy.Effects.Verdict, busy.Pacing.Style)
	}
	r := RunChecks(CheckInput{Spec: spec, Plan: plan, Style: busy})
	if byID(r)[CheckEffectDensity].Status != CheckFail || !strings.Contains(byID(r)[CheckEffectDensity].Detail, "flat") {
		t.Errorf("effect_density: %+v", byID(r)[CheckEffectDensity])
	}
	if byID(r)[CheckPaceBuilds].Status != CheckFail {
		t.Errorf("pace_builds: %+v", byID(r)[CheckPaceBuilds])
	}
	// Light effects: loosened.
	light := styleprofile.Aggregate(montageMeasures(t, 5, 3))
	if light.Effects.Verdict != styleprofile.EffectsLight {
		t.Fatalf("light profile: %s", light.Effects.Verdict)
	}
	r = RunChecks(CheckInput{Spec: spec, Plan: plan, Style: light})
	if byID(r)[CheckEffectDensity].Status == CheckFail || byID(r)[CheckPaceBuilds].Status == CheckFail {
		t.Errorf("light effects keep the minimum: %+v / %+v", byID(r)[CheckEffectDensity], byID(r)[CheckPaceBuilds])
	}
	// Long takes: loosened (the Gino profile).
	r = RunChecks(CheckInput{Spec: spec, Plan: plan, Style: ginoProfile(t)})
	if byID(r)[CheckPaceBuilds].Status == CheckFail {
		t.Errorf("pace_builds for long takes: %+v", byID(r)[CheckPaceBuilds])
	}
}
