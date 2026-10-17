package render

import (
	"fmt"
	"hash/fnv"
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The camera (P3-10): motion presets and the zoom of a zoom_through transition are one filter per clip, `perspective`,
// which samples a window of the already-reframed frame and maps it onto the whole frame. The window is always inside the
// frame, so no zoom or shake shows a border (the frame is zoomed, never shrunk). Its corners are expressions of the
// frame number `in`, evaluated per frame (`eval=frame`), so the zoom is sub-pixel smooth and the result is deterministic:
// everything the expressions contain is a number formatted here (never text from the spec), and the shake pattern comes
// from a hash of the clip's id.
//
// Why perspective and not zoompan or scale+crop: zoompan rounds the window to whole pixels and jitters on a slow zoom;
// scale with eval=frame changes the size of the stream per frame, and a crop after it cannot follow. perspective samples
// the window with cubic interpolation at fractional positions. It works on 8-bit planar YUV, so the clip is converted
// to yuv420p in front of it (the graph ends in yuv420p anyway).

// Fixed shapes of the presets, in frames or fractions, chosen once so a preset looks the same at every frame rate.
const (
	punchSeconds = 0.15 // the zoom of a punch_in takes about 5 frames: this many seconds, clamped to 4..6 frames
	punchMinF    = 4
	punchMaxF    = 6
	shakeSeconds = 0.4 // a shake dies out in this long
	shakeAttackF = 2   // and reaches full size in this many frames
	// shakeRoom is the part of the room the zoom leaves at the edges that the shake may use: the window never reaches the edge.
	shakeRoom = 0.9
	// zoomThroughStrength is how far a zoom_through zooms into the outgoing clip and out of the incoming one.
	zoomThroughStrength = 0.45
)

// punchRampFrames is how many frames the zoom of a punch_in takes: about 0.15 s, but 4 to 6 frames at any frame rate.
func punchRampFrames(fps int) int64 {
	return int64(min(max(math.Floor(punchSeconds*float64(fps)+0.5), punchMinF), punchMaxF))
}

// camera is what moves the picture of one clip: factors that multiply into the zoom, and the shake offset.
type camera struct {
	zooms []string // each an expression of `in` that is at least 1
	dx    string   // the shake offset as an expression in pixels, or "" for none
	dy    string
}

func (k *camera) empty() bool { return len(k.zooms) == 0 }

// zoom is the product of the factors.
func (k *camera) zoom() string {
	return "(" + strings.Join(k.zooms, ")*(") + ")"
}

// filters returns the filter chain of the camera: nothing when the clip does not move.
func (k *camera) filters() []string {
	if k.empty() {
		return nil
	}
	z := k.zoom()
	// The window is centred and has the size of the frame divided by the zoom; rx and ry are the room on each side.
	rx := fmt.Sprintf("W*(1-1/(%s))/2", z)
	ry := fmt.Sprintf("H*(1-1/(%s))/2", z)
	x0, x1 := rx, "W-"+rx
	y0, y1 := ry, "H-"+ry
	if k.dx != "" {
		x0, x1 = x0+"+"+k.dx, x1+"+"+k.dx
		y0, y1 = y0+"+"+k.dy, y1+"+"+k.dy
	}
	return []string{
		"format=yuv420p",
		fmt.Sprintf("perspective=eval=frame:sense=source:interpolation=cubic:x0='%s':y0='%s':x1='%s':y1='%s':x2='%s':y2='%s':x3='%s':y3='%s'",
			x0, y0, x1, y0, x0, y1, x1, y1),
	}
}

// cameraFor builds the camera of a clip from its motion preset and from the zoom_through transitions into and out of it.
func (c *compiler) cameraFor(cl clip) (*camera, error) {
	k := &camera{}
	if m := cl.item.Motion; m.Active() {
		if err := c.addMotion(k, cl, *m); err != nil {
			return nil, err
		}
	}
	// zoom_through: the outgoing clip zooms in over the first half of the transition and holds, the incoming clip holds
	// the zoom over the first half and zooms out over the second half, so at the middle of the crossfade both are zoomed.
	if cl.transition == timeline.TransitionZoomThrough && cl.overlapFrames > 0 {
		half := float64(cl.overlapFrames) / 2
		k.zooms = append(k.zooms, fmt.Sprintf("1+%s*pow(1-%s,3)", num(zoomThroughStrength), progress("in-"+num(half), num(half))))
	}
	if cl.idx+1 < len(c.clips) {
		if next := c.clips[cl.idx+1]; next.transition == timeline.TransitionZoomThrough && next.overlapFrames > 0 {
			half := float64(next.overlapFrames) / 2
			start := cl.outFrames - next.overlapFrames
			k.zooms = append(k.zooms, fmt.Sprintf("1+%s*pow(%s,3)", num(zoomThroughStrength), progress(fmt.Sprintf("in-%d", start), num(half))))
		}
	}
	return k, nil
}

// progress is the expression for how far through a window of length `length` the clip is, from 0 to 1.
func progress(from, length string) string {
	return fmt.Sprintf("clip((%s)/(%s),0,1)", from, length)
}

// smooth is smoothstep of a progress expression: slow, fast, slow.
func smooth(p string) string {
	return fmt.Sprintf("(%s)*(%s)*(3-2*(%s))", p, p, p)
}

func (c *compiler) addMotion(k *camera, cl clip, m timeline.Motion) error {
	s := m.StrengthOrDefault()
	if math.IsNaN(s) || s < timeline.MinMotionStrength || s > timeline.MaxMotionStrength {
		return fmt.Errorf("%w: clip %s has motion strength %s outside %s to %s", ErrInvalidSpec, cl.item.ID, num(s), num(timeline.MinMotionStrength), num(timeline.MaxMotionStrength))
	}
	last := max(cl.outFrames-1, 1)
	at := int64(-1) // frame of at_ms inside the clip, -1 when the spec gives none
	if m.AtMs != nil {
		at = min(timeline.Frame(*m.AtMs, c.fps), cl.outFrames-1)
	}
	switch m.Preset {
	case timeline.MotionPunchIn:
		ramp := punchRampFrames(c.fps)
		land := ramp // by default the zoom starts on the clip's first frame and lands one ramp later
		if at >= 0 {
			land = at
		}
		start := max(0, land-ramp)
		if land == start { // nothing before the landing: the clip starts zoomed
			k.zooms = append(k.zooms, "1+"+num(s))
			return nil
		}
		// Ease-out: fast at first, settling into the landing.
		p := progress(fmt.Sprintf("in-%d", start), fmt.Sprintf("%d", land-start))
		k.zooms = append(k.zooms, fmt.Sprintf("1+%s*(1-pow(1-%s,3))", num(s), p))
	case timeline.MotionPushIn:
		k.zooms = append(k.zooms, fmt.Sprintf("1+%s*%s", num(s), smooth(progress("in", fmt.Sprintf("%d", last)))))
	case timeline.MotionPullBack:
		k.zooms = append(k.zooms, fmt.Sprintf("1+%s*(1-%s)", num(s), smooth(progress("in", fmt.Sprintf("%d", last)))))
	case timeline.MotionShake:
		return c.addShake(k, cl, s, max(at, 0))
	default:
		return fmt.Errorf("%w: clip %s has unknown motion preset %q", ErrInvalidSpec, cl.item.ID, m.Preset)
	}
	return nil
}

// addShake adds a short camera shake starting at frame start. The zoom grows with the shake and falls back to 1 as it dies
// out; the offset is a sum of two sines per axis, with frequencies and phases taken from a hash of the clip's id, times a
// share of the room the zoom leaves, so the window stays inside the frame at every frame.
func (c *compiler) addShake(k *camera, cl clip, strength float64, start int64) error {
	length := max(int64(math.Floor(shakeSeconds*float64(c.fps)+0.5)), shakeAttackF+1)
	m := fmt.Sprintf("(in-%d)", start)
	// env: 0 before the shake, 0 on its first frame, full after the attack, then a quadratic fade to 0 at its end.
	env := fmt.Sprintf("if(between(%s,0,%d),min(%s/%d,1)*pow(1-%s/%d,2),0)", m, length, m, shakeAttackF, m, length)
	zoom := fmt.Sprintf("1+%s*%s", num(strength), env)
	k.zooms = append(k.zooms, zoom)

	axis := func(name string) string {
		// Each axis has its own 64-bit seed, a hash of the clip's id and the axis; four 16-bit draws use it up.
		h := fnv.New64a()
		_, _ = h.Write([]byte("cf-shake:" + cl.item.ID + ":" + name))
		seed := h.Sum64()
		next := func(lo, hi float64) float64 { // the next 16 bits of the seed, as a number in [lo, hi)
			v := float64(seed&0xffff) / 65536
			seed >>= 16
			return lo + v*(hi-lo)
		}
		w1 := 2 * math.Pi * next(11, 17) / float64(c.fps) // radians per frame, at 11 to 17 Hz and 19 to 27 Hz
		w2 := 2 * math.Pi * next(19, 27) / float64(c.fps)
		p1, p2 := next(0, 2*math.Pi), next(0, 2*math.Pi)
		return fmt.Sprintf("(sin(%s*%s+%s)+sin(%s*%s+%s))/2", num(round6(w1)), m, num(round6(p1)), num(round6(w2)), m, num(round6(p2)))
	}
	room := func(dim string) string { // the room the shake's own zoom leaves at each edge, times shakeRoom
		return fmt.Sprintf("%s*%s*(1-1/(%s))/2", num(shakeRoom), dim, zoom)
	}
	k.dx = fmt.Sprintf("%s*(%s)", room("W"), axis("x"))
	k.dy = fmt.Sprintf("%s*(%s)", room("H"), axis("y"))
	return nil
}
