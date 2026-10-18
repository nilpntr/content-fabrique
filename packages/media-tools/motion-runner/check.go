package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
)

// The frame checks. A blank frame where an overlay should be on screen, or ink outside every overlay's box (a template that
// painted the background, or text that ran out of its box), fails the job: the render would otherwise ship a title that is
// missing or a frame that hides the video.

// spillMargin is how far outside its box (as a share of the layout's height) an overlay may draw: a blur's fringe or a
// shadow's tail. Beyond it, ink counts as spill.
const spillMargin = 0.02

// inkCheck is one item that must show ink in one frame.
type inkCheck struct {
	item Item
}

// inkChecks maps each frame that has an ink check to the items to check there.
func inkChecks(j *Job) map[int64][]inkCheck {
	out := map[int64][]inkCheck{}
	for _, it := range j.Items {
		for _, t := range it.InkAtMs {
			n := j.FrameAt(t)
			if j.covers(n) {
				out[n] = append(out[n], inkCheck{item: it})
			}
		}
	}
	return out
}

// checkFrame decodes a frame and checks it: every item of checks has ink inside its box (at least minInkShare of the box's
// pixels at half opacity or more), and nothing is drawn far outside the boxes of the items on screen at that frame.
func checkFrame(data []byte, j *Job, checks []inkCheck, n int64, clip PixelRect) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("the capture is not a PNG: %w", err)
	}
	b := img.Bounds()
	if b.Dx() != clip.W || b.Dy() != clip.H {
		return fmt.Errorf("the capture is %dx%d, not the %dx%d clip", b.Dx(), b.Dy(), clip.W, clip.H)
	}
	inner := alphaOf(img)
	// Everything below is in frame pixels; the capture starts at the clip's corner.
	ox, oy := b.Min.X-clip.X, b.Min.Y-clip.Y
	alpha := func(x, y int) uint8 { return inner(x+ox, y+oy) }
	b = image.Rect(clip.X, clip.Y, clip.X+clip.W, clip.Y+clip.H)
	scale := j.Scale()
	for _, c := range checks {
		r := pixelRect(c.item.Box, scale, 0, b)
		ink, area := 0, r.Dx()*r.Dy()
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if alpha(x, y) >= 128 {
					ink++
				}
			}
		}
		if area == 0 || float64(ink) < minInkShare*float64(area) {
			return fmt.Errorf("item %s (%s) is blank at %d ms: %d of %d pixels of its box have ink, when it should be on screen", c.item.ID, c.item.Template, int64(j.FrameMs(n)), ink, area)
		}
	}
	// Spill: ink outside the boxes of the items that are on screen, each grown by the margin.
	ms := j.FrameMs(n)
	var boxes []image.Rectangle
	for _, it := range j.Items {
		if ms >= float64(it.StartMs) && ms < float64(it.EndMs) {
			boxes = append(boxes, pixelRect(it.Box, scale, spillMargin*float64(j.CSSHeight)*scale, b))
		}
	}
	spill := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if alpha(x, y) < 24 {
				continue
			}
			pt := image.Pt(x, y)
			inside := false
			for _, r := range boxes {
				if pt.In(r) {
					inside = true
					break
				}
			}
			if !inside {
				spill++
			}
		}
	}
	if limit := maxSpillShare * float64(b.Dx()*b.Dy()); float64(spill) > limit {
		return fmt.Errorf("%d pixels have ink outside every overlay's box at %d ms: a template drew outside its box", spill, int64(ms))
	}
	return nil
}

const (
	// minInkShare is the share of a box that must have ink when the item is fully on screen. A one-word title in a wide box
	// covers a few percent of it; a blank box covers none.
	minInkShare = 0.004
	// maxSpillShare is the share of the frame that may have ink outside the boxes (antialiasing, a stray blur pixel).
	maxSpillShare = 0.0005
)

// pixelRect maps a CSS box to frame pixels, grown by margin pixels and clipped to bounds.
func pixelRect(bx Box, scale, margin float64, bounds image.Rectangle) image.Rectangle {
	r := image.Rect(
		int(math.Floor(bx.X*scale-margin)), int(math.Floor(bx.Y*scale-margin)),
		int(math.Ceil((bx.X+bx.W)*scale+margin)), int(math.Ceil((bx.Y+bx.H)*scale+margin)),
	)
	return r.Intersect(bounds)
}

// alphaOf returns a fast alpha accessor for the image types png.Decode returns.
func alphaOf(img image.Image) func(x, y int) uint8 {
	switch m := img.(type) {
	case *image.NRGBA:
		return func(x, y int) uint8 { return m.Pix[m.PixOffset(x, y)+3] }
	case *image.RGBA:
		return func(x, y int) uint8 { return m.Pix[m.PixOffset(x, y)+3] }
	case *image.NRGBA64:
		return func(x, y int) uint8 { return m.Pix[m.PixOffset(x, y)+6] }
	case *image.RGBA64:
		return func(x, y int) uint8 { return m.Pix[m.PixOffset(x, y)+6] }
	default:
		// Formats without alpha (an opaque capture) are all ink.
		return func(x, y int) uint8 {
			_, _, _, a := img.At(x, y).RGBA()
			return uint8(a >> 8)
		}
	}
}
