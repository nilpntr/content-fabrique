package analysis

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// The signals pass and the analysis run on proxies of odd footage the ingest accepts (a tenth of a second, a thin strip, a
// tall strip, a still picture held for seconds) without an error or a panic, and find at most a shot or two.
func TestAnalysisSurvivesOddProxies(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := &media.SandboxRunner{
		Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root}}), Image: "unused-by-the-local-provider",
		Limits: sandbox.Limits{CPU: 2, MemoryMB: 1024, Timeout: time.Minute, PIDs: 256},
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, src string
		ms        int64
	}{
		{"a tenth of a second", "testsrc2=size=360x640:rate=30:duration=0.1", 100},
		{"a thin strip", "testsrc2=size=1280x160:rate=30:duration=2", 2000},
		{"a tall strip", "testsrc2=size=160x1280:rate=30:duration=2", 2000},
		{"a still", "color=c=gray:size=360x640:rate=30:duration=3", 3000},
	} {
		t.Run(c.name, func(t *testing.T) {
			clip := filepath.Join(root, filepath.Base(t.Name())+".mp4")
			gen := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", c.src,
				"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", clip)
			if out, err := gen.CombinedOutput(); err != nil {
				t.Skipf("this ffmpeg cannot make the test clip: %v: %s", err, out)
			}
			an, err := AnalyzeFile(context.Background(), runner, work, clip, c.ms, DefaultParams(), DefaultShotParams())
			if err != nil {
				t.Fatal(err)
			}
			if len(an.Shots) > 2 {
				t.Errorf("%d shots in a clip without cuts", len(an.Shots))
			}
		})
	}
}
