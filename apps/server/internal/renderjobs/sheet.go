package renderjobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The contact sheet (docs/design/timeline-v1.md section 6): one frame 100 ms after each cut plus the last frame, tiled four
// across, with the output timestamp drawn on every tile, as a JPEG. The editor agent checks its own work from this picture.
//
// ffmpeg, in the sandbox, only does what needs a decoder: it picks the frames from the rendered MP4 by frame number and scales
// them to the tile size, as PNG files. Everything after that is plain Go over those small files: the tiling, the timestamps
// (a built-in 5x7 digit font, so no font file is needed and every machine draws the same pixels) and the JPEG, whose quality is
// lowered until the sheet is about 300 KB. The files that come out of the sandbox are untrusted: regular files only, size
// capped, and the dimensions are read before the picture is decoded.

// The shape of the sheet.
const (
	// SheetColumns is how many tiles go across.
	SheetColumns = 4
	// SheetOffsetMs is how long after a cut the frame of its tile is taken: late enough to be past the cut itself.
	SheetOffsetMs = 100
	// SheetTargetBytes is the size the JPEG is made to fit in, by lowering its quality; a sheet that cannot reach it at the
	// lowest quality is kept as it is.
	SheetTargetBytes = 300 << 10
	sheetGap         = 4
	sheetMinQuality  = 40
	maxTilePNG       = 8 << 20
)

// Tile kinds.
const (
	TileShot = "shot"
	TileLast = "last"
)

// SheetTile is one tile of the sheet. Index counts row by row from 0.
type SheetTile struct {
	Index  int    `json:"index"`
	OutMs  int64  `json:"out_ms"`
	Frame  int64  `json:"frame"`
	ItemID string `json:"item_id,omitempty"`
	Kind   string `json:"kind"`
}

// SheetLayout is what the render stores about its contact sheet, so that a reader can map a tile to an output time and from
// there, through the time map, to a timeline item.
type SheetLayout struct {
	Columns    int         `json:"columns"`
	TileWidth  int         `json:"tile_width"`
	TileHeight int         `json:"tile_height"`
	Tiles      []SheetTile `json:"tiles"`
}

// PlanTiles chooses the frames of the contact sheet from the plan: for every main-track item the frame SheetOffsetMs after it
// starts (the first item starts at 0, the others at their cut), kept inside the item's own frames so that a shot shorter than
// the offset shows itself and not its neighbour, and the last frame of the video unless a tile already shows it. With more
// than MaxTiles shots the first MaxTiles-1 and the last frame are kept.
func PlanTiles(plan render.Plan) ([]SheetTile, error) {
	if plan.FPS <= 0 || plan.TotalFrames < 1 {
		return nil, errors.New("the plan has no frames")
	}
	var main []render.TimeMapEntry
	for _, e := range plan.TimeMap {
		if e.SourceInMs != nil {
			main = append(main, e)
		}
	}
	if len(main) == 0 {
		return nil, errors.New("the plan has no main-track items")
	}
	last := plan.TotalFrames - 1
	tiles := make([]SheetTile, 0, len(main)+1)
	for i, e := range main {
		lo := timeline.Frame(e.OutStartMs, plan.FPS)
		hi := plan.TotalFrames - 1
		if i+1 < len(main) {
			hi = timeline.Frame(main[i+1].OutStartMs, plan.FPS) - 1
		}
		hi = min(hi, last)
		f := min(max(timeline.Frame(e.OutStartMs+SheetOffsetMs, plan.FPS), lo), hi)
		tiles = append(tiles, SheetTile{Frame: f, OutMs: timeline.FrameStartMs(f, plan.FPS), ItemID: e.ItemID, Kind: TileShot})
	}
	if tiles[len(tiles)-1].Frame != last {
		tiles = append(tiles, SheetTile{Frame: last, OutMs: timeline.FrameStartMs(last, plan.FPS), Kind: TileLast})
	}
	if len(tiles) > MaxTiles {
		tiles = append(tiles[:MaxTiles-1], tiles[len(tiles)-1])
	}
	for i := range tiles {
		tiles[i].Index = i
	}
	return tiles, nil
}

// tileSize is the size of one tile: the video's shape with its longer side at most max pixels, and even.
func tileSize(out render.Size, tiles int) (w, h int) {
	// The more tiles, the smaller each one, so a long edit stays near the size target.
	limit := 384
	switch {
	case tiles > 36:
		limit = 224
	case tiles > 20:
		limit = 288
	}
	scale := float64(limit) / float64(max(out.Width, out.Height, 1))
	w = max(2, int(float64(out.Width)*scale+0.5)) &^ 1
	h = max(2, int(float64(out.Height)*scale+0.5)) &^ 1
	return w, h
}

// extractArgs is the ffmpeg command that writes the unique frames, in ascending order, as tile_001.png, tile_002.png ...,
// scaled to w x h. The input is the rendered MP4 (an untrusted file by the usual rules, whatever made it).
func extractArgs(frames []int64, w, h int) []string {
	var sel strings.Builder
	for i, f := range frames {
		if i > 0 {
			sel.WriteByte('+')
		}
		sel.WriteString("eq(n," + strconv.FormatInt(f, 10) + ")")
	}
	return []string{
		"ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-protocol_whitelist", "file", "-format_whitelist", media.UntrustedFormats,
		"-i", media.InputPath(0),
		"-vf", fmt.Sprintf("select='%s',scale=%d:%d:flags=area,format=rgb24", sel.String(), w, h),
		"-fps_mode", "passthrough", "-frames:v", strconv.Itoa(len(frames)),
		media.OutputPath("tile_%03d.png"),
	}
}

// makeSheet runs the frame extraction in the sandbox over mp4 and builds the sheet. dir is the job directory (for the
// extraction's output directory). A frame the video does not have means the render is not what its plan said: a fileError.
func (r *run) makeSheet(ctx context.Context, plan render.Plan, mp4 string) ([]byte, SheetLayout, error) {
	tiles, err := PlanTiles(plan)
	if err != nil {
		return nil, SheetLayout{}, fileErr(ReasonInvalidTimeline, err)
	}
	w, h := tileSize(plan.OutputSize, len(tiles))
	frames := make([]int64, len(tiles))
	for i, t := range tiles {
		frames[i] = t.Frame
	}
	slices.Sort(frames)
	frames = slices.Compact(frames)

	out, err := os.MkdirTemp(r.dir, "sheet-")
	if err != nil {
		return nil, SheetLayout{}, err
	}
	defer func() { _ = os.RemoveAll(out) }()
	args := extractArgs(frames, w, h)
	res, err := r.runner.Run(ctx, media.Invocation{
		Args: args, Inputs: []media.Input{{Host: mp4, Container: media.InputPath(0)}}, OutputDir: out, Timeout: SheetTimeout,
	})
	if err != nil {
		return nil, SheetLayout{}, fmt.Errorf("contact sheet: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, SheetLayout{}, toolFailure("contact sheet", res.ExitCode, res.Stderr)
	}

	byFrame := make(map[int64]image.Image, len(frames))
	for i, f := range frames {
		img, err := readTile(filepath.Join(out, fmt.Sprintf("tile_%03d.png", i+1)), w, h)
		if errors.Is(err, os.ErrNotExist) {
			return nil, SheetLayout{}, fileErr(ReasonRenderFailed, fmt.Errorf("the rendered video has no frame %d, but its plan has %d frames", f, plan.TotalFrames))
		}
		if err != nil {
			return nil, SheetLayout{}, fileErr(ReasonRenderFailed, fmt.Errorf("contact sheet frame %d: %w", f, err))
		}
		byFrame[f] = img
	}
	layout := SheetLayout{Columns: SheetColumns, TileWidth: w, TileHeight: h, Tiles: tiles}
	canvas := compose(layout, byFrame)
	data, err := encodeSheet(canvas)
	if err != nil {
		return nil, SheetLayout{}, err
	}
	return data, layout, nil
}

// readTile reads one PNG the sandbox wrote: a regular file of reasonable size whose dimensions are the tile's.
func readTile(path string, w, h int) (image.Image, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxTilePNG {
		return nil, errors.New("the frame is not a regular file of reasonable size")
	}
	b, err := os.ReadFile(path) //nolint:gosec // a file in our job directory, size checked above
	if err != nil {
		return nil, err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width != w || cfg.Height != h {
		return nil, fmt.Errorf("the frame is %dx%d, want %dx%d", cfg.Width, cfg.Height, w, h)
	}
	return png.Decode(bytes.NewReader(b))
}

// compose draws the tiles row by row on a black canvas, each with its timestamp.
func compose(l SheetLayout, frames map[int64]image.Image) *image.RGBA {
	n := len(l.Tiles)
	cols := min(l.Columns, n)
	rows := (n + l.Columns - 1) / l.Columns
	canvas := image.NewRGBA(image.Rect(0, 0, cols*l.TileWidth+(cols-1)*sheetGap, rows*l.TileHeight+(rows-1)*sheetGap))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	for _, t := range l.Tiles {
		x := (t.Index % l.Columns) * (l.TileWidth + sheetGap)
		y := (t.Index / l.Columns) * (l.TileHeight + sheetGap)
		r := image.Rect(x, y, x+l.TileWidth, y+l.TileHeight)
		draw.Draw(canvas, r, frames[t.Frame], image.Point{}, draw.Src)
		drawLabel(canvas, r, FormatTimestamp(t.OutMs))
	}
	return canvas
}

// FormatTimestamp is m:ss.mmm, the label of a tile.
func FormatTimestamp(ms int64) string {
	ms = max(ms, 0)
	return fmt.Sprintf("%d:%02d.%03d", ms/60000, ms/1000%60, ms%1000)
}

// encodeSheet writes the canvas as a JPEG, lowering the quality until it fits SheetTargetBytes (or the lowest quality is
// reached).
func encodeSheet(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	for q := 85; ; q -= 8 {
		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, fmt.Errorf("encode contact sheet: %w", err)
		}
		if buf.Len() <= SheetTargetBytes || q-8 < sheetMinQuality {
			return buf.Bytes(), nil
		}
	}
}

// ---- Timestamps -------------------------------------------------------------------------------------------------

// glyphs is a 5x7 pixel font for the characters of a timestamp. Each row is five characters wide, '#' is ink.
var glyphs = map[rune][7]string{
	'0': {".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."},
	'1': {"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'2': {".###.", "#...#", "....#", "...#.", "..#..", ".#...", "#####"},
	'3': {".###.", "#...#", "....#", "..##.", "....#", "#...#", ".###."},
	'4': {"...#.", "..##.", ".#.#.", "#..#.", "#####", "...#.", "...#."},
	'5': {"#####", "#....", "####.", "....#", "....#", "#...#", ".###."},
	'6': {".###.", "#....", "#....", "####.", "#...#", "#...#", ".###."},
	'7': {"#####", "....#", "...#.", "..#..", ".#...", ".#...", ".#..."},
	'8': {".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."},
	'9': {".###.", "#...#", "#...#", ".####", "....#", "....#", ".###."},
	':': {".....", "..#..", "..#..", ".....", "..#..", "..#..", "....."},
	'.': {".....", ".....", ".....", ".....", ".....", ".##..", ".##.."},
}

const (
	glyphW, glyphH = 5, 7
	glyphAdvance   = glyphW + 1
)

// drawLabel draws text in the top left corner of tile, white on a dark box, large enough to read on a small tile.
func drawLabel(dst *image.RGBA, tile image.Rectangle, text string) {
	scale := 2
	if tile.Dx() >= 240 {
		scale = 3
	}
	pad := 2 * scale
	textW := len([]rune(text))*glyphAdvance*scale - scale
	box := image.Rect(tile.Min.X, tile.Min.Y, min(tile.Min.X+textW+2*pad, tile.Max.X), min(tile.Min.Y+glyphH*scale+2*pad, tile.Max.Y))
	draw.Draw(dst, box, image.NewUniform(color.RGBA{0, 0, 0, 200}), image.Point{}, draw.Over)
	x := box.Min.X + pad
	for _, ch := range text {
		g, ok := glyphs[ch]
		if !ok {
			x += glyphAdvance * scale
			continue
		}
		for row, line := range g {
			for col, c := range line {
				if c != '#' {
					continue
				}
				px := image.Rect(x+col*scale, box.Min.Y+pad+row*scale, x+(col+1)*scale, box.Min.Y+pad+(row+1)*scale).Intersect(box)
				draw.Draw(dst, px, image.NewUniform(color.White), image.Point{}, draw.Src)
			}
		}
		x += glyphAdvance * scale
	}
}
