package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ingest"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

func runFootage(t *testing.T, deps footageDeps, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cmdFootage(args, &out, &errb, deps)
	return code, out.String(), errb.String()
}

// genFootage makes a tiny file with the host's ffmpeg, or skips the test.
func genFootage(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	out := filepath.Join(dir, name)
	cmd := exec.Command(ffmpeg, append(append([]string{"-nostdin", "-v", "error", "-y"}, args...), out)...) // #nosec G204 -- fixed arguments in a test
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot generate %s: %v: %s", name, err, b)
	}
	return out
}

func TestFootageCheckPrintsVerdictsInPlainWords(t *testing.T) {
	dir := t.TempDir()
	good := genFootage(t, dir, "gym.mp4", "-f", "lavfi", "-i", "testsrc2=size=360x640:rate=30:duration=2", "-f", "lavfi", "-i", "sine=duration=2",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac")
	b, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	again := filepath.Join(dir, "gym copy.mp4")
	if err := os.WriteFile(again, b, 0o600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.mov")
	if err := os.WriteFile(broken, bytes.Repeat([]byte("not a video "), 300), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errs := runFootage(t, defaultFootageDeps(), "check", good, again, broken, "--provider", "local")
	if code != 1 {
		t.Fatalf("exit %d, want 1 (one file is not usable): %s", code, errs)
	}
	for _, want := range []string{
		"gym.mp4 (as a clip): usable\n",
		"gym copy.mp4 (as a clip): usable, with a warning\n  - This is the same file as " + good + ".",
		"broken.mov: not usable\n  - ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, w := range []string{"ffmpeg", "ffprobe", "codec", "stream", "moov", "red", "green"} {
		if strings.Contains(strings.ToLower(out), w) {
			t.Errorf("the output uses %q:\n%s", w, out)
		}
	}

	// Only usable files: exit 0.
	if code, out, errs := runFootage(t, defaultFootageDeps(), "check", "--provider", "local", good); code != 0 || !strings.Contains(out, "usable") {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
}

func TestFootageCheckJSONAndMusic(t *testing.T) {
	dir := t.TempDir()
	// A phone-screen shaped video with sound, used as the music source.
	screen := genFootage(t, dir, "screen.mp4", "-f", "lavfi", "-i", "testsrc2=size=222x480:rate=30:duration=6", "-f", "lavfi", "-i", "sine=duration=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac")
	code, out, errs := runFootage(t, defaultFootageDeps(), "check", screen, "--as", "music", "--provider", "local", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	var res []footageResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res) != 1 {
		t.Fatalf("%v: %s", err, out)
	}
	a := res[0].Assessment
	if a.Use != ingest.UseMusic || a.Verdict != ingest.UsableWithWarning || !a.Has(ingest.CodeScreenRecording) || a.DurationMs < 5900 {
		t.Fatalf("%+v", a)
	}
	if !strings.Contains(out, `"verdict": "usable_with_warning"`) {
		t.Errorf("json: %s", out)
	}
}

func TestFootageUsageErrors(t *testing.T) {
	deps := defaultFootageDeps()
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "Usage:"},
		{[]string{"nope"}, 2, "unknown command"},
		{[]string{"check"}, 2, "name at least one file"},
		{[]string{"check", "x.mov", "--as", "poster"}, 2, "--as is clip or music"},
	} {
		code, _, errs := runFootage(t, deps, c.args...)
		if code != c.code || !strings.Contains(errs, c.want) {
			t.Errorf("%v: exit %d, stderr %q", c.args, code, errs)
		}
	}
	// A missing file is reported in its place; the others are still checked.
	if code, out, _ := runFootage(t, deps, "check", filepath.Join(t.TempDir(), "missing.mov"), "--provider", "local"); code != 1 || !strings.Contains(out, "missing.mov: could not be checked") {
		t.Errorf("missing file: exit %d: %s", code, out)
	}
	if code, out, _ := runFootage(t, deps, "help"); code != 0 || !strings.Contains(out, "footage check") {
		t.Errorf("help: %d %s", code, out)
	}
	prod := footageDeps{newProvider: newSandboxProvider, lookupEnv: func(string) (string, bool) { return "production", true }}
	if code, _, errs := runFootage(t, prod, "check", "x.mov", "--provider", "local"); code != 2 || errs == "" {
		t.Errorf("the local provider must be refused in production: %d %s", code, errs)
	}
}

// downProvider is a sandbox that cannot run anything, as rootless Docker without the gVisor runtime.
type downProvider struct{ runs *int }

func (downProvider) Name() string { return "hardened" }

func (p downProvider) Run(context.Context, sandbox.Job) (sandbox.Result, error) {
	*p.runs++
	return sandbox.Result{ExitCode: 125}, &sandbox.UnavailableError{Reason: "the gVisor runtime (runsc) is not registered with Docker", Err: errors.New("docker run exited 125")}
}

// A sandbox that cannot run is said to be unavailable, in plain words, with its own exit code; no file is called broken, and the
// other files are not tried.
func TestFootageCheckReportsAnUnavailableSandbox(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for _, name := range []string{"row.mov", "song.m4a"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("some bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	runs := 0
	deps := footageDeps{
		newProvider: func(string, string, ...string) (sandbox.Provider, error) { return downProvider{&runs}, nil },
		lookupEnv:   func(string) (string, bool) { return "", false },
	}
	code, out, errs := runFootage(t, deps, append([]string{"check", "--provider", "hardened"}, files...)...)
	if code != exitSandboxUnavailable {
		t.Fatalf("exit %d, want %d: %s %s", code, exitSandboxUnavailable, out, errs)
	}
	for _, want := range []string{
		"row.mov: could not be checked\n  - The sandbox is unavailable: the gVisor runtime (runsc) is not registered with Docker. This says nothing about the file",
		"song.m4a: could not be checked\n  - The sandbox is unavailable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"not usable", "can't be read", "export"} {
		if strings.Contains(out, bad) {
			t.Errorf("the output blames the file (%q):\n%s", bad, out)
		}
	}
	if runs != 1 {
		t.Errorf("the sandbox was tried %d times; once is enough", runs)
	}
	code, out, _ = runFootage(t, deps, "check", "--provider", "hardened", "--json", files[0])
	var res []footageResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res) != 1 || !res[0].SandboxUnavailable || res[0].Verdict != "" || code != exitSandboxUnavailable {
		t.Fatalf("json: %d %v %s", code, err, out)
	}
}
