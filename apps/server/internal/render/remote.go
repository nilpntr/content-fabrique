package render

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// ExecuteRemote is Execute for the remote sandbox provider (docs/design/render-runtime.md): nothing is mounted, the runner
// downloads the footage from presigned URLs, runs ffmpeg, and uploads what it wrote to presigned URLs. The plan and its ffmpeg
// commands are the ones Execute runs; only the paths and the transport differ.
//
// A plan whose measuring passes feed the encode (the bed's evening, the final profile's loudness pass) needs the worker between
// them, so it makes one remote job per pass: "<JobID>-bed", "<JobID>-measure", then "<JobID>-render". The encode job also runs
// the After steps, which read the video at RemoteVideoPath in the same /work, so the video is never downloaded again (the
// contact sheet's frames). The bed's meter files come back through the scratch files, the loudness statistics through the
// step's stderr, as they do from Execute.
//
// A plan with motion graphics (Plan.Motion) is refused with ErrRemoteMotion: the template bundle is a directory, which a remote
// job cannot take as an input yet.

// RemoteVideoPath is where the encode of a remote render writes the video: the path the After steps read it from.
var RemoteVideoPath = media.OutputPath(outName)

// ErrRemoteMotion is returned by ExecuteRemote for a plan with motion-graphics overlays.
var ErrRemoteMotion = errors.New("render: the remote sandbox provider does not render motion graphics yet; set MOTION_GRAPHICS=off (the graphics are drawn as text) or render with the docker or hardened provider")

// RemoteFiles is the object storage of a remote render: every URL is presigned, scoped to the render and expires with its job.
type RemoteFiles interface {
	// InputURL is the presigned GET URL of the plan input at location (an Input.Location the resolver returned).
	InputURL(ctx context.Context, location string) (string, error)
	// PutScratch stores a file the worker made for the runner (the subtitles script) and returns its presigned GET URL.
	PutScratch(ctx context.Context, name string, data []byte, contentType string) (string, error)
	// ScratchURL is the presigned PUT URL the runner uploads the scratch file name to.
	ScratchURL(ctx context.Context, name, contentType string) (string, error)
	// GetScratch reads back a scratch file the runner uploaded, refusing one over limit bytes.
	GetScratch(ctx context.Context, name string, limit int64) ([]byte, error)
}

// RemoteStep is a step run after the encode in the same remote job: it reads the video at RemoteVideoPath.
type RemoteStep struct {
	// Name is the tool's name in a ToolError when the step fails ("contact sheet").
	Name string
	Step sandbox.Step
}

// RemoteOptions configures ExecuteRemote.
type RemoteOptions struct {
	// Provider is the remote sandbox provider (sandbox.Remote).
	Provider sandbox.Provider
	// JobID names the render's jobs at the runner: the render id plus the attempt, so a retry never starts a second job for
	// the same attempt. Each job adds its pass ("-bed", "-measure", "-render").
	JobID string
	// Limits bound each ffmpeg run, as in Execute (DefaultLimits): Limits.Timeout is one run's limit. A job's own timeout is its
	// runs' timeouts plus Transfer.
	Limits sandbox.Limits
	// Transfer is the time a job is given for its downloads and uploads on top of its runs; zero means 10 minutes.
	Transfer time.Duration
	// Labels go with every job (workspace, render), for logs and billing.
	Labels map[string]string
	// MaxInputBytes caps the inputs of one job; zero means the runner's default.
	MaxInputBytes int64
	Files         RemoteFiles
	// Video is where the runner uploads the video (URL, ContentType, MaxBytes); its Path is set here.
	Video sandbox.Output
	// Stem, for a plan with an audio stem (Plan.StemLabel), is where the runner uploads it; nil leaves the stem out.
	Stem *sandbox.Output
	// FontsDir is the runner's fonts directory, which the ass filter reads; empty means ImageFontsDir (the media-tools image).
	FontsDir string
	// After are steps that run after the encode in the same job, and AfterOutputs what they write.
	After        []RemoteStep
	AfterOutputs []sandbox.Output
	// Progress, if set, is called when a pass has finished (a remote job reports no progress while it runs).
	Progress func(Progress)
}

const (
	defaultTransfer = 10 * time.Minute
	maxMeterBytes   = 16 << 20
)

// ExecuteRemote renders plan through the remote provider: see the comment above RemoteVideoPath. It returns the time the
// runner reports for its jobs (downloads, steps and uploads; not the wait for a busy runner), what the render is metered by.
func ExecuteRemote(ctx context.Context, plan Plan, opts RemoteOptions) (time.Duration, error) {
	switch {
	case opts.Provider == nil || opts.Files == nil:
		return 0, errors.New("render: a remote render needs a provider and its files")
	case opts.JobID == "" || opts.Video.URL == "":
		return 0, errors.New("render: a remote render needs a job id and the video's upload URL")
	case plan.Motion != nil:
		return 0, ErrRemoteMotion
	}
	inputs, err := mountInputs(plan.Inputs)
	if err != nil {
		return 0, err
	}
	x := &remoteRun{ctx: ctx, plan: plan, opts: opts, inputs: inputs, urls: map[string]string{}}
	err = x.execute()
	return x.ran, err
}

func (x *remoteRun) execute() error {
	plan, opts, inputs := x.plan, x.opts, x.inputs
	args := plan.Args
	if plan.Bed != nil && len(plan.Bed.MeterArgs) > 0 {
		swaps, err := x.measureBed()
		if err != nil {
			return err
		}
		args = applySwaps(args, swaps)
		if plan.Loudnorm != nil {
			ln := *plan.Loudnorm
			ln.Pass1Args = applySwaps(ln.Pass1Args, swaps)
			x.plan.Loudnorm = &ln
		}
	}
	if ln := x.plan.Loudnorm; ln != nil && ln.TwoPass {
		if len(ln.Pass1Args) == 0 {
			return errors.New("render: two-pass loudnorm without pass 1 arguments")
		}
		res, err := x.run("measure", "measure", []sandbox.Step{{Args: loudnessArgs(ln, inputs), Timeout: opts.Limits.Timeout}}, nil, nil)
		if err != nil {
			return err
		}
		measured, err := measuredFilter(ln.Filter, string(res.Stderr))
		if err != nil {
			return err
		}
		if measured != "" {
			var swapped bool
			if args, swapped = swapFilter(args, ln.Filter, measured); !swapped {
				return errors.New("render: the loudnorm filter is missing from the plan")
			}
		}
	}
	return x.encode(args)
}

// remoteRun is one ExecuteRemote.
type remoteRun struct {
	ctx    context.Context
	plan   Plan
	opts   RemoteOptions
	inputs []media.Input
	urls   map[string]string // container path -> presigned GET URL
	// ran is the sum of the jobs' durations as the runner reports them.
	ran time.Duration
}

// jobInputs are the downloads a command needs: every input it opens, presigned once per render.
func (x *remoteRun) jobInputs(args []string) ([]sandbox.Input, error) {
	var out []sandbox.Input
	for _, in := range usedInputs(args, x.inputs) {
		u, ok := x.urls[in.Container]
		if !ok {
			var err error
			if u, err = x.opts.Files.InputURL(x.ctx, in.Host); err != nil {
				return nil, fmt.Errorf("render: the URL of input %s: %w", in.Container, err)
			}
			x.urls[in.Container] = u
		}
		out = append(out, sandbox.Input{URL: u, Path: in.Container})
	}
	return out, nil
}

// run sends one job, with the inputs its first step opens plus extra, and waits for it. A step that exits non-zero is a
// ToolError named after the step (the first is "ffmpeg (<stage>)", an After step its own name).
func (x *remoteRun) run(pass, stage string, steps []sandbox.Step, extra []sandbox.Input, outputs []sandbox.Output, names ...string) (sandbox.Result, error) {
	in, err := x.jobInputs(steps[0].Args)
	if err != nil {
		return sandbox.Result{}, err
	}
	in = append(in, extra...)
	limits := x.opts.Limits
	limits.Timeout = x.opts.Transfer
	if limits.Timeout <= 0 {
		limits.Timeout = defaultTransfer
	}
	for _, s := range steps {
		limits.Timeout += s.Timeout
	}
	res, err := x.opts.Provider.Run(x.ctx, sandbox.Job{
		ID: x.opts.JobID + "-" + pass, Steps: steps, Inputs: in, Outputs: outputs, Limits: limits,
		Labels: x.opts.Labels, MaxInputBytes: x.opts.MaxInputBytes,
	})
	x.ran += res.Duration
	if err != nil {
		if x.ctx.Err() != nil {
			return res, fmt.Errorf("render: %s: %w", stage, x.ctx.Err())
		}
		return res, fmt.Errorf("render: ffmpeg (%s): %w", stage, err)
	}
	if res.ExitCode != 0 {
		tool := "ffmpeg (" + stage + ")"
		if i := len(res.Steps) - 1; i > 0 && i-1 < len(names) {
			tool = names[i-1]
		}
		return res, &media.ToolError{Tool: tool, ExitCode: res.ExitCode, Stderr: lastLines(string(res.Stderr), 20)}
	}
	if x.opts.Progress != nil {
		x.opts.Progress(Progress{Stage: stage, Fraction: 1, Done: true})
	}
	return res, nil
}

// measureBed runs the evening pass as its own job: the meter files come back as scratch files.
func (x *remoteRun) measureBed() (map[string]string, error) {
	bed := x.plan.Bed
	outs := make([]sandbox.Output, 0, len(bed.Clips))
	for _, bc := range bed.Clips {
		u, err := x.opts.Files.ScratchURL(x.ctx, bc.MeterFile, "text/plain")
		if err != nil {
			return nil, fmt.Errorf("render: the URL of %s: %w", bc.MeterFile, err)
		}
		outs = append(outs, sandbox.Output{Path: media.OutputPath(bc.MeterFile), URL: u, ContentType: "text/plain", MaxBytes: maxMeterBytes})
	}
	if _, err := x.run("bed", "measure", []sandbox.Step{{Args: bedArgs(bed, x.inputs), Timeout: x.opts.Limits.Timeout}}, nil, outs); err != nil {
		return nil, err
	}
	meters := make(map[string][]byte, len(bed.Clips))
	for _, bc := range bed.Clips {
		data, err := x.opts.Files.GetScratch(x.ctx, bc.MeterFile, maxMeterBytes)
		if err != nil {
			return nil, fmt.Errorf("render: read %s: %w", bc.MeterFile, err)
		}
		meters[bc.MeterFile] = data
	}
	return bedSwaps(bed, func(bc BedClip) (float64, bool) { return parseMeter(meters[bc.MeterFile]) }), nil
}

// encode runs the encode and the After steps as one job. The subtitles script arrives as a scratch input in /work, where the
// ass filter reads it by its relative name, and the filter reads the runner's fonts.
func (x *remoteRun) encode(args []string) error {
	plan, opts := x.plan, x.opts
	var extra []sandbox.Input
	if s := plan.Subtitles; s != nil {
		u, err := opts.Files.PutScratch(x.ctx, s.File, []byte(s.ASS), "text/plain")
		if err != nil {
			return fmt.Errorf("render: stage subtitles: %w", err)
		}
		extra = append(extra, sandbox.Input{URL: u, Path: media.OutputPath(s.File)})
		fonts := opts.FontsDir
		if fonts == "" {
			fonts = ImageFontsDir
		}
		args = withFontsDir(args, s, fonts)
	}
	withStem := plan.StemLabel != "" && opts.Stem != nil
	steps := []sandbox.Step{{Args: encodeArgs(plan, args, x.inputs, withStem), Timeout: opts.Limits.Timeout}}
	names := make([]string, 0, len(opts.After))
	for _, a := range opts.After {
		if a.Step.Timeout <= 0 {
			a.Step.Timeout = opts.Limits.Timeout
		}
		steps = append(steps, a.Step)
		names = append(names, a.Name)
	}
	video := opts.Video
	video.Path = RemoteVideoPath
	outs := []sandbox.Output{video}
	if withStem {
		stem := *opts.Stem
		stem.Path = media.OutputPath(stemName)
		outs = append(outs, stem)
	}
	outs = append(outs, opts.AfterOutputs...)
	_, err := x.run("render", "render", steps, extra, outs, names...)
	return err
}

// withFontsDir returns a copy of args whose ass filter reads the fonts in dir instead of the staged directory.
func withFontsDir(args []string, s *Subtitles, dir string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "fontsdir="+s.FontsDir, "fontsdir="+dir)
	}
	return out
}
