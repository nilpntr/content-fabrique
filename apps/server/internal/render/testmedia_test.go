package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Test media is generated when the tests run and cached in testdata/.cache (git-ignored): nothing binary is committed.
// Every video asset of fixtures/assets.json becomes a 640x360, 30 fps clip of the asset's duration whose frames carry
// the source frame number as large seven-segment digits (drawn with drawbox, so no font is needed and every ffmpeg draws
// the same pixels), four coloured corner marks and bars that move 7 px a frame. Everything is high contrast and soft-edged
// (a Gaussian blur of a pixel or more): ffmpeg 7 and 8 resample by slightly different rules, and x264 differs between
// builds, so hard edges and faint detail would score below the SSIM threshold across versions. An off-by-one cut, a wrong
// focus point or a wrong crop shows in the frame itself. Audio assets are sine tones, and the video assets carry one too.

// mediaVersion is part of the cache path: change it when the generator changes.
const mediaVersion = "v6"

const (
	srcW, srcH, srcFPS = 640, 360, 30
	digitW, digitH     = 32, 56
	digitT             = 6
	digitGap           = 12
	digitCount         = 4
)

// segments of a seven-segment digit, and which digits light them.
var segmentDigits = map[string][]int{
	"a": {0, 2, 3, 5, 6, 7, 8, 9},
	"b": {0, 1, 2, 3, 4, 7, 8, 9},
	"c": {0, 1, 3, 4, 5, 6, 7, 8, 9},
	"d": {0, 2, 3, 5, 6, 8, 9},
	"e": {0, 2, 6, 8},
	"f": {0, 4, 5, 6, 8, 9},
	"g": {2, 3, 4, 5, 6, 8, 9},
}

// segmentRect returns x, y, w, h of a segment of a digit whose top-left corner is (x0, y0).
func segmentRect(seg string, x0, y0 int) (x, y, w, h int) {
	t, dw, dh := digitT, digitW, digitH
	switch seg {
	case "a":
		return x0, y0, dw, t
	case "d":
		return x0, y0 + dh - t, dw, t
	case "g":
		return x0, y0 + (dh-t)/2, dw, t
	case "b":
		return x0 + dw - t, y0, t, dh / 2
	case "c":
		return x0 + dw - t, y0 + dh/2, t, dh / 2
	case "f":
		return x0, y0, t, dh / 2
	default: // e
		return x0, y0 + dh/2, t, dh / 2
	}
}

// videoFilter builds the picture of a test clip: background colour, grid, corner marks, the frame number and the bar.
func videoFilter() string {
	totalW := digitCount*digitW + (digitCount-1)*digitGap
	x0 := (srcW - totalW) / 2
	y0 := (srcH - digitH) / 2
	var f []string
	// Opaque black bars, 40 px wide every 80 px, that move 7 px a frame (the picture is cropped from a wider one), and
	// static dark bands: big, high-contrast shapes with hard edges, no fine lines, gradients or transparency. Every frame
	// differs from the next in the whole picture, however far a reframe has zoomed in, and the picture is robust to the
	// small differences between x264 versions, which show in flat or faint areas but not in strong structure.
	f = append(f, "drawgrid=w=80:h=360:t=40:c=black", fmt.Sprintf("crop=%d:%d:x='80-mod(n*7,80)':y=0", srcW, srcH), "drawgrid=w=640:h=180:t=30:c=0x202020")
	for _, m := range []struct {
		x, y int
		c    string
	}{{0, 0, "red"}, {srcW - 40, 0, "lime"}, {0, srcH - 40, "cyan"}, {srcW - 40, srcH - 40, "yellow"}} {
		f = append(f, fmt.Sprintf("drawbox=x=%d:y=%d:w=40:h=40:color=%s:t=fill", m.x, m.y, m.c))
	}
	f = append(f, fmt.Sprintf("drawbox=x=%d:y=%d:w=%d:h=%d:color=black@0.7:t=fill", x0-10, y0-10, totalW+20, digitH+20))
	pow := 1
	for i := range digitCount {
		// Digit i from the right.
		dx := x0 + (digitCount-1-i)*(digitW+digitGap)
		for _, seg := range []string{"a", "b", "c", "d", "e", "f", "g"} {
			var terms []string
			for _, d := range segmentDigits[seg] {
				terms = append(terms, fmt.Sprintf("eq(mod(floor(n/%d),10),%d)", pow, d))
			}
			x, y, w, h := segmentRect(seg, dx, y0)
			f = append(f, fmt.Sprintf("drawbox=x=%d:y=%d:w=%d:h=%d:color=white:t=fill:enable='%s'", x, y, w, h, strings.Join(terms, "+")))
		}
		pow *= 10
	}
	return strings.Join(f, ",")
}

type mediaSpec struct {
	id       string
	asset    timeline.Asset
	hue      string
	toneHz   int
	filename string
}

var (
	mediaOnce sync.Once
	mediaErr  error
	mediaDir  string
)

// videoColours and audioTones give every asset its own look and sound.
var (
	videoColours = []string{"0x2a6fdb", "0xd9822b", "0x2bb673", "0xb23fa0"}
	audioTones   = []int{220, 330, 440}
)

// testAssets returns the fixtures' asset catalog.
func testAssets(t testing.TB) timeline.MemoryCatalog {
	t.Helper()
	cat, err := timeline.LoadMemoryCatalog(filepath.Join(fixturesDir, "assets.json"))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func sortedAssetIDs(cat timeline.MemoryCatalog) []string {
	ids := make([]string, 0, len(cat))
	for id := range cat {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ffmpegTools skips the test when ffmpeg or ffprobe is missing and returns the binary paths.
func ffmpegTools(t testing.TB) (ffmpeg, ffprobe string) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not on PATH: skipping the render tests (CI installs a pinned ffmpeg; locally `brew install ffmpeg`)")
	}
	fp, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not on PATH: skipping the render tests")
	}
	return ff, fp
}

// ensureMedia generates the test media once per test run and returns the cache directory.
func ensureMedia(t testing.TB) (dir string, cat timeline.MemoryCatalog) {
	t.Helper()
	ff, _ := ffmpegTools(t)
	cat = testAssets(t)
	mediaOnce.Do(func() {
		mediaDir, mediaErr = generateMedia(ff, cat)
	})
	if mediaErr != nil {
		t.Fatalf("generate test media: %v", mediaErr)
	}
	return mediaDir, cat
}

func generateMedia(ffmpeg string, cat timeline.MemoryCatalog) (string, error) {
	dir, err := filepath.Abs(filepath.Join("testdata", ".cache", "media-"+mediaVersion))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		sem  = make(chan struct{}, 4)
	)
	vi, ai := 0, 0
	for _, id := range sortedAssetIDs(cat) {
		a := cat[id]
		var ms mediaSpec
		switch a.Kind {
		case timeline.AssetVideo:
			ms = mediaSpec{id: id, asset: a, hue: videoColours[vi%len(videoColours)], toneHz: 200 + 110*vi, filename: id + ".mp4"}
			vi++
		default:
			ms = mediaSpec{id: id, asset: a, toneHz: audioTones[ai%len(audioTones)], filename: id + ".m4a"}
			ai++
		}
		out := filepath.Join(dir, ms.filename)
		if _, err := os.Stat(out); err == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := generateOne(ffmpeg, ms, out); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return "", errs[0]
	}
	return dir, nil
}

func generateOne(ffmpeg string, ms mediaSpec, out string) error {
	dur := secs(ms.asset.DurationMs)
	// Write to a temporary name in the same directory and rename, so a half-written file is never taken for a cached one.
	tmp := out + ".tmp.mp4"
	if ms.asset.Kind == timeline.AssetAudio {
		tmp = out + ".tmp.m4a"
	}
	var args []string
	if ms.asset.Kind == timeline.AssetVideo {
		args = []string{
			"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", fmt.Sprintf("color=c=%s:s=%dx%d:r=%d:d=%s", ms.hue, srcW+80, srcH, srcFPS, dur),
			"-f", "lavfi", "-i", fmt.Sprintf("color=c=white:s=24x%d:r=%d:d=%s", srcH, srcFPS, dur),
			"-f", "lavfi", "-i", fmt.Sprintf("sine=f=%d:r=48000:d=%s", ms.toneHz, dur),
			"-filter_complex", fmt.Sprintf("[0:v]%s[bg];[bg][1:v]overlay=x='mod(n*7,%d)':y=0:eval=frame:shortest=1,format=yuv420p[v]", videoFilter(), srcW-24),
			"-map", "[v]", "-map", "2:a",
			"-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-g", "15", "-keyint_min", "15", "-sc_threshold", "0",
			"-c:a", "aac", "-b:a", "96k", "-shortest", "-f", "mp4", tmp,
		}
	} else {
		args = []string{
			"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", fmt.Sprintf("sine=f=%d:r=48000:d=%s", ms.toneHz, dur),
			"-c:a", "aac", "-b:a", "96k", "-f", "mp4", tmp,
		}
	}
	cmd := exec.CommandContext(context.Background(), ffmpeg, args...) //nolint:gosec // test helper with fixed arguments
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("generate %s: %w\n%s", ms.filename, err, stderr.String())
	}
	return os.Rename(tmp, out)
}

// mediaResolver resolves the fixtures' assets to the generated files, for both variants.
func mediaResolver(dir string, cat timeline.MemoryCatalog) AssetResolver {
	return ResolverFunc(func(assetID string, _ Variant) (ResolvedAsset, error) {
		a, ok := cat[assetID]
		if !ok {
			return ResolvedAsset{}, fmt.Errorf("unknown asset %s", assetID)
		}
		ext := ".mp4"
		if a.Kind == timeline.AssetAudio {
			ext = ".m4a"
		}
		return ResolvedAsset{Location: filepath.Join(dir, assetID+ext), HasAudio: true}, nil
	})
}
