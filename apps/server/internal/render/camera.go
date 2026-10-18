package render

import (
	"fmt"
	"hash/fnv"
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/subject"
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
	case timeline.MotionHandheld:
		c.addHandheld(k, cl, s, at)
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

// The living camera (P6-06): a tripod shot is given the motion of a hand-held one, so it never sits dead still. The zoom pushes slowly
// from HandheldBase over the base to the strength (smoothstep over the clip), the window drifts across the room the zoom leaves (from one
// side to the other, the direction alternating from one clip to the next) and sways slowly on both axes (sines of a fraction of a hertz,
// phases from a hash of the clip's id), and a punch-in of HandheldPunch lands at at_ms when it is given. The window stays inside the frame
// at every frame: the drift and the sway together use at most handheldDrift+handheldSway of the room the push leaves.
const (
	// HandheldBase is the zoom the living camera starts from, so it has room to drift from the first frame.
	HandheldBase = 0.03
	// HandheldPunch is the punch-in of a handheld clip at its at_ms.
	HandheldPunch = 0.06
	// handheldDrift and handheldSway are the shares of the horizontal room the drift crosses and the sway uses; handheldSwayY the share of
	// the vertical room the vertical sway uses.
	handheldDrift   = 0.55
	handheldSway    = 0.3
	handheldSwayY   = 0.4
	handheldSwayHz  = 0.35
	handheldSwayYHz = 0.23
	// handheldMargin is the room kept between the window and the top of the subject (the head, a raised hand or the lift) and the sides of
	// the head, as a fraction of the frame.
	handheldMargin = 0.02
)

func (c *compiler) addHandheld(k *camera, cl clip, strength float64, at int64) {
	last := max(cl.outFrames-1, 1)
	punch := 0.0
	if at >= 0 {
		punch = HandheldPunch
	}
	s, base, punch := c.handheldLimit(cl, strength, punch)
	p := progress("in", fmt.Sprintf("%d", last))
	push := fmt.Sprintf("1+%s+%s*%s", num(base), num(round6(s-base)), smooth(p))
	k.zooms = append(k.zooms, push)
	if punch > 0 {
		ramp := punchRampFrames(c.fps)
		start := max(0, at-ramp)
		if at == start {
			k.zooms = append(k.zooms, "1+"+num(punch))
		} else {
			k.zooms = append(k.zooms, fmt.Sprintf("1+%s*(1-pow(1-%s,3))", num(punch), progress(fmt.Sprintf("in-%d", start), fmt.Sprintf("%d", at-start))))
		}
	}
	dir := 1.0
	if cl.idx%2 == 1 {
		dir = -1
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte("cf-handheld:" + cl.item.ID))
	seed := h.Sum64()
	ph1 := float64(seed&0xffff) / 65536 * 2 * math.Pi
	ph2 := float64((seed>>16)&0xffff) / 65536 * 2 * math.Pi
	w1 := 2 * math.Pi * handheldSwayHz / float64(c.fps)
	w2 := 2 * math.Pi * handheldSwayYHz / float64(c.fps)
	roomX := fmt.Sprintf("W*(1-1/(%s))/2", push)
	roomY := fmt.Sprintf("H*(1-1/(%s))/2", push)
	k.dx = fmt.Sprintf("%s*(%s*(2*%s-1)+%s*sin(%s*in+%s))", roomX, num(dir*handheldDrift), p, num(handheldSway), num(round6(w1)), num(round6(ph1)))
	k.dy = fmt.Sprintf("%s*%s*sin(%s*in+%s)", roomY, num(handheldSwayY), num(round6(w2)), num(round6(ph2)))
}

// handheldLimit keeps the living camera off the subject: the push (and the punch-in) is limited so that the window, wherever the drift
// and the sway put it, never crops the top of the subject (the head, a raised hand, the lift) or the sides of the head, with
// handheldMargin to spare. Without a subject box the strength is as given. It returns the push, the base and the punch to use.
func (c *compiler) handheldLimit(cl clip, strength, punch float64) (s, base, p float64) {
	s, base, p = strength, HandheldBase, punch
	tr := cl.asset.Subject
	if tr == nil {
		return s, base, p
	}
	rf := timeline.Reframe{Mode: timeline.DefaultReframe}
	if cl.item.Reframe != nil {
		rf = *cl.item.Reframe
	}
	out := c.spec.Output
	w, hgt := float64(out.Width), float64(out.Height)
	src := cl.item.Source
	maxZ := math.Inf(1)
	// edge is the free share of the frame on one side, share how many times the room the push leaves the window may move toward it.
	limit := func(edge, share float64) {
		if edge <= handheldMargin {
			maxZ = math.Min(maxZ, 1)
			return
		}
		maxZ = math.Min(maxZ, 1/math.Max(1-2*(edge-handheldMargin)/share, 1e-6))
	}
	for _, sm := range tr.Near(src.InMs, src.OutMs) {
		bx0, by0, bx1, by1 := sm.Body()
		body := subject.MapBox(bx0, by0, bx1, by1, tr.Aspect, rf, out)
		hx0, hy0, hx1, hy1 := sm.Head()
		head := subject.MapBox(hx0, hy0, hx1, hy1, tr.Aspect, rf, out)
		top := math.Min(float64(body.Top), float64(head.Top)) / hgt
		limit(top, 1+handheldSwayY)
		limit(float64(head.Left)/w, 1+handheldDrift+handheldSway)
		limit(1-float64(head.Right)/w, 1+handheldDrift+handheldSway)
	}
	if math.IsInf(maxZ, 1) || (1+s)*(1+p) <= maxZ {
		return s, base, p
	}
	// Too close: the punch goes first, then the push, down to a breath of 1 % (a shot is never dead still, but the head stays whole).
	p = math.Max(0, maxZ/(1+s)-1)
	if p < 0.01 {
		p = 0
		s = math.Max(maxZ-1, 0.01)
	}
	base = math.Min(base, s/2)
	return round6(s), round6(base), round6(p)
}
