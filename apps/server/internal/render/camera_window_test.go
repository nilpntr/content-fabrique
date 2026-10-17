package render

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

var (
	perspectiveRe = regexp.MustCompile(`perspective=.*?:y3='[^']*'`)
	coordRe       = regexp.MustCompile(`:([xy][0-3])='([^']*)'`)
	inVarRe       = regexp.MustCompile(`\bin\b`)
	wVarRe        = regexp.MustCompile(`\bW\b`)
	hVarRe        = regexp.MustCompile(`\bH\b`)
)

// evalWindows evaluates the eight corner expressions of one perspective filter for frames 0 to frames-1 with ffmpeg's own
// expression evaluator (aevalsrc: one sample a frame, `n` for the frame number), for a frame of w x h pixels. It returns the
// corners of each frame as x0, y0, x1, y1, x2, y2, x3, y3.
func evalWindows(t *testing.T, ffmpeg, filter string, w, h, frames, fps int) [][8]float64 {
	t.Helper()
	exprs := map[string]string{}
	for _, m := range coordRe.FindAllStringSubmatch(filter, -1) {
		e := inVarRe.ReplaceAllString(m[2], "n")
		e = wVarRe.ReplaceAllString(e, strconv.Itoa(w))
		e = hVarRe.ReplaceAllString(e, strconv.Itoa(h))
		exprs[m[1]] = e
	}
	var list []string
	for _, k := range []string{"x0", "y0", "x1", "y1", "x2", "y2", "x3", "y3"} {
		e, ok := exprs[k]
		if !ok {
			t.Fatalf("no %s in %s", k, filter)
		}
		list = append(list, e)
	}
	// d is a little over the needed length (the source has no sample count), and the output is cut to the frames wanted.
	src := fmt.Sprintf("aevalsrc=exprs='%s':s=%d:d=%d", strings.Join(list, "|"), fps, frames/fps+2)
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", src, //nolint:gosec // test helper
		"-c:a", "pcm_f64le", "-f", "f64le", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("evaluate %s: %v\n%s", src, err, stderr.String())
	}
	data := stdout.Bytes()
	if len(data) < frames*8*8 {
		t.Fatalf("got %d bytes for %d frames of 8 values: %s", len(data), frames, stderr.String())
	}
	out := make([][8]float64, frames)
	for f := range out {
		for c := range 8 {
			out[f][c] = math.Float64frombits(binary.LittleEndian.Uint64(data[(f*8+c)*8:]))
		}
	}
	return out
}

// TestCameraWindowStaysInsideTheFrame evaluates the corner expressions the compiler wrote, for every preset at its weakest and
// strongest, at every frame rate, for several clip ids (the shake pattern comes from the id) and with zoom_through on both
// sides of a clip, and requires that at every frame the window is an axis-aligned rectangle inside the frame, with the
// frame's aspect ratio: x from 0 to W and y from 0 to H. A window inside the frame is what "no border, ever" means for a
// camera that samples a window of the frame, and this checks the guarantee itself; the render test checks the pixels.
func TestCameraWindowStaysInsideTheFrame(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	const w, h = 1080, 1920
	const eps = 1e-3
	check := func(name string, items []timeline.VideoItem, fps int) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			toolSem <- struct{}{}
			defer func() { <-toolSem }()
			spec := specOf(fps, items)
			plan, err := Compile(spec, Final, plainResolver())
			if err != nil {
				t.Fatal(err)
			}
			filters := perspectiveRe.FindAllString(plan.FilterComplex, -1)
			if len(filters) == 0 {
				t.Fatal("no camera in the graph")
			}
			for _, f := range filters {
				for n, win := range evalWindows(t, ffmpeg, f, w, h, 130, fps) {
					x0, y0, x1, y1, x2, y2, x3, y3 := win[0], win[1], win[2], win[3], win[4], win[5], win[6], win[7]
					if math.Abs(x0-x2) > eps || math.Abs(x1-x3) > eps || math.Abs(y0-y1) > eps || math.Abs(y2-y3) > eps {
						t.Fatalf("frame %d: the window is not a rectangle: %v", n, win)
					}
					if x0 < -eps || y0 < -eps || x1 > w+eps || y2 > h+eps || x1-x0 <= 0 || y2-y0 <= 0 {
						t.Fatalf("frame %d: the window x %.3f to %.3f, y %.3f to %.3f leaves the frame of %dx%d", n, x0, x1, y0, y2, w, h)
					}
					// The zoom is the frame over the window, the same across and down.
					across, down := w/(x1-x0), h/(y2-y0)
					if math.Abs(across-down) > 1e-3 || across < 1-1e-6 || across > 2.5 {
						t.Fatalf("frame %d: zoom %.5f across and %.5f down", n, across, down)
					}
				}
			}
		})
	}
	for _, fps := range []int{24, 25, 30, 60} {
		for _, s := range []float64{0.05, 0.12, 0.35} {
			for _, preset := range []string{timeline.MotionPunchIn, timeline.MotionPushIn, timeline.MotionPullBack, timeline.MotionShake} {
				for _, id := range []string{"c1", "a", "shaky_clip", "z9"} {
					motion := timeline.Motion{Preset: preset, Strength: ptr(s)}
					check(fmt.Sprintf("%dfps/%s/%v/%s", fps, preset, s, id), []timeline.VideoItem{clipAt(id, assetA, 0, 2000, 0, withMotion(motion))}, fps)
				}
			}
		}
		// A shake in the middle of a clip that zoom_through comes into and goes out of.
		items := []timeline.VideoItem{
			clipAt("c1", assetA, 0, 2000, 0),
			clipAt("c2", assetA, 0, 2000, 1700, withTransition(timeline.TransitionZoomThrough, 300), withMotion(timeline.Motion{Preset: timeline.MotionShake, Strength: ptr(0.35), AtMs: ptr(int64(1200))})),
			clipAt("c3", assetA, 0, 2000, 3400, withTransition(timeline.TransitionZoomThrough, 300)),
		}
		check(fmt.Sprintf("%dfps/zoom_through both sides", fps), items, fps)
	}
}
