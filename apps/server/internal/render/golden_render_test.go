package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Golden renders (design note section 7): every render fixture is rendered with the preview profile, a handful with the
// final profile too, and frames at sampled positions are compared with small PNGs committed under testdata/golden/frames
// by SSIM. The frames of the test media carry their source frame number, so a cut that is one frame off fails.

// goldenWidth is the width the sampled frames are scaled to before they are compared and committed; goldenTextWidth is the width
// for fixtures with text or captions, which need more pixels to show where the words are.
const (
	goldenWidth     = 108
	goldenTextWidth = 216
)

// goldenWidthFor is the comparison width of a plan: wider when it draws text or captions (not just the watermark).
func goldenWidthFor(p Plan) int {
	if p.Subtitles != nil && (p.Subtitles.TextEvents > 0 || p.Subtitles.CaptionEvents > 0) {
		return goldenTextWidth
	}
	return goldenWidth
}

// finalFixtures are the render fixtures that are also rendered with the final profile: one per kind of work (a cover
// with a focus point, a transition with a blur, blur_fill, an audio mix and the full edit).
var finalFixtures = []string{"v01_minimal_9x16", "v10_transition_whip", "v14_reframe_blur_fill", "v26_audio_licensed_track", "v30_full_edit", "v37_motion_9x16"}

// sample is a frame of the output that the golden test looks at.
type sample struct {
	frame int64
	// kind says why the frame is sampled: first, last, cut-1, cut, transition, after-transition or mid.
	kind string
	// sensitive is set for mid frames of clips that play at normal or higher speed, whose next frame must look
	// different, or the frame number could not tell an off-by-one apart.
	sensitive bool
	// text is set when a text item or a caption is on screen in the frame (not just the watermark).
	text bool
}

// sampleFrames picks the frames to compare: the first and last frame, the frames around every cut (the last frame before it and
// the first of the next item: a cut that is one frame early shows in the first, one that is late in the second), the middle of every transition, the first frame after a whip and the middle of every item.
func sampleFrames(t testing.TB, p Plan, spec *timeline.Spec) []sample {
	byFrame := map[int64]sample{}
	add := func(f int64, kind string, sensitive bool) {
		if f < 0 || f >= p.TotalFrames {
			return
		}
		if old, ok := byFrame[f]; ok {
			sensitive = sensitive || old.sensitive
			if old.kind != kind {
				kind = old.kind
			}
		}
		byFrame[f] = sample{frame: f, kind: kind, sensitive: sensitive}
	}
	add(0, "first", false)
	add(p.TotalFrames-1, "last", false)
	for _, c := range p.CutList {
		add(c.Frame-1, "cut-1", false)
		add(c.Frame, "cut", false)
		if c.TransitionFrames > 0 {
			add(c.Frame+c.TransitionFrames/2, "transition", false)
			// The first frame after a whip (or a zoom_through) is the incoming clip alone: a blur window that runs one frame too
			// long shows here. (Only for the transitions that have a window of their own to get wrong.)
			if blurred(c.Transition) {
				add(c.Frame+c.TransitionFrames, "after-transition", false)
			}
		}
	}
	// The middle of each item, from the time map (which assigns [out_start, out_end) to each main item). A text item is sampled
	// in the middle of its time as well, after its animation, and up to six caption events in the middle of theirs.
	for _, e := range p.TimeMap {
		if e.SourceInMs == nil {
			a, b := timeline.Frame(e.OutStartMs, p.FPS), timeline.Frame(e.OutEndMs, p.FPS)
			add((a+b)/2, "text", false)
			continue
		}
		startF := timeline.Frame(e.OutStartMs, p.FPS)
		endF := timeline.Frame(e.OutEndMs, p.FPS)
		// A clip with a speed ramp may play slowly somewhere, where the next frame can be a copy of this one.
		add((startF+endF)/2, "mid", slowest(e) >= 1 && endF-startF > 4 && (startF+endF)/2+1 < p.TotalFrames)
	}
	for _, f := range motionFrames(spec, p.FPS) {
		add(f, "motion", false)
	}
	if p.Subtitles != nil && p.Subtitles.CaptionEvents > 0 {
		var mids []int64
		for _, d := range parseDialogues(t, p.Subtitles.ASS) {
			if d.layer == layerCaption {
				mids = append(mids, (firstFrameAt(t, d.start, p.FPS)+firstFrameAt(t, d.end, p.FPS))/2)
			}
		}
		for i := 0; i < 6 && len(mids) > 0; i++ {
			add(mids[i*len(mids)/6%len(mids)], "caption", false)
		}
	}
	var shown [][2]int64 // frames [from, to) on which a text item or a caption is on screen
	if p.Subtitles != nil {
		for _, d := range parseDialogues(t, p.Subtitles.ASS) {
			if d.layer != layerWatermark {
				shown = append(shown, [2]int64{firstFrameAt(t, d.start, p.FPS), firstFrameAt(t, d.end, p.FPS)})
			}
		}
	}
	out := make([]sample, 0, len(byFrame))
	for _, s := range byFrame {
		s.text = slices.ContainsFunc(shown, func(r [2]int64) bool { return s.frame >= r[0] && s.frame < r[1] })
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b sample) int { return int(a.frame - b.frame) })
	return out
}

// slowest is the lowest speed a time map entry plays at: its speed, or the lowest point of its ramp.
func slowest(e TimeMapEntry) float64 {
	lo := *e.Speed
	for _, pt := range e.SpeedRamp {
		lo = math.Min(lo, pt.Speed)
	}
	return lo
}

// motionFrames picks frames inside the clips that have a motion preset, where the preset shows: a punch_in half way through and at
// the end of its zoom, a push_in or pull_back a quarter and three quarters in, a shake at its peak and as it dies out.
func motionFrames(spec *timeline.Spec, fps int) []int64 {
	main, _, ok := timeline.MainTrack(spec)
	if !ok {
		return nil
	}
	var out []int64
	for _, it := range main.Items {
		if !it.Motion.Active() {
			continue
		}
		start, end := timeline.Frame(it.AtMs, fps), timeline.Frame(timeline.EndMs(it), fps)
		n := end - start
		at := int64(0)
		if it.Motion.AtMs != nil {
			at = timeline.Frame(*it.Motion.AtMs, fps)
		}
		switch it.Motion.Preset {
		case timeline.MotionPunchIn:
			land := at
			if it.Motion.AtMs == nil {
				land = punchRampFrames(fps) // the default: the zoom starts with the clip and lands one ramp later
			}
			out = append(out, start+land/2, start+land+3)
		case timeline.MotionPushIn, timeline.MotionPullBack:
			out = append(out, start+n/4, start+n*3/4)
		case timeline.MotionShake:
			out = append(out, start+at+4, start+at+9)
		}
	}
	return out
}

// toolSem limits how many ffmpeg processes the golden tests start at once.
var toolSem = make(chan struct{}, max(1, runtime.NumCPU()/2))

func TestGoldenRenders(t *testing.T) {
	ffmpeg, ffprobe := ffmpegTools(t)
	dir, cat := ensureMedia(t)
	resolver := mediaResolver(dir, cat)

	for _, name := range renderFixtures(t) {
		profiles := []Profile{Preview}
		if slices.Contains(finalFixtures, name) {
			profiles = append(profiles, Final)
		}
		spec := loadFixture(t, name, cat)
		for _, prof := range profiles {
			t.Run(name+"/"+string(prof.Name), func(t *testing.T) {
				t.Parallel()
				toolSem <- struct{}{}
				defer func() { <-toolSem }()
				goldenRender(t, ffmpeg, ffprobe, name, spec, prof, resolver)
			})
		}
	}
}

func goldenRender(t *testing.T, ffmpeg, ffprobe, name string, spec *timeline.Spec, prof Profile, resolver AssetResolver) {
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
		if err := os.WriteFile(filepath.Join(keep, name+"."+string(prof.Name)+".mp4"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	info := probe(t, ffprobe, out)
	if info.width != plan.OutputSize.Width || info.height != plan.OutputSize.Height {
		t.Errorf("output is %dx%d, want %dx%d", info.width, info.height, plan.OutputSize.Width, plan.OutputSize.Height)
	}
	if info.frames != plan.TotalFrames {
		t.Errorf("output has %d frames, the plan says %d", info.frames, plan.TotalFrames)
	}
	if !info.hasAudio {
		t.Error("output has no audio stream")
	}
	if d := math.Abs(info.audioSec*1000 - float64(plan.DurationMs)); d > 60 {
		t.Errorf("audio is %.3f s long, video %d ms", info.audioSec, plan.DurationMs)
	}

	samples := sampleFrames(t, plan, spec)
	// The frame after each sensitive sample is extracted as well, to prove the test could see an off-by-one.
	want := map[int64]bool{}
	for _, s := range samples {
		want[s.frame] = true
		if s.sensitive {
			want[s.frame+1] = true
		}
	}
	frames := make([]int64, 0, len(want))
	for f := range want {
		frames = append(frames, f)
	}
	slices.Sort(frames)
	imgs := extractFrames(t, ffmpeg, out, frames, goldenWidthFor(plan))

	gdir := filepath.Join("testdata", "golden", "frames", name, string(prof.Name))
	if *update {
		if err := os.RemoveAll(gdir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(gdir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	lowest, highestNeighbour, lowestAt := 1.0, 0.0, ""
	for _, s := range samples {
		img := imgs[s.frame]
		path := filepath.Join(gdir, fmt.Sprintf("f%04d_%s.png", s.frame, strings.ReplaceAll(s.kind, "+", "p")))
		if *update {
			writePNG(t, path, img)
		} else {
			golden := readPNG(t, path)
			got := ssim(golden, img)
			if got < lowest {
				lowest, lowestAt = got, fmt.Sprintf("frame %d (%s)", s.frame, s.kind)
			}
			if floor := minSSIMFor(s); got < floor {
				t.Errorf("frame %d (%s): SSIM %.4f against %s, want at least %.2f", s.frame, s.kind, got, path, floor)
			}
		}
		if s.sensitive {
			got := ssim(img, imgs[s.frame+1])
			highestNeighbour = math.Max(highestNeighbour, got)
			if got >= minSSIM {
				t.Errorf("frame %d (%s) and the next frame score SSIM %.4f: this sample could not tell an off-by-one frame", s.frame, s.kind, got)
			}
		}
	}
	t.Logf("lowest SSIM against the golden frames %.4f at %s; highest SSIM between a frame and the next %.4f (an off-by-one frame must score below %.2f)", lowest, lowestAt, highestNeighbour, minSSIM)
	if !*update {
		// No golden frame may be left over from a different sampling.
		entries, err := os.ReadDir(gdir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != len(samples) {
			t.Errorf("%s holds %d frames, the plan samples %d: run `go test ./internal/render -update`", gdir, len(entries), len(samples))
		}
	}
}

type probeInfo struct {
	width, height int
	frames        int64
	hasAudio      bool
	audioSec      float64
}

func probe(t *testing.T, ffprobe, path string) probeInfo {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), ffprobe, "-v", "error", "-show_entries", "stream=codec_type,width,height,nb_frames,duration", "-of", "json", path) //nolint:gosec // test helper
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffprobe: %v\n%s", err, stderr.String())
	}
	var res struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			NbFrames  string `json:"nb_frames"`
			Duration  string `json:"duration"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	var info probeInfo
	for _, s := range res.Streams {
		switch s.CodecType {
		case "video":
			info.width, info.height = s.Width, s.Height
			info.frames, _ = strconv.ParseInt(s.NbFrames, 10, 64)
		case "audio":
			info.hasAudio = true
			info.audioSec, _ = strconv.ParseFloat(s.Duration, 64)
		}
	}
	return info
}

// extractFrames decodes the given frames (numbered from 0, ascending) of a video, scaled to width.
func extractFrames(t *testing.T, ffmpeg, video string, frames []int64, width int) map[int64]image.Image {
	t.Helper()
	dir := t.TempDir()
	terms := make([]string, len(frames))
	for i, f := range frames {
		terms[i] = fmt.Sprintf("eq(n,%d)", f)
	}
	vf := fmt.Sprintf("select='%s',scale=%d:-2:flags=area", strings.Join(terms, "+"), width)
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-i", video, "-vf", vf, "-fps_mode", "passthrough", filepath.Join(dir, "%05d.png")) //nolint:gosec // test helper
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("extract frames: %v\n%s", err, stderr.String())
	}
	out := make(map[int64]image.Image, len(frames))
	for i, f := range frames {
		path := filepath.Join(dir, fmt.Sprintf("%05d.png", i+1))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("frame %d was not extracted (the video has fewer frames?): %v", f, err)
		}
		out[f] = readPNG(t, path)
	}
	return out
}

func readPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test file
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/render -update` to create the golden frames)", err)
	}
	defer f.Close() //nolint:errcheck // read-only
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return img
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
