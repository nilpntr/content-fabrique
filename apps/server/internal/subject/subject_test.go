package subject

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
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
