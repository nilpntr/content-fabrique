package styleprofile

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
)

// scene is a synthetic picture: a texture of a few sines (a different one per shot), seen at a scale about the centre, moved by dx, dy
// pixels and blurred over a box of radius blur, as one grey thumbnail.
func scene(seed int, scale, dx, dy float64, blur int) []byte {
	fx, fy := 0.31+0.07*float64(seed), 0.23+0.05*float64(seed)
	tex := func(x, y float64) float64 {
		return 128 + 50*math.Sin(x*fx)*math.Cos(y*fy) + 30*math.Sin((x+y)*0.17+float64(seed)) + 20*math.Cos(x*0.53-y*0.11)
	}
	c := float64(tsz-1) / 2
	raw := make([]float64, tsz*tsz)
	for y := range tsz {
		for x := range tsz {
			raw[y*tsz+x] = tex(c+(float64(x)-c)/scale+dx, c+(float64(y)-c)/scale+dy)
		}
	}
	out := make([]byte, tsz*tsz)
	for y := range tsz {
		for x := range tsz {
			var sum float64
			n := 0
			for j := -blur; j <= blur; j++ {
				for i := -blur; i <= blur; i++ {
					xx, yy := min(max(x+i, 0), tsz-1), min(max(y+j, 0), tsz-1)
					sum += raw[yy*tsz+xx]
					n++
				}
			}
			out[y*tsz+x] = byte(math.Round(math.Max(0, math.Min(255, sum/float64(n)))))
		}
	}
	return out
}

// clip is 6 s of thumbnails with a cut at 3 s: the frame function gives each frame from its shot (0 or 1) and its time from the cut.
func clip(frame func(shot int, fromCut float64) []byte) ([][]byte, float64) {
	fps := media.StyleThumbFPS
	var frames [][]byte
	for i := range 6 * fps {
		t := float64(i)/float64(fps) - 3
		shot := 0
		if t >= 0 {
			shot = 1
		}
		frames = append(frames, frame(shot, t))
	}
	return frames, 3
}

// The zoom transition is told from what only looks like one (the live re-learn read a hand-held lifestyle video as zooms at its cuts):
// a zoom-through that scales into the cut and out of it, blurred at the switch, is a zoom; a camera push that runs into a plain cut, a
// hand-held camera on both shots and a plain cut are not.
func TestAZoomTransitionStraddlesTheCutWithABlur(t *testing.T) {
	zoomThrough, cut := clip(func(shot int, d float64) []byte {
		switch {
		case shot == 0 && d > -0.4: // scales in towards the cut, more and more blurred
			k := (d + 0.4) / 0.4
			return scene(0, 1+0.25*k, 0, 0, int(math.Round(2*k)))
		case shot == 1 && d < 0.4: // starts scaled and blurred, settles
			k := 1 - d/0.4
			return scene(1, 1+0.25*k, 0, 0, int(math.Round(2*k)))
		}
		return scene(shot, 1, 0, 0, 0)
	})
	if !zoomTransitionAt(zoomThrough, cut) {
		t.Error("a zoom-through is not read as a zoom")
	}

	pushIntoCut, cut := clip(func(shot int, d float64) []byte {
		if shot == 0 && d > -1.5 { // a slow camera push over the last 1.5 s of the shot, sharp, then a plain cut
			return scene(0, 1+0.12*(d+1.5)/1.5, 0, 0, 0)
		}
		return scene(shot, 1, 0, 0, 0)
	})
	if zoomTransitionAt(pushIntoCut, cut) {
		t.Error("a camera push into a plain cut is read as a zoom transition")
	}

	rng := rand.New(rand.NewPCG(7, 11))
	handheld, cut := clip(func(shot int, _ float64) []byte { // both shots shake and breathe the whole time
		return scene(shot, 1+0.02*(rng.Float64()-0.5), 3*(rng.Float64()-0.5), 3*(rng.Float64()-0.5), rng.IntN(2))
	})
	if zoomTransitionAt(handheld, cut) {
		t.Error("a hand-held camera is read as a zoom transition")
	}

	plain, cut := clip(func(shot int, _ float64) []byte { return scene(shot, 1, 0, 0, 0) })
	if zoomTransitionAt(plain, cut) {
		t.Error("a plain cut is read as a zoom transition")
	}

	// The same through transitionsOf: only the zoom-through counts.
	for name, c := range map[string]struct {
		frames [][]byte
		zoom   int
	}{"zoom-through": {zoomThrough, 1}, "push": {pushIntoCut, 0}, "hand-held": {handheld, 0}, "plain": {plain, 0}} {
		if ts := transitionsOf([]float64{cut}, nil, c.frames); ts.Zoom != c.zoom {
			t.Errorf("%s: %+v", name, ts)
		}
	}
}

// One video with a few zooms, or zooms on a sliver of the cuts, is not the creator's style.
func TestStrayTransitionsAreNotTheStyle(t *testing.T) {
	ref := func(cuts, zoom int) Measures {
		return Measures{Transitions: &TransitionStats{Cuts: cuts, Hard: cuts - zoom, Zoom: zoom}}
	}
	one := aggregateTransitions([]Measures{ref(10, 4), ref(10, 0), ref(10, 0)})
	if one.Used != TransitionsSome || one.Videos != 1 || one.Evident() || one.Level() != "" {
		t.Errorf("zooms in one video: %+v level %q", one, one.Level())
	}
	sliver := aggregateTransitions([]Measures{ref(25, 1), ref(25, 1), ref(25, 2)})
	if sliver.Videos != 3 || sliver.Share >= TransitionsMinShare || sliver.Level() != "" {
		t.Errorf("zooms on a sliver of the cuts: %+v level %q", sliver, sliver.Level())
	}
	style := aggregateTransitions([]Measures{ref(10, 3), ref(10, 3), ref(10, 0)})
	if !style.Evident() || style.Level() != LevelSubtle {
		t.Errorf("zooms on a fifth of the cuts in two videos: %+v level %q", style, style.Level())
	}
}
