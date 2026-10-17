package editor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Frames: the stills the editor looks at (docs/design/agent-runtime-v1.md section 4). They are cut from the asset's 720 px proxy
// by ffmpeg in the sandbox, scaled to 384 px on the long side, and cached in object storage under the asset's own prefix
// (ws/{workspace}/assets/{asset}/frames/), so a frame is made once however many runs look at it.

// Sizes of the frames.
const (
	// FrameLongSide is the long side of a frame in pixels.
	FrameLongSide = 384
	// MaxViewFrames is the most frames one view_clip call may ask for with at_ms.
	MaxViewFrames = 8
	// OverviewStepMs is the spacing of the overview, and MaxOverviewFrames its length: a frame every two seconds, at most twelve
	// (a longer clip is sampled evenly across its length instead).
	OverviewStepMs     = 2000
	MaxOverviewFrames  = 12
	maxFrameFileBytes  = 2 << 20
	maxProxyBytes      = 2 << 30
	frameBaseTimeout   = 30 * time.Second
	frameTimeoutPerOne = 5 * time.Second
)

// Frame is a still frame of a clip in object storage.
type Frame struct {
	// AtMs is the time in the clip.
	AtMs int64
	// Ref is the object key (ws/{workspace}/assets/{asset}/frames/...): what goes into an image block.
	Ref string
}

// FrameSource makes the frames of a clip. *FrameService is the real one.
type FrameSource interface {
	// Frames returns one frame per time, in the order given, making the ones that are not cached yet. The asset must be in the
	// workspace; the caller has checked that it belongs to the project.
	Frames(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, atMs []int64) ([]Frame, error)
}

// ErrNoVideo is returned by Frames for an asset that has no proxy to cut a picture from (an audio file, or one that is not
// ingested).
var ErrNoVideo = errors.New("editor: the asset has no video to take frames from")

// FrameObjects is the object storage the frames use. *storage.Client is the real one.
type FrameObjects interface {
	Get(ctx context.Context, key string) (storage.Object, error)
	Head(ctx context.Context, key string) (storage.ObjectInfo, error)
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error
}

// FrameStore is what the frames need from the database. *store.Store is the real one.
type FrameStore interface {
	ListAssetDerivatives(ctx context.Context, ws ids.WorkspaceID, assetIDs []ids.AssetID) ([]store.Derivative, error)
}

// FrameService cuts frames in the sandbox and caches them.
type FrameService struct {
	Store   FrameStore
	Objects FrameObjects
	// Runner runs ffmpeg: in production a media.SandboxRunner over the media-tools image.
	Runner media.Runner
	// TempDir must be inside the sandbox provider's allowed roots, without symlinks in its path.
	TempDir string
	Logger  *slog.Logger
}

var _ FrameSource = (*FrameService)(nil)

// Frames implements FrameSource.
func (s *FrameService) Frames(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, atMs []int64) ([]Frame, error) {
	if len(atMs) == 0 || len(atMs) > media.MaxFrameBatch {
		return nil, fmt.Errorf("editor: %d frames asked for, 1 to %d allowed", len(atMs), media.MaxFrameBatch)
	}
	out := make([]Frame, len(atMs))
	var missing []int
	for i, at := range atMs {
		key, err := storage.FrameKey(ws, asset, at, FrameLongSide)
		if err != nil {
			return nil, err
		}
		out[i] = Frame{AtMs: at, Ref: key}
		if _, err := s.Objects.Head(ctx, key); err != nil {
			if !errors.Is(err, storage.ErrNotFound) {
				return nil, fmt.Errorf("editor: look for a cached frame: %w", err)
			}
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	if err := s.extract(ctx, ws, asset, out, missing); err != nil {
		return nil, err
	}
	return out, nil
}

// proxyKey finds the asset's proxy and checks that its key is the key of a derivative of this very asset.
func (s *FrameService) proxyKey(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID) (string, error) {
	derivs, err := s.Store.ListAssetDerivatives(ctx, ws, []ids.AssetID{asset})
	if err != nil {
		return "", fmt.Errorf("editor: list derivatives: %w", err)
	}
	for _, d := range derivs {
		if d.Kind != store.DerivativeProxy720 || d.AssetID != asset || d.WorkspaceID != ws {
			continue
		}
		name, ok := strings.CutPrefix(d.R2Key, storage.AssetPrefix(ws, asset))
		if !ok || !storage.ValidDerivativeName(name) {
			return "", fmt.Errorf("editor: the proxy key of asset %s is not a derivative key of it", asset)
		}
		return d.R2Key, nil
	}
	return "", ErrNoVideo
}

// extract cuts the frames out[i] for i in missing, in one ffmpeg run, and stores them.
func (s *FrameService) extract(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, out []Frame, missing []int) error {
	proxy, err := s.proxyKey(ctx, ws, asset)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(s.TempDir, "frames-")
	if err != nil {
		return fmt.Errorf("editor: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Mkdir(filepath.Join(dir, "in"), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return err
	}
	local := filepath.Join(dir, "in", "proxy")
	if err := s.download(ctx, proxy, local); err != nil {
		return err
	}

	outDir, err := os.MkdirTemp(dir, "out-")
	if err != nil {
		return err
	}
	specs := make([]media.FrameSpec, len(missing))
	for n, i := range missing {
		specs[n] = media.FrameSpec{AtMs: out[i].AtMs, Output: media.OutputPath(fmt.Sprintf("f%d.jpg", n))}
	}
	args, err := media.FramesArgs(media.InputPath(0), specs, FrameLongSide)
	if err != nil {
		return err
	}
	res, err := s.Runner.Run(ctx, media.Invocation{
		Args: args, Inputs: []media.Input{{Host: local, Container: media.InputPath(0)}}, OutputDir: outDir,
		Timeout: frameBaseTimeout + time.Duration(len(missing))*frameTimeoutPerOne,
	})
	if err != nil {
		return fmt.Errorf("editor: ffmpeg: %w", err)
	}
	if res.ExitCode != 0 {
		return &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	for n, i := range missing {
		if err := s.store(ctx, filepath.Join(outDir, fmt.Sprintf("f%d.jpg", n)), out[i].Ref); err != nil {
			return err
		}
	}
	return nil
}

// download streams the proxy to path.
func (s *FrameService) download(ctx context.Context, key, path string) error {
	obj, err := s.Objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("editor: download the proxy: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size <= 0 || obj.Size > maxProxyBytes {
		return fmt.Errorf("editor: the proxy is %d bytes, which is not plausible", obj.Size)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- a path in our job dir; the container user must read it
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(obj.Body, maxProxyBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("editor: download the proxy: %w", err)
	}
	if n != obj.Size {
		return fmt.Errorf("editor: the proxy download is %d bytes, storage reported %d", n, obj.Size)
	}
	return nil
}

// jpegMagic is how every JPEG file starts.
var jpegMagic = []byte{0xFF, 0xD8, 0xFF}

// store checks what the sandbox wrote (output of a tool is untrusted: a regular file, small, a JPEG) and uploads it.
func (s *FrameService) store(ctx context.Context, path, key string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("editor: ffmpeg wrote no frame: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() < 100 || fi.Size() > maxFrameFileBytes {
		return fmt.Errorf("editor: ffmpeg wrote a frame that is not a regular file of a plausible size (%d bytes)", fi.Size())
	}
	f, err := os.Open(path) // #nosec G304 -- a file this process just checked, in its job dir
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, len(jpegMagic))
	if _, err := io.ReadFull(f, head); err != nil || !slices.Equal(head, jpegMagic) {
		return errors.New("editor: ffmpeg wrote a frame that is not a JPEG")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := s.Objects.Put(ctx, key, f, fi.Size(), "image/jpeg"); err != nil {
		return fmt.Errorf("editor: store a frame: %w", err)
	}
	return nil
}

// OverviewTimes are the times of the overview of a clip: a frame every OverviewStepMs, starting at 0, and at most
// MaxOverviewFrames of them. A clip that would need more is sampled evenly across its length (first frame at 0, last just
// before the end). The last time is always inside the clip (at least 100 ms before its end, so a seek there decodes a frame).
func OverviewTimes(durationMs int64) []int64 {
	if durationMs <= 0 {
		return nil
	}
	last := max(durationMs-100, 0)
	n := int(min(last/OverviewStepMs+1, MaxOverviewFrames))
	if n >= MaxOverviewFrames && last/OverviewStepMs+1 > MaxOverviewFrames {
		out := make([]int64, n)
		for i := range out {
			out[i] = last * int64(i) / int64(n-1)
		}
		return out
	}
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i) * OverviewStepMs
	}
	return out
}
