package editor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
)

var _ OverlaySampler = (*RenderMeasurer)(nil)

// RenderedRegions implements OverlaySampler: it downloads the version's preview and measures each probe's region on its frame in one
// ffmpeg run in the sandbox (media.RegionStatsArgs). A region whose frame the render does not have is left out of the answer.
func (m *RenderMeasurer) RenderedRegions(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int, probes []RegionProbe) ([]RegionLuma, error) {
	if len(probes) == 0 {
		return nil, nil
	}
	if len(probes) > media.MaxRegions {
		probes = probes[:media.MaxRegions]
	}
	r, err := m.previewOf(ctx, ws, project, version)
	if err != nil {
		return nil, err
	}
	want, err := storage.RenderKey(ws, project, r.ID, storage.RenderMP4Name)
	if err != nil || *r.Mp4Key != want {
		return nil, fmt.Errorf("editor: render %s has a video key that is not its own", r.ID)
	}
	dir, err := os.MkdirTemp(m.TempDir, "regionstats-")
	if err != nil {
		return nil, fmt.Errorf("editor: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Mkdir(filepath.Join(dir, "in"), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return nil, err
	}
	local := filepath.Join(dir, "in", "render")
	if err := m.download(ctx, want, local); err != nil {
		return nil, err
	}
	out, err := os.MkdirTemp(dir, "out-")
	if err != nil {
		return nil, err
	}
	crops := make([]media.RegionCrop, len(probes))
	for i, p := range probes {
		crops[i] = media.RegionCrop{Frame: p.Frame, X: p.X, Y: p.Y, W: p.W, H: p.H}
	}
	args, err := media.RegionStatsArgs(media.InputPath(0), crops)
	if err != nil {
		return nil, err
	}
	res, err := m.Runner.Run(ctx, media.Invocation{Args: args, Inputs: []media.Input{{Host: local, Container: media.InputPath(0)}}, OutputDir: out, Timeout: renderStatsTime})
	if err != nil {
		return nil, fmt.Errorf("editor: ffmpeg: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	var got []RegionLuma
	for i, p := range probes {
		path := filepath.Join(out, media.RegionStatsFile(i))
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxStatsBytes {
			continue // the render has no such frame
		}
		data, err := os.ReadFile(path) // #nosec G304 -- a file this process just checked, in its job dir
		if err != nil {
			return nil, err
		}
		l, err := ParseRegionStats(data)
		if errors.Is(err, errNoRegionFrame) {
			continue
		}
		if err != nil {
			return nil, err
		}
		l.Probe = p
		got = append(got, l)
	}
	return got, nil
}

var errNoRegionFrame = errors.New("editor: the region statistics hold no frame")

// ParseRegionStats reads the signalstats of one region (YLOW, YHIGH and YAVG of its first frame).
func ParseRegionStats(data []byte) (RegionLuma, error) {
	var l RegionLuma
	seen := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() && seen < 3 {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		var dst *float64
		switch k {
		case "lavfi.signalstats.YLOW":
			dst = &l.Low
		case "lavfi.signalstats.YHIGH":
			dst = &l.High
		case "lavfi.signalstats.YAVG":
			dst = &l.Avg
		default:
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 255 {
			return RegionLuma{}, fmt.Errorf("editor: bad region statistic %q", line)
		}
		*dst = f
		seen++
	}
	if err := sc.Err(); err != nil {
		return RegionLuma{}, err
	}
	if seen < 3 {
		return RegionLuma{}, errNoRegionFrame
	}
	return l, nil
}
