package render

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Render tests of the P3-10 features that the golden frames cannot pin by themselves: that a camera move never shows a
// border, and that a speed ramp shows the source frames the time map says it shows.

// genSource makes a test clip from lavfi source args (the -i part), 640x360 at 30 fps, no audio.
func genSource(t *testing.T, ffmpeg, name, lavfi string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name+".mp4")
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", lavfi,
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "10", "-g", "15", "-pix_fmt", "yuv420p", "-f", "mp4", out,
	}
	cmd := exec.CommandContext(context.Background(), ffmpeg, args...) //nolint:gosec // test helper with fixed arguments
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generate %s: %v\n%s", name, err, stderr.String())
	}
	return out
}

func oneFileResolver(path string) AssetResolver {
	return ResolverFunc(func(string, Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: path}, nil
	})
}

// minLuma is the darkest pixel (luma, 0 to 255) of any frame of a video.
func minLuma(t *testing.T, ffmpeg, video string) int {
	t.Helper()
	stats := filepath.Join(t.TempDir(), "stats.txt")
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-i", video, //nolint:gosec // test helper
		"-vf", "signalstats,metadata=mode=print:key=lavfi.signalstats.YMIN:file="+stats, "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("signalstats: %v\n%s", err, stderr.String())
	}
	data, err := os.ReadFile(stats) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	lowest, n := 255, 0
	for _, line := range strings.Split(string(data), "\n") {
		if _, v, ok := strings.Cut(line, "lavfi.signalstats.YMIN="); ok {
			y, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				t.Fatal(err)
			}
			lowest = min(lowest, y)
			n++
		}
	}
	if n == 0 {
		t.Fatalf("no frame statistics in %q", data)
	}
	return lowest
}

func renderSpec(t *testing.T, spec *timeline.Spec, prof Profile, resolver AssetResolver) (string, Plan) {
	t.Helper()
	plan, err := Compile(spec, prof, resolver)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.mp4")
	if err := Execute(context.Background(), plan, testRunner(t), ExecuteOptions{Output: out}); err != nil {
		t.Fatal(err)
	}
	// CF_RENDER_KEEP=<dir> keeps every rendered MP4, for looking at a failing frame.
	if keep := os.Getenv("CF_RENDER_KEEP"); keep != "" {
		data, err := os.ReadFile(out) //nolint:gosec // test file
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(keep, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(keep, strings.ReplaceAll(t.Name(), "/", "_")+".mp4"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return out, plan
}

// The slide transitions (whip_left, whip_right) are left out of this test on purpose: on the first frame of an xfade slide,
// ffmpeg 7.1 writes black into the two bottom-left pixels whatever the inputs are (found while writing this test; the whip
// has always had it, and it is not the camera's). The goldens cover the whips.
//
// TestCameraNeverShowsABorder renders every motion preset at its strongest, and the fade-based new transitions, over a bright flat source
// in every format and every reframe mode, and requires every pixel of every frame to be bright: no black from the filter, the format
// conversion or the interpolation at the edges (that the window stays inside the frame is TestCameraWindowStaysInsideTheFrame).
// The background is white, so the padding of contain is bright too; a control with a black background
// proves the check can see black.
func TestCameraNeverShowsABorder(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, cat := ensureMedia(t)
	_ = cat
	// The preview profile without its watermark, whose outline is dark.
	bare := Preview
	bare.Watermark = false
	landscape := genSource(t, ffmpeg, "landscape", "color=c=yellow:s=640x360:r=30:d=4")
	portrait := genSource(t, ffmpeg, "portrait", "color=c=yellow:s=360x640:r=30:d=4")

	items := func(bg string) []timeline.VideoItem {
		max35 := 0.35
		mk := func(id string, at int64, motion timeline.Motion, tr *timeline.Transition) timeline.VideoItem {
			return timeline.VideoItem{
				ID: id, AssetID: assetA, Source: timeline.Source{InMs: 500, OutMs: 1700}, AtMs: at, Motion: &motion, TransitionIn: tr,
			}
		}
		_ = bg
		return []timeline.VideoItem{
			mk("c1", 0, timeline.Motion{Preset: timeline.MotionPunchIn, Strength: &max35, AtMs: ptr(int64(300))}, nil),
			mk("c2", 900, timeline.Motion{Preset: timeline.MotionPushIn, Strength: &max35}, &timeline.Transition{Type: timeline.TransitionZoomThrough, DurationMs: 300}),
			mk("c3", 1800, timeline.Motion{Preset: timeline.MotionPullBack, Strength: &max35}, &timeline.Transition{Type: timeline.TransitionFlash, DurationMs: 300}),
			mk("c4", 2700, timeline.Motion{Preset: timeline.MotionShake, Strength: &max35, AtMs: ptr(int64(100))}, &timeline.Transition{Type: timeline.TransitionCrossfade, DurationMs: 300}),
			mk("c5", 3600, timeline.Motion{Preset: timeline.MotionShake, Strength: ptr(0.05)}, &timeline.Transition{Type: timeline.TransitionZoomThrough, DurationMs: 300}),
		}
	}
	type format struct {
		f    timeline.Format
		fps  int
		src  string
		name string
	}
	formats := []format{
		{timeline.Format9x16, 30, portrait, "9:16"},
		{timeline.Format1x1, 25, landscape, "1:1"},
		{timeline.Format4x5, 24, portrait, "4:5"},
		{timeline.Format16x9, 60, landscape, "16:9"},
	}
	modes := []string{timeline.ReframeCover, timeline.ReframeContain, timeline.ReframeBlurFill}

	build := func(f format, mode, bg string, reframeZoom float64) *timeline.Spec {
		w, h, _ := f.f.Size()
		its := items(bg)
		for i := range its {
			its[i].Reframe = &timeline.Reframe{Mode: mode, Zoom: ptr(reframeZoom)}
		}
		return &timeline.Spec{
			Schema: timeline.SchemaVersion,
			Output: timeline.Output{Format: f.f, Width: w, Height: h, FPS: f.fps, Background: bg},
			Tracks: []timeline.Track{&timeline.VideoTrack{ID: timeline.MainTrackID, Items: its}},
		}
	}

	for _, f := range formats {
		for _, mode := range modes {
			t.Run(f.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				toolSem <- struct{}{}
				defer func() { <-toolSem }()
				for _, z := range []float64{1, 2.5} { // the reframe zoom is under the camera: they compose
					spec := build(f, mode, "#ffffff", z)
					if res := timeline.Validate(mustMarshal(t, spec), testAssets(t)); !res.OK() {
						t.Fatalf("the test spec is not valid: %+v", res.Issues)
					}
					out, _ := renderSpec(t, spec, bare, oneFileResolver(f.src))
					// The yellow source is luma 210 and the white padding 235 (limited range): anything under 150 is black.
					if y := minLuma(t, ffmpeg, out); y < 150 {
						t.Errorf("reframe zoom %v: the darkest pixel of the render has luma %d: a border", z, y)
					}
				}
			})
		}
	}

	t.Run("control: the check sees the black padding of contain", func(t *testing.T) {
		spec := build(formats[0], timeline.ReframeContain, "#000000", 1)
		out, _ := renderSpec(t, spec, bare, oneFileResolver(landscape))
		if y := minLuma(t, ffmpeg, out); y >= 100 {
			t.Fatalf("a contain over a black background should show black, the darkest pixel is %d", y)
		}
	})
}

// timeCodedSource is a 16 s clip whose top half is bright by the second (16 + 14 per second of source time) and whose
// bottom half by the fraction of the second (16 + 200 per second, wrapping), so that the source time of a frame can be read
// back from the pixels to within a few milliseconds.
const timeCodedSource = "color=c=black:s=640x360:r=30:d=16,format=yuv420p,geq=lum='if(lt(Y,180),16+14*T,16+200*mod(T,1))':cb=128:cr=128"

// sourceSeconds reads the source time (seconds) off a frame of a render of the time-coded clip, from the luma of the middle of
// its top and bottom halves.
func sourceSeconds(frame []byte, w, h int) float64 {
	mean := func(y0, y1, x0, x1 int) float64 {
		sum, n := 0.0, 0
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				sum += float64(frame[y*w+x])
				n++
			}
		}
		return sum / float64(n)
	}
	coarse := (mean(h*25/100, h*40/100, w*25/100, w*60/100) - 16) / 14
	frac := (mean(h*60/100, h*90/100, w*25/100, w*60/100) - 16) / 200
	return math.Round(coarse-frac) + frac
}

// grayFrames decodes a video to 8-bit grey frames.
func grayFrames(t *testing.T, ffmpeg, video string, w, h int) [][]byte {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-i", video, //nolint:gosec // test helper
		"-vf", "extractplanes=y", "-f", "rawvideo", "-pix_fmt", "gray", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("decode: %v\n%s", err, stderr.String())
	}
	data := stdout.Bytes()
	size := w * h
	var frames [][]byte
	for len(data) >= size {
		frames = append(frames, data[:size])
		data = data[size:]
	}
	return frames
}

// TestSpeedRampFramesFollowTheTimeMap renders clips with speed ramps from a source that carries its own time and checks, for
// every output frame, that the source time on the frame is the one the ramp's maths, the plan's time map (what renderjobs
// stores) and the frame snapping say, to within a source frame. Each clip follows a clip of normal speed and precedes one of
// 2x, so the ramp's offsets and the cuts are tested too. A control shows that the check can tell a ramp from its average speed.
func TestSpeedRampFramesFollowTheTimeMap(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	src := genSource(t, ffmpeg, "timecoded", timeCodedSource)
	resolver := oneFileResolver(src)

	ramps := map[string][]timeline.SpeedPoint{
		"fast then slow":    {{AtMs: 0, Speed: 3}, {AtMs: 1500, Speed: 3}, {AtMs: 2200, Speed: 0.4}},
		"slow then fast":    {{AtMs: 0, Speed: 0.5}, {AtMs: 1200, Speed: 2}},
		"late first point":  {{AtMs: 600, Speed: 2}, {AtMs: 1800, Speed: 0.6}, {AtMs: 2400, Speed: 1.5}},
		"gentle and linear": {{AtMs: 0, Speed: 1}, {AtMs: 3000, Speed: 1.8}},
	}
	for name, pts := range ramps {
		for _, fps := range []int{30, 25, 24} {
			t.Run(fmt.Sprintf("%s/%dfps", name, fps), func(t *testing.T) {
				t.Parallel()
				toolSem <- struct{}{}
				defer func() { <-toolSem }()

				c2 := clipAt("c2", assetA, 2500, 5500, 0, withRamp(pts...))
				c1 := clipAt("c1", assetA, 1000, 2000, 0)
				c2.AtMs = timeline.EndMs(c1)
				c3 := clipAt("c3", assetA, 7000, 8400, timeline.EndMs(c2), withSpeed(2))
				spec := specOf(fps, []timeline.VideoItem{c1, c2, c3})
				if res := timeline.Validate(mustMarshal(t, spec), testAssets(t)); !res.OK() {
					t.Fatalf("not valid: %+v", res.Issues)
				}
				out, plan := renderSpec(t, spec, Preview, resolver)

				w, h := plan.OutputSize.Width, plan.OutputSize.Height
				frames := grayFrames(t, ffmpeg, out, w, h)
				if int64(len(frames)) != plan.TotalFrames {
					t.Fatalf("the render has %d frames, the plan says %d", len(frames), plan.TotalFrames)
				}
				if want := timeline.TotalDuration(spec); math.Abs(float64(plan.DurationMs-want)) > 1000/float64(fps) {
					t.Errorf("the plan is %d ms long, the spec's total is %d ms", plan.DurationMs, want)
				}

				// What the readout can differ by: the coding error of the pixels, the 30 fps source (a frame shows the nearest source frame),
				// and the fps filter, which gives output frame k the source frame whose rounded time is k, so up to half an output
				// frame of source time at the speed of the moment (the same for a constant speed, which this test does not change).
				const base = 0.012 + 1.0/30
				var worst, sum float64
				var avgWorst float64
				for k, fr := range frames {
					startMs := timeline.FrameStartMs(int64(k), fps)
					entry, mapped, ok := plan.SourceTimeMs(startMs)
					if !ok {
						t.Fatalf("frame %d (%d ms) is in no time map entry", k, startMs)
					}
					measured := sourceSeconds(fr, w, h)

					// What the maths says the frame shows: the clip's own offset at the frame's time.
					var item timeline.VideoItem
					main, _, _ := timeline.MainTrack(spec)
					for _, it := range main.Items {
						if it.ID == entry.ItemID {
							item = it
						}
					}
					// The clip's first frame is at Frame(at_ms), not at at_ms: boundaries are snapped to the frame grid.
					offset := float64(startMs - timeline.FrameStartMs(timeline.Frame(item.AtMs, fps), fps))
					exact := float64(item.Source.InMs)/1000 + item.SourceOffsetMs(offset)/1000
					// The speed at that moment, in source seconds per output second.
					speed := (item.SourceOffsetMs(offset+20) - item.SourceOffsetMs(math.Max(offset-20, 0))) / (offset + 20 - math.Max(offset-20, 0))
					tolerance := base + 0.5*speed/float64(fps)
					if d := math.Abs(measured - exact); d > worst {
						worst = d
					}
					sum += math.Abs(measured - exact)
					if d := math.Abs(measured - float64(mapped)/1000); d > tolerance {
						t.Errorf("frame %d (%d ms, %s): the picture shows source time %.3f s, the time map says %.3f s", k, startMs, entry.ItemID, measured, float64(mapped)/1000)
					}
					if d := math.Abs(measured - exact); d > tolerance {
						t.Errorf("frame %d (%d ms, %s): the picture shows source time %.3f s, the ramp's maths say %.3f s", k, startMs, entry.ItemID, measured, exact)
					}
					if item.HasRamp() {
						// The control: the average speed over the clip is a different mapping, and far off somewhere.
						avgExact := float64(item.Source.InMs)/1000 + float64(startMs-timeline.FrameStartMs(timeline.Frame(item.AtMs, fps), fps))*item.AverageSpeed()/1000
						avgWorst = math.Max(avgWorst, math.Abs(avgExact-exact))
					}
					if t.Failed() {
						break
					}
				}
				t.Logf("worst error %.1f ms, mean %.1f ms over %d frames; an average-speed mapping would be %.0f ms off at worst", worst*1000, sum/float64(len(frames))*1000, len(frames), avgWorst*1000)
				if avgWorst < 3*base {
					t.Errorf("the control is too weak: an average-speed mapping is only %.0f ms off", avgWorst*1000)
				}
			})
		}
	}
}
