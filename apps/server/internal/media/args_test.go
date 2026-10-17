package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

func TestProbeArgsGolden(t *testing.T) {
	got := strings.Join(ProbeArgs("/in/0"), " ")
	want := "ffprobe -v error -protocol_whitelist file -print_format json -show_format -show_streams -i /in/0"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestTestClipArgsGolden(t *testing.T) {
	a, err := TestClipArgs("/work/clip.mp4", 30)
	if err != nil {
		t.Fatal(err)
	}
	want := "ffmpeg -nostdin -hide_banner -v error -y " +
		"-f lavfi -i testsrc2=size=1920x1080:rate=30:duration=30 " +
		"-f lavfi -i sine=frequency=440:sample_rate=48000:duration=30 " +
		"-c:v libx264 -preset ultrafast -crf 18 -pix_fmt yuv420p -c:a aac -b:a 128k -movflags +faststart /work/clip.mp4"
	if got := strings.Join(a, " "); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	for _, n := range []int{0, -1, 3601} {
		if _, err := TestClipArgs("x", n); err == nil {
			t.Errorf("length %d must be refused", n)
		}
	}
}

func TestTranscodeArgsGolden(t *testing.T) {
	a, err := TranscodeH264Args("/in/0", "/work/out.mp4", TranscodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "ffmpeg -nostdin -hide_banner -v error -y -protocol_whitelist file -i /in/0 -map 0:v:0 -map 0:a:0? " +
		"-c:v libx264 -preset veryfast -crf 23 -pix_fmt yuv420p -c:a aac -b:a 128k -movflags +faststart /work/out.mp4"
	if got := strings.Join(a, " "); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if _, err := TranscodeH264Args("a", "b", TranscodeOptions{Preset: "x; rm -rf /"}); err == nil {
		t.Error("an unknown preset must be refused")
	}
	if _, err := TranscodeH264Args("a", "b", TranscodeOptions{CRF: 52}); err == nil {
		t.Error("CRF 52 must be refused")
	}
	if _, err := VersionArgs("sh"); err == nil {
		t.Error("only ffmpeg and ffprobe have version args")
	}
}

type fakeRunner struct {
	res sandbox.Result
	err error
	got Invocation
}

func (f *fakeRunner) Run(_ context.Context, inv Invocation) (sandbox.Result, error) {
	f.got = inv
	return f.res, f.err
}

func writeFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clip.mov")
	if err := os.WriteFile(p, make([]byte, 1234), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProbeThroughRunner(t *testing.T) {
	raw, err := os.ReadFile("testdata/iphone_portrait_apac.json")
	if err != nil {
		t.Fatal(err)
	}
	path := writeFile(t)
	r := &fakeRunner{res: sandbox.Result{Stdout: raw}}
	m, err := Probe(context.Background(), r, path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 2160 || m.Height != 3840 {
		t.Errorf("%+v", m)
	}
	if len(r.got.Inputs) != 1 || r.got.Inputs[0].Host != path || r.got.Inputs[0].Container != "/in/0" || r.got.OutputDir != "" {
		t.Errorf("invocation %+v", r.got)
	}
	if strings.Join(r.got.Args, " ") != strings.Join(ProbeArgs("/in/0"), " ") {
		t.Errorf("args %v", r.got.Args)
	}
}

func TestProbeErrors(t *testing.T) {
	path := writeFile(t)
	ctx := context.Background()
	if _, err := Probe(ctx, &fakeRunner{res: sandbox.Result{ExitCode: 1, Stderr: []byte("Invalid data found")}}, path); err == nil {
		t.Error("exit 1")
	} else {
		var te *ToolError
		if !errors.As(err, &te) || te.ExitCode != 1 || !strings.Contains(te.Stderr, "Invalid data") {
			t.Errorf("want a ToolError, got %v", err)
		}
	}
	if _, err := Probe(ctx, &fakeRunner{res: sandbox.Result{Stdout: []byte("{"), StdoutTruncated: true}}, path); err == nil {
		t.Error("truncated output")
	}
	if _, err := Probe(ctx, &fakeRunner{err: sandbox.ErrTimeout}, path); !errors.Is(err, sandbox.ErrTimeout) {
		t.Errorf("runner error must be wrapped: %v", err)
	}
	if _, err := Probe(ctx, &fakeRunner{}, filepath.Join(t.TempDir(), "missing.mov")); err == nil {
		t.Error("missing file")
	}
	if _, err := Probe(ctx, &fakeRunner{}, t.TempDir()); err == nil {
		t.Error("a directory is not a file")
	}
}

type fakeProvider struct{ job sandbox.Job }

func (*fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Run(_ context.Context, j sandbox.Job) (sandbox.Result, error) {
	f.job = j
	return sandbox.Result{}, nil
}

func TestSandboxRunnerBuildsJob(t *testing.T) {
	fp := &fakeProvider{}
	r := &SandboxRunner{Provider: fp, Image: "media-tools:dev", Limits: sandbox.DefaultLimits(), Labels: map[string]string{"kind": "probe"}}
	_, err := r.Run(context.Background(), Invocation{
		Args:      []string{"ffmpeg", "-i", InputPath(0), OutputPath("o.mp4")},
		Inputs:    []Input{{Host: "/h/in.mov", Container: InputPath(0)}},
		OutputDir: "/h/out",
	})
	if err != nil {
		t.Fatal(err)
	}
	j := fp.job
	if j.Image != "media-tools:dev" || j.Network != sandbox.NetworkNone || j.Labels["kind"] != "probe" {
		t.Errorf("job %+v", j)
	}
	if len(j.Mounts) != 2 || j.Mounts[0].Writable || j.Mounts[0].Container != "/in/0" ||
		!j.Mounts[1].Writable || j.Mounts[1].Container != "/work" || j.Mounts[1].Host != "/h/out" {
		t.Errorf("mounts %+v", j.Mounts)
	}
	if _, err := (&SandboxRunner{}).Run(context.Background(), Invocation{}); err == nil {
		t.Error("no provider")
	}
}
