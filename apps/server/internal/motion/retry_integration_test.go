package motion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// A frame whose screenshot hangs is drawn again (cf-motion's frames.go), with real Chrome in the sandbox: a hang is injected at one
// frame with the runner's test-only --test-hang. Hung once, the frame is drawn again on the same page; hung twice, Chrome is restarted
// and the render resumes from that frame; hung a third time, the render fails and says so. A frame drawn again, on the same page or
// on a fresh Chrome, is byte for byte the frame of a render that never hung.
func TestIntegrationFrameRetryInDocker(t *testing.T) {
	runner, root := dockerRunner(t)
	it := liveReadableItems()[0]
	job, err := BuildJob(out916, 30, timeline.Frame(it.DurationMs, 30), resolveAll(t, []timeline.GraphicsItem{it}))
	if err != nil {
		t.Fatal(err)
	}
	hung := job.Segments[0].StartFrame + 10
	run := func(name string, args ...string) (Report, Manifest, error) {
		work := filepath.Join(root, "retry-"+name)
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		rep, err := render(context.Background(), runner, job, work, filepath.Join(work, "bundle"), Options{InImage: true, runnerArgs: args}, Bundle())
		if err != nil {
			return rep, Manifest{}, err
		}
		m, err := ReadManifest(filepath.Join(work, OutDir), job)
		if err != nil {
			t.Fatal(err)
		}
		return rep, m, nil
	}
	_, normal, err := run("normal")
	if err != nil {
		t.Fatal(err)
	}
	_, want := combined(normal)
	for _, c := range []struct {
		times    int
		recovery ManifestRecovery
	}{{1, ManifestRecovery{Retries: 1}}, {2, ManifestRecovery{Retries: 1, Restarts: 1}}} {
		rep, m, err := run(fmt.Sprintf("hang%d", c.times), "--frame-timeout", "1s", "--test-hang", fmt.Sprintf("%d:%d", hung, c.times))
		if err != nil {
			t.Fatalf("frame %d hung %d times: %v", hung, c.times, err)
		}
		if rep.Recovery != c.recovery {
			t.Errorf("hung %d times: recovery %+v, want %+v", c.times, rep.Recovery, c.recovery)
		}
		if _, got := combined(m); got != want {
			for i, h := range m.Segments[0].SHA256 {
				if normal.Segments[0].SHA256[i] != h {
					t.Errorf("hung %d times: frame %d differs from the normal render", c.times, int64(i)+job.Segments[0].StartFrame)
				}
			}
		}
	}
	_, _, err = run("hang3", "--frame-timeout", "1s", "--test-hang", fmt.Sprintf("%d:3", hung))
	if err == nil || !strings.Contains(err.Error(), "nor once more after restarting chrome") || !strings.Contains(err.Error(), fmt.Sprintf("frame %d", hung)) {
		t.Fatalf("hung three times: %v", err)
	}
}
