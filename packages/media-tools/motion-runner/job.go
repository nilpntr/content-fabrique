package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
)

// Job is what the worker asks the runner to render (docs/design/motion-graphics-v1.md section 4). The worker writes it
// into the job's work directory; the runner reads it strictly (unknown fields are refused) and checks every bound before
// Chrome starts, because a bug in the worker must not turn into a runaway browser.
//
// The items are passed to the page as they are: the runner does not interpret a template's params beyond checking that
// they are a small JSON object. Every text in them is set with textContent by the templates, never parsed as markup.
type Job struct {
	Version int `json:"version"`
	// Width and Height are the pixel size of the frames (the render profile's size). CSSWidth and CSSHeight are the size the
	// templates lay out in (the spec's output size); the device scale factor is Width/CSSWidth.
	Width     int `json:"width"`
	Height    int `json:"height"`
	CSSWidth  int `json:"css_width"`
	CSSHeight int `json:"css_height"`
	FPS       int `json:"fps"`
	// Seed seeds the page's Math.random, so a template that wants jitter gets the same jitter on every render.
	Seed     uint32    `json:"seed"`
	Segments []Segment `json:"segments"`
	Fonts    []Font    `json:"fonts"`
	Items    []Item    `json:"items"`
}

// Segment is a run of frames to render, [StartFrame, EndFrame) in the timeline's frame numbers. Frames outside every
// segment have no overlay and are not rendered at all.
type Segment struct {
	StartFrame int64 `json:"start_frame"`
	EndFrame   int64 `json:"end_frame"`
	// Clip is the part of the frame that is captured, in frame pixels: the union of the boxes of the segment's items, grown by
	// the spill margin, on even coordinates. Only it is encoded and composited, so a small title costs a small capture.
	Clip PixelRect `json:"clip"`
}

// PixelRect is a rectangle in frame pixels.
type PixelRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Font is a font file the page loads (as bytes, never by URL) under a family name.
type Font struct {
	Family string `json:"family"`
	File   string `json:"file"`
}

// Item is one overlay: a template, its time on the timeline, its box and its resolved params.
type Item struct {
	ID       string `json:"id"`
	Template string `json:"template"`
	StartMs  int64  `json:"start_ms"`
	EndMs    int64  `json:"end_ms"`
	// Box is where the template may draw, in CSS pixels of the layout. The blank-frame and spill checks use it.
	Box Box `json:"box"`
	// InkAtMs are times (timeline ms) at which the template is fully on screen: the runner checks that the frame there has
	// ink inside the box, and fails the job when it is blank.
	InkAtMs []int64         `json:"ink_at_ms"`
	Params  json.RawMessage `json:"params"`
}

// Box is a rectangle in CSS pixels.
type Box struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Bounds of a job. They are generous for a 180 s edit at 60 fps and tight enough that a broken job fails before Chrome starts.
const (
	jobVersion     = 1
	maxSide        = 2160
	maxFrames      = 180 * 60
	maxItems       = 64
	maxSegments    = 64
	maxFonts       = 8
	maxParamsBytes = 16 << 10
	maxInkChecks   = 8
	maxTimeMs      = 180_000
)

var (
	idRE       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	templateRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	familyRE   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ]{0,39}$`)
	fontFileRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,78}\.(ttf|otf)$`)
)

// parseJob decodes and checks a job.
func parseJob(data []byte) (*Job, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var j Job
	if err := dec.Decode(&j); err != nil {
		return nil, fmt.Errorf("job: %w", err)
	}
	if dec.More() {
		return nil, errors.New("job: trailing data after the job object")
	}
	if err := j.check(); err != nil {
		return nil, fmt.Errorf("job: %w", err)
	}
	return &j, nil
}

// Scale is the device scale factor: frame pixels per CSS pixel.
func (j *Job) Scale() float64 { return float64(j.Width) / float64(j.CSSWidth) }

// FrameMs is the time of frame n in milliseconds, as a float: n x 1000 / fps exactly.
func (j *Job) FrameMs(n int64) float64 { return float64(n) * 1000 / float64(j.FPS) }

// FrameAt is the frame on screen at time ms: the last frame that starts at or before it.
func (j *Job) FrameAt(ms int64) int64 { return int64(math.Floor(float64(ms)*float64(j.FPS)/1000 + 1e-9)) }

// TotalFrames is the number of frames the job renders.
func (j *Job) TotalFrames() int64 {
	var n int64
	for _, s := range j.Segments {
		n += s.EndFrame - s.StartFrame
	}
	return n
}

func (j *Job) check() error {
	if j.Version != jobVersion {
		return fmt.Errorf("version %d, want %d", j.Version, jobVersion)
	}
	switch j.FPS {
	case 24, 25, 30, 60:
	default:
		return fmt.Errorf("fps %d is not one of 24, 25, 30, 60", j.FPS)
	}
	for _, v := range []int{j.Width, j.Height, j.CSSWidth, j.CSSHeight} {
		if v < 16 || v > maxSide {
			return fmt.Errorf("size %dx%d (layout %dx%d) is outside 16 to %d", j.Width, j.Height, j.CSSWidth, j.CSSHeight, maxSide)
		}
	}
	if j.Width > j.CSSWidth || j.Height > j.CSSHeight {
		return fmt.Errorf("frames of %dx%d are bigger than the %dx%d layout", j.Width, j.Height, j.CSSWidth, j.CSSHeight)
	}
	// The frame is the layout scaled by one factor; the height may be rounded up to an even number by the render profile.
	if want := float64(j.CSSHeight) * j.Scale(); math.Abs(want-float64(j.Height)) > 1.01 {
		return fmt.Errorf("frames of %dx%d are not the %dx%d layout scaled by one factor", j.Width, j.Height, j.CSSWidth, j.CSSHeight)
	}
	if len(j.Segments) == 0 || len(j.Segments) > maxSegments {
		return fmt.Errorf("%d segments, want 1 to %d", len(j.Segments), maxSegments)
	}
	last := int64(-1)
	for i, s := range j.Segments {
		if s.StartFrame < 0 || s.EndFrame <= s.StartFrame || s.StartFrame < last {
			return fmt.Errorf("segment %d [%d, %d) is empty, negative or overlaps the one before it", i, s.StartFrame, s.EndFrame)
		}
		c := s.Clip
		if c.X < 0 || c.Y < 0 || c.W < 2 || c.H < 2 || c.X+c.W > j.Width || c.Y+c.H > j.Height || c.X%2 != 0 || c.Y%2 != 0 || c.W%2 != 0 || c.H%2 != 0 {
			return fmt.Errorf("segment %d: the clip %+v is not an even rectangle inside the %dx%d frame", i, c, j.Width, j.Height)
		}
		last = s.EndFrame
	}
	if last > maxFrames || j.TotalFrames() > maxFrames {
		return fmt.Errorf("the segments reach frame %d, over the %d frames a job may render", last, maxFrames)
	}
	if len(j.Fonts) > maxFonts {
		return fmt.Errorf("%d fonts, at most %d", len(j.Fonts), maxFonts)
	}
	for _, f := range j.Fonts {
		if !familyRE.MatchString(f.Family) || !fontFileRE.MatchString(f.File) {
			return fmt.Errorf("font %q from %q: the family or the file name is not allowed", f.Family, f.File)
		}
	}
	if len(j.Items) == 0 || len(j.Items) > maxItems {
		return fmt.Errorf("%d items, want 1 to %d", len(j.Items), maxItems)
	}
	seen := map[string]bool{}
	for _, it := range j.Items {
		if err := it.check(j); err != nil {
			return fmt.Errorf("item %q: %w", it.ID, err)
		}
		if seen[it.ID] {
			return fmt.Errorf("item %q appears twice", it.ID)
		}
		seen[it.ID] = true
	}
	return nil
}

func (it *Item) check(j *Job) error {
	if !idRE.MatchString(it.ID) {
		return errors.New("the id is not allowed")
	}
	if !templateRE.MatchString(it.Template) {
		return errors.New("the template name is not allowed")
	}
	if it.StartMs < 0 || it.EndMs <= it.StartMs || it.EndMs > maxTimeMs {
		return fmt.Errorf("times [%d, %d) are empty or outside 0 to %d ms", it.StartMs, it.EndMs, maxTimeMs)
	}
	b := it.Box
	for _, v := range []float64{b.X, b.Y, b.W, b.H} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("the box is not finite")
		}
	}
	if b.W <= 0 || b.H <= 0 || b.X < 0 || b.Y < 0 || b.X+b.W > float64(j.CSSWidth)+0.5 || b.Y+b.H > float64(j.CSSHeight)+0.5 {
		return fmt.Errorf("the box %+v is empty or outside the %dx%d layout", b, j.CSSWidth, j.CSSHeight)
	}
	if len(it.InkAtMs) == 0 || len(it.InkAtMs) > maxInkChecks {
		return fmt.Errorf("%d ink checks, want 1 to %d", len(it.InkAtMs), maxInkChecks)
	}
	for _, t := range it.InkAtMs {
		if t < it.StartMs || t >= it.EndMs {
			return fmt.Errorf("ink check at %d ms is outside the item", t)
		}
	}
	if len(it.Params) == 0 || len(it.Params) > maxParamsBytes {
		return fmt.Errorf("params are empty or over %d bytes", maxParamsBytes)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(it.Params, &obj); err != nil {
		return fmt.Errorf("params are not a JSON object: %w", err)
	}
	return nil
}

// covers reports whether frame n is inside one of the segments.
func (j *Job) covers(n int64) bool {
	i := sort.Search(len(j.Segments), func(i int) bool { return j.Segments[i].EndFrame > n })
	return i < len(j.Segments) && j.Segments[i].StartFrame <= n
}
