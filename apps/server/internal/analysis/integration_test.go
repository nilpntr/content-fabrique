package analysis

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// The real pass: ffmpeg makes a clip of a box that moves up and down with a 3 s period (and a hard cut to another background half
// way), the local sandbox provider runs the signals pass as production does (minus the isolation), and the analysis finds the
// shots and the reps. It skips when this machine has no ffmpeg that can make the clip.
func TestRealPassOnAGeneratedClip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(root, "clip.mp4")
	graph := "color=c=0x303030:s=360x640:r=30:d=20[a];testsrc2=s=360x640:r=30:d=20[b];[a][b]concat=n=2:v=1:a=0[bg];" +
		"color=c=white:s=90x90:r=30:d=40[box];[bg][box]overlay=x=135:y='275+150*sin(2*PI*t/3)',format=yuv420p"
	gen := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", "nullsrc=s=16x16:d=1", "-filter_complex", graph,
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "30", "-keyint_min", "30", "-sc_threshold", "0", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot make the test clip: %v: %s", err, out)
	}

	runner := &media.SandboxRunner{
		Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root}}), Image: "unused-by-the-local-provider",
		Limits: sandbox.Limits{CPU: 2, MemoryMB: 1024, Timeout: time.Minute, PIDs: 256},
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	an, err := AnalyzeFile(context.Background(), runner, work, clip, 40000, Hints{Words: []string{"generated box"}}, DefaultParams(), DefaultShotParams())
	if err != nil {
		t.Fatal(err)
	}
	if an.Series.Len() < 395 || an.Series.Len() > 405 {
		t.Fatalf("%d samples for 40 s at 10 per second", an.Series.Len())
	}
	if len(an.Shots) != 2 {
		t.Fatalf("%d shots, want 2 (a cut at 20 s): %+v", len(an.Shots), an.Shots)
	}
	if cut := an.Shots[0].EndMs; cut < 19700 || cut > 20300 {
		t.Errorf("the cut is at %d ms, want about 20000", cut)
	}
	reps := momentsOf(an, KindRep)
	// two shots of 20 s with 3 s reps: about 5 reps each (the first and last half reps of a shot are not counted)
	if len(reps) < 6 || len(reps) > 14 {
		var parts []string
		for _, r := range reps {
			parts = append(parts, r.Description)
		}
		t.Fatalf("%d reps, want about 10: %s", len(reps), strings.Join(parts, "; "))
	}
	for _, r := range reps {
		if d := float64(r.EndMs-r.StartMs) / 1000; math.Abs(d-3) > 0.5 {
			t.Errorf("rep at %d ms is %.1f s long, want about 3", r.StartMs, d)
		}
		if r.StartMs < 20000 && r.EndMs > 20000 {
			t.Errorf("rep %d-%d ms crosses the cut", r.StartMs, r.EndMs)
		}
	}
	// the first shot is a plain grey background: dark and sharp enough; the line says what it found
	if line := an.Summary.Line(); !strings.Contains(line, "reps") {
		t.Errorf("summary line %q", line)
	}
}

func TestAnalysisArgsAreWellFormed(t *testing.T) {
	a := media.AnalysisArgs("/in/0")
	joined := strings.Join(a, " ")
	for _, want := range []string{"-filter_complex", "fps=10", "scale=32:32", "edgedetect", "scdet", "thumbs.raw", "file=sharp.txt", "file=scene.txt", "-f rawvideo"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q: %s", want, joined)
		}
	}
	if a[0] != "ffmpeg" || !strings.Contains(joined, "-protocol_whitelist file") {
		t.Errorf("the pass must be a local-files-only ffmpeg: %s", joined)
	}
}
