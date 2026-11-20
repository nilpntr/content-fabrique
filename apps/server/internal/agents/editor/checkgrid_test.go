package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/camshift"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// gridBeatMarkers are beats every 500 ms (120 bpm), so 2 beats are 1000 ms.
func gridBeatMarkers() []timeline.Marker {
	var m []timeline.Marker
	for i := int64(0); i < 60; i++ {
		m = append(m, timeline.Marker{AtMs: i * 500, Kind: timeline.MarkerBeat})
	}
	return m
}

// gridShots lays shots of the given lengths one after the other, each from its own stretch of the source.
func gridShots(lengths ...int64) []shot {
	var out []shot
	var in int64
	for _, l := range lengths {
		out = append(out, shot{asset: assetA, in: in, out: in + l})
		in += l + 1000
	}
	return out
}

// ginoStyle is Gino's style as his account holds it: hard cuts only, no motion, texts that just appear, the fixed 2-beat grid and every shot once.
func ginoStyle() *styleprofile.Resolved {
	r := styleprofile.Resolve(nil, nil, styleprofile.Settings{Transitions: styleprofile.LevelOff, CameraMotion: styleprofile.LevelOff, TextAnimation: styleprofile.LevelOff,
		CutGrid: styleprofile.CutGridFixed2, ShotReuse: styleprofile.ReuseOnce}, "")
	return &r
}

func TestAFixedTwoBeatGridPassesShotVarietyAndTheBeatArithmetic(t *testing.T) {
	// Four shots of 2 beats and a last one of 4 beats: 12 beats in all, every cut on an even beat.
	spec := specOf(t, gridShots(1000, 1000, 1000, 1000, 2000), title(0), gridBeatMarkers())
	r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Settings: ginoStyle()})
	wantStatus(t, r, CheckBeatGridFixed, CheckPass)
	wantStatus(t, r, CheckShotVariety, CheckSkip) // 4 shots of one length in a row would fail the usual rule
	wantStatus(t, r, CheckPaceBuilds, CheckSkip)
	// The pace rules that set the shot length give way to the grid too.
	wantStatus(t, r, CheckShotPacing, CheckSkip)
	wantStatus(t, r, CheckLongShot, CheckSkip)

	// A creator whose style does not declare the grid is held to the usual rule, even with the same shots.
	other := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan()})
	wantStatus(t, other, CheckShotVariety, CheckFail)
	wantStatus(t, other, CheckBeatGridFixed, CheckSkip)

	// Without a grid in the shots the old rule stands.
	flat := specOf(t, []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 3000, out: 5050}, {asset: assetA, in: 6000, out: 7980}, {asset: assetA, in: 9000, out: 12500}}, title(0), nil)
	r = RunChecks(CheckInput{Spec: flat})
	wantStatus(t, r, CheckShotVariety, CheckFail)
	wantStatus(t, r, CheckBeatGridFixed, CheckSkip)
}

func TestTheBeatArithmeticFailsWhatBreaksTheGrid(t *testing.T) {
	declared := func(lengths ...int64) Report {
		spec := specOf(t, gridShots(lengths...), title(0), gridBeatMarkers())
		return RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Settings: ginoStyle()})
	}
	// 5 shots of 2 beats and a 4-beat ending is 14 beats: not a multiple of 4.
	if c := wantStatus(t, declared(1000, 1000, 1000, 1000, 1000, 2000), CheckBeatGridFixed, CheckFail); !strings.Contains(c.Detail, "14 beats, not a multiple of 4") {
		t.Errorf("total: %s", c.Detail)
	}
	// The last shot is 3 beats.
	if c := wantStatus(t, declared(1000, 1000, 1000, 1500), CheckBeatGridFixed, CheckFail); !strings.Contains(c.Detail, "3 beats, not 4") {
		t.Errorf("last: %s", c.Detail)
	}
	// The second cut lands on beat 3.
	if c := wantStatus(t, declared(1000, 500, 1500, 2000), CheckBeatGridFixed, CheckFail); !strings.Contains(c.Detail, "an odd beat") {
		t.Errorf("odd: %s", c.Detail)
	}
	// A cut between beats.
	if c := wantStatus(t, declared(1000, 1230, 1000, 2000), CheckBeatGridFixed, CheckFail); !strings.Contains(c.Detail, "between beats") {
		t.Errorf("between: %s", c.Detail)
	}
	// Declared, but no beat grid in the edit.
	spec := specOf(t, gridShots(1000, 1000, 2000), title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: spec, Settings: ginoStyle()}), CheckBeatGridFixed, CheckFail)
}

func TestNoEffectsStyleStopsPushingMotion(t *testing.T) {
	off := ginoStyle()
	// Six plain, still, long-ish shots and a text that just appears: flat, static and bare, as Gino's edits are.
	spec := specOf(t, gridShots(2500, 2600, 2400, 2700, 2500, 2800), title(0), nil)
	asDefault := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Vibe: VibeLively})
	wantStatus(t, asDefault, CheckStaticShot, CheckFail)
	wantStatus(t, asDefault, CheckEffectDensity, CheckFail)
	wantStatus(t, asDefault, CheckTextWithoutEntrance, CheckFail)

	r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Vibe: VibeLively, Settings: off})
	wantStatus(t, r, CheckStaticShot, CheckSkip)
	wantStatus(t, r, CheckEffectDensity, CheckPass)
	wantStatus(t, r, CheckTextWithoutEntrance, CheckSkip)
}

func TestShotReused(t *testing.T) {
	ok := specOf(t, gridShots(1000, 1000, 1000), title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: ok, Settings: ginoStyle()}), CheckShotReused, CheckPass)
	// Not asked for by the style: not judged.
	tripodOther := map[string]AssetAnalysis{assetA: {Analysed: true, Camera: camshift.Static}}
	wantStatus(t, RunChecks(CheckInput{Spec: ok, Assets: tripodOther}), CheckShotReused, CheckSkip)

	// The same stretch of one clip twice (overlapping by half a second).
	twice := specOf(t, []shot{{asset: assetA, in: 0, out: 1000}, {asset: assetB, in: 0, out: 1000}, {asset: assetA, in: 500, out: 1500}}, title(0), nil)
	if c := wantStatus(t, RunChecks(CheckInput{Spec: twice, Settings: ginoStyle()}), CheckShotReused, CheckFail); !strings.Contains(c.Detail, "c1 and c3") {
		t.Errorf("overlap: %s", c.Detail)
	}

	// A tripod clip gives one shot, even from two different stretches.
	tripod := map[string]AssetAnalysis{assetA: {Analysed: true, Camera: camshift.Static}}
	if c := wantStatus(t, RunChecks(CheckInput{Spec: ok, Assets: tripod, Settings: ginoStyle()}), CheckShotReused, CheckFail); !strings.Contains(c.Detail, "tripod clip") {
		t.Errorf("tripod: %s", c.Detail)
	}
	one := specOf(t, []shot{{asset: assetA, in: 0, out: 1000}, {asset: assetB, in: 0, out: 1000}}, title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: one, Assets: tripod, Settings: ginoStyle()}), CheckShotReused, CheckPass)
}

func TestTheGridIsReadFromTheMarkers(t *testing.T) {
	gino := func(spec *timeline.Spec, mod func(*styleprofile.Resolved)) Check {
		set := ginoStyle()
		if mod != nil {
			mod(set)
		}
		return byID(RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Settings: set}))[CheckBeatGridFixed]
	}
	// A grid whose first beat is not at 0 (the song starts 120 ms in): the video's start counts as the first beat.
	late := func(first int64) []timeline.Marker {
		var m []timeline.Marker
		for i := int64(0); i < 60; i++ {
			m = append(m, timeline.Marker{AtMs: first + i*500, Kind: timeline.MarkerBeat})
		}
		return m
	}
	// The first shot runs from 0 to the second beat after the first (1120 ms), then 2 beats each and a 4-beat ending.
	spec := specOf(t, gridShots(1120, 1000, 1000, 1000, 2000), title(0), late(120))
	if c := gino(spec, nil); c.Status != CheckPass {
		t.Errorf("a grid from 120 ms: %s %s", c.Status, c.Detail)
	}
	// A cut nearer to one beat than to another is read as the nearest one, not the first within the tolerance: beats 40 ms apart.
	dense := []timeline.Marker{{AtMs: 0, Kind: timeline.MarkerBeat}, {AtMs: 1000, Kind: timeline.MarkerBeat}, {AtMs: 1030, Kind: timeline.MarkerBeat}, {AtMs: 2000, Kind: timeline.MarkerBeat}}
	for i := int64(3); i < 20; i++ {
		dense = append(dense, timeline.Marker{AtMs: i * 1000, Kind: timeline.MarkerBeat})
	}
	idx := (&checker{}).gridBeatsOf(dense)
	if i, ok := idx.index(1029, 66); !ok || i != 2 {
		t.Errorf("nearest beat: %d %v", i, ok)
	}
	// The tempo is the real one (500.5 ms here), so a cut 60 beats past the last marker still lands: a rounded gap would be 30 ms out.
	var drift []timeline.Marker
	for i := int64(0); i < 20; i++ {
		drift = append(drift, timeline.Marker{AtMs: i * 5005 / 10, Kind: timeline.MarkerBeat})
	}
	if i, ok := (&checker{}).gridBeatsOf(drift).index(19*5005/10+60*5005/10, 66); !ok || i != 79 {
		t.Errorf("extrapolated beat: %d %v", i, ok)
	}
}

func TestTheBeatArithmeticReportsEverythingAndSkipsWithoutMusic(t *testing.T) {
	// An odd cut, a 3-beat ending and a total of 7 beats are all in one answer.
	spec := specOf(t, gridShots(1000, 500, 1500, 1500), title(0), gridBeatMarkers())
	c := byID(RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Settings: ginoStyle()}))[CheckBeatGridFixed]
	for _, want := range []string{"an odd beat", "3 beats, not 4"} {
		if c.Status != CheckFail || !strings.Contains(c.Detail, want) {
			t.Errorf("want %q in %s %s", want, c.Status, c.Detail)
		}
	}
	// No music in the style: no beat grid is needed.
	set := ginoStyle()
	set.Music = styleprofile.MusicNever
	bare := specOf(t, gridShots(1000, 1000, 2000), title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: bare, Plan: workoutPlan(), Settings: set}), CheckBeatGridFixed, CheckSkip)
	// A plan without a tempo expects no grid.
	wantStatus(t, RunChecks(CheckInput{Spec: bare, Plan: &Plan{ContentType: TypeWorkout, TargetLengthS: 15}, Settings: ginoStyle()}), CheckBeatGridFixed, CheckSkip)
}

func TestCameraOffKeepsTheEffectMinimumWhileTransitionsAreOn(t *testing.T) {
	spec := specOf(t, gridShots(2500, 2600, 2400, 2700, 2500, 2800), title(0), nil)
	set := styleprofile.Resolve(nil, nil, styleprofile.Settings{Transitions: styleprofile.LevelSubtle, CameraMotion: styleprofile.LevelOff}, "")
	wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Vibe: VibeLively, Settings: &set}), CheckEffectDensity, CheckFail)
}
