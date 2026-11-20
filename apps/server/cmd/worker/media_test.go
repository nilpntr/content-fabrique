package main

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/config"
)

// MEDIA_SANDBOX_PROVIDER=remote runs the render job only: the jobs that mount files take MEDIA_SANDBOX_MOUNT_PROVIDER, and
// without it the worker refuses to start, naming them. A remote render refuses motion graphics.
func TestRemoteProviderPaths(t *testing.T) {
	cfg := config.Config{MediaSandboxProvider: config.SandboxRemote, MediaRunnerURL: "http://localhost:8090", MediaRunnerToken: "0123456789abcdef0123"}
	if _, err := newSandboxProvider(cfg, t.TempDir()); err == nil || !strings.Contains(err.Error(), "MEDIA_SANDBOX_MOUNT_PROVIDER") || !strings.Contains(err.Error(), "ingest") {
		t.Errorf("remote without a mount provider: %v", err)
	}
	cfg.MediaSandboxMountProvider = config.SandboxDocker
	if p, err := newSandboxProvider(cfg, t.TempDir()); err != nil || p.Name() != "docker" {
		t.Errorf("remote with docker for the mounts: %v", err)
	}
	if p, err := newRemoteProvider(cfg); err != nil || p.Name() != "remote" {
		t.Errorf("the render's provider: %v", err)
	}
	cfg.MotionGraphics, cfg.MediaTempDir = true, t.TempDir()
	if _, err := newRenderPipeline(cfg, nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "MOTION_GRAPHICS=off") {
		t.Errorf("a remote render with motion graphics: %v", err)
	}
}

func TestImageHasFonts(t *testing.T) {
	for out, want := range map[string]bool{
		"sha256:0123abcd 1\n":          true,
		"sha256:0123abcd <no value>\n": false, // an image built before the fonts were added has no label
		"sha256:0123abcd 0\n":          false,
		"sha256:0123abcd\n":            false,
		"":                             false,
	} {
		if got := imageHasFonts(out); got != want {
			t.Errorf("imageHasFonts(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestImageHasMotion(t *testing.T) {
	for out, want := range map[string]bool{
		"sha256:0123abcd 1 1\n":          true,
		"sha256:0123abcd 1 <no value>\n": false, // an image built before the motion-graphics renderer was added
		"sha256:0123abcd 1\n":            false,
		"":                               false,
	} {
		if got := imageHasMotion(out); got != want {
			t.Errorf("imageHasMotion(%q) = %v, want %v", out, got, want)
		}
	}
}
