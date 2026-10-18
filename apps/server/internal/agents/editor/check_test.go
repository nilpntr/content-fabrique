package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The checks are pure: they read a spec and the analysis, so they are tested on specs written here.

const (
	assetA = "ast_01j9z3x7m8c4v6n2q5r0t1y8aa"
	assetB = "ast_01j9z3x7m8c4v6n2q5r0t1y8bb"
)

// shot is one main-track clip: its asset, its source range and its speed (0 for 1).
type shot struct {
	asset   string
	in, out int64
	speed   float64
	motion  string
	trans   string
	ramp    bool
}

// specOf lays shots out one after the other.
func specOf(t *testing.T, shots []shot, texts []timeline.TextItem, markers []timeline.Marker) *timeline.Spec {
	t.Helper()
	var items []timeline.VideoItem
	var at int64
	for i, s := range shots {
		it := timeline.VideoItem{ID: "c" + string(rune('1'+i)), AssetID: s.asset, Source: timeline.Source{InMs: s.in, OutMs: s.out}, AtMs: at}
		if s.speed != 0 {
			sp := s.speed
			it.Speed = &sp
		}
		if s.motion != "" {
			it.Motion = &timeline.Motion{Preset: s.motion}
		}
		if s.trans != "" {
			it.TransitionIn = &timeline.Transition{Type: s.trans, DurationMs: 150}
		}
		if s.ramp {
			it.Speed = nil
			it.SpeedRamp = []timeline.SpeedPoint{{AtMs: 0, Speed: 2}, {AtMs: (s.out - s.in) / 2, Speed: 0.5}}
		}
		items = append(items, it)
		at += timeline.OutputDuration(it)
		if i+1 < len(shots) && shots[i+1].trans != "" {
			at -= 150 // the next shot starts this much before this one ends
		}
	}
	tracks := []timeline.Track{&timeline.VideoTrack{ID: "main", Items: items}}
	if texts != nil {
		tracks = append(tracks, &timeline.TextTrack{ID: "text", Items: texts})
	}
	return &timeline.Spec{Schema: timeline.SchemaVersion, Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30}, Tracks: tracks, Markers: markers}
}

func title(at int64) []timeline.TextItem {
	return []timeline.TextItem{{ID: "t1", AtMs: at, DurationMs: 1500, Text: "HEAVY", Style: "title_bold", Anchor: "upper_third"}}
}

// repsClip is a clip with two reps (2 to 5 s and 5 to 8 s, turning points at 3.5 s and 6.5 s) and dead time around them.
func repsClip() AssetAnalysis {
	return AssetAnalysis{
		Analysed: true,
		Reps:     []Span{{2000, 5000, 0}, {5000, 8000, 0}},
		Dead:     []Span{{0, 2000, 0}, {8000, 12000, 0}},
	}
}

func flatSeries2(n int, luma, high float64) analysis.Series {
	s := analysis.Series{StepMs: 100, Luma: make([]float64, n), High: make([]float64, n)}
	for i := range n {
		s.Luma[i], s.High[i] = luma, high
	}
	return s
}

func byID(r Report) map[string]Check {
	m := map[string]Check{}
	for _, c := range r.Checks {
		m[c.ID] = c
	}
	return m
}

func wantStatus(t *testing.T, r Report, id string, want CheckStatus) Check {
	t.Helper()
	c, ok := byID(r)[id]
	if !ok {
		t.Fatalf("no check %s", id)
	}
	if c.Status != want {
		t.Errorf("%s = %s (%s), want %s", id, c.Status, c.Detail, want)
	}
	if strings.Contains(c.Detail, "\n") || c.Detail == "" {
		t.Errorf("%s: the reason is not one line: %q", id, c.Detail)
	}
	return c
}

func TestHookChecks(t *testing.T) {
	long := specOf(t, []shot{{asset: assetA, in: 0, out: 3500}, {asset: assetA, in: 0, out: 1000}}, title(1200), nil)
	r := RunChecks(CheckInput{Version: 1, Spec: long})
	if c := wantStatus(t, r, CheckHookLength, CheckFail); !strings.Contains(c.Detail, "3.50 s") {
		t.Errorf("hook length reason: %s", c.Detail)
	}
	if c := wantStatus(t, r, CheckHookTitle, CheckFail); !strings.Contains(c.Detail, "1.20 s") {
		t.Errorf("hook title reason: %s", c.Detail)
	}

	good := specOf(t, []shot{{asset: assetA, in: 0, out: 1500}, {asset: assetA, in: 0, out: 1000}}, title(0), nil)
	r = RunChecks(CheckInput{Version: 1, Spec: good})
	wantStatus(t, r, CheckHookLength, CheckPass)
	wantStatus(t, r, CheckHookTitle, CheckPass)

	// No text at all fails; enabled captions count as text from the first word.
	bare := specOf(t, []shot{{asset: assetA, in: 0, out: 1500}}, nil, nil)
	wantStatus(t, RunChecks(CheckInput{Spec: bare}), CheckHookTitle, CheckFail)
	bare.Tracks = append(bare.Tracks, &timeline.CaptionsTrack{ID: "captions", From: "transcript", Style: "word_highlight", Enabled: true})
	wantStatus(t, RunChecks(CheckInput{Spec: bare}), CheckHookTitle, CheckPass)
}

func TestShotLengthChecks(t *testing.T) {
	flat := specOf(t, []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 0, out: 2050}, {asset: assetA, in: 0, out: 1980}, {asset: assetA, in: 0, out: 3500}}, title(0), nil)
	r := RunChecks(CheckInput{Spec: flat})
	if c := wantStatus(t, r, CheckShotVariety, CheckFail); !strings.Contains(c.Detail, "3 shots in a row") || !strings.Contains(c.Detail, "c1 to c3") {
		t.Errorf("variety reason: %s", c.Detail)
	}
	if r.Metrics.LongestEqualRun != 3 {
		t.Errorf("longest run %d", r.Metrics.LongestEqualRun)
	}

	// Two equal shots in a row are fine.
	two := specOf(t, []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 0, out: 900}, {asset: assetA, in: 0, out: 3000}}, title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: two}), CheckShotVariety, CheckPass)

	// Five shots that all sit within a few percent of each other, but never two next to each other that are equal.
	same := specOf(t, []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 0, out: 2300}, {asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 0, out: 2300}, {asset: assetA, in: 0, out: 2000}}, title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: same}), CheckShotVariety, CheckFail)

	// Pacing: the first live edit had shots of 4.5 to 5 s. A workout fails it, a tutorial does not.
	slow := specOf(t, []shot{{asset: assetA, in: 0, out: 4500}, {asset: assetA, in: 0, out: 5000}, {asset: assetA, in: 0, out: 4600}}, title(0), nil)
	if c := wantStatus(t, RunChecks(CheckInput{Spec: slow, Plan: &Plan{ContentType: TypeWorkout, TargetLengthS: 14}}), CheckShotPacing, CheckFail); !strings.Contains(c.Detail, "median shot is 4.60 s") {
		t.Errorf("pacing reason: %s", c.Detail)
	}
	wantStatus(t, RunChecks(CheckInput{Spec: slow, Plan: &Plan{ContentType: TypeTutorial, TargetLengthS: 14}}), CheckShotPacing, CheckPass)
}

func TestPaceBuilds(t *testing.T) {
	slowing := []shot{{asset: assetA, in: 0, out: 1000}, {asset: assetA, in: 0, out: 1200}, {asset: assetA, in: 0, out: 1500}, {asset: assetA, in: 0, out: 2200}, {asset: assetA, in: 0, out: 2600}, {asset: assetA, in: 0, out: 3000}, {asset: assetA, in: 0, out: 3500}}
	wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, slowing, title(0), nil)}), CheckPaceBuilds, CheckFail)
	speeding := []shot{{asset: assetA, in: 0, out: 3000}, {asset: assetA, in: 0, out: 2600}, {asset: assetA, in: 0, out: 2200}, {asset: assetA, in: 0, out: 1500}, {asset: assetA, in: 0, out: 1200}, {asset: assetA, in: 0, out: 1000}, {asset: assetA, in: 0, out: 3000}}
	wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, speeding, title(0), nil)}), CheckPaceBuilds, CheckPass)
	wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, slowing[:3], title(0), nil)}), CheckPaceBuilds, CheckSkip)
	wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, slowing, title(0), nil), Plan: &Plan{ContentType: TypeTalking, TargetLengthS: 20}}), CheckPaceBuilds, CheckSkip)
}

func TestRepBoundaryCheck(t *testing.T) {
	assets := map[string]AssetAnalysis{assetA: repsClip()}
	// Edges on a rep start (2000), the turning point of the first rep (3500), a rep end within 150 ms (5100) and a rep end (8000).
	on := specOf(t, []shot{{asset: assetA, in: 2000, out: 3500}, {asset: assetA, in: 5100, out: 8000}}, title(0), nil)
	r := RunChecks(CheckInput{Spec: on, Assets: assets})
	wantStatus(t, r, CheckRepBoundaries, CheckPass)
	if r.Metrics.RepEdgesOn != 4 || r.Metrics.RepEdgesTotal != 4 {
		t.Errorf("rep edges %d of %d", r.Metrics.RepEdgesOn, r.Metrics.RepEdgesTotal)
	}

	// A shot that starts 700 ms into a rep and ends 600 ms before its end is cut mid-rep at both edges.
	mid := specOf(t, []shot{{asset: assetA, in: 2700, out: 4400}, {asset: assetA, in: 5000, out: 8000}}, title(0), nil)
	r = RunChecks(CheckInput{Spec: mid, Assets: assets})
	c := wantStatus(t, r, CheckRepBoundaries, CheckFail)
	if !strings.Contains(c.Detail, "c1 starts at 2700 ms (nearest rep boundary 2000 ms, 700 ms off") || !strings.Contains(c.Detail, "2 of 4") {
		t.Errorf("rep boundary reason: %s", c.Detail)
	}

	// A clip with a hold (a pose): an edge inside the hold is not mid-rep.
	held := repsClip()
	held.Holds = []Span{{9000, 12000, 0}}
	hold := specOf(t, []shot{{asset: assetA, in: 2000, out: 3500}, {asset: assetA, in: 9500, out: 11000}}, title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: hold, Assets: map[string]AssetAnalysis{assetA: held}}), CheckRepBoundaries, CheckPass)

	// A clip without reps or without an analysis is not judged.
	wantStatus(t, RunChecks(CheckInput{Spec: mid}), CheckRepBoundaries, CheckSkip)
	wantStatus(t, RunChecks(CheckInput{Spec: mid, Assets: map[string]AssetAnalysis{assetA: {Analysed: true}}}), CheckRepBoundaries, CheckSkip)
}

func TestDeadTimeCheck(t *testing.T) {
	assets := map[string]AssetAnalysis{assetA: repsClip()}
	clean := specOf(t, []shot{{asset: assetA, in: 2000, out: 5000}, {asset: assetA, in: 5000, out: 8000}}, title(0), nil)
	r := RunChecks(CheckInput{Spec: clean, Assets: assets})
	wantStatus(t, r, CheckDeadTime, CheckPass)
	if r.Metrics.DeadShare != 0 {
		t.Errorf("dead share %v", r.Metrics.DeadShare)
	}
	// Half of the second shot (8000 to 10000 is dead; it plays 7000 to 9000) and the whole of a third: a third of the screen time.
	dirty := specOf(t, []shot{{asset: assetA, in: 2000, out: 5000}, {asset: assetA, in: 7000, out: 9000}, {asset: assetA, in: 9000, out: 11000}}, title(0), nil)
	r = RunChecks(CheckInput{Spec: dirty, Assets: assets})
	c := wantStatus(t, r, CheckDeadTime, CheckFail)
	if r.Metrics.DeadShare < 0.3 || !strings.Contains(c.Detail, "dead time") {
		t.Errorf("dead share %v: %s", r.Metrics.DeadShare, c.Detail)
	}
	wantStatus(t, RunChecks(CheckInput{Spec: dirty}), CheckDeadTime, CheckSkip)
}

func TestEffectDensityCheck(t *testing.T) {
	plain := make([]shot, 8)
	for i := range plain {
		plain[i] = shot{asset: assetA, in: 0, out: int64(1000 + 300*i)}
	}
	flat := RunChecks(CheckInput{Spec: specOf(t, plain, title(0), nil)})
	if c := wantStatus(t, flat, CheckEffectDensity, CheckFail); !strings.Contains(c.Detail, "flat") {
		t.Errorf("flat reason: %s", c.Detail)
	}
	// A tutorial does not need effects.
	wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, plain, title(0), nil), Plan: &Plan{ContentType: TypeTutorial, TargetLengthS: 20}}), CheckEffectDensity, CheckPass)

	fx := slicesCopy(plain)
	fx[1].motion, fx[4].trans = timeline.MotionPunchIn, timeline.TransitionFlash
	subtle := styleprofile.Resolve(nil, nil, styleprofile.Settings{Transitions: styleprofile.LevelSubtle}, "") // transitions are off by default
	r := RunChecks(CheckInput{Spec: specOf(t, fx, title(0), nil), Settings: &subtle})
	wantStatus(t, r, CheckEffectDensity, CheckPass)
	if r.Metrics.Effects != 2 || r.Metrics.EffectsPerCut != 0.29 {
		t.Errorf("effects %d, per cut %v", r.Metrics.Effects, r.Metrics.EffectsPerCut)
	}

	busy := slicesCopy(plain)
	for i := 1; i < 6; i++ {
		busy[i].motion = timeline.MotionPunchIn
	}
	if c := wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, busy, title(0), nil)}), CheckEffectDensity, CheckFail); !strings.Contains(c.Detail, "too busy") {
		t.Errorf("busy reason: %s", c.Detail)
	}

	// Speed ramps are for a few shots only.
	ramps := slicesCopy(plain)
	for i := 0; i < 5; i++ {
		ramps[i].ramp = true
	}
	long := make([]shot, 0, 20)
	long = append(long, ramps...)
	for i := 0; i < 12; i++ {
		long = append(long, shot{asset: assetA, in: 0, out: int64(1000 + 100*i)})
	}
	if c := wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, long, title(0), nil)}), CheckEffectDensity, CheckFail); !strings.Contains(c.Detail, "5 shots have a speed ramp") {
		t.Errorf("ramp reason: %s", c.Detail)
	}
}

func slicesCopy(s []shot) []shot { return append([]shot(nil), s...) }

func TestTextAndDurationChecks(t *testing.T) {
	sp := specOf(t, []shot{{asset: assetA, in: 0, out: 5000}, {asset: assetA, in: 0, out: 5000}, {asset: assetA, in: 0, out: 6000}}, title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: sp}), CheckTextSafe, CheckPass)
	bad := RunChecks(CheckInput{Spec: sp, Issues: []timeline.Issue{{Code: timeline.CodeUnsafeText, Path: "/tracks/1/items/0", Severity: timeline.SeverityError}}})
	if c := wantStatus(t, bad, CheckTextSafe, CheckFail); !strings.Contains(c.Detail, "unsafe_text") {
		t.Errorf("text reason: %s", c.Detail)
	}
	wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, []shot{{asset: assetA, in: 0, out: 5000}}, nil, nil)}), CheckTextSafe, CheckSkip)

	// 16 s is inside the default range; a plan for 30 s wants 22.5 to 37.5 s.
	wantStatus(t, RunChecks(CheckInput{Spec: sp}), CheckDuration, CheckPass)
	if c := wantStatus(t, RunChecks(CheckInput{Spec: sp, Plan: &Plan{ContentType: TypeWorkout, TargetLengthS: 30}}), CheckDuration, CheckFail); !strings.Contains(c.Detail, "under 22.50 s") {
		t.Errorf("duration reason: %s", c.Detail)
	}
	short := specOf(t, []shot{{asset: assetA, in: 0, out: 4000}}, title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: short}), CheckDuration, CheckFail)
}

// renderedFlat is the luma series of a rendered preview whose shots each have a constant brightness: segs gives, per shot in order,
// its length in ms, the mean luma and the p90 luma (8-bit limited range).
func renderedFlat(segs ...[3]float64) *analysis.Series {
	s := &analysis.Series{StepMs: 200}
	for _, seg := range segs {
		for range int(seg[0]) / 200 {
			s.Luma, s.High = append(s.Luma, seg[1]), append(s.High, seg[2])
		}
	}
	return s
}

// The cuts are judged by the picture the render shows, not by the footage.
func TestBrightnessJumpCheckUsesTheRender(t *testing.T) {
	shots := []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetB, in: 0, out: 2000}, {asset: assetA, in: 2000, out: 4000}}
	spec := func() *timeline.Spec { return specOf(t, shots, title(0), nil) }

	// Two low-key shots, p90 luma 0.11 and 0.14: not a jump (and no ratio of 1.8).
	r := RunChecks(CheckInput{Spec: spec(), Rendered: renderedFlat([3]float64{2000, 24, 40}, [3]float64{2000, 26, 47}, [3]float64{2000, 24, 40})})
	c := wantStatus(t, r, CheckBrightness, CheckPass)
	if r.Metrics.BrightnessJump != 0.03 || !strings.Contains(c.Detail, "0.03") {
		t.Errorf("jump %v: %s", r.Metrics.BrightnessJump, c.Detail)
	}
	// A dark shot next to a lit one is a jump in absolute terms (0.11 to 0.84).
	r = RunChecks(CheckInput{Spec: spec(), Rendered: renderedFlat([3]float64{2000, 24, 40}, [3]float64{2000, 120, 200}, [3]float64{2000, 24, 40})})
	if c := wantStatus(t, r, CheckBrightness, CheckFail); !strings.Contains(c.Detail, "c1 to c2 jumps 0.73") {
		t.Errorf("brightness reason: %s", c.Detail)
	}
	// In low-key footage the numbers are small: the founder's posing clip (p90 0.37) next to the gym clips (0.20) is only 0.17 apart,
	// and is a jump because it is 1.9 times as bright.
	r = RunChecks(CheckInput{Spec: spec(), Rendered: renderedFlat([3]float64{2000, 45, 98}, [3]float64{2000, 35, 60}, [3]float64{2000, 45, 98})})
	if c := wantStatus(t, r, CheckBrightness, CheckFail); !strings.Contains(c.Detail, "jumps 0.17") || !strings.Contains(c.Detail, "1.8 times") {
		t.Errorf("ratio reason: %s", c.Detail)
	}
	// Without a render there is nothing to measure, and the check says so instead of passing.
	c = wantStatus(t, RunChecks(CheckInput{Spec: spec()}), CheckBrightness, CheckSkip)
	if !strings.Contains(c.Detail, "render_preview") {
		t.Errorf("no preview: %s", c.Detail)
	}
}

// crushed compares the render with the footage. The numbers are the founder's: his ungraded render keeps all of its brightness, the
// first gym_moody left 0.24 to 0.40 of the p90 and none of the mean.
func TestCrushedCheck(t *testing.T) {
	// Source: the lit part (p90) at luma 60 (0.20) and a mean of 8-bit 35 (0.087).
	src := AssetAnalysis{Analysed: true, Series: flatSeries2(100, 35, 60), HasSeries: true}
	assets := map[string]AssetAnalysis{assetA: src}
	shots := []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 2000, out: 4000}}
	spec := func() *timeline.Spec { return specOf(t, shots, title(0), nil) }

	ungraded := renderedFlat([3]float64{2000, 35, 60}, [3]float64{2000, 34, 59})
	r := RunChecks(CheckInput{Spec: spec(), Assets: assets, Rendered: ungraded})
	c := wantStatus(t, r, CheckCrushed, CheckPass)
	if r.Metrics.RenderedKept < 0.98 || !strings.Contains(c.Detail, "2 shots rendered") {
		t.Errorf("ungraded: kept %v: %s", r.Metrics.RenderedKept, c.Detail)
	}

	// The graded render: p90 luma 26 (0.05), mean 13 (nothing): about a quarter of the lit part, none of the mean.
	graded := renderedFlat([3]float64{2000, 13, 27}, [3]float64{2000, 13, 29})
	r = RunChecks(CheckInput{Spec: spec(), Assets: assets, Rendered: graded})
	c = wantStatus(t, r, CheckCrushed, CheckFail)
	for _, want := range []string{"2 of 2 shots", "c1 keeps 25 %", "set_look none"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("crushed reason lacks %q: %s", want, c.Detail)
		}
	}
	// A shot that is nearly black fails whatever its footage says.
	black := renderedFlat([3]float64{2000, 17, 22}, [3]float64{2000, 34, 59})
	if c := wantStatus(t, RunChecks(CheckInput{Spec: spec(), Rendered: black}), CheckCrushed, CheckFail); !strings.Contains(c.Detail, "near black") {
		t.Errorf("black: %s", c.Detail)
	}
	// A shot that renders as dark as it was, a dark clip being dark, is not crushed: no ratio is held against a footage this dark.
	darkSrc := map[string]AssetAnalysis{assetA: {Analysed: true, Series: flatSeries2(100, 17, 36), HasSeries: true}}
	wantStatus(t, RunChecks(CheckInput{Spec: spec(), Assets: darkSrc, Rendered: renderedFlat([3]float64{2000, 17, 36}, [3]float64{2000, 17, 36})}), CheckCrushed, CheckPass)

	// A hint the clip carries is allowed for: a clip darkened on purpose by its exposure hint (a stop brighter than the target, so a
	// gain of 0.62 at the strongest) is not crushed when it renders at that gain, and is when it renders far below it.
	hinted := spec()
	ev, match := 0.7, true
	vt, _, _ := timeline.MainTrack(hinted)
	for i := range vt.Items {
		vt.Items[i].ExposureEv, vt.Items[i].Look = &ev, &timeline.Look{Preset: "none", Match: &match}
	}
	// p90 0.20 x 0.616 = 0.123 expected; 0.12 rendered is on target.
	onTarget := renderedFlat([3]float64{2000, 24, 43}, [3]float64{2000, 24, 43})
	wantStatus(t, RunChecks(CheckInput{Spec: hinted, Assets: assets, Rendered: onTarget}), CheckCrushed, CheckPass)
	wantStatus(t, RunChecks(CheckInput{Spec: hinted, Assets: assets, Rendered: graded}), CheckCrushed, CheckFail)

	// No render, no judgement.
	if c := wantStatus(t, RunChecks(CheckInput{Spec: spec(), Assets: assets}), CheckCrushed, CheckSkip); !strings.Contains(c.Detail, "render_preview") {
		t.Errorf("no preview: %s", c.Detail)
	}
}

// Only the middle of a shot is measured, away from the transitions at its ends.
func TestRenderedShotsAreMeasuredMidShot(t *testing.T) {
	shots := []shot{{asset: assetA, in: 0, out: 2000}, {asset: assetA, in: 0, out: 2000}}
	// The first shot is dark at its edges (a dip) and bright in the middle: 0 to 500 ms and 1500 to 2000 ms at 16, the rest at 120.
	rs := renderedFlat([3]float64{500, 16, 16}, [3]float64{1000, 120, 200}, [3]float64{500, 16, 16}, [3]float64{2000, 120, 200})
	r := RunChecks(CheckInput{Spec: specOf(t, shots, title(0), nil), Rendered: rs})
	wantStatus(t, r, CheckBrightness, CheckPass)
	if r.Metrics.BrightnessJump != 0 {
		t.Errorf("the edges of the shot were measured: jump %v", r.Metrics.BrightnessJump)
	}
}

func TestBeatSyncCheck(t *testing.T) {
	beats := func(offset int64) []timeline.Marker {
		var m []timeline.Marker
		for i := int64(0); i < 40; i++ {
			m = append(m, timeline.Marker{AtMs: i*484 + offset, Kind: timeline.MarkerBeat})
		}
		return m
	}
	// Cuts at 1452 (three beats), 2904 and 4356.
	shots := []shot{{asset: assetA, in: 0, out: 1452}, {asset: assetA, in: 0, out: 1452}, {asset: assetA, in: 0, out: 1452}, {asset: assetA, in: 0, out: 1452}}
	r := RunChecks(CheckInput{Spec: specOf(t, shots, title(0), beats(0))})
	wantStatus(t, r, CheckBeatSync, CheckPass)
	if r.Metrics.CutsOnBeat != 3 || r.Metrics.Cuts != 3 {
		t.Errorf("cuts on beat %d of %d", r.Metrics.CutsOnBeat, r.Metrics.Cuts)
	}
	// The same cuts against a grid shifted by 200 ms.
	if c := wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, shots, title(0), beats(200))}), CheckBeatSync, CheckFail); !strings.Contains(c.Detail, "0 of 3 cuts are on a beat") {
		t.Errorf("beat reason: %s", c.Detail)
	}
	// No grid: a workout must have one, a talking video has none.
	if c := wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, shots, title(0), nil), Plan: &Plan{ContentType: TypeWorkout, TargetLengthS: 6, TempoBPM: 124}}), CheckBeatSync, CheckFail); !strings.Contains(c.Detail, "no beat grid") {
		t.Errorf("no grid reason: %s", c.Detail)
	}
	wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, shots, title(0), nil), Plan: &Plan{ContentType: TypeTalking, TargetLengthS: 6}}), CheckBeatSync, CheckSkip)
	// A plan with tempo 0 has no grid to write, whatever the type: the check does not contradict the plan.
	wantStatus(t, RunChecks(CheckInput{Spec: specOf(t, shots, title(0), nil), Plan: &Plan{ContentType: TypeVlog, TargetLengthS: 6, TempoBPM: 0}}), CheckBeatSync, CheckSkip)
}

func TestReportText(t *testing.T) {
	r := RunChecks(CheckInput{Version: 3, Spec: specOf(t, []shot{{asset: assetA, in: 0, out: 3500}, {asset: assetA, in: 0, out: 1000}}, nil, nil), Plan: &Plan{ContentType: TypeWorkout, TargetLengthS: 5}})
	pass, fail, skip := r.Counts()
	if pass+fail+skip != 28 || fail == 0 {
		t.Errorf("counts %d %d %d", pass, fail, skip)
	}
	text := r.Text()
	for _, want := range []string{"check_edit for version 3 (workout, target 5 s)", "FAIL hook_length", "FAIL hook_title", "n/a  brightness_jump"} {
		if !strings.Contains(text, want) {
			t.Errorf("the text lacks %q:\n%s", want, text)
		}
	}
	if len(r.Failed()) != fail {
		t.Errorf("Failed() %v", r.Failed())
	}
	// A timeline with no clips reports that and nothing else.
	empty := RunChecks(CheckInput{Spec: &timeline.Spec{Tracks: []timeline.Track{&timeline.VideoTrack{ID: "main"}}}})
	if len(empty.Checks) != 1 || empty.Checks[0].Status != CheckFail {
		t.Errorf("empty: %+v", empty.Checks)
	}
}

// A transition into the next shot overlaps the end of this one: the shot's length is what it is on screen, from its start to the next
// shot's start, so a 1.95 s hook with a 250 ms whip out of it is a 1.95 s hook and not a 2.2 s one.
func TestShotLengthsCountTheTransitionOverlap(t *testing.T) {
	sp := specOf(t, []shot{{asset: assetA, in: 0, out: 2100}, {asset: assetA, in: 0, out: 2000, trans: timeline.TransitionFlash}}, title(0), nil)
	// The first shot plays for 2100 ms and the flash starts 150 ms before it ends: 1950 ms on screen.
	r := RunChecks(CheckInput{Spec: sp})
	if c := wantStatus(t, r, CheckHookLength, CheckPass); !strings.Contains(c.Detail, "1.95 s") {
		t.Errorf("hook: %s", c.Detail)
	}
	if r.Metrics.ShotMinMs != 1950 || r.Metrics.ShotMaxMs != 2000 {
		t.Errorf("shot lengths %d to %d", r.Metrics.ShotMinMs, r.Metrics.ShotMaxMs)
	}
	// Without the transition the same hook is 2.1 s and fails.
	plain := specOf(t, []shot{{asset: assetA, in: 0, out: 2100}, {asset: assetA, in: 0, out: 2000}}, title(0), nil)
	wantStatus(t, RunChecks(CheckInput{Spec: plain}), CheckHookLength, CheckFail)
}

// Dead time is mapped through the clip's own time: a stretch of dead time that a speed ramp rushes through counts for what it plays
// for, not for its share of the source.
func TestDeadTimeIsMappedThroughTheSpeedRamp(t *testing.T) {
	// 6 s of source. The first 2 s are dead and play at 3x (the ramp: 3x, then slow for the middle, then 3x); the clip's output is longer
	// than 2/6 of it would suggest.
	assets := map[string]AssetAnalysis{assetA: {Analysed: true, Dead: []Span{{0, 2000, 0}}}}
	ramped := specOf(t, []shot{{asset: assetA, in: 0, out: 6000, ramp: true}}, title(0), nil)
	rt, _, _ := timeline.MainTrack(ramped)
	it := rt.Items[0]
	it.SpeedRamp = []timeline.SpeedPoint{{AtMs: 0, Speed: 3}, {AtMs: 2000, Speed: 3}, {AtMs: 2500, Speed: 0.5}, {AtMs: 6000, Speed: 0.5}}
	rt.Items[0] = it
	r := RunChecks(CheckInput{Spec: ramped, Assets: assets})
	// The dead 2 s play for 667 ms of a clip of about 8.1 s: 8 %, where a linear share of the source would say 33 %.
	if r.Metrics.DeadShare < 0.06 || r.Metrics.DeadShare > 0.1 {
		t.Errorf("dead share %v, want about 0.08 (a linear share would be 0.33)", r.Metrics.DeadShare)
	}
	wantStatus(t, r, CheckDeadTime, CheckFail)
	// The same clip at a constant speed has the linear share.
	plain := specOf(t, []shot{{asset: assetA, in: 0, out: 6000, speed: 2}}, title(0), nil)
	if r := RunChecks(CheckInput{Spec: plain, Assets: assets}); r.Metrics.DeadShare != 0.333 {
		t.Errorf("constant speed: dead share %v", r.Metrics.DeadShare)
	}
}
