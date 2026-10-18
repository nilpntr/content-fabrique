package subject

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

func good() Sample { return Sample{AtMs: 1000, X: 0.2, Y: 0.2, W: 0.6, H: 0.7, HeadTopY: 0.22} }

func TestSampleBounds(t *testing.T) {
	if !good().Valid() {
		t.Fatal("a good sample is not valid")
	}
	nan, inf := math.NaN(), math.Inf(1)
	for name, mut := range map[string]func(*Sample){
		"nan x":          func(s *Sample) { s.X = nan },
		"inf w":          func(s *Sample) { s.W = inf },
		"negative x":     func(s *Sample) { s.X = -0.1 },
		"negative time":  func(s *Sample) { s.AtMs = -1 },
		"speck":          func(s *Sample) { s.W = 0.01 },
		"flat":           func(s *Sample) { s.H = 0.02 },
		"off the right":  func(s *Sample) { s.X = 0.5; s.W = 0.6 },
		"off the bottom": func(s *Sample) { s.Y = 0.5; s.H = 0.6; s.HeadTopY = 0.52 },
		"head above":     func(s *Sample) { s.HeadTopY = 0.1 },
		"head far down":  func(s *Sample) { s.HeadTopY = 0.8 },
		"huge":           func(s *Sample) { s.W = 1e300 },
	} {
		s := good()
		mut(&s)
		if s.Valid() {
			t.Errorf("%s was accepted: %+v", name, s)
		}
	}
	// A hair of overrun is clamped, not refused.
	s := Sample{AtMs: 0, X: 0.5, Y: 0.1, W: 0.51, H: 0.9, HeadTopY: 0.095}
	if !s.Valid() {
		t.Fatal("a hair over the edge was refused")
	}
	c := s.Clamp()
	if c.X+c.W > 1 || c.Y+c.H > 1 || c.HeadTopY < c.Y {
		t.Errorf("clamped %+v", c)
	}
}

func TestCleanDropsSortsAndCaps(t *testing.T) {
	var samples []Sample
	for i := 0; i < 20; i++ {
		s := good()
		s.AtMs = int64(20-i) * 100
		samples = append(samples, s)
	}
	samples = append(samples, Sample{AtMs: 5, X: 2, Y: 0, W: 0.5, H: 0.5})
	tr, ok := Clean(Track{Aspect: 0.5625, Samples: samples})
	if !ok || len(tr.Samples) != MaxSamples || tr.Samples[0].AtMs != 100 || tr.Samples[1].AtMs != 200 {
		t.Fatalf("%+v %v", tr, ok)
	}
	for _, a := range []float64{0, -1, 0.05, 6, math.NaN(), math.Inf(1)} {
		if _, ok := Clean(Track{Aspect: a, Samples: samples}); ok {
			t.Errorf("aspect %v was accepted", a)
		}
	}
	if _, ok := Clean(Track{Aspect: 1, Samples: []Sample{{X: 3}}}); ok {
		t.Error("a track with no usable sample")
	}
}

func TestFromDetailAndSubjects(t *testing.T) {
	detail := json.RawMessage(`{"prompt_version":"quicklook.v2","frames":[],"subjects":{"aspect":0.5625,"samples":[{"at_ms":1500,"x":0.2,"y":0.2,"w":0.6,"h":0.7,"head_top_y":0.22}]}}`)
	tr, ok := FromDetail(detail)
	if !ok || len(tr.Samples) != 1 || tr.Samples[0].HeadTopY != 0.22 {
		t.Fatalf("%+v %v", tr, ok)
	}
	for _, bad := range []string{``, `{}`, `not json`, `{"subjects":null}`, `{"subjects":{"aspect":0.5,"samples":[]}}`, `{"subjects":"x"}`} {
		if _, ok := FromDetail(json.RawMessage(bad)); ok {
			t.Errorf("%q was read", bad)
		}
	}
	if _, ok := FromSubjects(json.RawMessage(`{"aspect":0.5625,"samples":[{"at_ms":0,"x":0.1,"y":0.1,"w":0.5,"h":0.5,"head_top_y":0.12}]}`)); !ok {
		t.Error("bare subjects")
	}
}

func TestNearAndTop(t *testing.T) {
	tr := Track{Aspect: 0.5625, Samples: []Sample{{AtMs: 1000, X: 0.2, Y: 0.3, W: 0.5, H: 0.6, HeadTopY: 0.32}, {AtMs: 5000, X: 0.2, Y: 0.2, W: 0.5, H: 0.6, HeadTopY: 0.22}, {AtMs: 9000, X: 0.2, Y: 0.25, W: 0.5, H: 0.6, HeadTopY: 0.27}}}
	if got := tr.Near(4000, 6000); len(got) != 1 || got[0].AtMs != 5000 {
		t.Errorf("inside: %+v", got)
	}
	if got := tr.Near(6000, 7000); len(got) != 1 || got[0].AtMs != 5000 { // none inside: the one nearest the middle (6500)
		t.Errorf("none inside: %+v", got)
	}
	if got := tr.Near(0, 10000); len(got) != 3 {
		t.Errorf("all: %+v", got)
	}
	if pct, ok := TopPct(tr.Samples); !ok || pct != 22 {
		t.Errorf("top %v %v", pct, ok)
	}
	if _, ok := TopPct(nil); ok {
		t.Error("top of nothing")
	}
	if got := (Track{}).Near(0, 100); got != nil {
		t.Errorf("empty track: %+v", got)
	}
}

func TestHead(t *testing.T) {
	x0, y0, x1, y1 := good().Head()
	// 60 % of the width around the centre line (0.5), from the head top down 22 % of the box.
	if math.Abs(x0-0.32) > 1e-9 || math.Abs(x1-0.68) > 1e-9 || y0 != 0.22 || math.Abs(y1-(0.22+0.7*0.22)) > 1e-9 {
		t.Errorf("%v %v %v %v", x0, y0, x1, y1)
	}
}

func out9x16() timeline.Output {
	return timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30}
}

func TestMapBox(t *testing.T) {
	// A 9:16 frame in a 9:16 output is the identity, in pixels.
	r := MapBox(0.25, 0.2, 0.75, 0.4, 0.5625, timeline.Reframe{Mode: timeline.ReframeCover}, out9x16())
	if r.Left != 270 || r.Right != 810 || r.Top != 384 || r.Bottom != 768 {
		t.Errorf("identity: %+v", r)
	}
	// A 16:9 frame covering a 9:16 output: the height fills, the sides are cropped around the centre, so the centre stays the centre.
	r = MapBox(0.4, 0.2, 0.6, 0.4, 16.0/9, timeline.Reframe{Mode: timeline.ReframeCover}, out9x16())
	if cx := (r.Left + r.Right) / 2; cx < 535 || cx > 545 || r.Top != 384 {
		t.Errorf("cover: %+v", r)
	}
	// The same frame contained: it is a band across the middle of the output.
	r = MapBox(0, 0, 1, 1, 16.0/9, timeline.Reframe{Mode: timeline.ReframeContain}, out9x16())
	if r.Left != 0 || r.Right != 1080 || r.Top < 600 || r.Bottom > 1320 {
		t.Errorf("contain: %+v", r)
	}
	// A zoom of 2 magnifies about the centre of the output.
	r = MapBox(0.25, 0.25, 0.75, 0.75, 0.5625, timeline.Reframe{Mode: timeline.ReframeCover, Zoom: ptrF(2)}, out9x16())
	if r.Left > 0 || r.Right < 1080 { // 270..810 doubled about 540 is 0..1080
		t.Errorf("zoom: %+v", r)
	}
	// The focus moves the crop.
	left := MapBox(0.4, 0.2, 0.6, 0.4, 16.0/9, timeline.Reframe{Mode: timeline.ReframeCover, Focus: &timeline.Point{X: 0, Y: 0.5}}, out9x16())
	right := MapBox(0.4, 0.2, 0.6, 0.4, 16.0/9, timeline.Reframe{Mode: timeline.ReframeCover, Focus: &timeline.Point{X: 1, Y: 0.5}}, out9x16())
	if left.Left <= right.Left {
		t.Errorf("focus left %+v right %+v", left, right)
	}
}

func ptrF(v float64) *float64 { return &v }

func specWithClip(assetID string, in, out int64, rf *timeline.Reframe) *timeline.Spec {
	return &timeline.Spec{
		Schema: timeline.SchemaVersion, Output: out9x16(),
		Tracks: []timeline.Track{&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{
			{ID: "c1", AssetID: assetID, Source: timeline.Source{InMs: in, OutMs: out}, AtMs: 0, Reframe: rf},
			{ID: "c2", AssetID: "other", Source: timeline.Source{InMs: 0, OutMs: 3000}, AtMs: out - in},
		}}},
	}
}

func TestHeadRectsFollowTheClipsOnScreen(t *testing.T) {
	ix := Index{"a": {Aspect: 0.5625, Samples: []Sample{{AtMs: 1000, X: 0.2, Y: 0.2, W: 0.6, H: 0.7, HeadTopY: 0.22}, {AtMs: 8000, X: 0.2, Y: 0.4, W: 0.6, H: 0.5, HeadTopY: 0.42}}}}
	spec := specWithClip("a", 0, 5000, nil)
	// While c1 plays its first part, the sample at 1 s applies (the one at 8 s is outside the source range).
	rs := HeadRects(spec, ix.Lookup, 0, 3000)
	if len(rs) != 1 || rs[0].Top != 422 {
		t.Fatalf("c1: %+v", rs)
	}
	// While c2 plays (an asset with no subject) there is nothing to avoid.
	if rs := HeadRects(spec, ix.Lookup, 6000, 8000); len(rs) != 0 {
		t.Errorf("c2: %+v", rs)
	}
	// A text across both clips has the head of c1.
	if rs := HeadRects(spec, ix.Lookup, 4000, 7000); len(rs) != 1 {
		t.Errorf("across: %+v", rs)
	}
	if HeadRects(spec, nil, 0, 1000) != nil {
		t.Error("no lookup")
	}
	// A speed of 2 plays twice the source in the same time: the late sample comes into range.
	fast := specWithClip("a", 0, 10000, nil)
	fastTrack, _ := fast.Tracks[0].(*timeline.VideoTrack)
	fastTrack.Items[0].Speed = ptrF(2)
	if rs := HeadRects(fast, ix.Lookup, 0, 5000); len(rs) != 2 {
		t.Errorf("at speed 2: %+v", rs)
	}
}

// The camera moves the head: a punch-in (or a push-in, a pull-back, a shake, a zoom_through) magnifies the picture about its middle, so
// the head rises toward the title. The head region is the union of where it is at the least and at the greatest zoom while the text is on screen, and a title
// cleared above the head with no motion is cleared again when there is some.
func TestAPunchInBringsTheHeadBackUnderTheTitle(t *testing.T) {
	// A head whose top is at 25 % of the frame (480 px): a one-line title at the top of the zone (240 to 401) clears it.
	ix := Index{"a": {Aspect: 0.5625, Samples: []Sample{{AtMs: 1000, X: 0.2, Y: 0.23, W: 0.6, H: 0.7, HeadTopY: 0.25}}}}
	title := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 2000, Text: "BUILDING BACK 101", Style: "title_series", Anchor: timeline.AnchorUpperThird}
	still := specWithClip("a", 0, 5000, nil)
	rs := HeadRects(still, ix.Lookup, 0, 2000)
	if len(rs) != 1 || rs[0].Top != 480 {
		t.Fatalf("still: %+v", rs)
	}
	out := still.Output
	if _, av, _ := timeline.PlaceTextAvoiding(out, title, rs); av.Overlapped {
		t.Fatalf("the title should clear a still head: %+v", av)
	}
	// With a punch-in of 0.3 the head reaches (480 - 960) * 1.3 + 960 = 336 px: the union's top.
	punch := specWithClip("a", 0, 5000, nil)
	vt, _ := punch.Tracks[0].(*timeline.VideoTrack)
	strength := 0.3
	vt.Items[0].Motion = &timeline.Motion{Preset: timeline.MotionPunchIn, Strength: &strength}
	rs = HeadRects(punch, ix.Lookup, 0, 2000)
	if len(rs) != 1 || rs[0].Top != 336 || rs[0].Bottom < 718 {
		t.Fatalf("punch-in: %+v", rs)
	}
	pl, av, err := timeline.PlaceTextAvoiding(out, title, rs)
	if err != nil || !av.Overlapped || !av.Cleared {
		t.Fatalf("under the punch-in: %+v %v", av, err)
	}
	if pl.Box.Overlaps(rs[0]) || !pl.Zone.Safe(pl.Box) {
		t.Errorf("the title %+v is on the zoomed head %+v", pl.Box, rs[0])
	}
	// The head at the end of the zoom is inside the region too.
	zoomedTop := int((float64(480)-960)*1.3 + 960)
	if rs[0].Top > zoomedTop {
		t.Errorf("the region %+v misses the head at full zoom (top %d)", rs[0], zoomedTop)
	}
}

// twoClips is a hook clip of 5 s followed by a second one that a zoom_through brings in over the last 300 ms of the first.
func twoClips(motion *timeline.Motion) []timeline.VideoItem {
	return []timeline.VideoItem{
		{ID: "c1", AssetID: "a", Source: timeline.Source{InMs: 0, OutMs: 5000}, AtMs: 0, Motion: motion},
		{ID: "c2", AssetID: "a", Source: timeline.Source{InMs: 0, OutMs: 5000}, AtMs: 4700, TransitionIn: &timeline.Transition{Type: timeline.TransitionZoomThrough, DurationMs: 300}},
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The camera is read over the window the text is on screen: a punch_in only from its landing, a push_in or pull_back by the zoom its easing
// has reached, a zoom_through only through the overlap, a shake only while it lasts.
func TestCameraOverTheWindowOfTheText(t *testing.T) {
	s := 0.2
	at := int64(3000)
	punch := twoClips(&timeline.Motion{Preset: timeline.MotionPunchIn, Strength: &s, AtMs: &at})
	if c := cameraOver(punch, 0, 30, 0, 2000); c.hi != 1 || c.lo != 1 {
		t.Errorf("a punch-in that lands at 3 s, text before it: %+v", c)
	}
	if c := cameraOver(punch, 0, 30, 2800, 3300); !near(c.hi, 1.2) || c.lo >= 1.2 {
		t.Errorf("text on screen during the landing: %+v", c)
	}
	if c := cameraOver(punch, 0, 30, 3300, 4000); !near(c.lo, 1.2) || !near(c.hi, 1.2) {
		t.Errorf("text after the landing: %+v", c)
	}
	// No at_ms: the zoom lands about 0.15 s in.
	if c := cameraOver(twoClips(&timeline.Motion{Preset: timeline.MotionPunchIn, Strength: &s}), 0, 30, 0, 1000); !near(c.hi, 1.2) || c.lo != 1 {
		t.Errorf("a punch-in from the start: %+v", c)
	}

	push := twoClips(&timeline.Motion{Preset: timeline.MotionPushIn, Strength: &s})
	if c := cameraOver(push, 0, 30, 0, 500); c.hi >= 1.01 {
		t.Errorf("the first half second of a slow push: %+v", c)
	}
	if c := cameraOver(push, 0, 30, 0, 4900); c.hi < 1.19 || c.lo != 1 {
		t.Errorf("a push over the whole clip: %+v", c)
	}
	if c := cameraOver(push, 0, 30, 2400, 2600); c.lo < 1.09 || c.hi > 1.11 { // smoothstep is half way at the middle
		t.Errorf("the middle of a push: %+v", c)
	}
	pull := twoClips(&timeline.Motion{Preset: timeline.MotionPullBack, Strength: &s})
	if c := cameraOver(pull, 0, 30, 0, 100); c.hi < 1.19 {
		t.Errorf("the start of a pull-back: %+v", c)
	}
	if c := cameraOver(pull, 0, 30, 4400, 4650); c.hi >= 1.03 {
		t.Errorf("the end of a pull-back: %+v", c)
	}

	shake := twoClips(&timeline.Motion{Preset: timeline.MotionShake, Strength: &s, AtMs: &at})
	if c := cameraOver(shake, 0, 30, 0, 2500); c.shake != 1 || c.hi != 1 {
		t.Errorf("before the shake: %+v", c)
	}
	if c := cameraOver(shake, 0, 30, 3000, 3300); c.shake <= 1 || c.hi <= 1 {
		t.Errorf("during the shake: %+v", c)
	}
	if c := cameraOver(shake, 0, 30, 3600, 4500); c.shake != 1 || c.hi != 1 {
		t.Errorf("after the shake: %+v", c)
	}

	// A zoom_through touches only the overlap: the outgoing clip is clear before it and zoomed at its end, the incoming clip is zoomed at its
	// start and clear after the first half second.
	plain := twoClips(nil)
	if c := cameraOver(plain, 0, 30, 0, 4600); c.hi != 1 {
		t.Errorf("a hook that ends before the transition: %+v", c)
	}
	if c := cameraOver(plain, 0, 30, 4600, 5000); !near(c.hi, 1.45) {
		t.Errorf("the outgoing clip in the overlap: %+v", c)
	}
	if c := cameraOver(plain, 1, 30, 0, 100); !near(c.hi, 1.45) {
		t.Errorf("the incoming clip at its start: %+v", c)
	}
	if c := cameraOver(plain, 1, 30, 500, 2000); c.hi != 1 {
		t.Errorf("the incoming clip after the overlap: %+v", c)
	}
	// The renderer multiplies the factors: a punch-in of 0.2 on a clip that a zoom_through enters peaks at 1.2 * 1.45.
	both := twoClips(nil)
	both[1].Motion = &timeline.Motion{Preset: timeline.MotionPunchIn, Strength: &s, AtMs: new(int64)}
	if c := cameraOver(both, 1, 30, 0, 1000); !near(c.hi, 1.2*1.45) {
		t.Errorf("motion and zoom_through: %+v, want %v", c, 1.2*1.45)
	}
	// The first clip never has a transition in, and a clip alone has nothing to zoom through.
	if c := cameraOver(twoClips(nil)[:1], 0, 30, 0, 4900); c.hi != 1 {
		t.Errorf("a clip alone: %+v", c)
	}
}

func TestWithCamera(t *testing.T) {
	r := style.Rect{Left: 300, Top: 500, Right: 700, Bottom: 800}
	if got := withCamera(r, camera{lo: 1, hi: 1, shake: 1}, out9x16()); got != r {
		t.Errorf("no zoom: %+v", got)
	}
	// A box at a fixed zoom is that zoom's box; over a range it is the union of the two ends.
	fixed := withCamera(r, camera{lo: 1.2, hi: 1.2, shake: 1}, out9x16())
	if fixed.Top != int(math.Floor((500-960)*1.2+960)) || fixed.Left == r.Left {
		t.Errorf("a fixed zoom: %+v", fixed)
	}
	span := withCamera(r, camera{lo: 1, hi: 1.2, shake: 1}, out9x16())
	if span.Top != fixed.Top || span.Bottom != max(r.Bottom, fixed.Bottom) || span.Left != min(r.Left, fixed.Left) {
		t.Errorf("a range: %+v from %+v and %+v", span, r, fixed)
	}
	// A shake widens the region by what it can add.
	shaky := withCamera(r, camera{lo: 1, hi: 1.2, shake: 1.2}, out9x16())
	if shaky.Left >= span.Left || shaky.Top >= span.Top || shaky.Right <= span.Right || shaky.Bottom <= span.Bottom {
		t.Errorf("shake %+v against %+v", shaky, span)
	}
}

// A hook title that ends before the transition or the punch-in stays above the head, where the whole-clip widening pushed it below; a
// title on screen during the landing or the transition is still cleared.
func TestATitleBeforeTheCameraMovesStaysAboveTheHead(t *testing.T) {
	ix := Index{"a": {Aspect: 0.5625, Samples: []Sample{{AtMs: 1000, X: 0.2, Y: 0.23, W: 0.6, H: 0.7, HeadTopY: 0.25}}}}
	out := out9x16()
	title := func(at, dur int64) timeline.TextItem {
		return timeline.TextItem{ID: "t1", AtMs: at, DurationMs: dur, Text: "BUILDING BACK 101", Style: "title_series", Anchor: timeline.AnchorUpperThird}
	}
	spec := func(motion *timeline.Motion) *timeline.Spec {
		return &timeline.Spec{Schema: timeline.SchemaVersion, Output: out, Tracks: []timeline.Track{&timeline.VideoTrack{ID: "main", Items: twoClips(motion)}}}
	}
	check := func(name string, sp *timeline.Spec, ti timeline.TextItem, wantMoved bool) {
		t.Helper()
		rs := HeadRects(sp, ix.Lookup, ti.AtMs, ti.AtMs+ti.DurationMs)
		if len(rs) == 0 {
			t.Fatalf("%s: no head", name)
		}
		pl, av, err := timeline.PlaceTextAvoiding(out, ti, rs)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if av.Overlapped != wantMoved {
			t.Errorf("%s: overlapped %v, want %v (head %+v)", name, av.Overlapped, wantMoved, rs)
		}
		for _, r := range rs {
			if pl.Box.Overlaps(r) {
				t.Errorf("%s: the title %+v is on the head %+v", name, pl.Box, r)
			}
		}
		if wantMoved && !av.Cleared {
			t.Errorf("%s: not cleared: %+v", name, av)
		}
	}
	// A zoom_through at 4.7 s: a hook title over the first 2 s sees the head where it is, and stays at the top; the head at 25 % clears it.
	check("before the transition", spec(nil), title(0, 2000), false)
	if rs := HeadRects(spec(nil), ix.Lookup, 0, 2000); rs[0].Top != 480 {
		t.Errorf("the region was widened before the transition: %+v", rs)
	}
	// A title that is on screen as the transition runs sees the head zoomed in (1.45 about the middle), and is cleared.
	check("in the transition", spec(nil), title(4500, 500), true)
	// A punch-in landing at 3 s: before it the head is where it is; on screen as it lands, it is cleared.
	s := 0.3
	at := int64(3000)
	punch := &timeline.Motion{Preset: timeline.MotionPunchIn, Strength: &s, AtMs: &at}
	check("before the punch-in", spec(punch), title(0, 2000), false)
	check("on the landing", spec(punch), title(2800, 600), true)
}

// A speed ramp is followed: a clip that plays its first second slowly (a quarter speed) has played only a quarter of a second of source
// by 1 s, so the sample at 4 s is not on screen then, and is at the end.
func TestHeadRectsFollowASpeedRamp(t *testing.T) {
	ix := Index{"a": {Aspect: 0.5625, Samples: []Sample{{AtMs: 200, X: 0.2, Y: 0.2, W: 0.6, H: 0.7, HeadTopY: 0.22}, {AtMs: 4000, X: 0.2, Y: 0.4, W: 0.6, H: 0.5, HeadTopY: 0.42}}}}
	spec := specWithClip("a", 0, 6000, nil)
	vt, _ := spec.Tracks[0].(*timeline.VideoTrack)
	vt.Items[0].SpeedRamp = []timeline.SpeedPoint{{AtMs: 0, Speed: 0.25}, {AtMs: 1000, Speed: 0.25}, {AtMs: 1001, Speed: 4}}
	early := HeadRects(spec, ix.Lookup, 0, 800)
	if len(early) != 1 || early[0].Top != 422 { // only the sample at 0.2 s is in the first 0.8 s of output (0.2 s of source)
		t.Errorf("early: %+v", early)
	}
	if late := HeadRects(spec, ix.Lookup, 4300, 5200); len(late) != 1 || late[0].Top != 806 { // the first second of source takes 4 s of output, the rest 1.25 s: the sample at 4 s of source plays near the end
		t.Errorf("late: %+v (ramp output %d ms)", late, timeline.OutputDuration(vt.Items[0]))
	}
}

type fakeReader struct{ rows []store.AssetSubjectsRow }

func (f fakeReader) ListAssetSubjects(context.Context, ids.WorkspaceID, []ids.AssetID) ([]store.AssetSubjectsRow, error) {
	return f.rows, nil
}

func TestLoadForSpec(t *testing.T) {
	a := ids.AssetID{}
	a.UUID[0] = 7
	rd := fakeReader{rows: []store.AssetSubjectsRow{{AssetID: a, Subjects: []byte(`{"aspect":0.5625,"samples":[{"at_ms":0,"x":0.1,"y":0.1,"w":0.5,"h":0.5,"head_top_y":0.12}]}`)}}}
	spec := specWithClip(a.String(), 0, 3000, nil)
	ix, err := LoadForSpec(context.Background(), rd, ids.WorkspaceID{}, spec)
	if err != nil || len(ix) != 1 {
		t.Fatalf("%+v %v", ix, err)
	}
	if ix, err := LoadForSpec(context.Background(), rd, ids.WorkspaceID{}, &timeline.Spec{}); err != nil || ix != nil {
		t.Errorf("a spec with no clips: %+v %v", ix, err)
	}
}

func TestLoadCleansWhatTheStoreHolds(t *testing.T) {
	a, b := ids.AssetID{}, ids.AssetID{}
	a.UUID[0], b.UUID[0] = 1, 2
	rd := fakeReader{rows: []store.AssetSubjectsRow{
		{AssetID: a, Subjects: []byte(`{"aspect":0.5625,"samples":[{"at_ms":0,"x":0.1,"y":0.1,"w":0.5,"h":0.5,"head_top_y":0.12}]}`)},
		{AssetID: b, Subjects: []byte(`{"aspect":0.5625,"samples":[{"at_ms":0,"x":9,"y":0.1,"w":0.5,"h":0.5,"head_top_y":0.12}]}`)},
	}}
	ix, err := Load(context.Background(), rd, ids.WorkspaceID{}, []ids.AssetID{a, b})
	if err != nil || len(ix) != 1 {
		t.Fatalf("%+v %v", ix, err)
	}
	if _, ok := ix.Lookup(a.String()); !ok {
		t.Error("the good asset is missing")
	}
}
