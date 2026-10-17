package renderjobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

func shotsPlan(fps int, total int64, startsMs ...int64) render.Plan {
	p := render.Plan{FPS: fps, TotalFrames: total}
	for i, ms := range startsMs {
		end := total * 1000 / int64(fps)
		if i+1 < len(startsMs) {
			end = startsMs[i+1]
		}
		in, speed := int64(0), 1.0
		p.TimeMap = append(p.TimeMap, render.TimeMapEntry{OutStartMs: ms, OutEndMs: end, ItemID: fmt.Sprintf("c%d", i+1), AssetID: "ast_x", SourceInMs: &in, Speed: &speed})
	}
	return p
}

func frames(tiles []SheetTile) []int64 {
	out := make([]int64, len(tiles))
	for i, t := range tiles {
		out[i] = t.Frame
	}
	return out
}

func TestPlanTiles(t *testing.T) {
	t.Run("a frame 100 ms after every cut and the last frame", func(t *testing.T) {
		tiles, err := PlanTiles(shotsPlan(30, 300, 0, 2000, 5000))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := frames(tiles), []int64{3, 63, 153, 299}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("frames %v, want %v", got, want)
		}
		for i, want := range []struct {
			kind, item string
			outMs      int64
		}{{TileShot, "c1", 100}, {TileShot, "c2", 2100}, {TileShot, "c3", 5100}, {TileLast, "", 9967}} {
			if tl := tiles[i]; tl.Index != i || tl.Kind != want.kind || tl.ItemID != want.item || tl.OutMs != want.outMs {
				t.Errorf("tile %d = %+v, want %+v", i, tl, want)
			}
		}
	})
	t.Run("a shot shorter than the offset shows itself, not its neighbour", func(t *testing.T) {
		// c2 plays frames 60 and 61 only (2000 ms to 2050 ms at 30 fps): 100 ms after its cut is in c3.
		tiles, err := PlanTiles(shotsPlan(30, 300, 0, 2000, 2050))
		if err != nil {
			t.Fatal(err)
		}
		if f := tiles[1].Frame; f < 60 || f > 61 {
			t.Errorf("tile 1 shows frame %d, want 60 or 61 (inside its shot)", f)
		}
		if tiles[1].Frame >= tiles[2].Frame {
			t.Errorf("the tiles are not in order: %v", frames(tiles))
		}
	})
	t.Run("no second tile for a last frame that is already shown", func(t *testing.T) {
		tiles, err := PlanTiles(shotsPlan(30, 3, 0)) // a 100 ms edit: the offset is past its end
		if err != nil {
			t.Fatal(err)
		}
		if len(tiles) != 1 || tiles[0].Frame != 2 || tiles[0].Kind != TileShot {
			t.Fatalf("tiles %+v", tiles)
		}
	})
	t.Run("text items are not shots", func(t *testing.T) {
		p := shotsPlan(30, 300, 0, 5000)
		p.TimeMap = append(p.TimeMap, render.TimeMapEntry{OutStartMs: 1000, OutEndMs: 3000, ItemID: "t1"})
		tiles, err := PlanTiles(p)
		if err != nil || len(tiles) != 3 {
			t.Fatalf("tiles %+v, %v", tiles, err)
		}
	})
	t.Run("a long edit keeps the first shots and the last frame", func(t *testing.T) {
		starts := make([]int64, 100)
		for i := range starts {
			starts[i] = int64(i) * 1000
		}
		tiles, err := PlanTiles(shotsPlan(30, 3000, starts...))
		if err != nil {
			t.Fatal(err)
		}
		if len(tiles) != MaxTiles || tiles[MaxTiles-1].Kind != TileLast || tiles[MaxTiles-1].Frame != 2999 || tiles[MaxTiles-2].ItemID != fmt.Sprintf("c%d", MaxTiles-1) {
			t.Fatalf("%d tiles, last two %+v", len(tiles), tiles[len(tiles)-2:])
		}
		for i, tl := range tiles {
			if tl.Index != i {
				t.Fatalf("tile %d has index %d", i, tl.Index)
			}
		}
	})
	t.Run("nothing to show", func(t *testing.T) {
		if _, err := PlanTiles(render.Plan{FPS: 30, TotalFrames: 30}); err == nil {
			t.Error("a plan without shots was accepted")
		}
		if _, err := PlanTiles(render.Plan{}); err == nil {
			t.Error("an empty plan was accepted")
		}
	})
}

func TestTileSizeAndTimestamp(t *testing.T) {
	for _, c := range []struct {
		out    render.Size
		tiles  int
		ww, hh int
	}{
		{render.Size{Width: 540, Height: 960}, 11, 216, 384},
		{render.Size{Width: 1080, Height: 1920}, 11, 216, 384},
		{render.Size{Width: 960, Height: 540}, 5, 384, 216},
		{render.Size{Width: 540, Height: 676}, 5, 306, 384},
		{render.Size{Width: 540, Height: 960}, 30, 162, 288},
		{render.Size{Width: 540, Height: 960}, 50, 126, 224},
	} {
		if w, h := tileSize(c.out, c.tiles); w != c.ww || h != c.hh {
			t.Errorf("tileSize(%v, %d) = %dx%d, want %dx%d", c.out, c.tiles, w, h, c.ww, c.hh)
		}
	}
	for ms, want := range map[int64]string{0: "0:00.000", 100: "0:00.100", 65432: "1:05.432", 600000: "10:00.000", -5: "0:00.000"} {
		if got := FormatTimestamp(ms); got != want {
			t.Errorf("FormatTimestamp(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestFontCoversATimestamp(t *testing.T) {
	for _, r := range "0123456789:." {
		g, ok := glyphs[r]
		if !ok {
			t.Errorf("no glyph for %q", r)
			continue
		}
		for i, row := range g {
			if len(row) != glyphW || strings.Trim(row, "#.") != "" {
				t.Errorf("glyph %q row %d is %q", r, i, row)
			}
		}
	}
	// A label marks the corner of its tile, and only the corner.
	img := image.NewRGBA(image.Rect(0, 0, 220, 380))
	drawLabel(img, img.Bounds(), "0:12.345")
	if c := img.RGBAAt(2, 2); c.A == 0 {
		t.Error("the label box is not drawn")
	}
	if c := img.RGBAAt(200, 300); c.A != 0 {
		t.Errorf("something is drawn far from the label: %v", c)
	}
}

func TestEncodeSheetFitsTheTarget(t *testing.T) {
	// Smooth with a little noise, like a sheet of real frames: found at a quality that fits.
	img := image.NewRGBA(image.Rect(0, 0, 876, 1160))
	rng := rand.New(rand.NewPCG(1, 2))
	for y := range 1160 {
		for x := range 876 {
			n := uint8(rng.IntN(24))
			img.SetRGBA(x, y, color.RGBA{uint8(x/4) + n, uint8(y/5) + n, uint8((x+y)/8) + n, 255})
		}
	}
	data, err := encodeSheet(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > SheetTargetBytes {
		t.Errorf("%d bytes, the target is %d", len(data), SheetTargetBytes)
	}
	// Pure noise cannot fit: it still comes out as a valid JPEG, at the lowest quality.
	for y := range 1160 {
		for x := range 876 {
			img.SetRGBA(x, y, color.RGBA{uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255})
		}
	}
	data, err = encodeSheet(img)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		t.Errorf("not a JPEG: %v", err)
	}
}

func TestExtractArgs(t *testing.T) {
	args := extractArgs([]int64{3, 63, 299}, 216, 384)
	j := strings.Join(args, " ")
	for _, want := range []string{
		"-protocol_whitelist file -format_whitelist " + media.UntrustedFormats + " -i /in/0",
		"select='eq(n,3)+eq(n,63)+eq(n,299)',scale=216:384:flags=area,format=rgb24",
		"-fps_mode passthrough -frames:v 3 /work/tile_%03d.png",
	} {
		if !strings.Contains(j, want) {
			t.Errorf("missing %q in %s", want, j)
		}
	}
	if strings.Index(j, "-format_whitelist") > strings.Index(j, " -i ") {
		t.Error("the whitelist must come before -i to apply to it")
	}
}

func TestReasonFor(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errors.New("boom"), ReasonProcessing},
		{fmt.Errorf("render: %w", context.DeadlineExceeded), ReasonTimeout},
		{fmt.Errorf("ffmpeg: %w", sandbox.ErrTimeout), ReasonTimeout},
		{fmt.Errorf("stage: %w", context.Canceled), ReasonInterrupted},
		{fileErr(ReasonInvalidTimeline, errors.New("x")), ReasonInvalidTimeline},
	} {
		if got := reasonFor(c.err); got != c.want {
			t.Errorf("reasonFor(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// The job's own timeout covers the worst case, and River's rescue comes later than the timeout, so a running render is never
// handed to a second worker.
func TestTimeoutsAgree(t *testing.T) {
	if WorstCase() > jobs.RenderTimeout {
		t.Errorf("a render may take %v, the job timeout is %v", WorstCase(), jobs.RenderTimeout)
	}
	if jobs.RenderTimeout >= jobs.JobRescueAfter {
		t.Errorf("River rescues a job after %v, a render may run %v", jobs.JobRescueAfter, jobs.RenderTimeout)
	}
	if render.DefaultLimits().Timeout != ExecuteTimeout {
		t.Errorf("render.DefaultLimits has a timeout of %v, the budget assumes %v", render.DefaultLimits().Timeout, ExecuteTimeout)
	}
}

func TestToolFailureClassification(t *testing.T) {
	var fe *fileError
	if err := toolFailure("ffmpeg", 1, []byte("Invalid data found")); !errors.As(err, &fe) || fe.reason != ReasonRenderFailed {
		t.Errorf("exit 1: %v", err)
	}
	for _, code := range []int{137, 143} {
		err := toolFailure("ffmpeg", code, nil)
		if errors.As(err, &fe) || !errors.Is(err, errKilled) {
			t.Errorf("exit %d is a signal kill and must be retried, got %v", code, err)
		}
	}
}
