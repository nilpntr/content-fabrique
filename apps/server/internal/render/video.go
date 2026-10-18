package render

import (
	"fmt"
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// looks holds the fixed colour presets (design note: eq and curves settings per preset, no LUT files in v1). Every preset
// that uses curves works in planar RGB (format=gbrp) and converts back, so the filter's pixel format does not depend on
// what the ffmpeg version would pick.
var looks = map[string]string{
	"punchy": "eq=contrast=1.12:saturation=1.25:gamma=0.97",
	"warm":   "format=gbrp,curves=r='0/0 0.5/0.56 1/1':b='0/0 0.5/0.44 1/1',format=yuv420p,eq=saturation=1.05",
	"cool":   "format=gbrp,curves=r='0/0 0.5/0.45 1/1':b='0/0 0.5/0.56 1/1',format=yuv420p,eq=saturation=1.03",
	"bw":     "eq=saturation=0:contrast=1.1",
	"matte":  "format=gbrp,curves=all='0/0.07 1/0.93',format=yuv420p,eq=saturation=0.85:contrast=0.95",
	// auto (P3-10 part B) is a mild S-curve that keeps the black and white points where they are (a low-key frame, black wall and
	// lit subject, keeps its blacks), plus the clip's exposure_ev nudge (exposureFilter, applied for this preset whatever
	// look.match says). It never measures the picture: the compiler has no pass that could, and a mean-brightness measure would
	// wash out low-key footage anyway. The exposure hint comes from the analysis, from percentiles, and is bounded.
	"auto": "format=gbrp,curves=all='0/0 0.25/0.235 0.75/0.765 1/1',format=yuv420p",
	// gym_moody: warm skin (red up, blue down in the mids), a little less saturation, a gentle S in the upper half for contrast, and
	// a soft vignette with the filter's random dither off. Everything under about 0.15 is left where it is: the look is for gym
	// footage, which is shot low-key (a lit lifter in a dark room, p90 luma 0.10 to 0.33), and the first version of this preset, a
	// toe pulled from 0.12 to 0.07 plus eq contrast 1.08, took the rendered mean of such a shot from 17 to 5 of 255: the grade
	// crushed the footage it was made for. The preset darkens nothing that is already dark; its mood is the warm mids, the
	// contrast above the shadows and the corners.
	"gym_moody": "format=gbrp,curves=r='0/0 0.15/0.15 0.5/0.53 0.8/0.84 1/1':g='0/0 0.15/0.15 0.5/0.49 0.8/0.82 1/1':b='0/0 0.15/0.16 0.5/0.45 0.8/0.76 1/0.97',format=yuv420p,eq=saturation=0.92,vignette=angle=0.28:dither=0",
}

// The exposure nudge (look.match, and the preset auto): the hint is bounded by the spec to -1 to 1 stops, the applied correction
// to exposureMaxEV, and a hint under exposureDeadEV is left alone.
const (
	exposureMaxEV  = 0.7
	exposureDeadEV = 0.15
	// exposureKnee is where the gain turns into the shoulder, as a fraction of the luma range.
	exposureKnee = 0.5
)

// exposureFilter is the correction of a clip whose exposure hint is ev stops brighter than the target. It scales the luma by
// G = 2^-ev (ev limited to exposureMaxEV, so G is within 0.62 and 1.62) below the knee, which keeps the black point where it is
// (the 5th percentile of a low-key frame hardly moves), and above the knee runs a straight line from (knee, knee G) to the
// white point, which does not move either: highlights are preserved, not clipped. It works on 8-bit planar YUV in limited
// range, which the clip is converted to in front of it, and leaves chroma alone. It returns nothing for a hint too small to
// matter.
func exposureFilter(ev float64) []string {
	if math.Abs(ev) < exposureDeadEV {
		return nil
	}
	ev = math.Max(-exposureMaxEV, math.Min(exposureMaxEV, ev))
	g := math.Exp2(-ev)
	knee := exposureKnee
	top := knee * g                 // where the knee lands
	slope := (1 - top) / (1 - knee) // the shoulder to the white point
	x := "((val-16)/219)"
	return []string{"format=yuv420p", fmt.Sprintf("lutyuv=y='clip(16+219*if(lt(%s,%s),%s*%s,%s+%s*(%s-%s)),16,235)'",
		x, num(knee), x, num(round6(g)), num(round6(top)), num(round6(slope)), x, num(knee))}
}

// xfadeName maps a transition type to the xfade transition that renders it, from a fixed list: nothing from the spec is
// written into the graph. A whip is a slide, with a blur added by the caller (whip is the old name of whip_left). A
// zoom_through and a punch are plain fades between two clips that the camera has zoomed (camera.go), with a blur added by the caller. A
// flash is a dip to white.
func xfadeName(transition string) string {
	switch transition {
	case timeline.TransitionCrossfade:
		return "fade"
	case timeline.TransitionDipToBlack:
		return "fadeblack"
	case timeline.TransitionWhip, timeline.TransitionWhipLeft:
		return "slideleft"
	case timeline.TransitionWhipRight:
		return "slideright"
	case timeline.TransitionZoomThrough, timeline.TransitionPunch:
		return "fade"
	case timeline.TransitionFlash:
		return "fadewhite"
	}
	return ""
}

// blurred says whether a transition rides a motion-blur trail: the whips, which slide, the zoom_through and the punch.
func blurred(transition string) bool {
	switch transition {
	case timeline.TransitionWhip, timeline.TransitionWhipLeft, timeline.TransitionWhipRight, timeline.TransitionZoomThrough, timeline.TransitionPunch:
		return true
	}
	return false
}

// blurTrail is the motion blur of a transition (P6-06): three blurs stacked over nested windows of the transition (the whole of it, its
// middle half and its middle quarter), so the blur grows to a peak at the middle frame, where the picture switches, and falls off again;
// the frames before and after the transition are sharp (every window is end-exclusive). A whip slides, so its blur is directional and
// horizontal (dblur at 0 degrees); a zoom_through and a punch zoom, so theirs is a Gaussian blur. The strengths are fractions of the short
// side of the frame, so a blur looks the same in every format.
func (c *compiler) blurTrail(cl clip) []string {
	short := float64(min(c.w, c.h))
	n := cl.overlapFrames
	start := cl.startFrame
	type window struct{ from, to int64 }
	q := max(n/4, 0)
	wins := []window{
		{start, start + n},
		{start + n/4, start + n - n/4},
		{start + n/2 - max(q/2, 0), start + n/2 + max(q/2, 0) + 1},
	}
	var out []string
	for k, w := range wins {
		if w.to <= w.from {
			continue
		}
		enable := fmt.Sprintf("enable='gte(t,%s)*lt(t,%s)'", c.frameSecs(w.from), c.frameSecs(w.to))
		switch cl.transition {
		case timeline.TransitionZoomThrough, timeline.TransitionPunch:
			sigma := max(1, int(math.Round(short/blurSigmaDiv[k])))
			out = append(out, fmt.Sprintf("gblur=sigma=%d:%s", sigma, enable))
		default:
			radius := max(2, int(math.Round(short/blurRadiusDiv[k])))
			out = append(out, fmt.Sprintf("dblur=angle=0:radius=%d:%s", radius, enable))
		}
	}
	return out
}

// The blur of each window of a trail: the short side of the frame over these (a 1080 px side: sigma 9, 15 and 24; a directional radius of
// 22, 36 and 60 px).
var (
	blurSigmaDiv  = [3]float64{120, 72, 45}
	blurRadiusDiv = [3]float64{50, 30, 18}
)

// videoGraph returns the statements of the video part of the filter_complex. Its output pad is [vout]: the joined clips, with
// the ass filter after the join when there is something to draw (text, captions, the preview watermark).
func (c *compiler) videoGraph(subs *Subtitles) ([]string, error) {
	var stmts []string
	for _, cl := range c.clips {
		s, err := c.clipGraph(cl)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, s...)
	}
	if subs == nil && c.plan.Motion == nil {
		return append(stmts, c.joinGraph("vout")...), nil
	}
	stmts = append(stmts, c.joinGraph("vj")...)
	cur := "vj"
	if c.plan.Motion != nil { // the motion-graphics overlays, under the text (graphics.go)
		next := "vout"
		if subs != nil {
			next = "vg"
		}
		stmts, cur = append(stmts, c.overlayGraph(cur, next)...), next
	}
	if subs == nil {
		return stmts, nil
	}
	return append(stmts, chain([]string{cur}, []string{subs.filter()}, "vout")), nil
}

// clipGraph is the graph of one clip, ending in the pad [v<idx>]: trim on the source, setpts to restart the clock,
// setpts for the speed, fps, exactly outFrames frames, reframe, look, format and timebase.
func (c *compiler) clipGraph(cl clip) ([]string, error) {
	it := cl.item
	speed := it.SpeedOrDefault()
	src := []string{
		"trim=end=" + secs(it.Source.OutMs-it.Source.InMs),
		"setpts=PTS-STARTPTS",
	}
	switch {
	case it.HasRamp():
		expr, err := rampSetpts(it.SpeedRamp)
		if err != nil {
			return nil, fmt.Errorf("%w: clip %s: %w", ErrInvalidSpec, it.ID, err)
		}
		src = append(src, "setpts='"+expr+"'")
	case speed != 1:
		src = append(src, "setpts=PTS/"+num(speed))
	}
	// tpad repeats the last frame without limit and trim cuts exactly outFrames, so a source that falls a frame short
	// of the snapped duration (rounding at a slow speed, a damaged file) never makes the join drift.
	src = append(src,
		fmt.Sprintf("fps=%d", c.fps),
		"tpad=stop=-1:stop_mode=clone",
		fmt.Sprintf("trim=end_frame=%d", cl.outFrames),
		"setpts=PTS-STARTPTS",
	)

	reframe := timeline.Reframe{Mode: timeline.DefaultReframe}
	if it.Reframe != nil {
		reframe = *it.Reframe
	}
	look := timeline.LookNone
	if it.Look != nil && it.Look.Preset != "" {
		look = it.Look.Preset
	}
	var lookFilter string
	if look != timeline.LookNone {
		f, ok := looks[look]
		if !ok {
			return nil, fmt.Errorf("%w: clip %s has unknown look %q", ErrInvalidSpec, it.ID, look)
		}
		lookFilter = f
	}

	tail := []string{}
	if z := reframe.ZoomOrDefault(); z != 1 {
		cw, ch := even(int(float64(c.w)/z)), even(int(float64(c.h)/z))
		tail = append(tail, fmt.Sprintf("crop=%d:%d", cw, ch), fmt.Sprintf("scale=%d:%d:flags=bicubic", c.w, c.h))
	}
	// The camera moves the already-reframed frame (and the zoom above it), before the look.
	cam, err := c.cameraFor(cl)
	if err != nil {
		return nil, err
	}
	tail = append(tail, cam.filters()...)
	// look.match, and the preset auto: correct the clip's exposure by its hint, before the preset.
	if it.Look != nil && it.Look.AppliesExposure() && it.ExposureEv != nil {
		ev := *it.ExposureEv
		if math.IsNaN(ev) || ev < -1 || ev > 1 {
			return nil, fmt.Errorf("%w: clip %s has exposure_ev %s outside -1 to 1", ErrInvalidSpec, it.ID, num(ev))
		}
		tail = append(tail, exposureFilter(ev)...)
	}
	if lookFilter != "" {
		tail = append(tail, lookFilter)
	}
	// fps gives the stream a constant frame rate that xfade and concat can read from the link.
	tail = append(tail, "format=yuv420p", "setsar=1", fmt.Sprintf("fps=%d", c.fps), "settb=AVTB")

	in := fmt.Sprintf("%d:v", cl.idx)
	out := fmt.Sprintf("v%d", cl.idx)
	w, h := c.w, c.h

	switch reframe.Mode {
	case timeline.ReframeCover:
		focus := reframe.FocusOrDefault()
		// Scale to fill, then crop around the focus point. The window is clamped so that it stays inside the frame.
		f := append(src,
			fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase:force_divisible_by=2:flags=bicubic", w, h),
			fmt.Sprintf("crop=%d:%d:x='clip(%s*iw-%d,0,iw-%d)':y='clip(%s*ih-%d,0,ih-%d)'", w, h, num(focus.X), w/2, w, num(focus.Y), h/2, h),
			"setsar=1",
		)
		return []string{chain([]string{in}, append(f, tail...), out)}, nil

	case timeline.ReframeContain:
		bg, err := padColor(c.spec.Output.Background)
		if err != nil {
			return nil, err
		}
		f := append(src,
			fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2:flags=bicubic", w, h),
			fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s", w, h, bg),
			"setsar=1",
		)
		return []string{chain([]string{in}, append(f, tail...), out)}, nil

	case timeline.ReframeBlurFill:
		// A blurred, scaled copy behind a contain. The blur runs on a copy shrunk to a twelfth of the frame, which is
		// cheap and, scaled back up, as smooth as a large-radius blur.
		sw, sh := even(w/12), even(h/12)
		a, b, bg := fmt.Sprintf("b%da", cl.idx), fmt.Sprintf("b%db", cl.idx), fmt.Sprintf("b%dbg", cl.idx)
		fg := fmt.Sprintf("b%dfg", cl.idx)
		stmts := []string{
			chain([]string{in}, append(src, "setsar=1", "split=2"), a) + "[" + b + "]",
			chain([]string{b}, []string{
				fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase:force_divisible_by=2:flags=bicubic", w, h),
				fmt.Sprintf("crop=%d:%d", w, h),
				fmt.Sprintf("scale=%d:%d:flags=bicubic", sw, sh),
				"gblur=sigma=2",
				fmt.Sprintf("scale=%d:%d:flags=bicubic", w, h),
			}, bg),
			chain([]string{a}, []string{
				fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2:flags=bicubic", w, h),
			}, fg),
			chain([]string{bg, fg}, append([]string{"overlay=(main_w-overlay_w)/2:(main_h-overlay_h)/2"}, tail...), out),
		}
		return stmts, nil
	}
	return nil, fmt.Errorf("%w: clip %s has unknown reframe mode %q", ErrInvalidSpec, it.ID, reframe.Mode)
}

// joinGraph joins the clips into [vout]: a plain concat when every join is a cut, otherwise a chain that adds one clip
// at a time, with xfade for a transition and concat for a cut. xfade offsets come from the snapped cut list.
func (c *compiler) joinGraph(final string) []string {
	n := len(c.clips)
	if n == 1 {
		return []string{"[v0]null[" + final + "]"}
	}
	allCuts := true
	for _, cl := range c.clips[1:] {
		if cl.transition != timeline.TransitionCut {
			allCuts = false
		}
	}
	if allCuts {
		var in strings.Builder
		for i := range c.clips {
			fmt.Fprintf(&in, "[v%d]", i)
		}
		return []string{fmt.Sprintf("%sconcat=n=%d:v=1:a=0[%s]", in.String(), n, final)}
	}

	var stmts []string
	acc := "v0"
	for i := 1; i < n; i++ {
		cl := c.clips[i]
		out := fmt.Sprintf("j%d", i)
		if i == n-1 {
			out = final
		}
		pair := []string{acc, fmt.Sprintf("v%d", i)}
		if cl.transition == timeline.TransitionCut {
			stmts = append(stmts, chain(pair, []string{"concat=n=2:v=1:a=0"}, out))
			acc = out
			continue
		}
		offset := c.frameSecs(cl.startFrame)
		dur := c.frameSecs(cl.overlapFrames)
		xf := fmt.Sprintf("xfade=transition=%s:duration=%s:offset=%s", xfadeName(cl.transition), dur, offset)
		if !blurred(cl.transition) {
			stmts = append(stmts, chain(pair, []string{xf}, out))
			acc = out
			continue
		}
		// A whip, a zoom_through or a punch: the transition rides a motion-blur trail that peaks where the picture switches.
		mid := fmt.Sprintf("w%d", i)
		stmts = append(stmts,
			chain(pair, []string{xf}, mid),
			chain([]string{mid}, c.blurTrail(cl), out),
		)
		acc = out
	}
	return stmts
}

// padColor turns the spec's #rrggbb background into the 0xRRGGBB form ffmpeg reads.
func padColor(hex string) (string, error) {
	if len(hex) != 7 || hex[0] != '#' {
		return "", fmt.Errorf("%w: background %q is not #rrggbb", ErrInvalidSpec, hex)
	}
	for _, r := range hex[1:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return "", fmt.Errorf("%w: background %q is not #rrggbb", ErrInvalidSpec, hex)
		}
	}
	return "0x" + strings.ToUpper(hex[1:]), nil
}
