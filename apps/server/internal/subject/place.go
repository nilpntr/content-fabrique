package subject

import (
	"cmp"
	"math"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Lookup returns the subject track of an asset (by its TypeID string), or false when the asset has none.
type Lookup func(assetID string) (Track, bool)

func clampF(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// MapBox maps a box of fractions of a clip's frame (aspect is the frame's width over its height) to a box in pixels of the output, the
// way the renderer places the clip: cover scales to fill and crops around the focus, contain and blur_fill scale to fit and centre, and a
// zoom magnifies the middle of the result. The camera moves of a motion preset are not followed.
func MapBox(x0, y0, x1, y1, aspect float64, rf timeline.Reframe, out timeline.Output) style.Rect {
	w, h := float64(out.Width), float64(out.Height)
	sw, sh := aspect, 1.0
	var px func(u, v float64) (float64, float64)
	switch rf.Mode {
	case timeline.ReframeContain, timeline.ReframeBlurFill:
		s := math.Min(w/sw, h/sh)
		fw, fh := sw*s, sh*s
		ox, oy := (w-fw)/2, (h-fh)/2
		px = func(u, v float64) (float64, float64) { return u*fw + ox, v*fh + oy }
	default: // cover
		s := math.Max(w/sw, h/sh)
		fw, fh := sw*s, sh*s
		f := rf.FocusOrDefault()
		ox := clampF(f.X*fw-w/2, 0, math.Max(fw-w, 0))
		oy := clampF(f.Y*fh-h/2, 0, math.Max(fh-h, 0))
		px = func(u, v float64) (float64, float64) { return u*fw - ox, v*fh - oy }
	}
	z := rf.ZoomOrDefault()
	zoom := func(x, y float64) (float64, float64) { return (x-w/2)*z + w/2, (y-h/2)*z + h/2 }
	ax, ay := px(x0, y0)
	bx, by := px(x1, y1)
	ax, ay = zoom(ax, ay)
	bx, by = zoom(bx, by)
	return style.Rect{Left: int(math.Floor(ax)), Top: int(math.Floor(ay)), Right: int(math.Ceil(bx)), Bottom: int(math.Ceil(by))}
}

// Regions are the subjects of the main-track clips that are on screen between startMs and endMs of the output, in pixels of the output:
// the head (the head and shoulders) and the body (the whole subject box, a raised arm included, so "above the body" is above the highest
// hand or the head). ShowsOff says the clip is a pose or physique shot. A clip contributes the samples taken in the part of its source that plays in that
// window, or its nearest sample. Both regions are widened by the camera while the text is on screen.
func Regions(spec *timeline.Spec, lookup Lookup, startMs, endMs int64) []timeline.Subject {
	main, _, ok := timeline.MainTrack(spec)
	if !ok || lookup == nil {
		return nil
	}
	var out []timeline.Subject
	// The clips in time order, so a clip knows the transition into the next one.
	items := slices.Clone(main.Items)
	slices.SortStableFunc(items, func(a, b timeline.VideoItem) int { return cmp.Compare(a.AtMs, b.AtMs) })
	for i, it := range items {
		cs, ce := it.AtMs, it.AtMs+timeline.OutputDuration(it)
		os, oe := max(cs, startMs), min(ce, endMs)
		if oe <= os {
			continue
		}
		tr, ok := lookup(it.AssetID)
		if !ok {
			continue
		}
		in, outMs := sourceAt(it, os-cs), sourceAt(it, oe-cs)
		rf := timeline.Reframe{Mode: timeline.DefaultReframe}
		if it.Reframe != nil {
			rf = *it.Reframe
		}
		cam := cameraOver(items, i, spec.Output.FPS, os-cs, oe-cs)
		for _, s := range tr.Near(in, outMs) {
			x0, y0, x1, y1 := s.Head()
			head := MapBox(x0, y0, x1, y1, tr.Aspect, rf, spec.Output)
			bx0, by0, bx1, by1 := s.Body()
			body := MapBox(bx0, by0, bx1, by1, tr.Aspect, rf, spec.Output)
			tx0, ty0, tx1, ty1 := s.Torso()
			torso := MapBox(tx0, ty0, tx1, ty1, tr.Aspect, rf, spec.Output)
			out = append(out, timeline.Subject{
				Head: withCamera(head, cam, spec.Output), Body: withCamera(body, cam, spec.Output), Torso: withCamera(torso, cam, spec.Output),
				ShowsOff: tr.ShowsOff,
			})
		}
	}
	return out
}

// The shapes of the camera, as the renderer has them (render/camera.go, which this package cannot import): a zoom_through zooms by
// zoomThroughStrength, a punch_in takes punchSeconds (clamped to punchMinF..punchMaxF frames) to land, a shake lasts shakeSeconds, reaches
// full size in shakeAttackF frames and uses shakeRoom of the room the zoom leaves.
const (
	zoomThroughStrength = 0.45
	punchSeconds        = 0.15
	punchMinF           = 4
	punchMaxF           = 6
	shakeSeconds        = 0.4
	shakeAttackF        = 2
	shakeRoom           = 0.9
)

// camera is the zoom the camera gives one clip over a window of its play: the range of the total zoom (the factors of the motion preset and
// of the zoom_through transitions multiply, as in the renderer), and the largest zoom a shake reaches in it, 1 when there is no shake.
type camera struct {
	lo, hi, shake float64
}

// cameraOver is the camera of items[i] while the part of its output from fromMs to toMs (ms into the clip's own play) is on screen. It is
// the renderer's own maths, evaluated frame by frame: a punch_in is zoomed from its landing on, a push_in or pull_back at the zoom its easing
// has reached by then, a zoom_through only through the overlap of the transition, and a shake only while it lasts. A text that is on
// screen before the camera moves is therefore not widened by what happens after it.
func cameraOver(items []timeline.VideoItem, i int, fps int, fromMs, toMs int64) camera {
	if fps <= 0 {
		fps = 30
	}
	it := items[i]
	startF := timeline.Frame(it.AtMs, fps)
	outF := timeline.Frame(timeline.EndMs(it), fps) - startF
	if outF < 1 {
		return camera{lo: 1, hi: 1, shake: 1}
	}
	// The transitions into and out of the clip that are zoom_throughs, with the frames they overlap (the first clip never has one in).
	var inOverlap, outOverlap int64
	if i > 0 && it.TransitionIn != nil && it.TransitionIn.Type == timeline.TransitionZoomThrough {
		inOverlap = max(timeline.Frame(timeline.EndMs(items[i-1]), fps)-startF, 0)
	}
	if i+1 < len(items) && items[i+1].TransitionIn != nil && items[i+1].TransitionIn.Type == timeline.TransitionZoomThrough {
		outOverlap = max(timeline.Frame(timeline.EndMs(it), fps)-timeline.Frame(items[i+1].AtMs, fps), 0)
	}
	motion := func(int64) (z, shake float64) { return 1, 1 }
	if m := it.Motion; m.Active() {
		motion = motionAt(*m, outF, fps)
	}
	from := min(max(timeline.Frame(fromMs, fps), 0), outF-1)
	to := min(max(timeline.Frame(toMs, fps), from), outF-1)
	c := camera{lo: math.Inf(1), hi: 1, shake: 1}
	for f := from; f <= to; f++ {
		z, sh := motion(f)
		if inOverlap > 0 { // zoomed through the first half of the overlap, then zooming out over the second half
			half := float64(inOverlap) / 2
			z *= 1 + zoomThroughStrength*math.Pow(1-ramp(float64(f)-half, half), 3)
		}
		if outOverlap > 0 { // zooming in over the first half of the overlap, then held to the end
			half := float64(outOverlap) / 2
			z *= 1 + zoomThroughStrength*math.Pow(ramp(float64(f-(outF-outOverlap)), half), 3)
		}
		c.lo, c.hi, c.shake = math.Min(c.lo, z), math.Max(c.hi, z), math.Max(c.shake, sh)
	}
	if math.IsInf(c.lo, 1) {
		c.lo = 1
	}
	return c
}

// ramp is how far through a window of length `length` a point is, from 0 to 1 (clamped).
func ramp(from, length float64) float64 { return math.Min(math.Max(from/length, 0), 1) }

// motionAt is the zoom of a motion preset at a frame of the clip (outF frames long), and the zoom of its shake (1 when it does not shake).
func motionAt(m timeline.Motion, outF int64, fps int) func(f int64) (z, shake float64) {
	s := m.StrengthOrDefault()
	last := float64(max(outF-1, 1))
	at := int64(-1)
	if m.AtMs != nil {
		at = min(timeline.Frame(*m.AtMs, fps), outF-1)
	}
	smooth := func(p float64) float64 { return p * p * (3 - 2*p) }
	switch m.Preset {
	case timeline.MotionPunchIn:
		rampF := int64(min(max(math.Floor(punchSeconds*float64(fps)+0.5), punchMinF), punchMaxF))
		land := rampF
		if at >= 0 {
			land = at
		}
		start := max(0, land-rampF)
		return func(f int64) (float64, float64) {
			if land == start {
				return 1 + s, 1
			}
			return 1 + s*(1-math.Pow(1-ramp(float64(f-start), float64(land-start)), 3)), 1
		}
	case timeline.MotionPushIn:
		return func(f int64) (float64, float64) { return 1 + s*smooth(ramp(float64(f), last)), 1 }
	case timeline.MotionPullBack:
		return func(f int64) (float64, float64) { return 1 + s*(1-smooth(ramp(float64(f), last))), 1 }
	case timeline.MotionShake:
		start := max(at, 0)
		length := max(int64(math.Floor(shakeSeconds*float64(fps)+0.5)), shakeAttackF+1)
		return func(f int64) (float64, float64) {
			m := f - start
			if m < 0 || m > length {
				return 1, 1
			}
			env := math.Min(float64(m)/shakeAttackF, 1) * math.Pow(1-float64(m)/float64(length), 2)
			return 1 + s*env, 1 + s*env
		}
	}
	return func(int64) (float64, float64) { return 1, 1 }
}

// withCamera is the box a head can be in while the camera is as c says: the union of where it is at the least and at the greatest zoom of
// the window (the camera zooms about the middle of the frame, and a coordinate moves linearly with the zoom, so the bounding box of the two
// ends holds every moment in between), widened by what a shake can add.
func withCamera(r style.Rect, c camera, out timeline.Output) style.Rect {
	if c.hi <= 1 && c.lo <= 1 {
		return r
	}
	w, h := float64(out.Width), float64(out.Height)
	at := func(z float64) style.Rect {
		zx := func(x float64) float64 { return (x-w/2)*z + w/2 }
		zy := func(y float64) float64 { return (y-h/2)*z + h/2 }
		return style.Rect{
			Left: int(math.Floor(zx(float64(r.Left)))), Top: int(math.Floor(zy(float64(r.Top)))),
			Right: int(math.Ceil(zx(float64(r.Right)))), Bottom: int(math.Ceil(zy(float64(r.Bottom)))),
		}
	}
	a, b := at(c.lo), at(c.hi)
	u := style.Rect{Left: min(a.Left, b.Left), Top: min(a.Top, b.Top), Right: max(a.Right, b.Right), Bottom: max(a.Bottom, b.Bottom)}
	if c.shake > 1 {
		padX := int(math.Ceil(shakeRoom * w * (1 - 1/c.shake) / 2 * c.hi))
		padY := int(math.Ceil(shakeRoom * h * (1 - 1/c.shake) / 2 * c.hi))
		u.Left, u.Right, u.Top, u.Bottom = u.Left-padX, u.Right+padX, u.Top-padY, u.Bottom+padY
	}
	return u
}

// sourceAt is the time in the clip's source that plays t ms into the clip: through the speed ramp when it has one, else at its speed.
func sourceAt(it timeline.VideoItem, t int64) int64 {
	if it.HasRamp() {
		return it.Source.InMs + int64(timeline.RampSourceMs(it.SpeedRamp, float64(t)))
	}
	return it.Source.InMs + int64(float64(t)*it.SpeedOrDefault())
}
