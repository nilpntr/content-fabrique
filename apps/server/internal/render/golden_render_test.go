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

// goldenWidth is the width the sampled frames are scaled to before they are compared and committed.
const goldenWidth = 108

// finalFixtures are the render fixtures that are also rendered with the final profile: one per kind of work (a cover
// with a focus point, a transition with a blur, blur_fill, an audio mix and the full edit).
var finalFixtures = []string{"v01_minimal_9x16", "v10_transition_whip", "v14_reframe_blur_fill", "v26_audio_licensed_track", "v30_full_edit"}

// sample is a frame of the output that the golden test looks at.
type sample struct {
	frame int64
	// kind says why the frame is sampled: first, last, cut-1, cut, transition or mid.
	kind string
	// sensitive is set for mid frames of clips that play at normal or higher speed, whose next frame must look
	// different, or the frame number could not tell an off-by-one apart.
	sensitive bool
}

// sampleFrames picks the frames to compare: the first and last frame, the frames around every cut (the last frame before it and
// the first of the next item: a cut that is one frame early shows in the first, one that is late in the second), the middle of every transition and the middle of every item.
func sampleFrames(p Plan) []sample {
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
		}
	}
	// The middle of each item, from the time map (which assigns [out_start, out_end) to each main item).
	for _, e := range p.TimeMap {
		if e.SourceInMs == nil {
			continue
		}
		startF := msToFrame(e.OutStartMs, p.FPS)
		endF := msToFrame(e.OutEndMs, p.FPS)
		add((startF+endF)/2, "mid", *e.Speed >= 1 && endF-startF > 4 && (startF+endF)/2+1 < p.TotalFrames)
	}
	out := make([]sample, 0, len(byFrame))
	for _, s := range byFrame {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b sample) int { return int(a.frame - b.frame) })
	return out
}

func msToFrame(ms int64, fps int) int64 {
	return (2*ms*int64(fps) + 1000) / 2000
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
	if err := Execute(context.Background(), plan, LocalRunner{Binary: ffmpeg}, ExecuteOptions{Output: out}); err != nil {
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

	samples := sampleFrames(plan)
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
	imgs := extractFrames(t, ffmpeg, out, frames)

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
			if got < minSSIM {
				t.Errorf("frame %d (%s): SSIM %.4f against %s, want at least %.2f", s.frame, s.kind, got, path, minSSIM)
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

// extractFrames decodes the given frames (numbered from 0, ascending) of a video, scaled to goldenWidth.
func extractFrames(t *testing.T, ffmpeg, video string, frames []int64) map[int64]image.Image {
	t.Helper()
	dir := t.TempDir()
	terms := make([]string, len(frames))
	for i, f := range frames {
		terms[i] = fmt.Sprintf("eq(n,%d)", f)
	}
	vf := fmt.Sprintf("select='%s',scale=%d:-2:flags=area", strings.Join(terms, "+"), goldenWidth)
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
