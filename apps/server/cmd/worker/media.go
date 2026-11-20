package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ingest"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/renderjobs"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// ingestLimits bound every sandboxed tool run of the ingest. The timeout here is the default (ffprobe and the other quick
// tools); the pipeline sets a longer one for each run that depends on the clip's length (ingest.HeavyTimeout, LightTimeout).
func ingestLimits() sandbox.Limits {
	return sandbox.Limits{CPU: 2, MemoryMB: 3072, Timeout: ingest.ProbeTimeout, PIDs: 512}
}

// newSandboxProvider builds the provider of the media jobs that mount files (everything but a remote render), rooted at the job
// directory: MEDIA_SANDBOX_PROVIDER, or MEDIA_SANDBOX_MOUNT_PROVIDER when that is remote. The local provider isolates nothing, so
// it is refused in staging and production here too (config.Load already refuses it).
func newSandboxProvider(cfg config.Config, root string) (sandbox.Provider, error) {
	pol := sandbox.Policy{AllowedRoots: []string{root}}
	name := cfg.MountSandboxProvider()
	switch name {
	case config.SandboxDocker:
		return sandbox.NewDocker(pol), nil
	case config.SandboxHardened:
		return sandbox.NewHardened(pol), nil
	case config.SandboxLocal:
		if err := sandbox.CheckLocalAllowed(cfg.Env); err != nil {
			return nil, err
		}
		return sandbox.NewLocal(pol), nil
	case "":
		// MEDIA_SANDBOX_PROVIDER=remote without a mount provider. The remote provider runs jobs whose files are presigned URLs
		// (sandbox.Job Inputs, Outputs and Steps); only the render job builds them so far (docs/design/render-runtime.md).
		return nil, errors.New("MEDIA_SANDBOX_PROVIDER=remote runs the render job only: the ingest, clip analysis, transcription, style references and the editor's frames still mount files and do not build remote jobs yet; set MEDIA_SANDBOX_MOUNT_PROVIDER=docker (or hardened) for them")
	}
	return nil, fmt.Errorf("MEDIA_SANDBOX_PROVIDER: unknown provider %q", name)
}

// newRemoteProvider builds the remote provider of the render job (MEDIA_SANDBOX_PROVIDER=remote). config.Load checked the URL,
// the token and, in staging and production, that the runner is the gateway.
func newRemoteProvider(cfg config.Config) (sandbox.Provider, error) {
	if err := sandbox.CheckRemoteAllowed(cfg.Env, cfg.MediaRunnerURL, cfg.SandboxGatewayURL); err != nil {
		return nil, err
	}
	r, err := sandbox.NewRemote(cfg.MediaRunnerURL, cfg.MediaRunnerToken)
	if err != nil {
		return nil, err
	}
	// compose's runner runs one job at a time and a render sends one job per pass: a render waits this long for another's pass
	// before its attempt fails (ErrSandbox, retried), rather than the provider's two minutes.
	r.BusyWait = remoteBusyWait
	return r, nil
}

// remoteBusyWait is how long a remote render waits for a busy runner.
const remoteBusyWait = 10 * time.Minute

// newIngestPipeline builds the media pipeline for the asset_ingest job: the sandbox provider, the media-tools image, the
// job directory (created and resolved, so the sandbox's allowed root has no symlinks in it) and the real store and storage.
func newIngestPipeline(cfg config.Config, st *store.Store, objects *storage.Client, logger *slog.Logger) (*ingest.Pipeline, error) {
	if err := os.MkdirAll(cfg.MediaTempDir, 0o750); err != nil {
		return nil, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	root, err := filepath.EvalSymlinks(cfg.MediaTempDir)
	if err != nil {
		return nil, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	provider, err := newSandboxProvider(cfg, root)
	if err != nil {
		return nil, err
	}
	return ingest.New(ingest.Config{
		Store:   st,
		Objects: objects,
		Runner:  &media.SandboxRunner{Provider: provider, Image: cfg.MediaToolsImage, Limits: ingestLimits()},
		TempDir: filepath.Join(root, "ingest"),
		Logger:  logger,
	})
}

// newRenderPipeline builds the pipeline of the render job over the same sandbox, image and job directory root as the ingest,
// with the render limits (render.DefaultLimits: 4 CPUs, 4 GiB, 30 minutes a run). Its job directories are siblings of the
// ingest's.
func newRenderPipeline(cfg config.Config, st *store.Store, objects *storage.Client, meter *spend.ComputeMeter, logger *slog.Logger) (*renderjobs.Pipeline, error) {
	if err := os.MkdirAll(cfg.MediaTempDir, 0o750); err != nil {
		return nil, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	root, err := filepath.EvalSymlinks(cfg.MediaTempDir)
	if err != nil {
		return nil, fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	limits := render.DefaultLimits()
	rc := renderjobs.Config{
		Store:   st,
		Objects: objects,
		TempDir: filepath.Join(root, "render"),
		CPUs:    limits.CPU,
		Meter:   meter,
		Logger:  logger,
		// MOTION_GRAPHICS=off draws the graphics track as ASS text (D-35).
		MotionFallback: !cfg.MotionGraphics,
	}
	if cfg.MediaSandboxProvider == config.SandboxRemote {
		// The render job builds remote jobs: the runner fetches the footage and uploads the video through presigned URLs.
		if cfg.MotionGraphics {
			return nil, errors.New("MEDIA_SANDBOX_PROVIDER=remote does not render motion graphics yet (the template bundle is a directory, which a remote job cannot take): set MOTION_GRAPHICS=off, which draws the graphics as text, or use docker or hardened")
		}
		remote, err := newRemoteProvider(cfg)
		if err != nil {
			return nil, err
		}
		rc.Remote = &renderjobs.Remote{Provider: remote, Limits: limits}
		return renderjobs.New(rc)
	}
	provider, err := newSandboxProvider(cfg, root)
	if err != nil {
		return nil, err
	}
	rc.Runner = &media.SandboxRunner{Provider: provider, Image: cfg.MediaToolsImage, Limits: limits}
	return renderjobs.New(rc)
}

// fontsLabel is the label the media-tools Dockerfile sets when the image holds the title and caption fonts (/usr/share/fonts/cf).
// An image built before P3-04 lacks it, and every render with text on it would fail.
const fontsLabel = "org.contentfabrique.fonts"

// imageHasFonts reads the inspect output of imageInspectFormat: the image id, then the value of the fonts label.
func imageHasFonts(inspectOutput string) bool {
	f := strings.Fields(inspectOutput)
	return len(f) >= 2 && f[1] == "1"
}

// motionLabel is the label the media-tools Dockerfile sets when the image holds the motion-graphics renderer (chrome-headless-shell and
// cf-motion, D-35). An image built before it lacks it, and every render with a graphics track would fail.
const motionLabel = "org.contentfabrique.motion"

// imageHasMotion reads the third field of imageInspectFormat's output, the motion label.
func imageHasMotion(inspectOutput string) bool {
	f := strings.Fields(inspectOutput)
	return len(f) >= 3 && f[2] == "1"
}

// imageInspectFormat prints the image id, the fonts label and the motion label ("<no value>" when the image has none).
const imageInspectFormat = `{{.Id}} {{index .Config.Labels "` + fontsLabel + `"}} {{index .Config.Labels "` + motionLabel + `"}}`

// warnIfImageMissing logs when a container provider is selected and the media-tools image is not on this machine: the
// sandbox never pulls at job time, so every ingest would fail until the image is built or loaded. It also logs when the image
// is there but was built before the fonts were added to it (no fonts label), because renders with text would fail then.
func warnIfImageMissing(ctx context.Context, cfg config.Config, logger *slog.Logger) {
	if p := cfg.MountSandboxProvider(); p == config.SandboxLocal || p == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", imageInspectFormat, cfg.MediaToolsImage).Output() // #nosec G204 -- the image name is configuration
	if err != nil {
		logger.Warn("the media-tools image is not available to Docker; asset ingests will fail until it is built (task media:build, or docker build -t media-tools:dev packages/media-tools) or MEDIA_TOOLS_IMAGE names an image that exists",
			"image", cfg.MediaToolsImage, "provider", cfg.MountSandboxProvider(), "error", err)
		return
	}
	if !imageHasFonts(string(out)) {
		logger.Warn("the media-tools image has no title and caption fonts (it was built before they were added); renders with text will fail until it is rebuilt: task media:build",
			"image", cfg.MediaToolsImage, "label", fontsLabel)
	}
	if cfg.MotionGraphics && !imageHasMotion(string(out)) {
		logger.Warn("the media-tools image has no motion-graphics renderer (it was built before it was added); renders with a graphics track will fail until it is rebuilt (task media:build) or MOTION_GRAPHICS=off draws them as text",
			"image", cfg.MediaToolsImage, "label", motionLabel)
	}
}
