package editor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	// OverviewStepMs is the spacing of the overview, and MaxOverviewFrames its length: a frame every two seconds, at most eight
	// (a longer clip is sampled evenly across its length instead).
	OverviewStepMs    = 2000
	MaxOverviewFrames = 8
	// EndMarginMs is how far before the end of the proxy the last frame may be: a seek to the very end decodes nothing.
	EndMarginMs        = 100
	maxFrameFileBytes  = 2 << 20
	maxProxyBytes      = 2 << 30
	frameBaseTimeout   = 30 * time.Second
	frameTimeoutPerOne = 5 * time.Second
)

// Frame is a still frame of a clip in object storage, or the reason there is none.
type Frame struct {
	// AtMs is the time in the clip the picture shows. RequestedMs is the time that was asked for; it differs when the request
	// was past the end of the proxy (or before its start) and was moved.
	AtMs        int64
	RequestedMs int64
	// Ref is the object key (ws/{workspace}/assets/{asset}/frames/...): what goes into an image block. Empty when Err is set.
	Ref string
	// Err says, for the model, why there is no picture at this moment.
	Err string
	// JPEG holds the picture's bytes when this call has just made it (so a caller that wants the bytes need not read them
	// back); nil for a frame that was already cached.
	JPEG []byte
}

// FrameSource makes the frames of a clip. *FrameService is the real one.
type FrameSource interface {
	// Frames returns one entry per requested time, in the order given, making the pictures that are not cached yet. A time
	// outside the proxy is moved into it. A moment that cannot be decoded is an entry with Err set while the others are made; the
	// error is ErrNoFrames (wrapping what went wrong) only when no entry has a picture, ErrNoVideo for an asset with no proxy.
	// The asset must be in the workspace; the caller has checked that it belongs to the project.
	Frames(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, atMs []int64) ([]Frame, error)
}

// Errors of Frames.
var (
	// ErrNoVideo is returned for an asset that has no proxy to cut a picture from (an audio file, or one that is not ingested).
	ErrNoVideo = errors.New("editor: the asset has no video to take frames from")
	// ErrNoFrames is returned when none of the requested moments could be decoded.
	ErrNoFrames = errors.New("editor: no frame could be made")
)

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

// ClampMs moves a requested time into the proxy: not before 0, and not after durationMs less EndMarginMs. A duration of zero
// (unknown) only stops the time from going below 0.
func ClampMs(at, durationMs int64) int64 {
	at = max(at, 0)
	if durationMs > 0 {
		at = min(at, max(durationMs-EndMarginMs, 0))
	}
	return at
}

// Frames implements FrameSource: FramesSized at FrameLongSide.
func (s *FrameService) Frames(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, atMs []int64) ([]Frame, error) {
	return s.FramesSized(ctx, ws, asset, atMs, FrameLongSide)
}

// FramesSized is Frames at another size: longSide pixels on the long side (16 to 4096). The cache key holds the size, so
// pictures of different sizes of one moment live side by side. The quick look (P1-04b) takes its candidates at 512 px with it.
func (s *FrameService) FramesSized(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, atMs []int64, longSide int) ([]Frame, error) {
	if len(atMs) == 0 || len(atMs) > media.MaxFrameBatch {
		return nil, fmt.Errorf("editor: %d frames asked for, 1 to %d allowed", len(atMs), media.MaxFrameBatch)
	}
	if longSide < 16 || longSide > 4096 {
		return nil, fmt.Errorf("editor: a frame size of %d px is outside 16..4096", longSide)
	}
	proxy, err := s.proxy(ctx, ws, asset)
	if err != nil {
		return nil, err
	}
	out := make([]Frame, len(atMs))
	var missing []int        // the first frame of each distinct time that is not cached
	first := map[int64]int{} // shown time -> index of the first frame that shows it
	for i, req := range atMs {
		at := ClampMs(req, proxy.durationMs)
		key, err := storage.FrameKey(ws, asset, at, longSide)
		if err != nil {
			return nil, err
		}
		out[i] = Frame{AtMs: at, RequestedMs: req, Ref: key}
		if _, seen := first[at]; seen {
			continue
		}
		first[at] = i
		if _, err := s.Objects.Head(ctx, key); err != nil {
			if !errors.Is(err, storage.ErrNotFound) {
				return nil, fmt.Errorf("editor: look for a cached frame: %w", err)
			}
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		cause, err := s.extract(ctx, asset, proxy.key, out, missing, longSide)
		if err != nil {
			return nil, err
		}
		// The frames that were not made, and the ones that show the same moment.
		for i := range out {
			if j := first[out[i].AtMs]; j != i && out[j].Err != "" {
				out[i].Ref, out[i].Err = "", out[j].Err
			}
		}
		made := 0
		for _, f := range out {
			if f.Err == "" {
				made++
			}
		}
		if made == 0 {
			return nil, fmt.Errorf("%w: %w", ErrNoFrames, cause)
		}
	}
	return out, nil
}

// proxyInfo is the asset's proxy as the frames need it.
type proxyInfo struct {
	key        string
	durationMs int64
}

// proxy finds the asset's proxy and checks that its key is the key of a derivative of this very asset. The duration is the
// proxy's own (it can be a little shorter than the original's), 0 when the derivative does not say.
func (s *FrameService) proxy(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID) (proxyInfo, error) {
	derivs, err := s.Store.ListAssetDerivatives(ctx, ws, []ids.AssetID{asset})
	if err != nil {
		return proxyInfo{}, fmt.Errorf("editor: list derivatives: %w", err)
	}
	for _, d := range derivs {
		if d.Kind != store.DerivativeProxy720 || d.AssetID != asset || d.WorkspaceID != ws {
			continue
		}
		name, ok := strings.CutPrefix(d.R2Key, storage.AssetPrefix(ws, asset))
		if !ok || !storage.ValidDerivativeName(name) {
			return proxyInfo{}, fmt.Errorf("editor: the proxy key of asset %s is not a derivative key of it", asset)
		}
		var meta struct {
			DurationMs int64 `json:"duration_ms"`
		}
		_ = json.Unmarshal(d.Meta, &meta)
		return proxyInfo{key: d.R2Key, durationMs: max(meta.DurationMs, 0)}, nil
	}
	return proxyInfo{}, ErrNoVideo
}

// extract cuts the frames out[i] for i in missing, in one ffmpeg run, and stores them. A frame that did not come out right gets
// Err; cause is the first thing that went wrong (nil when everything worked). The error is for what is not the clip's doing:
// the sandbox, storage, the disk.
func (s *FrameService) extract(ctx context.Context, asset ids.AssetID, proxyKey string, out []Frame, missing []int, longSide int) (cause, err error) {
	dir, err := os.MkdirTemp(s.TempDir, "frames-")
	if err != nil {
		return nil, fmt.Errorf("editor: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Mkdir(filepath.Join(dir, "in"), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return nil, err
	}
	local := filepath.Join(dir, "in", "proxy")
	if err := s.download(ctx, proxyKey, local); err != nil {
		return nil, err
	}

	outDir, err := os.MkdirTemp(dir, "out-")
	if err != nil {
		return nil, err
	}
	specs := make([]media.FrameSpec, len(missing))
	for n, i := range missing {
		specs[n] = media.FrameSpec{AtMs: out[i].AtMs, Output: media.OutputPath(fmt.Sprintf("f%d.jpg", n))}
	}
	args, err := media.FramesArgs(media.InputPath(0), specs, longSide)
	if err != nil {
		return nil, err
	}
	res, err := s.Runner.Run(ctx, media.Invocation{
		Args: args, Inputs: []media.Input{{Host: local, Container: media.InputPath(0)}}, OutputDir: outDir,
		Timeout: frameBaseTimeout + time.Duration(len(missing))*frameTimeoutPerOne,
	})
	if err != nil {
		return nil, fmt.Errorf("editor: ffmpeg: %w", err)
	}
	if res.ExitCode != 0 {
		// ffmpeg exits with an error when one of its outputs got no picture, and still writes the others: look at each.
		cause = &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	for n, i := range missing {
		data, serr := s.store(ctx, filepath.Join(outDir, fmt.Sprintf("f%d.jpg", n)), out[i].Ref)
		out[i].JPEG = data
		switch {
		case serr == nil:
		case errors.Is(serr, errBadFrame):
			if cause == nil {
				cause = serr
			}
			s.log().WarnContext(ctx, "editor: a frame could not be made", "asset_id", asset.String(), "at_ms", out[i].AtMs, "error", serr)
			out[i].Ref, out[i].Err = "", fmt.Sprintf("the picture at %d ms could not be decoded", out[i].AtMs)
		default:
			return nil, serr
		}
	}
	return cause, nil
}

func (s *FrameService) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
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

// errBadFrame marks a frame the tool did not produce, or produced as something else than a small JPEG.
var errBadFrame = errors.New("editor: no usable frame")

// store checks what the sandbox wrote (output of a tool is untrusted: a regular file, small, a JPEG) and uploads it, returning
// the bytes it uploaded. A file that is missing or wrong is errBadFrame; a failure of storage is returned as it is.
func (s *FrameService) store(ctx context.Context, path, key string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: ffmpeg wrote no frame", errBadFrame)
	}
	if !fi.Mode().IsRegular() || fi.Size() < 100 || fi.Size() > maxFrameFileBytes {
		return nil, fmt.Errorf("%w: ffmpeg wrote a frame that is not a regular file of a plausible size (%d bytes)", errBadFrame, fi.Size())
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a file this process just checked, in its job dir; at most maxFrameFileBytes
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadFrame, err)
	}
	if !bytes.HasPrefix(data, jpegMagic) {
		return nil, fmt.Errorf("%w: ffmpeg wrote a frame that is not a JPEG", errBadFrame)
	}
	if err := s.Objects.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "image/jpeg"); err != nil {
		return nil, fmt.Errorf("editor: store a frame: %w", err)
	}
	return data, nil
}

// OverviewTimes are the times of the overview of a clip: a frame every OverviewStepMs, starting at 0, and at most
// MaxOverviewFrames of them. A clip that would need more is sampled evenly across its length instead (first frame at 0, last
// EndMarginMs before the end).
func OverviewTimes(durationMs int64) []int64 {
	if durationMs <= 0 {
		return nil
	}
	last := max(durationMs-EndMarginMs, 0)
	steps := last/OverviewStepMs + 1
	n := int(min(steps, MaxOverviewFrames))
	out := make([]int64, n)
	for i := range out {
		if steps > MaxOverviewFrames {
			out[i] = last * int64(i) / int64(n-1)
		} else {
			out[i] = int64(i) * OverviewStepMs
		}
	}
	return out
}
