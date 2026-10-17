package quicklook

import (
	"bytes"
	"encoding/json"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
)

// The real thing: ffmpeg cuts the candidates from a real H.264 file through the local sandbox provider (the same code path as
// production, minus the isolation), the sharpness of each is measured, and the picks avoid the blurred moments. It skips when
// this machine has no ffmpeg that can make the clip.
func TestFramesFromARealClip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(root, "proxy.mp4")
	// 12 s of a moving test pattern at 1280x720, blurred during the first second of every three: the candidates sit in the
	// middle of 1 s slices, so each quarter of the clip has one blurred candidate (at 3k + 0.5 s) and two sharp ones.
	gen := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25:duration=12",
		"-vf", "boxblur=14:enable='lt(mod(t,3),1)'", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "25", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot make the test clip: %v: %s", err, out)
	}
	mp4, err := os.ReadFile(clip)
	if err != nil {
		t.Fatal(err)
	}

	runner := &media.SandboxRunner{
		Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root}}), Image: "unused-by-the-local-provider",
		Limits: sandbox.Limits{CPU: 2, MemoryMB: 1024, Timeout: time.Minute, PIDs: 256},
	}
	r := newRig(t, rigOptions{runner: runner, tempDir: filepath.Join(root, "quicklook")})
	r.objs.objs[storage.AssetPrefix(r.asset.WorkspaceID, r.asset.ID)+"proxy_720.mp4"] = mp4
	r.store.derivs[0].SizeBytes = int64(len(mp4))

	res := mustRun(t, r, Options{})
	if res.Outcome != OutcomeDone {
		t.Fatalf("result = %+v", res)
	}
	var d detail
	if err := json.Unmarshal(r.store.snapshot().QuickLook, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Frames) != Frames {
		t.Fatalf("%d frames, want %d", len(d.Frames), Frames)
	}
	for i, f := range d.Frames {
		if f.AtMs%3000 == 500 {
			t.Errorf("frame %d was cut at %d ms, inside a blurred second", i, f.AtMs)
		}
		if i > 0 && f.AtMs <= d.Frames[i-1].AtMs {
			t.Errorf("frames are not in time order: %+v", d.Frames)
		}
		// One frame per quarter of the 12 s clip.
		if lo, hi := int64(i)*3000, int64(i+1)*3000; f.AtMs < lo || f.AtMs >= hi {
			t.Errorf("frame %d at %d ms is outside its quarter [%d, %d)", i, f.AtMs, lo, hi)
		}
		b := r.objs.objs[storage.AssetPrefix(r.asset.WorkspaceID, r.asset.ID)+f.Name]
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("frame %d is not a JPEG: %v", i, err)
		}
		if long := max(cfg.Width, cfg.Height); long != LongSide || cfg.Width%2 != 0 || cfg.Height%2 != 0 {
			t.Errorf("frame %d is %dx%d, want %d px on the long side, both sides even", i, cfg.Width, cfg.Height, LongSide)
		}
		if len(b) > 200<<10 {
			t.Errorf("frame %d is %d bytes", i, len(b))
		}
	}

	// The job directory is gone and the original was never part of this.
	if left, _ := filepath.Glob(filepath.Join(root, "quicklook", "quicklook-*")); len(left) != 0 {
		t.Errorf("job directories left behind: %v", left)
	}
}
