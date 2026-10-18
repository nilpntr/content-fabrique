package render

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The motion-blur trail of a transition (P6-06), measured on the rendered frames: the picture is sharpest away from the transition, the
// blur grows to its peak at the middle frame (where the picture switches) and the frames before and after the transition are as sharp as
// the footage. Sharpness is the mean absolute difference of horizontally adjacent pixels of a grey copy of the frame.
func TestTheBlurTrailPeaksAtTheCutAndLeavesTheNeighboursSharp(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	src := filepath.Join(t.TempDir(), "pattern.mp4")
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", //nolint:gosec // test helper
		"testsrc2=s=720x1280:r=30:d=4", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "12", "-pix_fmt", "yuv420p", src)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, b)
	}
	resolver := ResolverFunc(func(string, Variant) (ResolvedAsset, error) { return ResolvedAsset{Location: src}, nil })
	for _, typ := range []string{timeline.TransitionWhipLeft, timeline.TransitionZoomThrough, timeline.TransitionPunch} {
		t.Run(typ, func(t *testing.T) {
			// The transition runs from frame 51 (1.7 s) for 9 frames (300 ms): its middle is frame 55.
			spec := specOf(30, []timeline.VideoItem{
				clipAt("c1", assetA, 0, 2000, 0),
				clipAt("c2", assetB, 1000, 3000, 1700, withTransition(typ, 300)),
			})
			out, plan := renderSpec(t, spec, Preview, resolver)
			w, h := plan.OutputSize.Width, plan.OutputSize.Height
			raw := trailFrames(t, ffmpeg, out, w, h)
			sharp := func(f int) float64 {
				px := raw[f*w*h : (f+1)*w*h]
				var s float64
				for y := range h {
					for x := 1; x < w; x++ {
						d := int(px[y*w+x]) - int(px[y*w+x-1])
						if d < 0 {
							d = -d
						}
						s += float64(d)
					}
				}
				return s / float64(h*(w-1))
			}
			before, peak, after := sharp(49), sharp(55), sharp(62)
			edgeIn, nearIn := sharp(51), sharp(53)
			t.Logf("%s: sharpness before %.2f, first frame %.2f, near %.2f, middle %.2f, after %.2f", typ, before, edgeIn, nearIn, peak, after)
			if peak > 0.7*before || peak > 0.7*after {
				t.Errorf("the middle frame is not much blurrier than its neighbours: %.2f against %.2f and %.2f", peak, before, after)
			}
			if !(edgeIn > peak && nearIn > peak) {
				t.Errorf("the blur does not grow toward the middle: %.2f, %.2f, %.2f", edgeIn, nearIn, peak)
			}
			if typ != timeline.TransitionWhipLeft { // a whip slides, so the frame after it is the incoming clip still settling; the zooms land
				if before < 0.9*sharp(40) {
					t.Errorf("the frame before the transition is blurred: %.2f against %.2f", before, sharp(40))
				}
			}
		})
	}
}

// trailFrames decodes the video to w x h grey frames.
func trailFrames(t *testing.T, ffmpeg, file string, w, h int) []byte {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-i", file, //nolint:gosec // test helper
		"-vf", fmt.Sprintf("scale=%d:%d:flags=area,format=gray", w, h), "-f", "rawvideo", "-")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return out
}
