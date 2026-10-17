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

// HeadRects are the head regions of the subjects of the main-track clips that are on screen between startMs and endMs of the output, in
// pixels of the output. A clip contributes the samples taken in the part of its source that plays in that window, or its nearest sample.
func HeadRects(spec *timeline.Spec, lookup Lookup, startMs, endMs int64) []style.Rect {
	main, _, ok := timeline.MainTrack(spec)
	if !ok || lookup == nil {
		return nil
	}
	var out []style.Rect
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
		zoom, shake := cameraZoom(items, i)
		for _, s := range tr.Near(in, outMs) {
			x0, y0, x1, y1 := s.Head()
			r := MapBox(x0, y0, x1, y1, tr.Aspect, rf, spec.Output)
			out = append(out, withCamera(r, zoom, shake, spec.Output))
		}
	}
	return out
}

// zoomThroughStrength is how far a zoom_through transition zooms into the outgoing clip and out of the incoming one (render's constant).
const zoomThroughStrength = 0.45

// shakeRoom is the share of the room the zoom leaves at the edges that a shake may use (render's constant).
const shakeRoom = 0.9

// cameraZoom is the strongest zoom the camera gives the clip at items[i] during its play, and whether it shakes: the peak of its motion
// preset (a punch_in lands at 1 + strength, a push_in ends there, a pull_back starts there, a shake peaks there) and of the
// zoom_through transitions into and out of it. The renderer's own maths, taken at its extremes (render/camera.go).
func cameraZoom(items []timeline.VideoItem, i int) (zoom float64, shake bool) {
	zoom = 1
	if m := items[i].Motion; m.Active() {
		zoom = 1 + m.StrengthOrDefault()
		shake = m.Preset == timeline.MotionShake
	}
	into := items[i].TransitionIn != nil && items[i].TransitionIn.Type == timeline.TransitionZoomThrough
	out := i+1 < len(items) && items[i+1].TransitionIn != nil && items[i+1].TransitionIn.Type == timeline.TransitionZoomThrough
	if into || out {
		zoom *= 1 + zoomThroughStrength // the renderer multiplies the factors
	}
	return zoom, shake
}

// withCamera is the box a head can be in at any moment of the clip: the union of where it is with no camera move and where it is at the
// strongest zoom (the camera zooms about the middle of the frame, and a coordinate moves linearly with the zoom, so the bounding box of the
// two ends holds every frame in between), widened by what a shake can add.
func withCamera(r style.Rect, zoom float64, shake bool, out timeline.Output) style.Rect {
	if zoom <= 1 {
		return r
	}
	w, h := float64(out.Width), float64(out.Height)
	zx := func(x float64) float64 { return (x-w/2)*zoom + w/2 }
	zy := func(y float64) float64 { return (y-h/2)*zoom + h/2 }
	u := style.Rect{
		Left:   min(r.Left, int(math.Floor(zx(float64(r.Left))))),
		Top:    min(r.Top, int(math.Floor(zy(float64(r.Top))))),
		Right:  max(r.Right, int(math.Ceil(zx(float64(r.Right))))),
		Bottom: max(r.Bottom, int(math.Ceil(zy(float64(r.Bottom))))),
	}
	if shake {
		padX := int(math.Ceil(shakeRoom * w * (1 - 1/zoom) / 2 * zoom))
		padY := int(math.Ceil(shakeRoom * h * (1 - 1/zoom) / 2 * zoom))
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
