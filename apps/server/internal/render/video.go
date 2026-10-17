package render

import (
	"fmt"
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
}

// xfadeName maps a transition type to the xfade transition that renders it. A whip is a slideleft, with a blur added
// by the caller.
func xfadeName(transition string) string {
	switch transition {
	case timeline.TransitionCrossfade:
		return "fade"
	case timeline.TransitionDipToBlack:
		return "fadeblack"
	case timeline.TransitionWhip:
		return "slideleft"
	}
	return ""
}

// videoGraph returns the statements of the video part of the filter_complex. Its output pad is [vout].
func (c *compiler) videoGraph() ([]string, error) {
	var stmts []string
	for _, cl := range c.clips {
		s, err := c.clipGraph(cl)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, s...)
	}
	return append(stmts, c.joinGraph()...), nil
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
	if speed != 1 {
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
func (c *compiler) joinGraph() []string {
	n := len(c.clips)
	if n == 1 {
		return []string{"[v0]null[vout]"}
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
		return []string{fmt.Sprintf("%sconcat=n=%d:v=1:a=0[vout]", in.String(), n)}
	}

	var stmts []string
	acc := "v0"
	for i := 1; i < n; i++ {
		cl := c.clips[i]
		out := fmt.Sprintf("j%d", i)
		if i == n-1 {
			out = "vout"
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
		if cl.transition != timeline.TransitionWhip {
			stmts = append(stmts, chain(pair, []string{xf}, out))
			acc = out
			continue
		}
		// A whip: a slide with a short blur while it lasts.
		mid := fmt.Sprintf("w%d", i)
		sigma := max(2, min(c.w, c.h)/90)
		stmts = append(stmts,
			chain(pair, []string{xf}, mid),
			chain([]string{mid}, []string{
				// End-exclusive: the first frame after the transition is not blurred.
				fmt.Sprintf("gblur=sigma=%d:enable='gte(t,%s)*lt(t,%s)'", sigma, offset, c.frameSecs(cl.startFrame+cl.overlapFrames)),
			}, out),
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
