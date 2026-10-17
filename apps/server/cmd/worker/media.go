package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ingest"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// ingestLimits bound every sandboxed tool run of the ingest. The timeout here is the default (ffprobe and the other quick
// tools); the pipeline sets a longer one for each run that depends on the clip's length (ingest.HeavyTimeout, LightTimeout).
func ingestLimits() sandbox.Limits {
	return sandbox.Limits{CPU: 2, MemoryMB: 3072, Timeout: ingest.ProbeTimeout, PIDs: 512}
}

// newSandboxProvider builds the provider MEDIA_SANDBOX_PROVIDER names, rooted at the job directory. The local provider
// isolates nothing, so it is refused in staging and production here too (config.Load already refuses it).
func newSandboxProvider(cfg config.Config, root string) (sandbox.Provider, error) {
	pol := sandbox.Policy{AllowedRoots: []string{root}}
	switch cfg.MediaSandboxProvider {
	case config.SandboxDocker:
		return sandbox.NewDocker(pol), nil
	case config.SandboxHardened:
		return sandbox.NewHardened(pol), nil
	case config.SandboxLocal:
		if err := sandbox.CheckLocalAllowed(cfg.Env); err != nil {
			return nil, err
		}
		return sandbox.NewLocal(pol), nil
	}
	return nil, fmt.Errorf("MEDIA_SANDBOX_PROVIDER: unknown provider %q", cfg.MediaSandboxProvider)
}

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

// warnIfImageMissing logs when a container provider is selected and the media-tools image is not on this machine: the
// sandbox never pulls at job time, so every ingest would fail until the image is built or loaded.
func warnIfImageMissing(ctx context.Context, cfg config.Config, logger *slog.Logger) {
	if cfg.MediaSandboxProvider == config.SandboxLocal {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", cfg.MediaToolsImage).Run(); err != nil { // #nosec G204 -- the image name is configuration
		logger.Warn("the media-tools image is not available to Docker; asset ingests will fail until it is built (docker build -t media-tools:dev packages/media-tools) or MEDIA_TOOLS_IMAGE names an image that exists",
			"image", cfg.MediaToolsImage, "provider", cfg.MediaSandboxProvider, "error", err)
	}
}
