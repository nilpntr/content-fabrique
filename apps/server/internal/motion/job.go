package motion

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"sort"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Job is what cf-motion renders (packages/media-tools/motion-runner/job.go is the reader; the two are kept equal by
// TestJobMatchesRunnerContract, which writes the runner's testdata/job.json).
type Job struct {
	Version   int       `json:"version"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	CSSWidth  int       `json:"css_width"`
	CSSHeight int       `json:"css_height"`
	FPS       int       `json:"fps"`
	Seed      uint32    `json:"seed"`
	Segments  []Segment `json:"segments"`
	Fonts     []Font    `json:"fonts"`
	Items     []Item    `json:"items"`
}

// Segment is a run of frames [StartFrame, EndFrame) that has overlays, and the part of the frame (Clip, in frame pixels) they are in.
type Segment struct {
	StartFrame int64     `json:"start_frame"`
	EndFrame   int64     `json:"end_frame"`
	Clip       PixelRect `json:"clip"`
}

// Frames is the number of frames of the segment.
func (s Segment) Frames() int64 { return s.EndFrame - s.StartFrame }

// PixelRect is a rectangle in frame pixels.
type PixelRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Font is a font the page loads, by family name and file name (in the fonts directory of the sandbox).
type Font struct {
	Family string `json:"family"`
	File   string `json:"file"`
}

// Item is one overlay of the job.
type Item struct {
	ID       string     `json:"id"`
	Template string     `json:"template"`
	StartMs  int64      `json:"start_ms"`
	EndMs    int64      `json:"end_ms"`
	Box      Box        `json:"box"`
	InkAtMs  []int64    `json:"ink_at_ms"`
	Params   PageParams `json:"params"`
}

// Box is a rectangle in layout (CSS) pixels.
type Box struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// SpillMargin is how far outside its box an overlay may draw (a blur's fringe, an overshoot), as a share of the layout's height. The
// clip of a segment is the union of its boxes grown by it; the runner fails a frame that draws further out.
const SpillMargin = 0.02

// MaxSegments is the most segments a job has; overlays close together share one.
const MaxSegments = 64

// BuildJob makes the job for items on an output rendered at w x h pixels (the profile's size) and fps.
//
// The frames are always the output's own size (a device scale factor of 1): Chrome's software raster is deterministic at 1 and not at
// a fractional factor, so a smaller render (the preview) scales the frames in ffmpeg (render.overlayGraph).
func BuildJob(out timeline.Output, fps int, totalFrames int64, items []Resolved) (Job, error) {
	if len(items) == 0 {
		return Job{}, fmt.Errorf("motion: no items")
	}
	j := Job{Version: 1, Width: out.Width, Height: out.Height, CSSWidth: out.Width, CSSHeight: out.Height, FPS: fps}
	sorted := slices.Clone(items)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Item.AtMs < sorted[b].Item.AtMs })
	hash := fnv.New32a()
	fonts := map[string]style.Font{}
	for _, r := range sorted {
		_, _ = hash.Write([]byte(r.Item.ID + "\x00"))
		fonts[r.Font.File] = r.Font
		b := r.Box
		j.Items = append(j.Items, Item{
			ID: r.Item.ID, Template: r.Item.Template, StartMs: r.Item.AtMs, EndMs: r.Item.EndMs(),
			Box:     Box{X: float64(b.Left), Y: float64(b.Top), W: float64(b.Width()), H: float64(b.Height())},
			InkAtMs: r.InkTimesMs(), Params: r.Params,
		})
	}
	j.Seed = hash.Sum32()
	for _, f := range sortedFonts(fonts) {
		j.Fonts = append(j.Fonts, Font{Family: f.Family, File: f.File})
	}
	j.Segments = segments(j, sorted, totalFrames)
	return j, nil
}

func sortedFonts(m map[string]style.Font) []style.Font {
	out := make([]style.Font, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].File < out[b].File })
	return out
}

// segments merges the frame ranges of the items into runs (overlapping or touching ranges become one, and the closest are merged
// until there are at most MaxSegments), each with the union of its items' boxes as its clip.
func segments(j Job, items []Resolved, total int64) []Segment {
	type run struct {
		start, end int64
		boxes      []style.Rect
	}
	var runs []run
	for _, r := range items {
		s, e := timeline.Frame(r.Item.AtMs, j.FPS), min(total, timeline.Frame(r.Item.EndMs(), j.FPS))
		if e <= s {
			continue
		}
		if n := len(runs); n > 0 && s <= runs[n-1].end {
			runs[n-1].end = max(runs[n-1].end, e)
			runs[n-1].boxes = append(runs[n-1].boxes, r.Box)
			continue
		}
		runs = append(runs, run{start: s, end: e, boxes: []style.Rect{r.Box}})
	}
	for len(runs) > MaxSegments {
		best, gap := 0, int64(math.MaxInt64)
		for i := 0; i+1 < len(runs); i++ {
			if g := runs[i+1].start - runs[i].end; g < gap {
				best, gap = i, g
			}
		}
		runs[best].end = runs[best+1].end
		runs[best].boxes = append(runs[best].boxes, runs[best+1].boxes...)
		runs = slices.Delete(runs, best+1, best+2)
	}
	scale := float64(j.Width) / float64(j.CSSWidth)
	margin := SpillMargin * float64(j.CSSHeight) * scale
	out := make([]Segment, 0, len(runs))
	for _, r := range runs {
		u := r.boxes[0]
		for _, b := range r.boxes[1:] {
			u = style.Rect{Left: min(u.Left, b.Left), Top: min(u.Top, b.Top), Right: max(u.Right, b.Right), Bottom: max(u.Bottom, b.Bottom)}
		}
		x0 := max(0, evenDown(float64(u.Left)*scale-margin))
		y0 := max(0, evenDown(float64(u.Top)*scale-margin))
		x1 := min(j.Width&^1, evenUp(float64(u.Right)*scale+margin))
		y1 := min(j.Height&^1, evenUp(float64(u.Bottom)*scale+margin))
		out = append(out, Segment{StartFrame: r.start, EndFrame: r.end, Clip: PixelRect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}})
	}
	return out
}

func evenDown(f float64) int { return int(math.Floor(f/2)) * 2 }
func evenUp(f float64) int   { return int(math.Ceil(f/2)) * 2 }

// Encode writes the job as the runner reads it.
func (j Job) Encode() ([]byte, error) { return json.MarshalIndent(j, "", " ") }

// TotalFrames is the number of frames the job renders.
func (j Job) TotalFrames() int64 {
	var n int64
	for _, s := range j.Segments {
		n += s.Frames()
	}
	return n
}
