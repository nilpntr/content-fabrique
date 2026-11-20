package render

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// fakeRemote records the jobs ExecuteRemote sends and answers each with what answer returns.
type fakeRemote struct {
	jobs   []sandbox.Job
	answer func(sandbox.Job) (sandbox.Result, error)
}

func (f *fakeRemote) Name() string { return "remote" }

func (f *fakeRemote) Run(_ context.Context, j sandbox.Job) (sandbox.Result, error) {
	f.jobs = append(f.jobs, j)
	return f.answer(j)
}

// memFiles is RemoteFiles over maps.
type memFiles struct {
	scratch map[string][]byte
}

func (m *memFiles) InputURL(_ context.Context, location string) (string, error) {
	return "https://objects.example" + location, nil
}

func (m *memFiles) PutScratch(_ context.Context, name string, data []byte, _ string) (string, error) {
	m.scratch[name] = data
	return "https://objects.example/scratch/" + name, nil
}

func (m *memFiles) ScratchURL(_ context.Context, name, _ string) (string, error) {
	return "https://objects.example/put/" + name, nil
}

func (m *memFiles) GetScratch(_ context.Context, name string, _ int64) ([]byte, error) {
	b, ok := m.scratch[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}

// A plan with the bed's evening, the loudness pass and subtitles makes three jobs: the meter files and the loudness statistics come
// back and are swapped into the encode, which reads the subtitles from /work and the runner's fonts.
func TestExecuteRemoteJobs(t *testing.T) {
	plan := Plan{
		DurationMs: 1000,
		Inputs:     []Input{{Location: "/stage/in/0"}, {Location: "/stage/in/1"}},
		Args:       []string{"-i", "/stage/in/0", "-i", "/stage/in/1", "-filter_complex", "[0:a]volume=0dB[b];[b]loudnorm=I=-14[a];ass=filename=subs.ass:fontsdir=fonts"},
		Bed: &BedPlan{TargetLUFS: -24, MaxGainDB: 18, MeterArgs: []string{"-i", "/stage/in/0", "-filter_complex", "ebur128"},
			Clips: []BedClip{{ItemID: "c1", Role: "bed", Filter: "volume=0dB", MeterFile: "bedmeter_0.txt"}}},
		Loudnorm:  &LoudnormPlan{Filter: "loudnorm=I=-14", TwoPass: true, Pass1Args: []string{"-i", "/stage/in/1", "-af", "loudnorm=I=-14:print_format=json"}},
		Subtitles: &Subtitles{File: "subs.ass", FontsDir: "fonts", ASS: "[Script Info]"},
	}
	files := &memFiles{scratch: map[string][]byte{"bedmeter_0.txt": []byte("frame:1\nlavfi.r128.I=-30.0\n")}}
	stats := `{"input_i":"-20.0","input_tp":"-3.0","input_lra":"4.0","input_thresh":"-30.0","target_offset":"0.5"}`
	f := &fakeRemote{answer: func(j sandbox.Job) (sandbox.Result, error) {
		res := sandbox.Result{Stderr: []byte(stats), Duration: time.Second}
		for range j.Steps {
			res.Steps = append(res.Steps, sandbox.Result{})
		}
		return res, nil
	}}
	var stages []string
	ran, err := ExecuteRemote(context.Background(), plan, RemoteOptions{
		Provider: f, JobID: "ren_x-j1-a1", Limits: DefaultLimits(), Files: files, Video: sandbox.Output{URL: "https://objects.example/put/v"},
		After:    []RemoteStep{{Name: "contact sheet", Step: sandbox.Step{Args: []string{"ffmpeg", "-i", RemoteVideoPath}}}},
		Progress: func(p Progress) { stages = append(stages, p.Stage) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if ran != 3*time.Second {
		t.Errorf("the runner's time is %v, want the three jobs' 3s", ran)
	}
	if len(f.jobs) != 3 || f.jobs[0].ID != "ren_x-j1-a1-bed" || f.jobs[1].ID != "ren_x-j1-a1-measure" || f.jobs[2].ID != "ren_x-j1-a1-render" {
		t.Fatalf("jobs %+v", f.jobs)
	}
	bed, measure, enc := f.jobs[0], f.jobs[1], f.jobs[2]
	if len(bed.Inputs) != 1 || bed.Inputs[0].Path != "/in/0" || len(bed.Outputs) != 1 || bed.Outputs[0].Path != "/work/bedmeter_0.txt" {
		t.Errorf("bed job %+v", bed)
	}
	if len(measure.Inputs) != 1 || measure.Inputs[0].Path != "/in/1" || measure.Inputs[0].URL != "https://objects.example/stage/in/1" {
		t.Errorf("measure job inputs %+v", measure.Inputs)
	}
	encode := strings.Join(enc.Steps[0].Args, " ")
	// -30 LUFS to -24 is +6 dB; the measured loudnorm values are in the filter; the fonts are the image's.
	for _, want := range []string{"volume=6dB", "measured_I=-20", "fontsdir=" + ImageFontsDir, "filename=subs.ass", "-i /in/0 -i /in/1", "/work/out.mp4"} {
		if !strings.Contains(encode, want) {
			t.Errorf("the encode lacks %q: %s", want, encode)
		}
	}
	if len(enc.Steps) != 2 || enc.Steps[1].Timeout != DefaultLimits().Timeout || enc.Limits.Timeout != 2*DefaultLimits().Timeout+defaultTransfer {
		t.Errorf("encode steps %+v, limits %+v", enc.Steps, enc.Limits)
	}
	var subs bool
	for _, in := range enc.Inputs {
		subs = subs || (in.Path == "/work/subs.ass" && in.URL == "https://objects.example/scratch/subs.ass")
	}
	if !subs || string(files.scratch["subs.ass"]) != "[Script Info]" {
		t.Errorf("the subtitles are not a /work input: %+v", enc.Inputs)
	}
	if len(enc.Outputs) != 1 || enc.Outputs[0].Path != RemoteVideoPath {
		t.Errorf("outputs %+v", enc.Outputs)
	}
	if fmt.Sprint(stages) != "[measure measure render]" {
		t.Errorf("stages %v", stages)
	}
}

// A failing step is a ToolError named after it; the provider's errors pass through; motion graphics are refused.
func TestExecuteRemoteErrors(t *testing.T) {
	plan := Plan{Inputs: []Input{{Location: "/stage/in/0"}}, Args: []string{"-i", "/stage/in/0"}}
	opts := func(f *fakeRemote) RemoteOptions {
		return RemoteOptions{Provider: f, JobID: "ren_x-1", Limits: DefaultLimits(), Files: &memFiles{}, Video: sandbox.Output{URL: "https://o/v"},
			After: []RemoteStep{{Name: "contact sheet", Step: sandbox.Step{Args: []string{"ffmpeg"}, Timeout: time.Minute}}}}
	}
	failAt := func(step int) *fakeRemote {
		return &fakeRemote{answer: func(sandbox.Job) (sandbox.Result, error) {
			res := sandbox.Result{ExitCode: 1, Stderr: []byte("bad")}
			res.Steps = make([]sandbox.Result, step+1)
			return res, nil
		}}
	}
	var te *media.ToolError
	if _, err := ExecuteRemote(context.Background(), plan, opts(failAt(0))); !errors.As(err, &te) || te.Tool != "ffmpeg (render)" {
		t.Errorf("a failing encode: %v", err)
	}
	if _, err := ExecuteRemote(context.Background(), plan, opts(failAt(1))); !errors.As(err, &te) || te.Tool != "contact sheet" {
		t.Errorf("a failing After step: %v", err)
	}
	files := &fakeRemote{answer: func(sandbox.Job) (sandbox.Result, error) {
		return sandbox.Result{}, fmt.Errorf("%w: output missing", sandbox.ErrFiles)
	}}
	if _, err := ExecuteRemote(context.Background(), plan, opts(files)); !errors.Is(err, sandbox.ErrFiles) {
		t.Errorf("ErrFiles: %v", err)
	}
	motion := plan
	motion.Motion = &MotionPlan{}
	if _, err := ExecuteRemote(context.Background(), motion, opts(failAt(0))); !errors.Is(err, ErrRemoteMotion) {
		t.Errorf("motion graphics: %v", err)
	}
	if _, err := ExecuteRemote(context.Background(), Plan{Inputs: []Input{{Location: "relative"}}}, opts(failAt(0))); !errors.Is(err, ErrRemoteInput) {
		t.Errorf("an input that is not a location: %v", err)
	}
}
