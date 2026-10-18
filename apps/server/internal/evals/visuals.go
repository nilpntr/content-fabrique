package evals

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The judge's pictures: the contact sheet the render job made, strips of the frames just before and after each cut of the render, and a
// contact sheet of each of the creator's reference videos. Frames are cut by ffmpeg in the media sandbox; the reference videos are local
// files and stay local (only these small sheets go to the model).

// Objects reads object storage. *storage.Client is the real one.
type Objects interface {
	Get(ctx context.Context, key string) (storage.Object, error)
}

// Renders lists a project's renders. *store.Store is the real one.
type Renders interface {
	ListRenders(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, f store.RenderFilter, limit int) ([]store.Render, error)
}

// Pictures cuts the judge's pictures.
type Pictures struct {
	Renders Renders
	Objects Objects
	Runner  media.Runner
	// TempDir is inside the sandbox's allowed roots.
	TempDir string
}

// Limits of the pictures.
const (
	maxSheetBytes  = 16 << 20
	maxRenderBytes = 1 << 30
	stripCuts      = 12  // cuts shown, at most
	stripAroundMs  = 200 // before and after the cut
	stripLongSide  = 320
	refFrames      = 6
	refLongSide    = 320
	maxReferences  = 3
)

// ErrNoRender says the version has no ready preview render.
var ErrNoRender = errors.New("the version has no ready preview render")

// Render returns the version's contact sheet and the strips around its cuts.
func (p *Pictures) Render(ctx context.Context, t Target, spec *timeline.Spec) (*Picture, []Picture, error) {
	list, err := p.Renders.ListRenders(ctx, t.Workspace, t.Project, store.RenderFilter{Version: t.Version, Profile: store.ProfilePreview}, 5)
	if err != nil {
		return nil, nil, err
	}
	var r *store.Render
	for i := range list {
		if list[i].Status == store.RenderReady && list[i].ContactSheetKey != nil && list[i].Mp4Key != nil && list[i].ObjectsPurgedAt == nil {
			r = &list[i]
			break
		}
	}
	if r == nil {
		return nil, nil, ErrNoRender
	}
	sheet, err := p.read(ctx, *r.ContactSheetKey, maxSheetBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("the contact sheet: %w", err)
	}
	render := &Picture{Label: fmt.Sprintf("The edit's contact sheet: frames from the whole %.1f s video, in order, each labelled with its time.", float64(timeline.TotalDuration(spec))/1000), JPEG: sheet}
	cuts := cutTimes(spec)
	if len(cuts) == 0 || p.Runner == nil {
		return render, nil, nil
	}
	dir, err := os.MkdirTemp(p.TempDir, "judge-")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	mp4 := filepath.Join(dir, "render.mp4")
	if err := p.download(ctx, *r.Mp4Key, mp4); err != nil {
		return nil, nil, fmt.Errorf("the render: %w", err)
	}
	var times []int64
	for _, c := range cuts {
		times = append(times, max(c-stripAroundMs, 0), c+stripAroundMs)
	}
	frames, err := p.frames(ctx, mp4, dir, times, stripLongSide)
	if err != nil {
		return nil, nil, err
	}
	var strips []Picture
	for i := 0; i < len(cuts); i += 4 {
		end := min(i+4, len(cuts))
		var row []image.Image
		var at []string
		for k := i; k < end; k++ {
			row = append(row, frames[2*k], frames[2*k+1])
			at = append(at, fmt.Sprintf("%.2f s", float64(cuts[k])/1000))
		}
		img, err := grid(row, len(row))
		if err != nil {
			return nil, nil, err
		}
		strips = append(strips, Picture{Label: fmt.Sprintf("Cuts at %s: for each cut, the frame %d ms before it, then the frame %d ms after it, left to right.", strings.Join(at, ", "), stripAroundMs, stripAroundMs), JPEG: img})
	}
	return render, strips, nil
}

// cutTimes are the output times of the main track's cuts, at most stripCuts, spread over the video.
func cutTimes(spec *timeline.Spec) []int64 {
	main, _, ok := timeline.MainTrack(spec)
	if !ok {
		return nil
	}
	var cuts []int64
	for i, it := range main.Items {
		if i > 0 {
			cuts = append(cuts, it.AtMs)
		}
	}
	if len(cuts) <= stripCuts {
		return cuts
	}
	out := make([]int64, 0, stripCuts)
	for k := range stripCuts {
		out = append(out, cuts[k*(len(cuts)-1)/(stripCuts-1)])
	}
	return slices.Compact(out)
}

// References returns a contact sheet of each of the creator's reference videos in dir: the ones the style profile names first, then
// the others, at most maxReferences.
func (p *Pictures) References(ctx context.Context, dir string, names []string) ([]Picture, error) {
	if dir == "" || p.Runner == nil {
		return nil, nil
	}
	if strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[2:])
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("the reference videos: %w", err)
	}
	var files []string
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.Type().IsRegular() && (ext == ".mp4" || ext == ".mov" || ext == ".m4v") {
			files = append(files, e.Name())
		}
	}
	named := map[string]bool{}
	for _, n := range names {
		named[strings.TrimSpace(n)] = true
	}
	sort.SliceStable(files, func(i, j int) bool { return named[files[i]] && !named[files[j]] })
	if len(files) > maxReferences {
		files = files[:maxReferences]
	}
	tmp, err := os.MkdirTemp(p.TempDir, "judgeref-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	var out []Picture
	for i, f := range files {
		// The references are short; frames past the end are simply not cut.
		times := []int64{500, 2000, 4000, 6500, 9500, 13000}
		frames, err := p.framesLoose(ctx, filepath.Join(dir, f), filepath.Join(tmp, fmt.Sprint(i)), times[:refFrames], refLongSide)
		if err != nil || len(frames) == 0 {
			continue
		}
		img, err := grid(frames, 3)
		if err != nil {
			return nil, err
		}
		out = append(out, Picture{Label: fmt.Sprintf("One of the creator's own finished videos (reference %d): frames at 0.5, 2, 4, 6.5, 9.5 and 13 s.", i+1), JPEG: img})
	}
	return out, nil
}

func (p *Pictures) read(ctx context.Context, key string, limit int64) ([]byte, error) {
	obj, err := p.Objects.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size <= 0 || obj.Size > limit {
		return nil, fmt.Errorf("the object is %d bytes", obj.Size)
	}
	return io.ReadAll(io.LimitReader(obj.Body, limit))
}

func (p *Pictures) download(ctx context.Context, key, path string) error {
	obj, err := p.Objects.Get(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size <= 0 || obj.Size > maxRenderBytes {
		return fmt.Errorf("the render is %d bytes", obj.Size)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- our temp dir; the container user reads it
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(obj.Body, maxRenderBytes))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// frames cuts the frames at times (all of them, or an error) from a file.
func (p *Pictures) frames(ctx context.Context, path, dir string, times []int64, longSide int) ([]image.Image, error) {
	var out []image.Image
	for start := 0; start < len(times); start += media.MaxFrameBatch {
		batch := times[start:min(start+media.MaxFrameBatch, len(times))]
		imgs, err := p.cut(ctx, path, filepath.Join(dir, fmt.Sprint("b", start)), batch, longSide)
		if err != nil {
			return nil, err
		}
		for i, img := range imgs {
			if img == nil {
				return nil, fmt.Errorf("no frame at %d ms", batch[i])
			}
		}
		out = append(out, imgs...)
	}
	return out, nil
}

// framesLoose is frames, keeping the ones that were cut.
func (p *Pictures) framesLoose(ctx context.Context, path, dir string, times []int64, longSide int) ([]image.Image, error) {
	imgs, err := p.cut(ctx, path, dir, times, longSide)
	if err != nil {
		return nil, err
	}
	var out []image.Image
	for _, img := range imgs {
		if img != nil {
			out = append(out, img)
		}
	}
	return out, nil
}

func (p *Pictures) cut(ctx context.Context, path, dir string, times []int64, longSide int) ([]image.Image, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	specs := make([]media.FrameSpec, len(times))
	for i, t := range times {
		specs[i] = media.FrameSpec{AtMs: t, Output: media.OutputPath(fmt.Sprintf("f%02d.jpg", i))}
	}
	args, err := media.FramesArgs(media.InputPath(0), specs, longSide)
	if err != nil {
		return nil, err
	}
	if _, err := p.Runner.Run(ctx, media.Invocation{Args: args, Inputs: []media.Input{{Host: abs, Container: media.InputPath(0)}}, OutputDir: dir, Timeout: 2 * time.Minute}); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	out := make([]image.Image, len(times))
	for i := range times {
		raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("f%02d.jpg", i))) // #nosec G304 -- our job dir
		if err != nil {
			continue
		}
		if img, err := jpeg.Decode(bytes.NewReader(raw)); err == nil {
			out[i] = img
		}
	}
	return out, nil
}

// grid lays images out in rows of cols, each cell the size of the largest image, on black, as a JPEG.
func grid(imgs []image.Image, cols int) ([]byte, error) {
	if len(imgs) == 0 || cols < 1 {
		return nil, errors.New("no images to lay out")
	}
	cw, ch := 0, 0
	for _, im := range imgs {
		cw, ch = max(cw, im.Bounds().Dx()), max(ch, im.Bounds().Dy())
	}
	const gap = 6
	rows := (len(imgs) + cols - 1) / cols
	dst := image.NewRGBA(image.Rect(0, 0, cols*cw+(cols-1)*gap, rows*ch+(rows-1)*gap))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.Black}, image.Point{}, draw.Src)
	for i, im := range imgs {
		x, y := (i%cols)*(cw+gap), (i/cols)*(ch+gap)
		draw.Draw(dst, image.Rect(x, y, x+im.Bounds().Dx(), y+im.Bounds().Dy()), im, im.Bounds().Min, draw.Src)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
