package render

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// fakeRunner is a media.Runner that records the invocations and answers them from a script. Like ffmpeg, it writes the
// -progress file and the output into the work directory.
type fakeRunner struct {
	mu    sync.Mutex
	calls []media.Invocation
	// run answers the call-th invocation: stderr text, the progress file's content and whether the output is written.
	run func(call int) fakeAnswer
}

type fakeAnswer struct {
	stderr   string
	progress string
	exit     int
	noOutput bool
	err      error
}

func (f *fakeRunner) Run(_ context.Context, inv media.Invocation) (sandbox.Result, error) {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, inv)
	f.mu.Unlock()
	var ans fakeAnswer
	if f.run != nil {
		ans = f.run(n)
	}
	if ans.err != nil {
		return sandbox.Result{}, ans.err
	}
	for i, a := range inv.Args {
		if a == "-progress" && ans.progress != "" {
			if err := os.WriteFile(hostPath(inv, inv.Args[i+1]), []byte(ans.progress), 0o600); err != nil {
				return sandbox.Result{}, err
			}
		}
	}
	if last := inv.Args[len(inv.Args)-1]; strings.HasPrefix(last, sandbox.WorkDir+"/") && !ans.noOutput && ans.exit == 0 {
		if err := os.WriteFile(hostPath(inv, last), []byte("mp4"), 0o600); err != nil {
			return sandbox.Result{}, err
		}
	}
	return sandbox.Result{ExitCode: ans.exit, Stderr: []byte(ans.stderr)}, nil
}

func hostPath(inv media.Invocation, container string) string {
	return filepath.Join(inv.OutputDir, strings.TrimPrefix(container, sandbox.WorkDir+"/"))
}

func (f *fakeRunner) joined(i int) string { return strings.Join(f.calls[i].Args, " ") }

const loudnormJSON = `
[Parsed_loudnorm_5 @ 0x1234]
{
	"input_i" : "-23.45",
	"input_tp" : "-6.10",
	"input_lra" : "3.20",
	"input_thresh" : "-33.70",
	"output_i" : "-14.62",
	"output_tp" : "-1.50",
	"output_lra" : "2.60",
	"output_thresh" : "-24.95",
	"normalization_type" : "dynamic",
	"target_offset" : "0.62"
}
`

func audioSpec() *timeline.Spec {
	return specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 3000, 0, withSourceAudio(0))})
}

func TestExecuteFinalRunsTwoPasses(t *testing.T) {
	plan := mustCompile(t, audioSpec(), Final)
	if plan.Loudnorm == nil || !plan.Loudnorm.TwoPass {
		t.Fatal("the final profile with audio must plan two passes")
	}
	var progress []Progress
	r := &fakeRunner{run: func(call int) fakeAnswer {
		if call == 0 {
			return fakeAnswer{stderr: loudnormJSON, progress: "frame=45\nout_time_us=1500000\nspeed=2.0x\nprogress=continue\n"}
		}
		return fakeAnswer{progress: "frame=90\nout_time_us=3000000\nspeed=1.5x\nprogress=end\n"}
	}}
	out := filepath.Join(t.TempDir(), "out.mp4")
	err := Execute(context.Background(), plan, r, ExecuteOptions{Output: out, Progress: func(p Progress) { progress = append(progress, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("%d ffmpeg runs, want 2", len(r.calls))
	}
	pass1, pass2 := r.joined(0), r.joined(1)
	if !strings.Contains(pass1, "print_format=json") || !strings.HasSuffix(pass1, "-f null -") || !strings.Contains(pass1, "-loglevel info") || !strings.HasPrefix(pass1, "ffmpeg ") {
		t.Errorf("pass 1: %s", pass1)
	}
	want := plan.Loudnorm.Filter + ":measured_I=-23.45:measured_TP=-6.1:measured_LRA=3.2:measured_thresh=-33.7:offset=0.62:linear=true"
	if !strings.Contains(pass2, want) || strings.Contains(pass2, "print_format") {
		t.Errorf("pass 2 lacks the measured filter %q:\n%s", want, pass2)
	}
	if !strings.HasSuffix(pass2, "-y /work/out.mp4") || !strings.Contains(pass2, "-progress /work/progress-render.txt") {
		t.Errorf("pass 2: %s", pass2)
	}
	// The input is mounted read-only at /in/0 and its location is rewritten; the work directory is the writable mount.
	if !strings.Contains(pass2, "-i /in/0 ") || strings.Contains(pass2, "/a/original/") {
		t.Errorf("input paths: %s", pass2)
	}
	if got := r.calls[1].Inputs; len(got) != 1 || got[0].Host != "/a/original/"+assetA || got[0].Container != "/in/0" {
		t.Errorf("inputs %+v", got)
	}
	if r.calls[1].OutputDir == "" || r.calls[1].OutputDir != r.calls[0].OutputDir {
		t.Errorf("work directories %q %q", r.calls[0].OutputDir, r.calls[1].OutputDir)
	}
	// Both passes keep the program's own protocol and format whitelist.
	if !strings.Contains(pass2, "-protocol_whitelist file -format_whitelist "+media.UntrustedFormats) {
		t.Errorf("whitelists: %s", pass2)
	}
	// The plan is not changed by running it, and the output is where the caller asked.
	if strings.Contains(strings.Join(plan.Args, " "), "measured_I") {
		t.Error("Execute modified the plan's arguments")
	}
	if data, err := os.ReadFile(out); err != nil || string(data) != "mp4" {
		t.Errorf("output: %q %v", data, err)
	}
	// The default work directory is removed afterwards.
	if _, err := os.Stat(r.calls[0].OutputDir); !os.IsNotExist(err) {
		t.Errorf("work directory left behind: %v", err)
	}

	if len(progress) != 2 {
		t.Fatalf("progress %+v", progress)
	}
	if p := progress[0]; p.Stage != "measure" || p.Frame != 45 || p.OutTimeMs != 1500 || p.Fraction != 0.5 || p.Speed != 2 || p.Done {
		t.Errorf("first progress %+v", p)
	}
	if p := progress[1]; p.Stage != "render" || p.Frame != 90 || p.OutTimeMs != 3000 || p.Fraction != 1 || p.Speed != 1.5 || !p.Done {
		t.Errorf("second progress %+v", p)
	}
}

func TestExecutePreviewIsOnePass(t *testing.T) {
	plan := mustCompile(t, audioSpec(), Preview)
	r := &fakeRunner{}
	if err := Execute(context.Background(), plan, r, ExecuteOptions{Output: filepath.Join(t.TempDir(), "o.mp4")}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || !strings.Contains(r.joined(0), plan.Loudnorm.Filter+",") {
		t.Errorf("calls %v", r.calls)
	}
}

// A silent mix has nothing to normalise: no loudnorm in the graph, and so a single run even in the final profile.
func TestExecuteSilentFinalIsOnePass(t *testing.T) {
	plan := mustCompile(t, specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 3000, 0)}), Final)
	if plan.Loudnorm != nil {
		t.Fatal("no audio sources: no loudnorm")
	}
	r := &fakeRunner{}
	if err := Execute(context.Background(), plan, r, ExecuteOptions{Output: filepath.Join(t.TempDir(), "o.mp4")}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Errorf("%d runs", len(r.calls))
	}
}

// When the measuring pass hears silence (a source with no signal), the second pass would fail on -inf: the single-pass
// filter runs as planned.
func TestExecuteMeasuredSilenceKeepsSinglePass(t *testing.T) {
	plan := mustCompile(t, audioSpec(), Final)
	r := &fakeRunner{run: func(call int) fakeAnswer {
		if call == 0 {
			return fakeAnswer{stderr: strings.ReplaceAll(loudnormJSON, `"-23.45"`, `"-inf"`)}
		}
		return fakeAnswer{}
	}}
	if err := Execute(context.Background(), plan, r, ExecuteOptions{Output: filepath.Join(t.TempDir(), "o.mp4")}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.joined(1), "measured_I") {
		t.Error("silence must not be measured into the second pass")
	}
}

// The measuring pass opens only the inputs whose audio the mix uses: here the clip has no source audio, so only the music
// track is an input, renumbered to 0.
func TestMeasuringPassOpensOnlyAudioInputs(t *testing.T) {
	spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 3000, 0), clipAt("c2", assetB, 0, 3000, 3000, withSourceAudio(0))},
		&timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{
			&timeline.LicensedTrack{AudioCommon: timeline.AudioCommon{ID: "s1", Export: timeline.ExportLicensed, SourceOffsetMs: 2000}, AssetID: assetMusic, LicenseID: "lic_01jg1bfdc146rw2b0vngmx6p6f"},
		}})
	plan := mustCompile(t, spec, Final)
	if len(plan.Inputs) != 3 {
		t.Fatalf("inputs %+v", plan.Inputs)
	}
	r := &fakeRunner{run: func(call int) fakeAnswer {
		if call == 0 {
			return fakeAnswer{stderr: loudnormJSON}
		}
		return fakeAnswer{}
	}}
	if err := Execute(context.Background(), plan, r, ExecuteOptions{Output: filepath.Join(t.TempDir(), "o.mp4")}); err != nil {
		t.Fatal(err)
	}
	p1 := r.calls[0]
	if n := strings.Count(strings.Join(p1.Args, " "), " -i "); n != 2 {
		t.Errorf("the measuring pass opens %d inputs, want 2 (clip 2 and the music):\n%s", n, strings.Join(p1.Args, " "))
	}
	g := r.joined(0)
	for _, want := range []string{"[0:a]atrim=end=3.000", "[1:a]atrim=end=6.000", "-i /in/1 -protocol_whitelist file -format_whitelist " + media.UntrustedFormats + " -ss 2.000 -t 6.000 -i /in/2"} {
		if !strings.Contains(g, want) {
			t.Errorf("pass 1 lacks %q:\n%s", want, g)
		}
	}
	if strings.Contains(g, "/in/0") {
		t.Errorf("clip 1 has no audio in the mix and must not be opened:\n%s", g)
	}
	if got := len(r.calls[0].Inputs); got != 2 {
		t.Errorf("pass 1 mounts %d files, want 2", got)
	}
	if got := len(r.calls[1].Inputs); got != 3 {
		t.Errorf("pass 2 mounts %d files, want 3", got)
	}
}

func TestExecuteErrors(t *testing.T) {
	plan := mustCompile(t, audioSpec(), Preview)
	out := filepath.Join(t.TempDir(), "o.mp4")
	if err := Execute(context.Background(), plan, &fakeRunner{}, ExecuteOptions{}); err == nil {
		t.Error("no output path must be an error")
	}
	if err := Execute(context.Background(), plan, nil, ExecuteOptions{Output: out}); err == nil {
		t.Error("no runner must be an error")
	}

	failing := &fakeRunner{run: func(int) fakeAnswer {
		return fakeAnswer{exit: 254, stderr: "Error opening input: No such file or directory\n"}
	}}
	err := Execute(context.Background(), plan, failing, ExecuteOptions{Output: out})
	var te *media.ToolError
	if !errors.As(err, &te) || te.ExitCode != 254 || !strings.Contains(te.Stderr, "No such file or directory") {
		t.Errorf("a failing ffmpeg must report its exit code and the tail of its log: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := &fakeRunner{run: func(int) fakeAnswer { return fakeAnswer{err: errors.New("signal: killed")} }}
	if err := Execute(ctx, plan, cancelled, ExecuteOptions{Output: out}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}

	for _, sentinel := range []error{sandbox.ErrSandbox, sandbox.ErrTimeout} {
		broken := &fakeRunner{run: func(int) fakeAnswer { return fakeAnswer{err: sentinel} }}
		if err := Execute(context.Background(), plan, broken, ExecuteOptions{Output: out}); !errors.Is(err, sentinel) {
			t.Errorf("%v must come back wrapped: %v", sentinel, err)
		}
	}

	silent := &fakeRunner{run: func(int) fakeAnswer { return fakeAnswer{noOutput: true} }}
	if err := Execute(context.Background(), plan, silent, ExecuteOptions{Output: out}); err == nil || !strings.Contains(err.Error(), "output was not written") {
		t.Errorf("an ffmpeg that wrote nothing: %v", err)
	}

	finalPlan := mustCompile(t, audioSpec(), Final)
	if err := Execute(context.Background(), finalPlan, &fakeRunner{}, ExecuteOptions{Output: out}); err == nil || !strings.Contains(err.Error(), "loudnorm statistics") {
		t.Errorf("a measuring pass that prints nothing: %v", err)
	}

	// The sandbox has no network: an input that is not a local path is refused before anything runs.
	remote := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: "https://example.com/" + id, HasAudio: true}, nil
	})
	rp, err := Compile(audioSpec(), Preview, remote)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	if err := Execute(context.Background(), rp, r, ExecuteOptions{Output: out}); !errors.Is(err, ErrRemoteInput) || len(r.calls) != 0 {
		t.Errorf("a URL input: %v (%d runs)", err, len(r.calls))
	}
}

func TestContainerArgs(t *testing.T) {
	in := []Input{{Location: "/x/a.mp4"}, {Location: "/x/b.mp4"}, {Location: "/x/a.mp4"}}
	mounts, err := mountInputs(in)
	if err != nil {
		t.Fatal(err)
	}
	if mounts[0].Container != "/in/0" || mounts[1].Container != "/in/1" || mounts[2].Container != "/in/0" {
		t.Errorf("the same file gets one mount: %+v", mounts)
	}
	got := containerArgs([]string{"-i", "/x/a.mp4", "-ss", "1", "-i", "/x/b.mp4", "-map", "/x/a.mp4"}, mounts)
	if want := []string{"-i", "/in/0", "-ss", "1", "-i", "/in/1", "-map", "/x/a.mp4"}; !slices.Equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if u := usedInputs(got, mounts); len(u) != 2 {
		t.Errorf("used inputs %+v", u)
	}
}

// -progress output arrives in arbitrary chunks.
func TestProgressWriterChunks(t *testing.T) {
	plan := Plan{DurationMs: 4000}
	var got []Progress
	w := &progressWriter{plan: plan, stage: "render", fn: func(p Progress) { got = append(got, p) }}
	for _, chunk := range []string{"fra", "me=12\nout_ti", "me_us=500000\nspeed=N/A\nprog", "ress=continue\nframe=24\nout_time_ms=1000000\npro", "gress=end\n"} {
		_, _ = w.Write([]byte(chunk))
	}
	w.flush()
	if len(got) != 2 || got[0].Frame != 12 || got[0].OutTimeMs != 500 || got[0].Fraction != 0.125 || got[0].Speed != 0 || got[1].OutTimeMs != 1000 || !got[1].Done {
		t.Errorf("progress %+v", got)
	}
}

func TestMeasuredFilter(t *testing.T) {
	f, err := measuredFilter("loudnorm=I=-14:TP=-1:LRA=11", "some log\n"+loudnormJSON+"\n")
	if err != nil || !strings.HasPrefix(f, "loudnorm=I=-14:TP=-1:LRA=11:measured_I=-23.45:") || !strings.HasSuffix(f, ":linear=true") {
		t.Errorf("%q %v", f, err)
	}
	if _, err := measuredFilter("x", "no json here"); err == nil {
		t.Error("a log without statistics must be an error")
	}
	if _, err := measuredFilter("x", `{"input_i":"abc"}`); err == nil {
		t.Error("a statistic that is not a number must be an error")
	}
}
