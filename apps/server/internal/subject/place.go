package subject

import (
	"math"

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
	for _, it := range main.Items {
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
		for _, s := range tr.Near(in, outMs) {
			x0, y0, x1, y1 := s.Head()
			out = append(out, MapBox(x0, y0, x1, y1, tr.Aspect, rf, spec.Output))
		}
	}
	return out
}

// sourceAt is the time in the clip's source that plays t ms into the clip: through the speed ramp when it has one, else at its speed.
func sourceAt(it timeline.VideoItem, t int64) int64 {
	if it.HasRamp() {
		return it.Source.InMs + int64(timeline.RampSourceMs(it.SpeedRamp, float64(t)))
	}
	return it.Source.InMs + int64(float64(t)*it.SpeedOrDefault())
}
