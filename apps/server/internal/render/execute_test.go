package render

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// fakeRunner records the commands and answers them from a script.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	run   func(call int, cmd Command) error
}

func (f *fakeRunner) Run(_ context.Context, cmd Command) error {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, slices.Clone(cmd.Args))
	f.mu.Unlock()
	if f.run != nil {
		return f.run(n, cmd)
	}
	return nil
}

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
	r := &fakeRunner{run: func(call int, cmd Command) error {
		if call == 0 {
			_, _ = io.WriteString(cmd.Stderr, loudnormJSON)
			_, _ = io.WriteString(cmd.Stdout, "frame=45\nout_time_us=1500000\nspeed=2.0x\nprogress=continue\n")
		} else {
			_, _ = io.WriteString(cmd.Stdout, "frame=90\nout_time_us=3000000\nspeed=1.5x\nprogress=end\n")
		}
		return nil
	}}
	err := Execute(context.Background(), plan, r, ExecuteOptions{Output: "/tmp/out.mp4", Progress: func(p Progress) { progress = append(progress, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("%d ffmpeg runs, want 2", len(r.calls))
	}
	pass1, pass2 := strings.Join(r.calls[0], " "), strings.Join(r.calls[1], " ")
	if !strings.Contains(pass1, "print_format=json") || !strings.HasSuffix(pass1, "-f null -") || !strings.Contains(pass1, "-loglevel info") {
		t.Errorf("pass 1: %s", pass1)
	}
	want := plan.Loudnorm.Filter + ":measured_I=-23.45:measured_TP=-6.1:measured_LRA=3.2:measured_thresh=-33.7:offset=0.62:linear=true"
	if !strings.Contains(pass2, want) || strings.Contains(pass2, "print_format") {
		t.Errorf("pass 2 lacks the measured filter %q:\n%s", want, pass2)
	}
	if !strings.HasSuffix(pass2, "-y /tmp/out.mp4") || !strings.Contains(pass2, "-progress pipe:1") {
		t.Errorf("pass 2: %s", pass2)
	}
	// The plan is not changed by running it.
	if strings.Contains(strings.Join(plan.Args, " "), "measured_I") {
		t.Error("Execute modified the plan's arguments")
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
	if err := Execute(context.Background(), plan, r, ExecuteOptions{Output: "o.mp4"}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || !strings.Contains(strings.Join(r.calls[0], " "), plan.Loudnorm.Filter+",") {
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
	if err := Execute(context.Background(), plan, r, ExecuteOptions{Output: "o.mp4"}); err != nil {
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
	r := &fakeRunner{run: func(call int, cmd Command) error {
		if call == 0 {
			_, _ = io.WriteString(cmd.Stderr, strings.ReplaceAll(loudnormJSON, `"-23.45"`, `"-inf"`))
		}
		return nil
	}}
	if err := Execute(context.Background(), plan, r, ExecuteOptions{Output: "o.mp4"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(r.calls[1], " "), "measured_I") {
		t.Error("silence must not be measured into the second pass")
	}
}

func TestExecuteErrors(t *testing.T) {
	plan := mustCompile(t, audioSpec(), Preview)
	if err := Execute(context.Background(), plan, &fakeRunner{}, ExecuteOptions{}); err == nil {
		t.Error("no output path must be an error")
	}

	boom := &fakeRunner{run: func(_ int, cmd Command) error {
		_, _ = io.WriteString(cmd.Stderr, "Error opening input: No such file or directory\n")
		return errors.New("exit status 254")
	}}
	err := Execute(context.Background(), plan, boom, ExecuteOptions{Output: "o.mp4"})
	if err == nil || !strings.Contains(err.Error(), "exit status 254") || !strings.Contains(err.Error(), "No such file or directory") {
		t.Errorf("a failing ffmpeg must report its exit status and the tail of its log: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := &fakeRunner{run: func(int, Command) error { return errors.New("signal: killed") }}
	if err := Execute(ctx, plan, cancelled, ExecuteOptions{Output: "o.mp4"}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}

	missing := &fakeRunner{run: func(int, Command) error { return ErrFFmpegNotFound }}
	if err := Execute(context.Background(), plan, missing, ExecuteOptions{Output: "o.mp4"}); !errors.Is(err, ErrFFmpegNotFound) {
		t.Errorf("a missing binary: %v", err)
	}

	finalPlan := mustCompile(t, audioSpec(), Final)
	noStats := &fakeRunner{}
	if err := Execute(context.Background(), finalPlan, noStats, ExecuteOptions{Output: "o.mp4"}); err == nil || !strings.Contains(err.Error(), "loudnorm statistics") {
		t.Errorf("a measuring pass that prints nothing: %v", err)
	}
}

func TestLocalRunnerWithoutFFmpeg(t *testing.T) {
	err := LocalRunner{Binary: "cf-no-such-ffmpeg-binary"}.Run(context.Background(), Command{Args: []string{"-version"}})
	if !errors.Is(err, ErrFFmpegNotFound) {
		t.Errorf("err %v, want ErrFFmpegNotFound", err)
	}
}

// -progress output arrives in arbitrary chunks.
func TestProgressWriterChunks(t *testing.T) {
	plan := Plan{DurationMs: 4000}
	var got []Progress
	w := &progressWriter{plan: plan, stage: "render", fn: func(p Progress) { got = append(got, p) }}
	for _, chunk := range []string{"fra", "me=12\nout_ti", "me_us=500000\nspeed=N/A\nprog", "ress=continue\nframe=24\nout_time_ms=1000000\npro", "gress=end\n"} {
		_, _ = io.WriteString(w, chunk)
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
