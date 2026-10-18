package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/camshift"
	"github.com/nilpntr/content-fabrique/internal/music"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Three long tripod takes and a hard-cut hook, as the founder's static edit was: dead still, all hard cuts, a text that just appears.
func staticEdit(t *testing.T) *timeline.Spec {
	t.Helper()
	return specOf(t, []shot{{asset: assetA, in: 0, out: 2500}, {asset: assetA, in: 3000, out: 12000}, {asset: assetB, in: 0, out: 9000}, {asset: assetB, in: 10000, out: 19000}}, title(0), nil)
}

func TestTheStaticEditFailsTheEnergyChecksAtLively(t *testing.T) {
	r := RunChecks(CheckInput{Spec: staticEdit(t), Plan: workoutPlan()})
	c := wantStatus(t, r, CheckStaticShot, CheckFail)
	if !strings.Contains(c.Detail, "set_motion with preset handheld") || !strings.Contains(c.Detail, "c2 (9.0 s)") {
		t.Errorf("static: %s", c.Detail)
	}
	// Plain cuts are the style at every vibe: the hard cuts pass.
	wantStatus(t, r, CheckTransitionsStyle, CheckPass)
	c = wantStatus(t, r, CheckTextWithoutEntrance, CheckFail)
	if !strings.Contains(c.Detail, "t1 has no entrance and no exit") {
		t.Errorf("text: %s", c.Detail)
	}
	// At calm, the text motion is not asked for; the camera still breathes.
	calm := RunChecks(CheckInput{Spec: staticEdit(t), Plan: workoutPlan(), Vibe: VibeCalm})
	wantStatus(t, calm, CheckTransitionsStyle, CheckPass)
	wantStatus(t, calm, CheckTextWithoutEntrance, CheckSkip)
	wantStatus(t, calm, CheckStaticShot, CheckFail)
	// Footage that moves by itself is not dead still.
	moving := RunChecks(CheckInput{Spec: staticEdit(t), Plan: workoutPlan(), Assets: map[string]AssetAnalysis{assetA: {Analysed: true, Camera: camshift.Handheld}, assetB: {Analysed: true, Camera: camshift.Handheld}}})
	wantStatus(t, moving, CheckStaticShot, CheckPass)
}

func TestTheLivelyEditPassesTheEnergyChecks(t *testing.T) {
	spec := specOf(t, []shot{
		{asset: assetA, in: 0, out: 2500, motion: timeline.MotionHandheld},
		{asset: assetA, in: 3000, out: 6000, motion: timeline.MotionHandheld},
		{asset: assetB, in: 0, out: 3000, motion: timeline.MotionHandheld},
		{asset: assetB, in: 4000, out: 7000, motion: timeline.MotionHandheld},
	}, title(0), nil)
	tt, _ := spec.Tracks[1].(*timeline.TextTrack)
	tt.Items[0].Animation, tt.Items[0].Exit = "pop", "pop_out"
	for _, vibe := range Vibes {
		r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Vibe: vibe})
		for _, id := range []string{CheckStaticShot, CheckTransitionsStyle} {
			wantStatus(t, r, id, CheckPass)
		}
		// The living camera is not an effect.
		if r.Metrics.Effects != 0 {
			t.Errorf("%s: effects %d: the handheld clips were counted", vibe, r.Metrics.Effects)
		}
	}
	wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Vibe: VibeLively}), CheckTextWithoutEntrance, CheckPass)
}

// Transitions follow the creator's effective style, never the vibe: none by default at every vibe (a creator's verdict on punch and
// zoom_through transitions was that they look like an automatic AI edit); lots when the creator asks for lots, and then an edit without
// them fails; a few when subtle; and when their videos use them, only those kinds at about their rate.
func TestTransitionsMatchTheStyleNotTheVibe(t *testing.T) {
	withTrans := func(types ...string) *timeline.Spec {
		shots := []shot{{asset: assetA, in: 0, out: 2000}}
		for i, ty := range types {
			shots = append(shots, shot{asset: assetA, in: int64(i+1) * 2000, out: int64(i+1)*2000 + 2000, trans: ty})
		}
		return specOf(t, shots, nil, nil)
	}
	cut := timeline.TransitionCut
	punchy := withTrans(timeline.TransitionPunch, cut, timeline.TransitionZoomThrough, cut)
	for _, vibe := range Vibes {
		c := wantStatus(t, RunChecks(CheckInput{Spec: punchy, Plan: workoutPlan(), Vibe: vibe}), CheckTransitionsStyle, CheckFail)
		if !strings.Contains(c.Detail, "plain cuts on the beat (transitions off, the default)") || !strings.Contains(c.Detail, "automatic AI edit") {
			t.Errorf("%s: %s", vibe, c.Detail)
		}
	}
	set := func(level string) *styleprofile.Resolved {
		r := styleprofile.Resolve(nil, nil, styleprofile.Settings{Transitions: level}, "")
		return &r
	}
	plain := withTrans(cut, cut, cut, cut)
	c := wantStatus(t, RunChecks(CheckInput{Spec: plain, Plan: workoutPlan(), Settings: set(styleprofile.LevelLots)}), CheckTransitionsStyle, CheckFail)
	if !strings.Contains(c.Detail, "0 of 4 cuts are transitions; the style asks for lots (the creator's choice)") {
		t.Errorf("lots: %s", c.Detail)
	}
	wantStatus(t, RunChecks(CheckInput{Spec: punchy, Plan: workoutPlan(), Settings: set(styleprofile.LevelLots)}), CheckTransitionsStyle, CheckPass)
	wantStatus(t, RunChecks(CheckInput{Spec: plain, Plan: workoutPlan(), Settings: set(styleprofile.LevelSubtle)}), CheckTransitionsStyle, CheckPass)
	wantStatus(t, RunChecks(CheckInput{Spec: punchy, Plan: workoutPlan(), Settings: set(styleprofile.LevelSubtle)}), CheckTransitionsStyle, CheckFail)

	// Videos that blend at a fifth of their cuts: a crossfade is theirs, a punch is not.
	p := ginoProfile(t)
	p.Transitions = styleprofile.TransitionsProfile{Measured: 5, Cuts: 20, Used: styleprofile.TransitionsSome, Kinds: []string{"blend"}, Share: 0.2, Videos: 3}
	measured := styleprofile.Resolve(p, nil, styleprofile.Settings{}, "")
	if measured.Transitions != styleprofile.LevelSubtle || measured.Sources.Transitions != styleprofile.SourceMeasured {
		t.Fatalf("measured %+v", measured)
	}
	wantStatus(t, RunChecks(CheckInput{Spec: withTrans(timeline.TransitionCrossfade, cut, cut, cut), Plan: workoutPlan(), Style: p, Settings: &measured}), CheckTransitionsStyle, CheckPass)
	c = wantStatus(t, RunChecks(CheckInput{Spec: withTrans(timeline.TransitionPunch, cut, cut, cut), Plan: workoutPlan(), Style: p, Settings: &measured}), CheckTransitionsStyle, CheckFail)
	if !strings.Contains(c.Detail, "the creator's videos use only blend") || !strings.Contains(c.Detail, "crossfade") {
		t.Errorf("measured kinds: %s", c.Detail)
	}
}

// A transition switches the picture at its middle: beat_sync counts it on the beat when its middle is.
func TestATransitionIsOnTheBeatAtItsMiddle(t *testing.T) {
	// The first shot is 2000 ms, so the whip (150 ms in specOf) starts at 1850 and switches at 1925; the second cut is hard at 3850.
	spec := specOf(t, []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 0, out: 2000, trans: timeline.TransitionWhipLeft}, {asset: assetB, in: 0, out: 2000}}, nil,
		[]timeline.Marker{{AtMs: 1925, Kind: timeline.MarkerBeat}, {AtMs: 3850, Kind: timeline.MarkerBeat}})
	if c := wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: workoutPlan()}), CheckBeatSync, CheckPass); !strings.Contains(c.Detail, "2 of 2 cuts") {
		t.Errorf("%s", c.Detail)
	}
}

func TestTransitionVariety(t *testing.T) {
	mk := func(types ...string) *timeline.Spec {
		shots := []shot{{asset: assetA, in: 0, out: 2000}}
		for i, ty := range types {
			shots = append(shots, shot{asset: assetA, in: int64(i) * 2000, out: int64(i)*2000 + 2000, trans: ty})
		}
		return specOf(t, shots, nil, nil)
	}
	r := RunChecks(CheckInput{Spec: mk(timeline.TransitionWhip, timeline.TransitionWhipLeft, timeline.TransitionWhipLeft)})
	if c := wantStatus(t, r, CheckTransitionVariety, CheckFail); !strings.Contains(c.Detail, "three whip_left transitions in a row") {
		t.Errorf("%s", c.Detail)
	}
	wantStatus(t, RunChecks(CheckInput{Spec: mk(timeline.TransitionWhipLeft, timeline.TransitionPunch, timeline.TransitionWhipLeft)}), CheckTransitionVariety, CheckPass)
	wantStatus(t, RunChecks(CheckInput{Spec: mk(timeline.TransitionPunch)}), CheckTransitionVariety, CheckSkip)
}

func TestChapterTitles(t *testing.T) {
	plan := workoutPlan()
	plan.Shots = []PlanShot{{Role: "hook"}, {Role: "step", Chapter: 1}, {Role: "step", Chapter: 1}, {Role: "step", Chapter: 2}}
	texts := []timeline.TextItem{{ID: "t1", AtMs: 0, DurationMs: 1500, Text: "3 ESSENTIALS", Style: "title_series", Anchor: "upper_third"}}
	spec := specOf(t, []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 3000, out: 6000}, {asset: assetB, in: 0, out: 3000}}, texts, nil)
	c := wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: plan}), CheckChapterTitles, CheckFail)
	if !strings.Contains(c.Detail, "2 chapters and the edit 0 numbered chapter titles") {
		t.Errorf("%s", c.Detail)
	}
	tt, _ := spec.Tracks[1].(*timeline.TextTrack)
	tt.Items = append(tt.Items, timeline.TextItem{ID: "t2", AtMs: 2000, DurationMs: 1200, Text: "1. SHRUGS", Style: "title_series", Anchor: "upper_third"},
		timeline.TextItem{ID: "t3", AtMs: 5000, DurationMs: 1200, Text: "2) LIFTING STRAPS", Style: "title_series", Anchor: "upper_third"})
	wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: plan}), CheckChapterTitles, CheckPass)
	wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: workoutPlan()}), CheckChapterTitles, CheckSkip)
	if IsChapterTitle("101 BACK") || !IsChapterTitle("3. HORIZONTALE PULL") {
		t.Error("IsChapterTitle")
	}
}

// A song at 124 BPM from 0, with its drop at 2903 ms (beat 6): the cuts are judged against its beats, the hook against its drop.
const assetMusicSong = "ast_01j0h9rew727hk2pt0v5b31h44"

func songAnalysis() music.Analysis {
	var beats, downs []int64
	for k := range 60 {
		b := int64(float64(k)*60000/124 + 0.5)
		beats = append(beats, b)
		if k%4 == 2 {
			downs = append(downs, b)
		}
	}
	return music.Analysis{Version: music.Version, Music: true, TempoBPM: 124, BeatsMs: beats, DownbeatsMs: downs, DropMs: beats[6], DurationMs: 30000}
}

// withSong puts the song on the spec at at_ms 0 from offset, and returns the song on the timeline.
func withSong(spec *timeline.Spec, offset int64) *TimelineMusic {
	spec.Tracks = append(spec.Tracks, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{
		&timeline.CreatorAudio{AudioCommon: timeline.AudioCommon{ID: "song", SourceOffsetMs: offset, Export: timeline.ExportLicensed}, AssetID: assetMusicSong},
	}})
	return MusicOnTimeline(spec, map[string]music.Analysis{assetMusicSong: songAnalysis()})
}

func TestCutsOnTheMusicAndTheHookOnTheDrop(t *testing.T) {
	// Shots of 2903 (to the drop), 1935 (4 beats) and 2500 ms (off the grid): the second cut is on a beat, the third is not.
	spec := specOf(t, []shot{{asset: assetA, in: 0, out: 2903}, {asset: assetA, in: 3000, out: 4935}, {asset: assetB, in: 0, out: 2500}, {asset: assetB, in: 3000, out: 6000}}, nil, nil)
	m := withSong(spec, 0)
	if m == nil || !m.HasDrop || m.DropMs != 2903 {
		t.Fatalf("song %+v", m)
	}
	r := RunChecks(CheckInput{Spec: spec, Music: m})
	wantStatus(t, r, CheckHookMissesDrop, CheckPass)
	// The hook runs until the drop (2.9 s): with a song that is not too long for a hook.
	if c := wantStatus(t, r, CheckHookLength, CheckPass); !strings.Contains(c.Detail, "at most 3.10 s") {
		t.Errorf("hook: %s", c.Detail)
	}
	c := wantStatus(t, r, CheckOffBeatCut, CheckFail)
	if !strings.Contains(c.Detail, "2 of 3 cuts land on the song's beats") || !strings.Contains(c.Detail, "c4 at 7338 ms") || !strings.Contains(c.Detail, "sound_id song") {
		t.Errorf("off beat: %s", c.Detail)
	}
	// The song started 500 ms later in its own time: its drop plays at 2403 ms, the hook misses it, and the fix moves the song back.
	spec = specOf(t, []shot{{asset: assetA, in: 0, out: 2903}, {asset: assetA, in: 3000, out: 6000}}, nil, nil)
	m = withSong(spec, 500)
	c = wantStatus(t, RunChecks(CheckInput{Spec: spec, Music: m}), CheckHookMissesDrop, CheckFail)
	if !strings.Contains(c.Detail, "drops at 2403 ms") || !strings.Contains(c.Detail, "set source_offset_ms of song to 0") {
		t.Errorf("drop: %s", c.Detail)
	}
	// No song: not applicable.
	wantStatus(t, RunChecks(CheckInput{Spec: spec}), CheckOffBeatCut, CheckSkip)
	wantStatus(t, RunChecks(CheckInput{Spec: spec}), CheckHookMissesDrop, CheckSkip)
	// get_music's text names the times and how to use them.
	text := MusicText(assetMusicSong, songAnalysis(), 0, 5000)
	for _, want := range []string{"beats_ms (11): 0, 484, 968", "drop_ms: 2903", "source_offset_ms = drop_ms - (the time of the first cut)", "snap_cuts_to_beats"} {
		if !strings.Contains(text, want) {
			t.Errorf("get_music lacks %q:\n%s", want, text)
		}
	}
}
