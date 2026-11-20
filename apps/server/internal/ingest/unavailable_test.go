package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// scriptedProvider is a sandbox provider that runs nothing: it answers ffprobe with a recorded probe of an iPhone clip, the full
// decode with a clean run to the end, and the sound extraction with whatever audio says.
type scriptedProvider struct {
	probe []byte
	audio func(job sandbox.Job) (sandbox.Result, error)
	// down, when set, is what every job returns.
	down error
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Run(_ context.Context, job sandbox.Job) (sandbox.Result, error) {
	if p.down != nil {
		return sandbox.Result{ExitCode: -1}, p.down
	}
	switch {
	case job.Args[0] == "ffprobe":
		return sandbox.Result{Stdout: p.probe}, nil
	case slices.Contains(job.Args, "null"): // the full decode
		return sandbox.Result{Stdout: []byte("out_time_us=87100000\nprogress=end\n")}, nil
	}
	return p.audio(job)
}

func scriptedCheck(t *testing.T, p *scriptedProvider) (Assessment, error) {
	t.Helper()
	probe, err := os.ReadFile(filepath.Join("..", "media", "testdata", "iphone_portrait_apac.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.probe = probe
	dir := t.TempDir()
	clip := filepath.Join(dir, "row.mov")
	if err := os.WriteFile(clip, []byte("a clip"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &media.SandboxRunner{Provider: p, Image: "media-tools:dev", Limits: sandbox.DefaultLimits()}
	return Check(context.Background(), runner, clip, CheckOptions{WorkDir: dir})
}

// The live test on a rootless Docker VPS: every ffmpeg run that writes /work failed with "Permission denied" (exit 243), and the
// footage check told the creator the sound of every clip "can't be read". A sandbox that cannot write its output is an error that
// names the sandbox, never a finding about the file.
func TestCheckDoesNotBlameTheFileForTheSandbox(t *testing.T) {
	for _, c := range []struct {
		name  string
		p     *scriptedProvider
		cause string
	}{
		{"the output directory is not writable", &scriptedProvider{audio: func(sandbox.Job) (sandbox.Result, error) {
			return sandbox.Result{ExitCode: 243, Stderr: []byte("[out#0/wav @ 0x55d0] Error opening output /work/audio_16k.wav: Permission denied\nError opening output file /work/audio_16k.wav.\n")}, nil
		}}, "cannot read its input or write its output directory"},
		{"docker is not running", &scriptedProvider{down: &sandbox.UnavailableError{Reason: "Docker is not running, or this user cannot reach it"}}, "Docker is not running"},
		{"the gVisor runtime is missing", &scriptedProvider{down: &sandbox.UnavailableError{Reason: "the gVisor runtime (runsc) is not registered with Docker"}}, "runsc"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, err := scriptedCheck(t, c.p)
			reason, ok := sandbox.Unavailable(err)
			if !ok || !strings.Contains(reason, c.cause) {
				t.Fatalf("want an unavailable sandbox (%q), got %q, %v, err %v", c.cause, reason, ok, err)
			}
			if len(a.Findings) != 0 || a.Verdict != "" {
				t.Fatalf("the file was judged: %+v", a)
			}
		})
	}
}

// A sound track that ffmpeg itself rejects is still the file's problem: the clip is usable without its sound.
func TestCheckStillJudgesABadSoundTrack(t *testing.T) {
	a, err := scriptedCheck(t, &scriptedProvider{audio: func(sandbox.Job) (sandbox.Result, error) {
		return sandbox.Result{ExitCode: 183, Stderr: []byte("/in/0: Invalid data found when processing input\n")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Has(CodeSoundUnreadable) || a.Verdict != UsableWithWarning {
		t.Fatalf("%+v", a)
	}
}

// The ingest itself: a sandbox that cannot write is returned as an unavailable sandbox (the job waits), never a fileError that
// fails the asset with a reason shown to the creator.
func TestToolErrorsFromAnUnavailableSandboxAreNotFileErrors(t *testing.T) {
	p := &scriptedProvider{down: &sandbox.UnavailableError{Reason: "Docker is not running"}}
	runner := &media.SandboxRunner{Provider: p, Image: "media-tools:dev", Limits: sandbox.DefaultLimits()}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.mov")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := execTool(context.Background(), runner, dir, ProbeTimeout, []string{"ffmpeg", "-i", media.InputPath(0)}, in)
	var fe *fileError
	if toolFailed(err) || errors.As(err, &fe) {
		t.Fatalf("an unavailable sandbox must not look like a tool failure: %v", err)
	}
	if _, ok := sandbox.Unavailable(err); !ok {
		t.Fatalf("want an unavailable sandbox, got %v", err)
	}
}
