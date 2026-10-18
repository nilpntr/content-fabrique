package motion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// recordRunner records the invocation and fails it, so render stops after the run.
type recordRunner struct{ inv media.Invocation }

func (r *recordRunner) Run(_ context.Context, inv media.Invocation) (sandbox.Result, error) {
	r.inv = inv
	return sandbox.Result{}, errors.New("recorded")
}

// The motion run's one writable mount is its own fresh directory (work/motion: the job and the frames); the bundle is staged outside
// it and mounted read-only only, so inside the sandbox nothing can write to it; the run gets the motion limits (Chrome needs more than
// ffmpeg); and the job file is new (a symlink planted in its place is not followed).
func TestTheRunMountsTheBundleReadOnlyOnly(t *testing.T) {
	job, err := BuildJob(out916, 30, 30, resolveAll(t, []timeline.GraphicsItem{sampleItems()[0]}))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	bundle := filepath.Join(work, "motion-bundle")
	rr := &recordRunner{}
	_, err = Render(context.Background(), rr, job, work, bundle, Options{InImage: true})
	if !errors.Is(err, ErrRun) {
		t.Fatalf("err %v", err)
	}
	inv := rr.inv
	if inv.OutputDir != filepath.Join(work, OutDir) {
		t.Errorf("the writable mount is %s, not the run's own directory", inv.OutputDir)
	}
	if len(inv.Inputs) != 1 || inv.Inputs[0].Host != bundle {
		t.Errorf("inputs %+v", inv.Inputs)
	}
	if inv.Limits == nil || *inv.Limits != DefaultLimits() {
		t.Errorf("limits %+v, want the motion limits %+v", inv.Limits, DefaultLimits())
	}
	if !slices.Contains(inv.Args, sandbox.WorkDir) || !slices.Contains(inv.Args, media.OutputPath(jobFile)) {
		t.Errorf("args %v", inv.Args)
	}
	if _, err := os.Stat(filepath.Join(work, OutDir, jobFile)); err != nil {
		t.Errorf("the job is not in the run's directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bundle, "scene.html")); err != nil {
		t.Errorf("the bundle was not staged: %v", err)
	}

	// A bundle directory inside the run's writable directory is refused.
	if _, err := Render(context.Background(), rr, job, work, filepath.Join(work, OutDir, "b"), Options{InImage: true}); err == nil || !errors.Is(err, ErrRun) {
		t.Errorf("a bundle inside the writable directory: %v", err)
	}
}

func TestWriteNewRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, jobFile)); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(dir, jobFile), []byte("{}")); err == nil {
		t.Error("written through a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("the target changed: %q", b)
	}
}
