package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The files of an asset: fixed names, so a re-run replaces them.
const (
	fileProxy     = "proxy_720.mp4"
	filePoster    = "poster.jpg"
	fileSprite    = "sprite.jpg"
	fileSpriteVTT = "sprite.vtt"
	fileWaveform  = "waveform.json"
	fileAudio16k  = "audio_16k.wav"
	fileHover     = "hover_preview.mp4"
)

// Size caps for what comes out of the sandbox, per file.
const (
	maxImageFile = 64 << 20
	maxVideoFile = storage.MaxPutSize
)

// errNoOutput is a tool that exited cleanly and wrote nothing.
var errNoOutput = errors.New("the tool produced no output")

// exec runs one media tool in the sandbox with the given host files mounted read-only as /in/0, /in/1 ... and returns the
// fresh output directory (mounted at /work). A tool that ran and failed is a *media.ToolError; anything else is the
// sandbox's or the context's failure.
func (r *run) exec(ctx context.Context, args []string, inputs ...string) (string, error) {
	out, err := os.MkdirTemp(r.dir, "out-")
	if err != nil {
		return "", err
	}
	ins := make([]media.Input, len(inputs))
	for i, p := range inputs {
		ins[i] = media.Input{Host: p, Container: media.InputPath(i)}
	}
	res, err := r.runner.Run(ctx, media.Invocation{Args: args, Inputs: ins, OutputDir: out})
	if err != nil {
		return out, fmt.Errorf("%s: %w", args[0], err)
	}
	if res.ExitCode != 0 {
		return out, &media.ToolError{Tool: args[0], ExitCode: res.ExitCode, Stderr: snippet(res.Stderr)}
	}
	return out, nil
}

func snippet(b []byte) string {
	const limit = 1024
	if len(b) > limit {
		b = b[:limit]
	}
	return string(b)
}

// toolFailed reports whether err is a tool that ran and failed (the file's fault, usually).
func toolFailed(err error) bool {
	var te *media.ToolError
	return errors.As(err, &te)
}

// adopt moves a file the tool wrote into the job's own directory, after checking what it is: the sandbox's output is
// untrusted, so a symlink, a device or a huge file is refused. It removes the tool's output directory.
func (r *run) adopt(outDir, name string, maxSize int64) (path string, size int64, err error) {
	defer func() { _ = os.RemoveAll(outDir) }()
	src := filepath.Join(outDir, name)
	fi, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, errNoOutput
	}
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, fmt.Errorf("the tool wrote %s as something other than a regular file", name)
	}
	if fi.Size() > maxSize {
		return "", 0, fmt.Errorf("%s is %d bytes, over the limit of %d", name, fi.Size(), maxSize)
	}
	dst := filepath.Join(r.dir, "derived", name)
	if err := os.Rename(src, dst); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(dst, 0o644); err != nil { // #nosec G302 -- the next tool may run as another uid and must read it
		return "", 0, err
	}
	return dst, fi.Size(), nil
}

// keep records a file for upload.
func (r *run) keep(kind, name, contentType, path string, size int64, meta map[string]any) {
	r.derived = append(r.derived, &derivative{kind: kind, name: name, contentType: contentType, path: path, size: size, meta: meta})
}

// writeText stores a file this process made itself (the VTT track, waveform.json) as a derivative.
func (r *run) writeText(kind, name, contentType string, data []byte, meta map[string]any) error {
	path := filepath.Join(r.dir, "derived", name)
	if err := os.WriteFile(path, data, 0o644); err != nil { // #nosec G306 -- read back by this process only
		return err
	}
	r.keep(kind, name, contentType, path, int64(len(data)), meta)
	return nil
}

// ---- Video ---------------------------------------------------------------------------------------------------------

func (r *run) video(ctx context.Context, original string, meta media.Metadata) (ingested, error) {
	// A video ffmpeg cannot decode fails here, in seconds, and not at the end of a long transcode.
	args, err := media.DecodeCheckArgs(media.InputPath(0), meta)
	if err != nil {
		return ingested{}, err
	}
	out, err := r.exec(ctx, args, original)
	_ = os.RemoveAll(out)
	if toolFailed(err) {
		return ingested{}, fileErr(store.FailureUndecodableVideo, err)
	}
	if err != nil {
		return ingested{}, err
	}

	if err := r.progress(ctx, pctProxy, store.StageProxy); err != nil {
		return ingested{}, err
	}
	proxyPath, proxy, err := r.makeProxy(ctx, original, &meta)
	if err != nil {
		return ingested{}, err
	}
	pw, ph := proxy.Width, proxy.Height
	fps := proxy.FPS.Value
	dur := proxy.DurationMs
	r.keep(store.DerivativeProxy720, fileProxy, "video/mp4", proxyPath, r.sizeOf(proxyPath), map[string]any{
		"width": pw, "height": ph, "duration_ms": dur, "fps": fps, "codec": "h264", "has_audio": proxy.HasAudio,
	})

	if err := r.progress(ctx, pctDerivatives, store.StageDerivatives); err != nil {
		return ingested{}, err
	}
	if err := r.makePoster(ctx, proxyPath, dur, pw, ph); err != nil {
		return ingested{}, err
	}
	if err := r.makeSprite(ctx, proxyPath, dur, pw, ph); err != nil {
		return ingested{}, err
	}
	if err := r.makeHover(ctx, proxyPath, dur, pw, ph, fps); err != nil {
		return ingested{}, err
	}
	if meta.SelectedAudio != nil {
		if err := r.makeAudio(ctx, original, meta); err != nil {
			return ingested{}, err
		}
	}

	// The asset has audio when the proxy does: the selected track was decodable and encoded. (meta.SelectedAudio is cleared
	// by makeProxy when it was not.)
	res := store.IngestResult{
		DurationMs: meta.DurationMs, Width: meta.Width, Height: meta.Height, FPS: meta.FPS.Value, Rotation: meta.ClockwiseDegrees(),
		VideoCodec: meta.Codecs.Video, HasAudio: proxy.HasAudio, HDR: meta.HDR,
	}
	if proxy.HasAudio {
		res.AudioCodec = selectedCodec(meta)
	}
	return ingested{result: res}, nil
}

// makeProxy runs the transcode. A file whose audio track will not encode is retried without audio, so a damaged audio track
// does not cost the user the picture; meta.SelectedAudio is then cleared.
func (r *run) makeProxy(ctx context.Context, original string, meta *media.Metadata) (string, media.Metadata, error) {
	for {
		args, err := media.ProxyArgs(media.InputPath(0), media.OutputPath(fileProxy), *meta)
		if err != nil {
			return "", media.Metadata{}, fileErr(store.FailureProcessing, err)
		}
		out, err := r.exec(ctx, args, original)
		if err == nil {
			path, _, aerr := r.adopt(out, fileProxy, maxVideoFile)
			if aerr != nil {
				return "", media.Metadata{}, fileErr(store.FailureUndecodableVideo, aerr)
			}
			proxy, perr := media.Probe(ctx, r.runner, path)
			if perr != nil {
				if toolFailed(perr) || errors.Is(perr, media.ErrUnparsable) {
					return "", media.Metadata{}, fileErr(store.FailureCorruptMedia, perr)
				}
				return "", media.Metadata{}, fmt.Errorf("probe proxy: %w", perr)
			}
			if err := checkProxy(proxy, *meta); err != nil {
				return "", media.Metadata{}, fileErr(store.FailureCorruptMedia, err)
			}
			return path, proxy, nil
		}
		_ = os.RemoveAll(out)
		if !toolFailed(err) {
			return "", media.Metadata{}, err
		}
		if meta.SelectedAudio == nil {
			return "", media.Metadata{}, fileErr(store.FailureUndecodableVideo, err)
		}
		r.log.WarnContext(ctx, "the proxy would not encode with the audio track; trying without it", "error", err)
		meta.SelectedAudio = nil
	}
}

// checkProxy verifies what the sandbox made: an upright H.264 video of the size we asked for with nothing left to rotate,
// at least half as long as the source.
func checkProxy(proxy, src media.Metadata) error {
	w, h := media.ScaledSize(src.Width, src.Height, media.ProxyShortSide)
	switch {
	case !proxy.HasVideo || proxy.Codecs.Video != "h264":
		return errors.New("the proxy has no H.264 video")
	case proxy.Width != w || proxy.Height != h || proxy.Rotation != 0:
		return fmt.Errorf("the proxy is %dx%d (rotation %d), want %dx%d upright", proxy.Width, proxy.Height, proxy.Rotation, w, h)
	case proxy.DurationMs <= 0 || proxy.DurationMs*2 < src.DurationMs:
		return fmt.Errorf("the proxy is %d ms long, the source %d ms", proxy.DurationMs, src.DurationMs)
	}
	return nil
}

func (r *run) makePoster(ctx context.Context, proxy string, durMs int64, w, h int) error {
	at := media.PosterAtMs(durMs)
	for {
		path, size, err := r.posterAt(ctx, proxy, at)
		if err == nil {
			r.keep(store.DerivativePoster, filePoster, "image/jpeg", path, size, map[string]any{"width": w, "height": h, "at_ms": at})
			return nil
		}
		if !errors.Is(err, errNoOutput) && !toolFailed(err) {
			return err // the sandbox or the context, not the file
		}
		if at == 0 {
			return fileErr(store.FailureCorruptMedia, err)
		}
		at = 0 // a seek past the last frame produces nothing: take the first frame instead
	}
}

// posterAt makes the poster from the frame at atMs.
func (r *run) posterAt(ctx context.Context, proxy string, atMs int64) (string, int64, error) {
	out, err := r.exec(ctx, media.PosterArgs(media.InputPath(0), media.OutputPath(filePoster), atMs), proxy)
	if err != nil {
		_ = os.RemoveAll(out)
		return "", 0, err
	}
	return r.adopt(out, filePoster, maxImageFile)
}

func (r *run) makeSprite(ctx context.Context, proxy string, durMs int64, w, h int) error {
	grid, err := media.NewSpriteGrid(durMs, w, h)
	if err != nil {
		return fileErr(store.FailureNoDuration, err)
	}
	args, err := media.SpriteArgs(media.InputPath(0), media.OutputPath(fileSprite), grid)
	if err != nil {
		return err
	}
	out, err := r.exec(ctx, args, proxy)
	if err != nil {
		_ = os.RemoveAll(out)
		if toolFailed(err) {
			return fileErr(store.FailureCorruptMedia, err)
		}
		return err
	}
	path, size, err := r.adopt(out, fileSprite, maxImageFile)
	if err != nil {
		return fileErr(store.FailureCorruptMedia, err)
	}
	r.keep(store.DerivativeSprite, fileSprite, "image/jpeg", path, size, map[string]any{
		"tile_width": grid.TileW, "tile_height": grid.TileH, "cols": grid.Cols, "rows": grid.Rows, "count": grid.Count,
		"interval_sec": grid.IntervalSec, "width": grid.Width(), "height": grid.Height(),
	})
	vtt := media.SpriteVTT(grid, durMs, fileSprite)
	return r.writeText(store.DerivativeSpriteVTT, fileSpriteVTT, "text/vtt", []byte(vtt), map[string]any{"cues": grid.Count, "interval_sec": grid.IntervalSec})
}

func (r *run) makeHover(ctx context.Context, proxy string, durMs int64, w, h int, fps float64) error {
	segs := media.HoverSegments(durMs)
	args, err := media.HoverArgs(media.InputPath(0), media.OutputPath(fileHover), segs, w, h, fps)
	if err != nil {
		return err
	}
	out, err := r.exec(ctx, args, proxy)
	if err != nil {
		_ = os.RemoveAll(out)
		if toolFailed(err) {
			return fileErr(store.FailureCorruptMedia, err)
		}
		return err
	}
	path, size, err := r.adopt(out, fileHover, maxVideoFile)
	if err != nil {
		return fileErr(store.FailureCorruptMedia, err)
	}
	hw, hh := media.ScaledSize(w, h, media.HoverShortSide)
	var total int64
	for _, s := range segs {
		total += s.LenMs
	}
	r.keep(store.DerivativeHoverPreview, fileHover, "video/mp4", path, size, map[string]any{
		"width": hw, "height": hh, "duration_ms": total, "segments": len(segs), "muted": true,
	})
	return nil
}

// makeAudio extracts audio_16k.wav from the selected stream and computes the waveform from it. A track that will not
// extract does not fail the asset (the proxy and the pictures are fine): the asset just has no 16 kHz audio and no
// waveform. A sandbox that is down or a cancelled job is returned, so the job runs again.
func (r *run) makeAudio(ctx context.Context, original string, meta media.Metadata) error {
	err := r.extractAudio(ctx, original, meta)
	if err == nil {
		return nil
	}
	if toolFailed(err) || errors.Is(err, errBadAudio) {
		r.log.WarnContext(ctx, "the audio track could not be extracted; the asset has no 16 kHz audio and no waveform", "error", err)
		return nil
	}
	return err
}

// errBadAudio marks a problem with the audio the tool produced (as opposed to the sandbox failing).
var errBadAudio = errors.New("the extracted audio is unusable")

func (r *run) extractAudio(ctx context.Context, original string, meta media.Metadata) error {
	args, err := media.Audio16kArgs(media.InputPath(0), media.OutputPath(fileAudio16k), meta)
	if err != nil {
		return err
	}
	out, err := r.exec(ctx, args, original)
	if err != nil {
		_ = os.RemoveAll(out)
		return err
	}
	bad := func(err error) error { return fmt.Errorf("%w: %w", errBadAudio, err) }
	path, size, err := r.adopt(out, fileAudio16k, maxVideoFile)
	if err != nil {
		return bad(err)
	}
	wf, err := ComputeWaveform(path, media.WaveformPoints)
	if err != nil {
		return bad(err)
	}
	data, err := json.Marshal(wf)
	if err != nil {
		return err
	}
	if err := r.writeText(store.DerivativeWaveform, fileWaveform, "application/json", data, map[string]any{"points": len(wf.Peaks), "duration_ms": wf.DurationMs}); err != nil {
		return err
	}
	r.keep(store.DerivativeAudio16k, fileAudio16k, "audio/wav", path, size, map[string]any{
		"sample_rate": media.AudioSampleRate, "channels": 1, "duration_ms": wf.DurationMs,
	})
	return nil
}

func selectedCodec(m media.Metadata) string {
	if a, ok := m.SelectedAudioStream(); ok {
		return a.Codec
	}
	return ""
}

func (r *run) sizeOf(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// ---- Audio and image assets -----------------------------------------------------------------------------------------

func (r *run) audio(ctx context.Context, original string, meta media.Metadata) (ingested, error) {
	if err := r.progress(ctx, pctDerivatives, store.StageDerivatives); err != nil {
		return ingested{}, err
	}
	if err := r.extractAudio(ctx, original, meta); err != nil {
		if toolFailed(err) || errors.Is(err, errBadAudio) {
			return ingested{}, fileErr(store.FailureNoAudio, err)
		}
		return ingested{}, err
	}
	return ingested{result: store.IngestResult{
		DurationMs: meta.DurationMs, AudioCodec: selectedCodec(meta), HasAudio: true,
	}}, nil
}

func (r *run) image(ctx context.Context, original string, meta media.Metadata) (ingested, error) {
	if err := r.progress(ctx, pctDerivatives, store.StageDerivatives); err != nil {
		return ingested{}, err
	}
	args, err := media.ImagePosterArgs(media.InputPath(0), media.OutputPath(filePoster), meta)
	if err != nil {
		return ingested{}, fileErr(store.FailureUnsupportedFormat, err)
	}
	out, err := r.exec(ctx, args, original)
	if err != nil {
		_ = os.RemoveAll(out)
		if toolFailed(err) {
			return ingested{}, fileErr(store.FailureUndecodableVideo, err)
		}
		return ingested{}, err
	}
	path, size, err := r.adopt(out, filePoster, maxImageFile)
	if err != nil {
		return ingested{}, fileErr(store.FailureCorruptMedia, err)
	}
	w, h := media.ScaledSize(meta.Width, meta.Height, media.ProxyShortSide)
	r.keep(store.DerivativePoster, filePoster, "image/jpeg", path, size, map[string]any{"width": w, "height": h})
	return ingested{result: store.IngestResult{
		Width: meta.Width, Height: meta.Height, Rotation: meta.ClockwiseDegrees(), VideoCodec: meta.Codecs.Video,
	}}, nil
}

// ---- Upload --------------------------------------------------------------------------------------------------------

// upload writes every derivative to object storage, under the asset's prefix and nowhere else, and returns the rows to
// record. Progress moves from pctUpload to 98 as files go up.
func (r *run) upload(ctx context.Context) ([]store.NewDerivative, error) {
	rows := make([]store.NewDerivative, 0, len(r.derived))
	for i, d := range r.derived {
		f, err := os.Open(d.path)
		if err != nil {
			return nil, err
		}
		key, err := r.objs.put(ctx, d.name, f, d.size, d.contentType)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("upload %s: %w", d.name, err)
		}
		meta, err := json.Marshal(d.meta)
		if err != nil {
			return nil, err
		}
		rows = append(rows, store.NewDerivative{Kind: d.kind, Key: key, ContentType: d.contentType, SizeBytes: d.size, Meta: meta})
		if err := r.progress(ctx, pctUpload+(98-pctUpload)*(i+1)/len(r.derived), store.StageUploading); err != nil {
			return nil, err
		}
	}
	return rows, nil
}
