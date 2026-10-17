package editor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The rendered brightness of a version (check_edit's `crushed` and `brightness_jump` checks). The source analysis says how bright the
// footage is, not how bright the render is: a look can change the picture a lot. The first live Opus run put the gym_moody look on
// every clip of low-key footage and the grade crushed it to near black (a rendered mean of 5 of 255 against 17 for the same shot
// ungraded), and a check that read only the source analysis passed it. So the checks also look at the preview render of the version,
// in the same sandbox the frames come from.

// ErrNoPreview is what a RenderSampler returns when the version has no ready preview render (yet).
var ErrNoPreview = errors.New("editor: the version has no preview render")

// RenderSampler measures the preview render of a version. *RenderMeasurer is the real one.
type RenderSampler interface {
	// Rendered returns the luma of the preview render of the version, a sample every media.LumaStatsStepMs from the first frame: the
	// mean in Luma and the 90th percentile in High, 8-bit limited range like the analysis series, so analysis.ExposureOf applies.
	// ErrNoPreview when there is no ready preview of this version.
	Rendered(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (analysis.Series, error)
}

// RenderStore is what the measurer needs from the database. *store.Store is the real one.
type RenderStore interface {
	ListRenders(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, f store.RenderFilter, limit int) ([]store.Render, error)
}

// RenderMeasurer measures preview renders with ffmpeg in the sandbox.
type RenderMeasurer struct {
	Store   RenderStore
	Objects FrameObjects
	// Runner runs ffmpeg: in production a media.SandboxRunner over the media-tools image.
	Runner media.Runner
	// TempDir must be inside the sandbox provider's allowed roots, without symlinks in its path.
	TempDir string
	Logger  *slog.Logger
}

var _ RenderSampler = (*RenderMeasurer)(nil)

const (
	maxRenderBytes  = 1 << 30
	maxStatsBytes   = 16 << 20
	renderStatsTime = 60 * time.Second
)

// previewOf is the newest ready preview render of a version that still has its files, or ErrNoPreview.
func (m *RenderMeasurer) previewOf(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (*store.Render, error) {
	renders, err := m.Store.ListRenders(ctx, ws, project, store.RenderFilter{Version: version, Profile: store.ProfilePreview}, 5)
	if err != nil {
		return nil, fmt.Errorf("editor: list renders: %w", err)
	}
	for i := range renders { // newest first
		if renders[i].Status == store.RenderReady && renders[i].Mp4Key != nil && renders[i].ObjectsPurgedAt == nil {
			return &renders[i], nil
		}
	}
	return nil, ErrNoPreview
}

// Rendered implements RenderSampler.
func (m *RenderMeasurer) Rendered(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (analysis.Series, error) {
	r, err := m.previewOf(ctx, ws, project, version)
	if err != nil {
		return analysis.Series{}, err
	}
	want, err := storage.RenderKey(ws, project, r.ID, storage.RenderMP4Name)
	if err != nil || *r.Mp4Key != want {
		return analysis.Series{}, fmt.Errorf("editor: render %s has a video key that is not its own", r.ID)
	}
	dir, err := os.MkdirTemp(m.TempDir, "renderstats-")
	if err != nil {
		return analysis.Series{}, fmt.Errorf("editor: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Mkdir(filepath.Join(dir, "in"), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return analysis.Series{}, err
	}
	local := filepath.Join(dir, "in", "render")
	if err := m.download(ctx, want, local); err != nil {
		return analysis.Series{}, err
	}
	out, err := os.MkdirTemp(dir, "out-")
	if err != nil {
		return analysis.Series{}, err
	}
	res, err := m.Runner.Run(ctx, media.Invocation{
		Args: media.LumaStatsArgs(media.InputPath(0)), Inputs: []media.Input{{Host: local, Container: media.InputPath(0)}},
		OutputDir: out, Timeout: renderStatsTime,
	})
	if err != nil {
		return analysis.Series{}, fmt.Errorf("editor: ffmpeg: %w", err)
	}
	if res.ExitCode != 0 {
		return analysis.Series{}, &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	path := filepath.Join(out, media.LumaStatsFile)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxStatsBytes {
		return analysis.Series{}, errors.New("editor: ffmpeg wrote no usable luma statistics")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a file this process just checked, in its job dir
	if err != nil {
		return analysis.Series{}, err
	}
	return ParseLumaStats(data)
}

// ParseLumaStats reads ffmpeg's metadata print of signalstats ("frame:12 pts:... \n lavfi.signalstats.YAVG=33.5 ...") into a series
// with the mean luma (YAVG) and the 90th percentile luma (YHIGH) of each sampled frame.
func ParseLumaStats(data []byte) (analysis.Series, error) {
	s := analysis.Series{StepMs: media.LumaStatsStepMs}
	var avg, high float64
	var haveAvg, haveHigh, inFrame bool
	flush := func() {
		if inFrame && haveAvg && haveHigh {
			s.Luma, s.High = append(s.Luma, avg), append(s.High, high)
		}
		avg, high, haveAvg, haveHigh = 0, 0, false, false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "frame:"):
			flush()
			inFrame = true
		case strings.HasPrefix(line, "lavfi.signalstats.YAVG="), strings.HasPrefix(line, "lavfi.signalstats.YHIGH="):
			k, v, _ := strings.Cut(line, "=")
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return analysis.Series{}, fmt.Errorf("editor: bad luma statistic %q", line)
			}
			if strings.HasSuffix(k, "YAVG") {
				avg, haveAvg = f, true
			} else {
				high, haveHigh = f, true
			}
		}
		if len(s.Luma) > media.MaxLumaSamples {
			return analysis.Series{}, errors.New("editor: too many luma samples")
		}
	}
	if err := sc.Err(); err != nil {
		return analysis.Series{}, err
	}
	flush()
	if len(s.Luma) == 0 {
		return analysis.Series{}, errors.New("editor: the luma statistics hold no frame")
	}
	return s, nil
}

func (m *RenderMeasurer) download(ctx context.Context, key, path string) error {
	return m.fetch(ctx, key, path, maxRenderBytes)
}

// fetch downloads an object of at most limit bytes to a new file.
func (m *RenderMeasurer) fetch(ctx context.Context, key, path string, limit int64) error {
	obj, err := m.Objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("editor: download the render: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size <= 0 || obj.Size > limit {
		return fmt.Errorf("editor: the render is %d bytes, which is not plausible", obj.Size)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- a path in our job dir; the container user must read it
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(obj.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("editor: download the render: %w", err)
	}
	if n != obj.Size {
		return fmt.Errorf("editor: the render download is %d bytes, storage reported %d", n, obj.Size)
	}
	return nil
}
