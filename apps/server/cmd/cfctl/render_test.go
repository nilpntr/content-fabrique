package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	renderFixture = "../../../../packages/schemas/timeline/fixtures/valid/v01_minimal_9x16.json"
	renderAsset   = "ast_01j3er3kwcgrasdgd4aznmxxe0"
)

func runRender(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(append([]string{"render"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestRenderUsageErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"no arguments":    {},
		"no assets":       {renderFixture, "-o", "x.mp4"},
		"no output":       {renderFixture, "--assets", "."},
		"two specs":       {renderFixture, renderFixture, "--assets", ".", "-o", "x.mp4"},
		"unknown profile": {renderFixture, "--assets", ".", "-o", "x.mp4", "--profile", "draft"},
		"unknown flag":    {renderFixture, "--nope"},
	} {
		if code, _, _ := runRender(args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if code, out, _ := runRender("help"); code != 0 || !strings.Contains(out, "cfctl render <spec.json>") {
		t.Errorf("render help: exit %d, %s", code, out)
	}
	// flag.ErrHelp prints the usage to stderr and exits 0.
	if code, _, errOut := runRender("--help"); code != 0 || !strings.Contains(errOut, "cfctl render <spec.json>") {
		t.Errorf("--help: exit %d, %s", code, errOut)
	}
}

func TestRenderDryRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, renderAsset+".mp4"), []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The spec may come before or after the flags.
	for _, args := range [][]string{
		{renderFixture, "--assets", dir, "--dry-run", "--ffmpeg", "/nonexistent/ffmpeg"},
		{"--assets", dir, "--profile", "preview", renderFixture, "--dry-run", "--ffmpeg", "/nonexistent/ffmpeg"},
	} {
		code, out, errOut := runRender(args...)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		for _, want := range []string{filepath.Join(dir, renderAsset+".mp4"), "-filter_complex", "-ss 3.200", "[0:v]trim=end=3.000", "<out.mp4>", "540:960"} {
			if !strings.Contains(out, want) {
				t.Errorf("dry run output lacks %q:\n%s", want, out)
			}
		}
	}

	code, out, _ := runRender(renderFixture, "--assets", dir, "--dry-run", "--profile", "final", "--ffmpeg", "/nonexistent/ffmpeg")
	if code != 0 || !strings.Contains(out, "1080:1920") || !strings.Contains(out, "-crf 18") {
		t.Errorf("final dry run: %d\n%s", code, out)
	}
}

func TestRenderRefusesWhatItCannotFind(t *testing.T) {
	dir := t.TempDir()
	if code, _, errOut := runRender(renderFixture, "--assets", dir, "--dry-run"); code != 1 || !strings.Contains(errOut, renderAsset) {
		t.Errorf("a missing asset: exit %d, %s", code, errOut)
	}
	if code, _, errOut := runRender(filepath.Join(dir, "missing.json"), "--assets", dir, "--dry-run"); code != 1 || !strings.Contains(errOut, "missing.json") {
		t.Errorf("a missing spec: exit %d, %s", code, errOut)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"schema":"contentfabrique.timeline/v1","surprise":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runRender(bad, "--assets", dir, "--dry-run"); code != 1 {
		t.Errorf("an unknown field must be refused, exit %d", code)
	}
	// Asset ids are compared with the names in the directory exactly: no glob, no extension in the id.
	for _, f := range []string{"ast_a.mp4", "ast_ab.mp4", "ast_a.b.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gr := &dirResolver{dir: dir, ffmpeg: "/nonexistent/ffmpeg"}
	if got, err := gr.Resolve("ast_a", ""); err != nil || filepath.Base(got.Location) != "ast_a.mp4" {
		t.Errorf("ast_a: %+v %v", got, err)
	}
	for _, id := range []string{"ast_*", "ast_?", "ast_[a]", "ast_a.mp4", "ast_"} {
		if got, err := gr.Resolve(id, ""); err == nil {
			t.Errorf("asset id %q must not match anything, got %+v", id, got)
		}
	}
	// An asset id from a spec must not leave the assets directory.
	r := &dirResolver{dir: dir}
	for _, id := range []string{"../secret", "a/b", `a\b`, ""} {
		if _, err := r.Resolve(id, ""); err == nil {
			t.Errorf("asset id %q must be refused", id)
		}
	}
}

func TestRenderRuns(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not on PATH")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, renderAsset+".mp4")
	gen := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=30:d=7", "-f", "lavfi", "-i", "sine=d=7", "-shortest", "-pix_fmt", "yuv420p", src) //nolint:gosec // test helper
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out := filepath.Join(dir, "out.mp4")
	code, stdout, stderr := runRender(renderFixture, "--assets", dir, "-o", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "wrote "+out) || !strings.Contains(stderr, "100%") {
		t.Errorf("stdout %q, stderr %q", stdout, stderr)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Errorf("no output: %v", err)
	}
}
