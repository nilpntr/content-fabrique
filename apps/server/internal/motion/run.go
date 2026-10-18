package motion

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// bundleFS is the template bundle: scene.html, the runtime, the base stylesheet and the templates. It is mounted read-only into the
// sandbox for every job, so the templates a render uses are the ones of the server that compiled it.
//
//go:embed bundle
var bundleFS embed.FS

// Program is the runner's name in the media-tools image (and on PATH for the local provider).
const Program = "cf-motion"

// The paths of the runner's world inside the media-tools image.
const (
	ImageChrome   = "/opt/chrome/chrome-headless-shell"
	ImageFontsDir = "/usr/share/fonts/cf"
	jobFile       = "motion-job.json"
	// OutDir is the directory, inside the work directory, that the frames are written to; the render's ffmpeg reads them from there.
	OutDir = "motion"
)

// Bundle returns the template bundle.
func Bundle() fs.FS {
	sub, err := fs.Sub(bundleFS, "bundle")
	if err != nil {
		panic(err) // the embed is a literal directory
	}
	return sub
}

// BundleHash is the SHA-256 of the bundle's files (names and contents, in order): it changes when any template changes, and the
// golden hashes of the frames are recorded against it.
func BundleHash() string {
	h := sha256.New()
	_ = fs.WalkDir(Bundle(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(Bundle(), p)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(h, p+"\x00"+strconv.Itoa(len(data))+"\x00")
		_, _ = h.Write(data)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

// StageBundle writes the bundle into dir (which must exist), readable by any uid: the sandbox user may not be ours.
func StageBundle(dir string) error { return stage(Bundle(), dir) }

func stage(bundle fs.FS, dir string) error {
	return fs.WalkDir(bundle, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755) //nolint:gosec // read by the sandbox user
		}
		data, err := fs.ReadFile(bundle, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644) //nolint:gosec // read by the sandbox user
	})
}

// writeNew writes a file that must not exist yet, and does not follow a symlink in its place.
func writeNew(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644) //nolint:gosec // read by the sandbox user
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Options configure a run.
type Options struct {
	// InImage says the runner executes in the media-tools image (the docker and hardened providers); the local provider runs the
	// host's cf-motion with Chrome and FontsDir.
	InImage bool
	// Chrome and FontsDir are host paths for the local provider: the chrome-headless-shell binary and the vendored fonts.
	Chrome   string
	FontsDir string
	// Timeout bounds the run; zero is the runner's own limit.
	Timeout time.Duration
	// runnerArgs are more arguments for cf-motion, for the sandbox tests only (a frame timeout, an injected hang).
	runnerArgs []string
}

// Report is what a run did, from the runner's manifest.
type Report struct {
	Chrome string
	// Arch is the architecture the runner ran on (Skia's software raster is deterministic per architecture).
	Arch     string
	Frames   int64
	Timing   ManifestTiming
	Duration time.Duration
	// Recovery is how many frames the runner drew again after a hang, and how often it restarted Chrome for one (frames.go of
	// cf-motion); zero in a normal run.
	Recovery ManifestRecovery
}

// Manifest is the runner's manifest.json.
type Manifest struct {
	Version  int               `json:"version"`
	Chrome   string            `json:"chrome"`
	Arch     string            `json:"arch"`
	Segments []ManifestSegment `json:"segments"`
	Timing   ManifestTiming    `json:"timing"`
	Recovery ManifestRecovery  `json:"recovery"`
}

// ManifestRecovery is what the runner needed to draw every frame: retries on the same page and restarts of Chrome.
type ManifestRecovery struct {
	Retries  int `json:"retries"`
	Restarts int `json:"restarts"`
}

// ManifestSegment is one segment's frames.
type ManifestSegment struct {
	StartFrame int64     `json:"start_frame"`
	Frames     int64     `json:"frames"`
	Dir        string    `json:"dir"`
	Clip       PixelRect `json:"clip"`
	SHA256     []string  `json:"sha256"`
}

// ManifestTiming is the runner's own timing, in ms.
type ManifestTiming struct {
	LaunchMs int64 `json:"launch_ms"`
	LoadMs   int64 `json:"load_ms"`
	FramesMs int64 `json:"frames_ms"`
	TotalMs  int64 `json:"total_ms"`
}

// FramePattern is the image2 pattern of segment k's frames, relative to the work directory: motion/s<k>/f%06d.png.
func FramePattern(k int) string { return path.Join(OutDir, "s"+strconv.Itoa(k), "f%06d.png") }

// ErrRun wraps every failure of a motion run: the runner refused the job, Chrome failed, a page error, a blank or spilling frame, or
// an output that is not what the job asked for.
var ErrRun = errors.New("motion: the overlay render failed")

// Render runs job through runner and checks what comes back. The run's one writable mount is its own fresh directory, work/motion
// (OutDir), which gets the job file and the frames (left there for the render's ffmpeg); nothing else of work is mounted. bundleDir is
// a directory the bundle is staged into, inside the provider's allowed roots and outside work/motion (the render stages it in
// work/motion-bundle), so inside the sandbox the bundle is only reachable through its read-only mount.
func Render(ctx context.Context, runner media.Runner, job Job, work, bundleDir string, o Options) (Report, error) {
	return render(ctx, runner, job, work, bundleDir, o, Bundle())
}

// render is Render with the bundle to stage: the sandbox tests run hostile pages through the same path.
func render(ctx context.Context, runner media.Runner, job Job, work, bundleDir string, o Options, bundle fs.FS) (Report, error) {
	start := time.Now()
	out := filepath.Join(work, OutDir)
	if rel, err := filepath.Rel(out, bundleDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Report{}, fmt.Errorf("%w: the bundle directory %s is inside the run's writable directory %s", ErrRun, bundleDir, out)
	}
	// A fresh output directory for every run: nothing an earlier run (or anything else) left there is read or written through.
	if err := os.RemoveAll(out); err != nil {
		return Report{}, fmt.Errorf("motion: clear the output directory: %w", err)
	}
	if err := os.Mkdir(out, 0o755); err != nil { //nolint:gosec // written by the sandbox user, which may be another uid
		return Report{}, fmt.Errorf("motion: the output directory: %w", err)
	}
	if err := stage(bundle, bundleDir); err != nil {
		return Report{}, fmt.Errorf("motion: stage the bundle: %w", err)
	}
	data, err := job.Encode()
	if err != nil {
		return Report{}, err
	}
	if err := writeNew(filepath.Join(out, jobFile), data); err != nil {
		return Report{}, fmt.Errorf("motion: write the job: %w", err)
	}
	chrome, fonts := ImageChrome, ImageFontsDir
	if !o.InImage {
		chrome, fonts = o.Chrome, o.FontsDir
		if chrome == "" || fonts == "" {
			return Report{}, fmt.Errorf("%w: the local provider needs a chrome-headless-shell (CF_MOTION_CHROME) and the fonts directory", ErrRun)
		}
	}
	args := []string{Program,
		"--chrome", chrome,
		"--bundle", media.InputPath(0),
		"--fonts", fonts,
		"--job", media.OutputPath(jobFile),
		"--out", sandbox.WorkDir,
		"--tmp", "/tmp",
	}
	args = append(args, o.runnerArgs...)
	lim := DefaultLimits()
	res, err := runner.Run(ctx, media.Invocation{Args: args, Inputs: []media.Input{{Host: bundleDir, Container: media.InputPath(0)}}, OutputDir: out,
		Limits: &lim, Timeout: o.Timeout})
	if err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrRun, err)
	}
	if res.ExitCode != 0 {
		return Report{}, fmt.Errorf("%w: %w", ErrRun, &media.ToolError{Tool: Program, ExitCode: res.ExitCode, Stderr: tail(string(res.Stderr), 4000)})
	}
	m, err := ReadManifest(out, job)
	if err != nil {
		return Report{}, err
	}
	return Report{Chrome: m.Chrome, Arch: m.Arch, Frames: job.TotalFrames(), Timing: m.Timing, Duration: time.Since(start), Recovery: m.Recovery}, nil
}

// ReadManifest reads the runner's manifest from out and checks it against the job: one segment per job segment with the same
// frames and clip, every frame a regular file of reasonable size whose SHA-256 is the manifest's. The output of the sandbox is
// untrusted: no symlinks, no huge files, nothing the job did not ask for.
func ReadManifest(out string, job Job) (Manifest, error) {
	bad := func(format string, args ...any) (Manifest, error) {
		return Manifest{}, fmt.Errorf("%w: %s", ErrRun, fmt.Sprintf(format, args...))
	}
	data, err := readRegular(filepath.Join(out, "manifest.json"), 4<<20)
	if err != nil {
		return bad("no manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return bad("the manifest is not JSON: %v", err)
	}
	if m.Version != 1 || len(m.Segments) != len(job.Segments) {
		return bad("the manifest has %d segments, the job %d", len(m.Segments), len(job.Segments))
	}
	for k, s := range m.Segments {
		want := job.Segments[k]
		if s.StartFrame != want.StartFrame || s.Frames != want.Frames() || s.Clip != want.Clip || s.Dir != "s"+strconv.Itoa(k) || int64(len(s.SHA256)) != s.Frames {
			return bad("segment %d of the manifest is not the job's", k)
		}
		for i := int64(0); i < s.Frames; i++ {
			name := filepath.Join(out, s.Dir, fmt.Sprintf("f%06d.png", i))
			data, err := readRegular(name, 32<<20)
			if err != nil {
				return bad("frame %d of segment %d: %v", i, k, err)
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != s.SHA256[i] {
				return bad("frame %d of segment %d does not match its hash", i, k)
			}
		}
	}
	return m, nil
}

func readRegular(name string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(name))
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s is %d bytes, over %d", filepath.Base(name), fi.Size(), limit)
	}
	return os.ReadFile(name) //nolint:gosec // a file in our work directory, checked above
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// DefaultLimits are the sandbox limits of a motion job: Chrome needs more processes and memory than ffmpeg, and less time.
func DefaultLimits() sandbox.Limits {
	return sandbox.Limits{CPU: 4, MemoryMB: 3072, Timeout: 10 * time.Minute, PIDs: 1024}
}
