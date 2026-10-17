package editor

import (
	"bytes"
	"context"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
)

// The real thing, end to end, without a model: a generated proxy in MinIO, ffmpeg (through the local sandbox provider, which
// needs ffmpeg on the machine), frames cached under the asset's prefix, read back by the image resolver. It skips without ffmpeg or
// Docker.
func TestFramesWithRealFFmpegAndMinIO(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	cfg := storagetest.NewBucket(t) // skips without Docker
	objects, err := storage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	a := e.asset(e.wsA, clip{durMs: 6000, noProxy: true})

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxyFile := filepath.Join(root, "proxy.mp4")
	gen := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=720x1280:rate=30:duration=6",
		"-pix_fmt", "yuv420p", "-c:v", "libx264", "-g", "30", proxyFile)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot generate the test proxy: %v: %s", err, out)
	}
	f, err := os.Open(proxyFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fi, _ := f.Stat()
	proxyKey, _ := storage.DerivativeKey(e.wsA.ID, a, "proxy_720.mp4")
	ctx := context.Background()
	if err := objects.Put(ctx, proxyKey, f, fi.Size(), "video/mp4"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO asset_derivatives (workspace_id, asset_id, kind, r2_key, content_type, size_bytes) VALUES ($1, $2, 'proxy_720', $3, 'video/mp4', $4)`,
		e.wsA.ID, a, proxyKey, fi.Size()); err != nil {
		t.Fatal(err)
	}

	runner := &media.SandboxRunner{
		Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root}}), Image: "unused",
		Limits: sandbox.Limits{CPU: 1, MemoryMB: 512, Timeout: time.Minute, PIDs: 128},
	}
	work := filepath.Join(root, "frames")
	if err := os.Mkdir(work, 0o750); err != nil {
		t.Fatal(err)
	}
	fs := &FrameService{Store: e.st, Objects: objects, Runner: runner, TempDir: work, Logger: quiet}

	times := OverviewTimes(6000)
	frames, err := fs.Frames(ctx, e.wsA.ID, a, times)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("%d frames for a 6 s clip", len(frames))
	}
	resolver := ImageResolver{Objects: objects}
	wctx := llm.WithWorkspace(ctx, e.wsA.ID)
	var first []byte
	for i, fr := range frames {
		mt, data, err := resolver.ResolveImage(wctx, fr.Ref)
		if err != nil || mt != "image/jpeg" {
			t.Fatalf("frame %d: %s %v", i, mt, err)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		// A 720x1280 portrait proxy: 384 px on the long side, 216 on the short one, both even.
		if cfg.Height != 384 || cfg.Width != 216 {
			t.Errorf("frame %d is %dx%d, want 216x384", i, cfg.Width, cfg.Height)
		}
		if i == 0 {
			first = data
		} else if bytes.Equal(first, data) {
			t.Errorf("frame %d is the same picture as frame 0: the seek did nothing", i)
		}
	}
	// Cached: a second look makes no new object (the cache key is stable) and runs nothing.
	before, _ := objects.Head(ctx, frames[1].Ref)
	if _, err := fs.Frames(ctx, e.wsA.ID, a, times); err != nil {
		t.Fatal(err)
	}
	if after, _ := objects.Head(ctx, frames[1].Ref); after.ETag != before.ETag {
		t.Error("a cached frame was written again")
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}
