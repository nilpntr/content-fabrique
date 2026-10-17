package timeline

import (
	"encoding/json"
	"math"
	"testing"
)

// numericOut integrates 1/speed over [0, s] by the midpoint rule on the ramp's own definition (linear speed between the points,
// flat outside them), without using the closed form.
func numericOut(points []SpeedPoint, s float64) float64 {
	speedAt := func(x float64) float64 {
		if x <= float64(points[0].AtMs) {
			return points[0].Speed
		}
		for i := 1; i < len(points); i++ {
			a, b := float64(points[i-1].AtMs), float64(points[i].AtMs)
			if x <= b {
				return points[i-1].Speed + (points[i].Speed-points[i-1].Speed)*(x-a)/(b-a)
			}
		}
		return points[len(points)-1].Speed
	}
	const steps = 200000
	h := s / steps
	total := 0.0
	for i := range steps {
		total += h / speedAt((float64(i)+0.5)*h)
	}
	return total
}

var rampCases = map[string][]SpeedPoint{
	"fast then slow":     {{AtMs: 0, Speed: 3}, {AtMs: 1500, Speed: 3}, {AtMs: 2200, Speed: 0.4}},
	"two points":         {{AtMs: 0, Speed: 0.25}, {AtMs: 1000, Speed: 4}},
	"first point late":   {{AtMs: 500, Speed: 2}, {AtMs: 1500, Speed: 0.5}},
	"constant":           {{AtMs: 0, Speed: 2}, {AtMs: 1000, Speed: 2}},
	"up, down, up":       {{AtMs: 0, Speed: 1}, {AtMs: 700, Speed: 4}, {AtMs: 1400, Speed: 0.25}, {AtMs: 2000, Speed: 2}},
	"nearly constant":    {{AtMs: 0, Speed: 1}, {AtMs: 3000, Speed: 1.0000000001}},
	"steep and short":    {{AtMs: 0, Speed: 0.25}, {AtMs: 10, Speed: 4}},
	"points at the end":  {{AtMs: 0, Speed: 1}, {AtMs: 2999, Speed: 2}, {AtMs: 3000, Speed: 3}},
	"ramp past the span": {{AtMs: 0, Speed: 1}, {AtMs: 9000, Speed: 4}},
}

func TestRampOutputIsTheIntegralOfOneOverSpeed(t *testing.T) {
	for name, pts := range rampCases {
		t.Run(name, func(t *testing.T) {
			for _, s := range []float64{1, 250, 999, 1500, 2000, 3000} {
				got, want := RampOutMs(pts, s), numericOut(pts, s)
				if math.Abs(got-want) > 1e-3*math.Max(1, want)*0.01+1e-3 {
					t.Errorf("T(%v) = %v, the integral is %v", s, got, want)
				}
			}
		})
	}
}

func TestRampSourceIsTheInverseOfRampOut(t *testing.T) {
	for name, pts := range rampCases {
		t.Run(name, func(t *testing.T) {
			prev := -1.0
			for s := 0.0; s <= 3000; s += 7.5 {
				out := RampOutMs(pts, s)
				if out <= prev && s > 0 {
					t.Fatalf("T is not increasing at %v: %v after %v", s, out, prev)
				}
				prev = out
				if back := RampSourceMs(pts, out); math.Abs(back-s) > 1e-6*math.Max(1, s) {
					t.Fatalf("source(T(%v)) = %v", s, back)
				}
			}
		})
	}
}

func TestRampSpeedsAreWhatThePointsSay(t *testing.T) {
	pts := []SpeedPoint{{AtMs: 0, Speed: 3}, {AtMs: 1000, Speed: 3}, {AtMs: 2000, Speed: 0.5}}
	// The speed is the derivative of the source time with respect to the output time: constant 3 over the first second, then
	// falling linearly in source time down to 0.5 at 2000 ms.
	speed := func(out float64) float64 {
		const h = 1e-3
		return (RampSourceMs(pts, out+h) - RampSourceMs(pts, out-h)) / (2 * h)
	}
	if got := speed(100); math.Abs(got-3) > 1e-6 {
		t.Errorf("speed at 100 ms of output = %v, want 3", got)
	}
	// At source 1500 the speed is half way between 3 and 0.5.
	if got, want := speed(RampOutMs(pts, 1500)), 1.75; math.Abs(got-want) > 1e-4 {
		t.Errorf("speed at source 1500 ms = %v, want %v", got, want)
	}
	// After the last point the clip stays at the last speed.
	end := RampOutMs(pts, 2000)
	if got := speed(end + 500); math.Abs(got-0.5) > 1e-6 {
		t.Errorf("speed after the last point = %v, want 0.5", got)
	}
}

func TestOutputDurationOfARamp(t *testing.T) {
	it := clipAt("c1", 1000, 3000, 0) // 2000 ms of source
	it.SpeedRamp = []SpeedPoint{{AtMs: 0, Speed: 2}, {AtMs: 1000, Speed: 2}, {AtMs: 1600, Speed: 0.5}}
	// 1000 ms at 2x is 500; 600 ms from 2x to 0.5x is ln(4)/0.0025 = 554.5; the last 400 ms at 0.5x is 800.
	if got := OutputDuration(it); got != 1855 {
		t.Errorf("OutputDuration = %d, want 1855", got)
	}
	if got := EndMs(it); got != 1855 {
		t.Errorf("EndMs = %d, want 1855", got)
	}
	// A constant ramp is the plain speed.
	flat := clipAt("c1", 0, 3001, 0)
	flat.SpeedRamp = []SpeedPoint{{AtMs: 0, Speed: 2}, {AtMs: 500, Speed: 2}}
	if got, want := OutputDuration(flat), int64(1501); got != want {
		t.Errorf("a flat ramp at 2x plays %d ms, want %d (the speed 2 rule: 3001 ms is 1501)", got, want)
	}
	// The ramp wins over the speed, as the schema note says.
	both := flat
	both.Speed = ptr(4.0)
	if OutputDuration(both) != OutputDuration(flat) {
		t.Error("speed and speed_ramp: the ramp must win")
	}
	if avg := it.AverageSpeed(); math.Abs(avg-2000.0/1855) > 1e-9 {
		t.Errorf("AverageSpeed = %v", avg)
	}
}

func TestRampMathStaysFiniteOnNonsense(t *testing.T) {
	bad := [][]SpeedPoint{
		nil,
		{{AtMs: 0, Speed: 0}},
		{{AtMs: 0, Speed: -1}, {AtMs: 10, Speed: 2}},
		{{AtMs: 100, Speed: 1}, {AtMs: 50, Speed: 2}, {AtMs: 50, Speed: 3}},
		{{AtMs: 0, Speed: math.NaN()}, {AtMs: 10, Speed: math.Inf(1)}},
	}
	for _, pts := range bad {
		out := RampOutMs(pts, 1234)
		src := RampSourceMs(pts, 1234)
		if math.IsNaN(out) || math.IsInf(out, 0) || math.IsNaN(src) || math.IsInf(src, 0) {
			t.Errorf("%v: T = %v, source = %v", pts, out, src)
		}
	}
}

func TestSpeedRampValidation(t *testing.T) {
	ramp := func(pts ...SpeedPoint) VideoItem {
		c := clipAt("c1", 0, 3000, 0)
		c.SpeedRamp = pts
		return c
	}
	good := []SpeedPoint{{AtMs: 0, Speed: 3}, {AtMs: 1500, Speed: 0.5}}
	cases := []struct {
		name string
		clip VideoItem
		want []Code
	}{
		{"a sorted ramp", ramp(good...), nil},
		{"the last point at the end of the source range", ramp(SpeedPoint{AtMs: 0, Speed: 1}, SpeedPoint{AtMs: 3000, Speed: 2}), nil},
		{"a point one ms past the source range", ramp(SpeedPoint{AtMs: 0, Speed: 1}, SpeedPoint{AtMs: 3001, Speed: 2}), []Code{CodeInvalidSpeedRamp}},
		{"equal at_ms", ramp(SpeedPoint{AtMs: 0, Speed: 1}, SpeedPoint{AtMs: 500, Speed: 2}, SpeedPoint{AtMs: 500, Speed: 3}), []Code{CodeInvalidSpeedRamp}},
		{"descending at_ms", ramp(SpeedPoint{AtMs: 0, Speed: 1}, SpeedPoint{AtMs: 900, Speed: 2}, SpeedPoint{AtMs: 500, Speed: 3}), []Code{CodeInvalidSpeedRamp}},
		{"a speed too, which the ramp would replace", func() VideoItem { c := ramp(good...); c.Speed = ptr(2.0); return c }(), []Code{CodeInvalidSpeedRamp}},
		{"a ramp, and source audio on, is muted with a warning", func() VideoItem {
			c := ramp(good...)
			c.Audio = &VideoAudio{UseSource: ptr(true)}
			return c
		}(), []Code{CodeRampAudioMuted}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := validateSpec(t, withClips(baseSpec(), tc.clip))
			wantCodes(t, res, tc.want...)
			for _, is := range res.Issues {
				if is.Code == CodeRampAudioMuted && is.Severity != SeverityWarning {
					t.Errorf("ramp_audio_muted must be a warning, got %v", is.Severity)
				}
			}
		})
	}
}

func TestRampShortensTheClipAndShotTooShortUsesIt(t *testing.T) {
	// 2000 ms of source at 4x is 500 ms; a ramp that stays at 4x for 1200 ms of source plays 300 ms.
	c := clipAt("c1", 0, 1200, 0)
	c.SpeedRamp = []SpeedPoint{{AtMs: 0, Speed: 4}, {AtMs: 600, Speed: 4}}
	wantCodes(t, validateSpec(t, withClips(baseSpec(), c, clipAt("c2", 0, 5000, 300))))
	c.Source.OutMs = 1196 // 299 ms
	wantCodes(t, validateSpec(t, withClips(baseSpec(), c, clipAt("c2", 0, 5000, 299))), CodeShotTooShort)
}

func TestMotionValidation(t *testing.T) {
	motion := func(dur int64, m Motion) VideoItem {
		c := clipAt("c1", 0, dur, 0)
		c.Motion = &m
		return c
	}
	cases := []struct {
		name string
		clip VideoItem
		want []Code
	}{
		{"every preset on a long enough clip", motion(2000, Motion{Preset: MotionPunchIn, AtMs: ptr(int64(500)), Strength: ptr(0.2)}), nil},
		{"push_in at exactly 400 ms", motion(400, Motion{Preset: MotionPushIn}), nil},
		{"push_in at 399 ms", motion(399, Motion{Preset: MotionPushIn}), []Code{CodeInvalidMotion}},
		{"the preset none on a short clip is nothing", motion(300, Motion{Preset: MotionNone}), nil},
		{"none with a strength", motion(2000, Motion{Preset: MotionNone, Strength: ptr(0.1)}), []Code{CodeInvalidMotion}},
		{"at_ms on push_in", motion(2000, Motion{Preset: MotionPushIn, AtMs: ptr(int64(100))}), []Code{CodeInvalidMotion}},
		{"at_ms on pull_back", motion(2000, Motion{Preset: MotionPullBack, AtMs: ptr(int64(0))}), []Code{CodeInvalidMotion}},
		{"punch_in at the last ms of the clip", motion(2000, Motion{Preset: MotionPunchIn, AtMs: ptr(int64(1999))}), nil},
		{"punch_in at the end of the clip", motion(2000, Motion{Preset: MotionPunchIn, AtMs: ptr(int64(2000))}), []Code{CodeInvalidMotion}},
		{"shake past the end of the clip", motion(2000, Motion{Preset: MotionShake, AtMs: ptr(int64(5000))}), []Code{CodeInvalidMotion}},
		{"a short clip and an at_ms past it is one problem each", motion(300, Motion{Preset: MotionShake, AtMs: ptr(int64(900))}), []Code{CodeInvalidMotion}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.clip
			spec := withClips(baseSpec(), c, clipAt("c2", 0, 5000, OutputDuration(c)))
			wantCodes(t, validateSpec(t, spec), tc.want...)
		})
	}
	// A motion with the length of the clip counted at its speed: 1600 ms of source at 4x plays 400 ms.
	fast := motion(1600, Motion{Preset: MotionShake})
	fast.Speed = ptr(4.0)
	wantCodes(t, validateSpec(t, withClips(baseSpec(), fast, clipAt("c2", 0, 5000, 400))))
}

// Everything a spec can put into the filter graph is a number or an enum: values that are not what the schema allows are
// refused before the compiler sees them. The renderer's own tests (render/hostile_test.go) check the compiler as well.
func TestHostileValuesAreRefused(t *testing.T) {
	hostile := []string{
		`punch_in;drawtext=text=x`, `punch_in,scale=1:1`, `punch_in'`, "punch_in\n", `PUNCH_IN`, `punch_in `, `shake]`, ``, `..\/..`,
		`whip_left,drawtext=fontfile=/etc/passwd`, `zoom_through[x]`, `flash:duration=1`, `xfade`,
	}
	mutate := func(js string) Result {
		return Validate([]byte(js), unitCatalog)
	}
	const head = `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},"tracks":[{"id":"main","type":"video","items":[`
	const clip1 = `{"id":"c1","asset_id":"ast_01j3er3kwcgrasdgd4aznmxxe0","source":{"in_ms":0,"out_ms":3000},"at_ms":0`
	const tail = `]}],"markers":[]}`
	for _, h := range hostile {
		q := jsonString(h)
		for name, js := range map[string]string{
			"motion preset":   head + clip1 + `,"motion":{"preset":` + q + `}}` + tail,
			"transition type": head + clip1 + `},{"id":"c2","asset_id":"ast_01j3er3kwcgrasdgd4aznmxxe0","source":{"in_ms":0,"out_ms":3000},"at_ms":2700,"transition_in":{"type":` + q + `,"duration_ms":300}}` + tail,
		} {
			res := mutate(js)
			if res.OK() {
				t.Errorf("%s %q was accepted", name, h)
			}
		}
	}
	// Numbers outside their ranges, and text where a number goes.
	for name, js := range map[string]string{
		"strength 0.04":      head + clip1 + `,"motion":{"preset":"shake","strength":0.04}}` + tail,
		"strength 0.36":      head + clip1 + `,"motion":{"preset":"shake","strength":0.36}}` + tail,
		"strength as text":   head + clip1 + `,"motion":{"preset":"shake","strength":"0.1;x"}}` + tail,
		"at_ms as text":      head + clip1 + `,"motion":{"preset":"punch_in","at_ms":"1)+(2"}}` + tail,
		"at_ms negative":     head + clip1 + `,"motion":{"preset":"punch_in","at_ms":-5}}` + tail,
		"ramp speed text":    head + clip1 + `,"speed_ramp":[{"at_ms":0,"speed":"1"},{"at_ms":5,"speed":2}]}` + tail,
		"ramp speed 0":       head + clip1 + `,"speed_ramp":[{"at_ms":0,"speed":0},{"at_ms":5,"speed":2}]}` + tail,
		"ramp speed 4.01":    head + clip1 + `,"speed_ramp":[{"at_ms":0,"speed":1},{"at_ms":5,"speed":4.01}]}` + tail,
		"ramp at_ms as text": head + clip1 + `,"speed_ramp":[{"at_ms":"0","speed":1},{"at_ms":5,"speed":2}]}` + tail,
		"ramp point field":   head + clip1 + `,"speed_ramp":[{"at_ms":0,"speed":1,"expr":"x"},{"at_ms":5,"speed":2}]}` + tail,
		"unknown motion key": head + clip1 + `,"motion":{"preset":"shake","expr":"x"}}` + tail,
	} {
		if res := mutate(js); res.OK() {
			t.Errorf("%s was accepted", name)
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
