package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The turning points: a rep's measured turning point (the analysis's peak_ms) is a legal edge, also when it is not the midpoint (a fast
// pull and a slow return); the midpoint stands in only when the analysis has none.
func TestTheMeasuredTurningPointIsARepBoundary(t *testing.T) {
	// One slow rep from 10000 to 14400 ms whose turn (the top of the pull) is at 11300, far from the midpoint 12200.
	clip := AssetAnalysis{Analysed: true, Reps: []Span{{StartMs: 10000, EndMs: 14400, TurnMs: 11300}}}
	run := func(in, out int64) Report {
		spec := specOf(t, []shot{{asset: assetA, in: in, out: out}, {asset: assetB, in: 0, out: 2000}}, title(0), nil)
		return RunChecks(CheckInput{Spec: spec, Assets: map[string]AssetAnalysis{assetA: clip}})
	}
	// Half a rep: from the turn to the end. On.
	wantStatus(t, run(11300, 14400), CheckRepBoundaries, CheckPass)
	// The midpoint is not the turn of this rep: off.
	c := wantStatus(t, run(12200, 14400), CheckRepBoundaries, CheckFail)
	if !strings.Contains(c.Detail, "nearest rep boundary 11300 ms") {
		t.Errorf("detail: %s", c.Detail)
	}
	// Without a measured turn the midpoint stands in (older analyses).
	clip.Reps[0].TurnMs = 0
	wantStatus(t, run(12200, 14400), CheckRepBoundaries, CheckPass)
}

// The tolerance is what the measurement allows (half an analysis sample, the snap, half a frame: 150 ms at speed 1, longer in the source
// at a higher speed) or, for a slow rep, 5 % of its length (still at the turn); a fast rep keeps 150.
func TestRepToleranceFollowsTheMeasurementAndTheRepsTempo(t *testing.T) {
	for _, c := range []struct {
		speed  float64
		repLen int64
		want   int64
	}{
		{1, 3000, 150},     // a 3 s rep: 5 % is 150
		{1, 1500, 150},     // a fast rep is not held tighter than the measurement
		{1, 4347, 217},     // a slow rep of 4.3 s: 217 ms is still at the turn
		{1.018, 2000, 152}, // a shot played a hair fast: the snap and the frame are longer in the source
		{1.25, 2000, 175},
		{1, 9000, 250}, // capped
	} {
		if got := repTolerance(c.speed, c.repLen); got != c.want {
			t.Errorf("speed %v rep %d: %d, want %d", c.speed, c.repLen, got, c.want)
		}
	}
	// The case of the live run: an edge 153 ms from the end of a 4.3 s rep, on a beat, at speed 1.018, is on the rep; the same 153 ms on a
	// rep of 1.5 s is not.
	slow := AssetAnalysis{Analysed: true, Reps: []Span{{StartMs: 21000, EndMs: 25300}}}
	spec := specOf(t, []shot{{asset: assetA, in: 21000, out: 25453}, {asset: assetB, in: 0, out: 2000}}, title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: spec, Assets: map[string]AssetAnalysis{assetA: slow}}), CheckRepBoundaries, CheckPass)
	fast := AssetAnalysis{Analysed: true, Reps: []Span{{StartMs: 23800, EndMs: 25300}}}
	spec = specOf(t, []shot{{asset: assetA, in: 23800, out: 25453}, {asset: assetB, in: 0, out: 2000}}, title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: spec, Assets: map[string]AssetAnalysis{assetA: fast}}), CheckRepBoundaries, CheckFail)
}

// A profile shaped by numbers only (a test fixture, not any creator's): five references, cut-up videos with a median shot of 2 s.
func paceProfile(median, p90, ending float64) *styleprofile.Profile {
	return &styleprofile.Profile{References: 5, Pacing: styleprofile.PacingProfile{
		Style: styleprofile.PaceMixed, ShotS: styleprofile.Range{Median: median}, ShotP90S: p90, EndingS: styleprofile.Range{Median: ending},
	}}
}

func TestPaceTargetsComeFromTheProfileScaledByTheVibeAndTheSetting(t *testing.T) {
	p := paceProfile(2.0, 4.0, 2.5)
	lively := Pace(p, TypeWorkout, VibeLively, "")
	if !lively.Applies || !lively.FromProfile || lively.MedianMs != 2000 || lively.MedianLoMs != 1200 || lively.MedianHiMs != 2800 || lively.MaxShotMs != 4000 || lively.EndingMs != 2500 || lively.EndingMovingMs != 4000 {
		t.Errorf("lively %+v", lively)
	}
	if hype := Pace(p, TypeWorkout, VibeHype, ""); hype.MedianMs != 1400 || hype.MaxShotMs != 2800 || hype.EndingMs != 1750 {
		t.Errorf("hype %+v", hype)
	}
	if calm := Pace(p, TypeWorkout, VibeCalm, ""); calm.MedianMs != 2600 || calm.EndingMs != 3250 {
		t.Errorf("calm %+v", calm)
	}
	// The creator's own setting scales after the vibe.
	if slower := Pace(p, TypeWorkout, VibeLively, "slower"); slower.MedianMs != 2500 || slower.Setting != "slower" {
		t.Errorf("slower %+v", slower)
	}
	if faster := Pace(p, TypeWorkout, VibeLively, "faster"); faster.MedianMs != 1600 {
		t.Errorf("faster %+v", faster)
	}
	// Another creator, another pace: nothing is fixed.
	if other := Pace(paceProfile(3.2, 6.0, 4.0), TypeWorkout, VibeLively, ""); other.MedianMs != 3200 || other.MaxShotMs != 6000 || other.EndingMs != 4000 {
		t.Errorf("other %+v", other)
	}
	// A profile from before the 90th percentile and the ending were measured: ratios of the median.
	if old := Pace(paceProfile(2.0, 0, 0), TypeWorkout, VibeLively, ""); old.MaxShotMs != 3500 || old.EndingMs != 2500 {
		t.Errorf("old %+v", old)
	}
	// No profile: the defaults, in beats of the default grid.
	if def := Pace(nil, TypeWorkout, VibeLively, ""); !def.Applies || def.FromProfile || def.MedianMs != 1935 || def.MaxShotMs != 3387 {
		t.Errorf("defaults %+v", def)
	}
	// Talking follows the speech; a long-take creator at calm keeps their long takes.
	if talk := Pace(p, TypeTalking, VibeLively, ""); talk.Applies {
		t.Errorf("talking %+v", talk)
	}
	long := paceProfile(2.0, 4.0, 2.5)
	long.Pacing.Style = styleprofile.PaceLongTake
	if calm := Pace(long, TypeWorkout, VibeCalm, ""); calm.Applies {
		t.Errorf("long takes at calm %+v", calm)
	}
	if lv := Pace(long, TypeWorkout, VibeLively, ""); !lv.Applies || lv.MedianMs != 2000 {
		t.Errorf("long takes at lively %+v", lv)
	}
	if d := lively.Describe(); !strings.Contains(d, "the creator's measured shot lengths at the lively vibe") || !strings.Contains(d, "turning point") {
		t.Errorf("describe %q", d)
	}
}

// The shot-length checks: a slow edit of five long shots (the hook, three full slow reps, a long held ending) against a creator whose
// shots run about 2 s fails shot_pacing, long_shot and held_ending; the same footage cut at the turning points and with a short ending
// passes them.
func TestTheShotLengthChecksHoldTheEditToTheCreatorsPace(t *testing.T) {
	p := paceProfile(2.0, 3.5, 2.5)
	slow := specOf(t, []shot{{asset: assetA, in: 0, out: 1900}, {asset: assetA, in: 10000, out: 15300}, {asset: assetA, in: 20000, out: 24000},
		{asset: assetA, in: 30000, out: 35300}, {asset: assetB, in: 0, out: 3400}}, title(0), nil)
	r := RunChecks(CheckInput{Spec: slow, Plan: workoutPlan(), Style: p, Vibe: VibeLively})
	c := wantStatus(t, r, CheckShotPacing, CheckFail)
	if !strings.Contains(c.Detail, "the target is about 2.00 s (1.20 s to 2.80 s") || !strings.Contains(c.Detail, "turning point") {
		t.Errorf("pacing: %s", c.Detail)
	}
	c = wantStatus(t, r, CheckLongShot, CheckFail)
	if !strings.Contains(c.Detail, "c2 (5.30 s), c3 (4.00 s), c4 (5.30 s) are longer than 3.50 s") {
		t.Errorf("long: %s", c.Detail)
	}
	c = wantStatus(t, r, CheckHeldEnding, CheckFail)
	if !strings.Contains(c.Detail, "held 3.40 s and sits still; at most 2.50 s") {
		t.Errorf("ending: %s", c.Detail)
	}

	quick := specOf(t, []shot{{asset: assetA, in: 0, out: 1900}, {asset: assetA, in: 10000, out: 12200}, {asset: assetA, in: 12200, out: 14000},
		{asset: assetA, in: 20000, out: 22600}, {asset: assetA, in: 30000, out: 31500}, {asset: assetA, in: 31500, out: 33600}, {asset: assetB, in: 0, out: 2300}}, title(0), nil)
	r = RunChecks(CheckInput{Spec: quick, Plan: workoutPlan(), Style: p, Vibe: VibeLively})
	wantStatus(t, r, CheckShotPacing, CheckPass)
	wantStatus(t, r, CheckLongShot, CheckPass)
	wantStatus(t, r, CheckHeldEnding, CheckPass)
	if r.Metrics.PaceMedianMs != 2000 || r.Metrics.PaceMaxShotMs != 3500 {
		t.Errorf("metrics %+v", r.Metrics)
	}
}

// A held ending may run longer when it moves: a push with a punch-in on a beat.
func TestAMovingEndingMayBeHeldLonger(t *testing.T) {
	p := paceProfile(2.0, 3.5, 2.5)
	spec := specOf(t, []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 10000, out: 12000}, {asset: assetB, in: 0, out: 3500}}, title(0), nil)
	main, _, _ := timeline.MainTrack(spec)
	last := &main.Items[len(main.Items)-1]
	at := int64(1000) // the ending starts at 4000; a beat every 500 ms
	last.Motion = &timeline.Motion{Preset: timeline.MotionHandheld, AtMs: &at}
	for b := int64(0); b <= 8000; b += 500 {
		spec.Markers = append(spec.Markers, timeline.Marker{Kind: timeline.MarkerBeat, AtMs: b})
	}
	c := wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: p, Vibe: VibeLively}), CheckHeldEnding, CheckPass)
	if !strings.Contains(c.Detail, "with a push and a punch-in on a beat") {
		t.Errorf("moving: %s", c.Detail)
	}
	off := int64(1230) // the punch is off the beat: a still ending
	last.Motion.AtMs = &off
	wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: p, Vibe: VibeLively}), CheckHeldEnding, CheckFail)
}

// The creator's pace in "Your style" (slower x1.25, like my videos x1, faster x0.8) scales the targets after the vibe: check_edit takes it
// from the effective settings when the input has none, and plan_edit's targets say it.
func TestTheCreatorsPaceSettingScalesThePaceTargets(t *testing.T) {
	p := paceProfile(2.0, 3.5, 2.5)
	spec := specOf(t, []shot{{asset: assetA, in: 0, out: 1900}, {asset: assetA, in: 10000, out: 12000}, {asset: assetB, in: 0, out: 2000}}, title(0), nil)
	for setting, want := range map[string]int64{styleprofile.PaceSlower: 2500, styleprofile.PaceLikeMine: 2000, styleprofile.PaceFaster: 1600} {
		set := stillSettings(p)
		set.Pace = setting
		r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: p, Vibe: VibeLively, Settings: set})
		if r.Metrics.PaceMedianMs != want {
			t.Errorf("%s: the target median is %d ms, want %d", setting, r.Metrics.PaceMedianMs, want)
		}
	}
	set := stillSettings(p)
	set.Pace = styleprofile.PaceFaster
	if d := Pace(p, TypeWorkout, VibeLively, paceSetting(set)).Describe(); !strings.Contains(d, `the creator's pace setting "faster" (x0.8)`) {
		t.Errorf("describe: %s", d)
	}
	if paceSetting(nil) != "" {
		t.Error("no style has no pace setting")
	}
}
