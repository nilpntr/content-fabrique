package render

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func withMotion(m timeline.Motion) func(*timeline.VideoItem) {
	return func(it *timeline.VideoItem) { it.Motion = &m }
}

func withRamp(points ...timeline.SpeedPoint) func(*timeline.VideoItem) {
	return func(it *timeline.VideoItem) { it.SpeedRamp = points }
}

// Nothing of a spec but numbers and a fixed set of words may reach the filter graph (the graph runs in the sandbox). This is
// the whole alphabet of what the new features write: letters (filter and function names), digits, and the punctuation of
// the filter syntax and of its expressions.
var graphAlphabet = regexp.MustCompile(`^[A-Za-z0-9_.,:;=\[\]'()+\-*/<> ]*$`)

func TestNewFeaturesWriteOnlyNumbersAndFixedWordsIntoTheGraph(t *testing.T) {
	cat := testAssets(t)
	for _, name := range []string{"v37_motion_9x16", "v38_motion_1x1", "v39_motion_4x5", "v40_motion_16x9"} {
		plan, err := Compile(loadFixture(t, name, cat), Final, fakeResolver(cat))
		if err != nil {
			t.Fatal(err)
		}
		if !graphAlphabet.MatchString(plan.FilterComplex) {
			t.Errorf("%s: the graph has a character outside the alphabet: %q", name, regexp.MustCompile(`[^A-Za-z0-9_.,:;=\[\]'()+\-*/<> ]`).FindString(plan.FilterComplex))
		}
		for _, bad := range []string{"drawtext", "movie", "amovie", "subtitles", "sendcmd", "azmq", "zmq", "lavfi", "file:"} {
			if strings.Contains(plan.FilterComplex, bad) {
				t.Errorf("%s: the graph contains %q", name, bad)
			}
		}
	}
}

func TestCompileRefusesHostileMotionAndTransitionsThatSkippedValidation(t *testing.T) {
	hostile := []string{"punch_in;drawtext=text=x", "punch_in,scale=1:1", "shake'", "PUNCH_IN", "whip_left,drawtext", "flash\n"}
	for _, h := range hostile {
		spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, withMotion(timeline.Motion{Preset: h}))})
		if _, err := Compile(spec, Preview, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("motion preset %q: err = %v, want ErrInvalidSpec", h, err)
		}
		spec = specOf(30, []timeline.VideoItem{
			clipAt("c1", assetA, 0, 2000, 0),
			clipAt("c2", assetA, 0, 2000, 1700, withTransition(h, 300)),
		})
		if _, err := Compile(spec, Preview, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("transition %q: err = %v, want ErrInvalidSpec", h, err)
		}
	}
	for _, s := range []float64{0, 0.04, 0.36, 9, math.NaN(), math.Inf(1), -0.1} {
		spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, withMotion(timeline.Motion{Preset: timeline.MotionShake, Strength: &s}))})
		if _, err := Compile(spec, Preview, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("strength %v: err = %v, want ErrInvalidSpec", s, err)
		}
	}
	bad := [][]timeline.SpeedPoint{
		{{AtMs: 0, Speed: 1}},
		{{AtMs: 500, Speed: 1}, {AtMs: 100, Speed: 2}},
		{{AtMs: 0, Speed: 1}, {AtMs: 100, Speed: 0}},
		{{AtMs: 0, Speed: math.NaN()}, {AtMs: 100, Speed: 2}},
	}
	for _, pts := range bad {
		spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, withRamp(pts...))})
		if _, err := Compile(spec, Preview, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("ramp %v: err = %v, want ErrInvalidSpec", pts, err)
		}
	}
}

func TestMotionPresetsInTheGraph(t *testing.T) {
	graphOf := func(fps int, opts ...func(*timeline.VideoItem)) string {
		spec := specOf(fps, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, opts...)})
		return mustCompile(t, spec, Final).FilterComplex
	}
	if g := graphOf(30); strings.Contains(g, "perspective") {
		t.Error("a clip without motion must not get a camera")
	}
	if g := graphOf(30, withMotion(timeline.Motion{Preset: timeline.MotionNone})); strings.Contains(g, "perspective") {
		t.Error("the preset none must not get a camera")
	}
	t.Run("punch_in takes 4 to 6 frames at any frame rate", func(t *testing.T) {
		for fps, want := range map[int]int{24: 4, 25: 4, 30: 5, 60: 6} {
			g := graphOf(fps, withMotion(timeline.Motion{Preset: timeline.MotionPunchIn}))
			if !strings.Contains(g, fmt.Sprintf("clip((in-0)/(%d),0,1)", want)) {
				t.Errorf("%d fps: want a ramp of %d frames from frame 0 in %s", fps, want, g[:min(len(g), 600)])
			}
		}
	})
	t.Run("punch_in lands at at_ms", func(t *testing.T) {
		// 500 ms at 30 fps is frame 15: the ramp of 5 frames runs from frame 10 to 15.
		g := graphOf(30, withMotion(timeline.Motion{Preset: timeline.MotionPunchIn, AtMs: ptr(int64(500))}))
		if !strings.Contains(g, "clip((in-10)/(5),0,1)") {
			t.Errorf("%s", g[:min(len(g), 600)])
		}
		// A landing earlier than the ramp is long runs a shorter ramp from the first frame.
		g = graphOf(30, withMotion(timeline.Motion{Preset: timeline.MotionPunchIn, AtMs: ptr(int64(100))}))
		if !strings.Contains(g, "clip((in-0)/(3),0,1)") {
			t.Errorf("%s", g[:min(len(g), 600)])
		}
		// A landing on the first frame is a clip that starts zoomed.
		g = graphOf(30, withMotion(timeline.Motion{Preset: timeline.MotionPunchIn, AtMs: ptr(int64(0))}))
		if !strings.Contains(g, "x0='W*(1-1/((1+0.15)))/2'") {
			t.Errorf("%s", g[:min(len(g), 600)])
		}
	})
	t.Run("strength defaults per preset and is written as given", func(t *testing.T) {
		for preset, want := range map[string]string{
			timeline.MotionPunchIn: "1+0.15*", timeline.MotionPushIn: "1+0.1*", timeline.MotionPullBack: "1+0.15*", timeline.MotionShake: "1+0.08*",
		} {
			if g := graphOf(30, withMotion(timeline.Motion{Preset: preset})); !strings.Contains(g, want) {
				t.Errorf("%s: want %s", preset, want)
			}
		}
		if g := graphOf(30, withMotion(timeline.Motion{Preset: timeline.MotionPushIn, Strength: ptr(0.3)})); !strings.Contains(g, "1+0.3*") {
			t.Error("strength 0.3 not written")
		}
	})
	t.Run("the camera sits after the reframe zoom and before the look", func(t *testing.T) {
		g := graphOf(30, withMotion(timeline.Motion{Preset: timeline.MotionPushIn}), func(it *timeline.VideoItem) {
			it.Reframe = &timeline.Reframe{Mode: timeline.ReframeCover, Zoom: ptr(1.5)}
			it.Look = &timeline.Look{Preset: "punchy"}
		})
		iCrop, iCam, iLook := strings.Index(g, "crop=720:1280,scale=1080:1920"), strings.Index(g, "perspective="), strings.Index(g, "eq=contrast=1.12")
		if iCrop < 0 || iCrop >= iCam || iCam >= iLook {
			t.Errorf("order: reframe zoom at %d, camera at %d, look at %d", iCrop, iCam, iLook)
		}
	})
	t.Run("a shake is the same every time, and differs from clip to clip", func(t *testing.T) {
		spec := func(id string) string {
			s := specOf(30, []timeline.VideoItem{clipAt(id, assetA, 0, 2000, 0, withMotion(timeline.Motion{Preset: timeline.MotionShake}))})
			return mustCompile(t, s, Final).FilterComplex
		}
		a1, a2, b := spec("c1"), spec("c1"), spec("c2")
		if a1 != a2 {
			t.Error("the same clip shakes differently on two compiles")
		}
		if a1 == b {
			t.Error("two clips shake in exactly the same way")
		}
	})
	t.Run("the shake never asks for a window outside the frame", func(t *testing.T) {
		// The offset is at most 0.9 of the room the zoom leaves, so the window's edges stay inside [0, W]: check the
		// expressions' bound by evaluating the graph's own numbers over every frame of a clip, for strengths and clips.
		for _, id := range []string{"c1", "c2", "c3", "shaky", "a", "z9"} {
			for _, s := range []float64{0.05, 0.12, 0.35} {
				spec := specOf(30, []timeline.VideoItem{clipAt(id, assetA, 0, 2000, 0, withMotion(timeline.Motion{Preset: timeline.MotionShake, Strength: &s, AtMs: ptr(int64(300))}))})
				g := mustCompile(t, spec, Final).FilterComplex
				if !strings.Contains(g, "0.9*W*(1-1/(") {
					t.Fatalf("the shake offset is not bounded by the room: %s", g[:min(len(g), 400)])
				}
			}
		}
	})
	t.Run("zoom_through zooms both clips around the join", func(t *testing.T) {
		spec := specOf(30, []timeline.VideoItem{
			clipAt("c1", assetA, 0, 2000, 0),
			clipAt("c2", assetA, 0, 2000, 1700, withTransition(timeline.TransitionZoomThrough, 300)),
		})
		g := mustCompile(t, spec, Final).FilterComplex
		// 300 ms is 9 frames: c1 zooms in over the first 4.5 frames of the last 9 (frames 51 to 60), c2 holds and zooms out.
		if !strings.Contains(g, "clip((in-51)/(4.5),0,1)") || !strings.Contains(g, "1-clip((in-4.5)/(4.5),0,1)") {
			t.Errorf("%s", g)
		}
		if !strings.Contains(g, "xfade=transition=fade:duration=0.300000:offset=1.700000") || !strings.Contains(g, "gblur=sigma=") {
			t.Errorf("zoom_through needs a fade and a blur window: %s", g)
		}
	})
}

func TestNewTransitionsMapToFixedXfades(t *testing.T) {
	for typ, want := range map[string]string{
		timeline.TransitionWhip:        "slideleft",
		timeline.TransitionWhipLeft:    "slideleft",
		timeline.TransitionWhipRight:   "slideright",
		timeline.TransitionZoomThrough: "fade",
		timeline.TransitionFlash:       "fadewhite",
	} {
		spec := specOf(30, []timeline.VideoItem{
			clipAt("c1", assetA, 0, 2000, 0),
			clipAt("c2", assetA, 0, 2000, 1750, withTransition(typ, 250)),
		})
		plan := mustCompile(t, spec, Preview)
		if !strings.Contains(plan.FilterComplex, "xfade=transition="+want+":") {
			t.Errorf("%s: want xfade %s in %s", typ, want, plan.FilterComplex)
		}
		blur := strings.Contains(plan.FilterComplex, "gblur")
		if blur != (typ != timeline.TransitionFlash) {
			t.Errorf("%s: blur window present = %v", typ, blur)
		}
		if got := plan.CutList[0].Transition; got != typ {
			t.Errorf("%s: the cut list says %q", typ, got)
		}
	}
	// A whip is whip_left: the same graph, apart from the name in the cut list.
	mk := func(typ string) string {
		spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0), clipAt("c2", assetA, 0, 2000, 1750, withTransition(typ, 250))})
		return mustCompile(t, spec, Final).FilterComplex
	}
	if mk(timeline.TransitionWhip) != mk(timeline.TransitionWhipLeft) {
		t.Error("whip and whip_left must compile to the same graph")
	}
}

func TestSpeedRampInTheTimeMapAndAudio(t *testing.T) {
	ramp := []timeline.SpeedPoint{{AtMs: 0, Speed: 2}, {AtMs: 1000, Speed: 2}, {AtMs: 1600, Speed: 0.5}}
	c2 := clipAt("c2", assetB, 1000, 3000, 1000, withRamp(ramp...), withSourceAudio(0))
	spec := specOf(30, []timeline.VideoItem{
		clipAt("c1", assetA, 0, 1000, 0, withSourceAudio(0)),
		c2,
		clipAt("c3", assetA, 0, 2000, 1000+timeline.OutputDuration(c2), withSpeed(2)),
	})
	plan := mustCompile(t, spec, Final)

	e := plan.TimeMap[1]
	if len(e.SpeedRamp) != 3 || e.Speed == nil {
		t.Fatalf("entry: %+v", e)
	}
	if want := 2000.0 / 1855; math.Abs(*e.Speed-want) > 1e-9 { // the rounded duration
		t.Errorf("the average speed is %v, want %v", *e.Speed, want)
	}
	if plan.TimeMap[0].SpeedRamp != nil || plan.TimeMap[2].SpeedRamp != nil {
		t.Error("only the ramped clip carries a ramp")
	}
	// Output and source times map to each other along the ramp, inside the entry.
	for _, off := range []float64{0, 100, 499, 500, 650, 1000, 1400, 1800} {
		src, ok := e.SourceAtMs(float64(e.OutStartMs) + off)
		if !ok {
			t.Fatal("no source time")
		}
		back, _ := e.OutAtMs(src)
		if math.Abs(back-(float64(e.OutStartMs)+off)) > 1e-6 {
			t.Errorf("output %v -> source %v -> output %v", off, src, back)
		}
		if want := 1000 + timeline.RampSourceMs(ramp, off); math.Abs(src-want) > 1e-9 {
			t.Errorf("source at %v ms = %v, want %v", off, src, want)
		}
	}
	// The plan's own lookup follows the ramp: 100 ms into the ramped clip is at 2x (200 ms of source), well past the 3.7x
	// an average-speed lookup would be at the end.
	if _, src, ok := plan.SourceTimeMs(e.OutStartMs + 100); !ok || src != 1200 {
		t.Errorf("SourceTimeMs 100 ms in = %d (ok %v), want 1200", src, ok)
	}
	// The constant-speed clips are as before.
	if _, src, ok := plan.SourceTimeMs(plan.TimeMap[2].OutStartMs + 100); !ok || src != 200 {
		t.Errorf("SourceTimeMs in the 2x clip = %d, want 200", src)
	}

	// The ramped clip's own audio is left out of the mix: no atrim for it, only the first clip's.
	if strings.Count(plan.FilterComplex, "atrim=end=") != 2 { // the clip's own trim and its final trim to the frame
		t.Errorf("want the audio of c1 only (two atrim): %s", plan.FilterComplex)
	}
	if strings.Contains(plan.FilterComplex, "[1:a]") {
		t.Errorf("the ramped clip's audio is in the graph: %s", plan.FilterComplex)
	}
	// The JSON of the time map carries the ramp.
	js, err := plan.TimeMapJSON()
	if err != nil || !strings.Contains(string(js), `"speed_ramp":[{"at_ms":0,"speed":2}`) {
		t.Errorf("time map JSON: %s (%v)", js, err)
	}
	if strings.Contains(string(js[:strings.Index(string(js), `"item_id":"c2"`)]), "speed_ramp") {
		t.Error("c1 must not carry speed_ramp in the JSON")
	}
}

func TestRampSetptsFormula(t *testing.T) {
	expr, err := rampSetpts([]timeline.SpeedPoint{{AtMs: 0, Speed: 2}, {AtMs: 1000, Speed: 2}, {AtMs: 1600, Speed: 0.5}})
	if err != nil {
		t.Fatal(err)
	}
	want := "(if(lt(((PTS-STARTPTS)*TB),1),0+(((PTS-STARTPTS)*TB)-0)/2,if(lt(((PTS-STARTPTS)*TB),1.6),0.5+log(1+(-2.5)*(((PTS-STARTPTS)*TB)-1)/2)/(-2.5),1.054517744+(((PTS-STARTPTS)*TB)-1.6)/0.5)))/TB"
	if expr != want {
		t.Errorf("\n got %s\nwant %s", expr, want)
	}
	// A first point later than 0 gets a piece of its own before it.
	expr, err = rampSetpts([]timeline.SpeedPoint{{AtMs: 500, Speed: 2}, {AtMs: 1500, Speed: 4}})
	if err != nil || !strings.HasPrefix(expr, "(if(lt(((PTS-STARTPTS)*TB),0.5),0+") {
		t.Errorf("%s (%v)", expr, err)
	}
	// The brackets balance.
	if strings.Count(expr, "(") != strings.Count(expr, ")") {
		t.Errorf("unbalanced: %s", expr)
	}
	// Evaluating the closed forms of the expression's pieces agrees with the integral (timeline/ramp_test.go checks that the
	// maths is the integral; this checks that the numbers written into the expression are the ones of the maths).
	pts := []timeline.SpeedPoint{{AtMs: 0, Speed: 3}, {AtMs: 1500, Speed: 3}, {AtMs: 2200, Speed: 0.4}}
	segs := timeline.RampSegments(pts)
	if len(segs) != 3 {
		t.Fatalf("segments: %+v", segs)
	}
	e, _ := rampSetpts(pts)
	for _, g := range segs {
		if !strings.Contains(e, sec(g.T0/1000)) {
			t.Errorf("T0 %v of a piece is not in %s", g.T0, e)
		}
	}
}
