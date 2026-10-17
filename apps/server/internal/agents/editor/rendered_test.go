package editor

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func TestParseLumaStats(t *testing.T) {
	text := `frame:0    pts:0       pts_time:0
lavfi.signalstats.YMIN=16
lavfi.signalstats.YLOW=18
lavfi.signalstats.YAVG=33.5
lavfi.signalstats.YHIGH=61
lavfi.signalstats.YMAX=200
frame:1    pts:6       pts_time:0.2
lavfi.signalstats.YAVG=34
lavfi.signalstats.YHIGH=62
`
	s, err := ParseLumaStats([]byte(text))
	if err != nil || s.StepMs != media.LumaStatsStepMs || len(s.Luma) != 2 || s.Luma[0] != 33.5 || s.High[1] != 62 {
		t.Fatalf("%+v %v", s, err)
	}
	for _, bad := range []string{"", "frame:0\nlavfi.signalstats.YAVG=nan\nlavfi.signalstats.YHIGH=1\n", "frame:0\nlavfi.signalstats.YAVG=1\n"} {
		if _, err := ParseLumaStats([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// the renders the fake store lists
type fakeRenders struct{ renders []store.Render }

func (f fakeRenders) ListRenders(context.Context, ids.WorkspaceID, ids.ProjectID, store.RenderFilter, int) ([]store.Render, error) {
	return f.renders, nil
}

// The measurer over a generated preview and the real ffmpeg (through the local sandbox provider): a flat dark grey video measures as
// the grey it is, at the rate the pass samples. It skips without ffmpeg.
func TestRenderMeasurerWithRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "render.mp4")
	// RGB 0x30 grey is luma 57 in limited range.
	gen := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=0x303030:size=270x480:rate=30:duration=2",
		"-pix_fmt", "yuv420p", "-c:v", "libx264", file)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot generate the test video: %v: %s", err, out)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	ws := ids.WorkspaceID{UUID: newAssetID().UUID}
	prj := ids.ProjectID{UUID: newAssetID().UUID}
	rid := ids.RenderID{UUID: newAssetID().UUID}
	key, err := storage.RenderKey(ws, prj, rid, storage.RenderMP4Name)
	if err != nil {
		t.Fatal(err)
	}
	objs := newMemObjects()
	objs.set(key, data)
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o750); err != nil {
		t.Fatal(err)
	}
	m := &RenderMeasurer{
		Store:   fakeRenders{renders: []store.Render{{ID: rid, WorkspaceID: ws, ProjectID: prj, Status: store.RenderReady, Mp4Key: &key}}},
		Objects: objs, TempDir: work, Logger: quiet,
		Runner: &media.SandboxRunner{Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root}}), Image: "unused",
			Limits: sandbox.Limits{CPU: 1, MemoryMB: 512, Timeout: time.Minute, PIDs: 128}},
	}
	s, err := m.Rendered(context.Background(), ws, prj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Luma); n < 9 || n > 11 {
		t.Errorf("%d samples for 2 s at %d fps", n, media.LumaStatsFPS)
	}
	e, ok := s.WholeExposure()
	if !ok || math.Abs(e.P90-(57-16)/219.0) > 0.03 || math.Abs(e.Mean-(57-16)/219.0) > 0.03 {
		t.Errorf("a flat grey of luma 57 measures p90 %.3f, mean %.3f", e.P90, e.Mean)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}

	// No ready preview of the version: ErrNoPreview. A render whose key is not its own is refused.
	none := &RenderMeasurer{Store: fakeRenders{renders: []store.Render{{ID: rid, Status: store.RenderFailed}}}, Objects: objs, TempDir: work}
	if _, err := none.Rendered(context.Background(), ws, prj, 1); !errors.Is(err, ErrNoPreview) {
		t.Errorf("no ready render: %v", err)
	}
	other := "ws/x/other.mp4"
	bad := &RenderMeasurer{Store: fakeRenders{renders: []store.Render{{ID: rid, WorkspaceID: ws, ProjectID: prj, Status: store.RenderReady, Mp4Key: &other}}}, Objects: objs, TempDir: work}
	if _, err := bad.Rendered(context.Background(), ws, prj, 1); err == nil || !strings.Contains(err.Error(), "not its own") {
		t.Errorf("a foreign key: %v", err)
	}
}
