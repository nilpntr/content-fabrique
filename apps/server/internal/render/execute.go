package render

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// Execute runs ffmpeg through a media.Runner, which in production is a media.SandboxRunner over one of the sandbox
// providers of P1-03 (docker, hardened) and in development the local provider (sandbox.NewLocal, no isolation).
//
// Every input of the plan is a local file that the sandbox mounts read-only at /in/<n>; the sandbox has no network, so
// the worker stages assets before rendering and the resolver returns their paths. ffmpeg writes its output and its
// -progress file into a work directory, the one writable mount; Execute polls the progress file on the host while the
// job runs, and moves the output to ExecuteOptions.Output when it is done.

// DefaultLimits are the sandbox limits of a render job. The local provider enforces only the timeout.
func DefaultLimits() sandbox.Limits {
	return sandbox.Limits{CPU: 4, MemoryMB: 4096, Timeout: 30 * time.Minute, PIDs: 1024}
}

// ErrRemoteInput is returned when a plan input is not a local file path.
var ErrRemoteInput = errors.New("render: input is not a local file: stage the asset before rendering")

// Progress is one progress event of a render.
type Progress struct {
	// Stage is "measure" for the loudness pass of the final profile and "render" for the encode.
	Stage string
	// Frame is the number of frames written so far and OutTimeMs the output time reached.
	Frame     int64
	OutTimeMs int64
	// Fraction is OutTimeMs over the plan's duration, between 0 and 1.
	Fraction float64
	// Speed is ffmpeg's speed multiple (1.0 is real time), 0 when it has not reported one.
	Speed float64
	// Done is true on ffmpeg's closing "progress=end" event.
	Done bool
}

// ExecuteOptions configures Execute.
type ExecuteOptions struct {
	// Output is the path of the MP4 to write.
	Output string
	// WorkDir is the host directory mounted writable in the sandbox. It must be inside the provider's allowed roots, and
	// empty or unused by anything else. Empty means a fresh directory under os.TempDir, removed afterwards.
	WorkDir string
	// Progress, if set, is called for every progress block ffmpeg prints (about twice a second).
	Progress func(Progress)
	// FontsDir is the host directory that holds the font files the plan's subtitles name (packages/media-tools/fonts). Empty
	// means packages/media-tools/fonts found above the working directory. It is only used when the runner runs the host's ffmpeg (the
	// local provider): in the media-tools image (docker, hardened) the render reads the image's own fonts, ImageFontsDir, and
	// nothing is staged. A plan without text, captions or watermark needs no fonts.
	FontsDir string
}

const (
	outName       = "out.mp4"
	pollInterval  = 200 * time.Millisecond
	ffmpegProgram = "ffmpeg"
)

// Execute renders plan to opts.Output. The final profile first runs the loudness measuring pass and swaps the measured
// values into the graph; every other profile is a single run.
func Execute(ctx context.Context, plan Plan, runner media.Runner, opts ExecuteOptions) error {
	if opts.Output == "" {
		return errors.New("render: no output path")
	}
	if runner == nil {
		return errors.New("render: no runner")
	}
	inputs, err := mountInputs(plan.Inputs)
	if err != nil {
		return err
	}

	work := opts.WorkDir
	if work == "" {
		work, err = os.MkdirTemp("", "cf-render-")
		if err != nil {
			return fmt.Errorf("render: work directory: %w", err)
		}
		defer func() { _ = os.RemoveAll(work) }()
	}
	// Only a clean slate: a left-over output must never be taken for this run's.
	for _, n := range []string{outName, "progress-render.txt", "progress-measure.txt", subtitleFile} {
		_ = os.Remove(filepath.Join(work, n))
	}
	_ = os.RemoveAll(filepath.Join(work, fontsDirName))
	inImage := runsInImage(runner)
	if err := stageSubtitles(plan, work, opts.FontsDir, inImage); err != nil {
		return err
	}

	args := plan.Args
	if ln := plan.Loudnorm; ln != nil && ln.TwoPass {
		measured, err := measureLoudness(ctx, plan, runner, inputs, work, opts)
		if err != nil {
			return err
		}
		if measured != "" {
			var swapped bool
			if args, swapped = swapFilter(plan.Args, ln.Filter, measured); !swapped {
				return errors.New("render: the loudnorm filter is missing from the plan")
			}
		}
	}

	if inImage {
		args = useImageFonts(args, plan.Subtitles)
	}
	full := append(ffmpegPrefix("error", "progress-render.txt"), containerArgs(args, inputs)...)
	full = append(full, "-y", media.OutputPath(outName))
	if _, err := runFFmpeg(ctx, runner, full, inputs, work, plan, "render", "progress-render.txt", opts.Progress); err != nil {
		return err
	}
	return moveFile(filepath.Join(work, outName), opts.Output)
}

// ffmpegPrefix is the start of every ffmpeg command: no stdin, quiet, no stats, and a -progress file in the work
// directory (the sandbox does not stream stdout).
func ffmpegPrefix(logLevel, progressFile string) []string {
	return []string{ffmpegProgram, "-hide_banner", "-nostdin", "-loglevel", logLevel, "-nostats", "-progress", media.OutputPath(progressFile)}
}

// mountInputs gives every distinct local file of the plan a container path. The result is indexed like plan.Inputs.
func mountInputs(in []Input) ([]media.Input, error) {
	seen := map[string]string{}
	out := make([]media.Input, len(in))
	for i, p := range in {
		if !filepath.IsAbs(p.Location) {
			return nil, fmt.Errorf("%w: %q (item %s)", ErrRemoteInput, p.Location, p.ItemID)
		}
		container, ok := seen[p.Location]
		if !ok {
			container = media.InputPath(len(seen))
			seen[p.Location] = container
		}
		out[i] = media.Input{Host: p.Location, Container: container}
	}
	return out, nil
}

// containerArgs replaces the input locations of an argument list (the argument after each -i) with their container paths.
// It works for the main command and for the measuring pass, which opens only some of the inputs.
func containerArgs(args []string, inputs []media.Input) []string {
	byHost := make(map[string]string, len(inputs))
	for _, in := range inputs {
		byHost[in.Host] = in.Container
	}
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "-i" {
			if c, ok := byHost[out[i+1]]; ok {
				out[i+1] = c
			}
			i++
		}
	}
	return out
}

// usedInputs returns the mounts a command needs: every distinct container path that appears after -i.
func usedInputs(args []string, inputs []media.Input) []media.Input {
	want := map[string]bool{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-i" {
			want[args[i+1]] = true
		}
	}
	var out []media.Input
	seen := map[string]bool{}
	for _, in := range inputs {
		if want[in.Container] && !seen[in.Container] {
			seen[in.Container] = true
			out = append(out, in)
		}
	}
	return out
}

// runFFmpeg runs one ffmpeg command in the sandbox, feeds its progress file to the callback and turns a failure into an
// error with the tail of its log. It returns the job's stderr.
func runFFmpeg(ctx context.Context, runner media.Runner, args []string, inputs []media.Input, work string, plan Plan, stage, progressFile string, onProgress func(Progress)) ([]byte, error) {
	pw := &progressWriter{plan: plan, stage: stage, fn: onProgress}
	stop := tailProgress(filepath.Join(work, progressFile), pw)
	res, err := runner.Run(ctx, media.Invocation{Args: args, Inputs: usedInputs(args, inputs), OutputDir: work})
	stop()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("render: %s: %w", stage, ctx.Err())
		}
		return nil, fmt.Errorf("render: ffmpeg (%s): %w", stage, err)
	}
	if res.ExitCode != 0 {
		return res.Stderr, &media.ToolError{Tool: "ffmpeg (" + stage + ")", ExitCode: res.ExitCode, Stderr: lastLines(string(res.Stderr), 20)}
	}
	return res.Stderr, nil
}

// tailProgress reads a growing -progress file in the background and feeds new bytes to w; the returned function stops it
// after one last read.
func tailProgress(path string, w *progressWriter) (stop func()) {
	if w.fn == nil {
		return func() {}
	}
	done, finished := make(chan struct{}), make(chan struct{})
	var offset int64
	read := func() {
		f, err := os.Open(path) //nolint:gosec // a file in the work directory we created
		if err != nil {
			return
		}
		defer f.Close() //nolint:errcheck // read-only
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return
		}
		n, _ := io.Copy(w, f)
		offset += n
	}
	go func() {
		defer close(finished)
		t := time.NewTicker(pollInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				read()
			case <-done:
				read()
				w.flush()
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// moveFile renames src to dst, or copies it when they are on different file systems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src) //nolint:gosec // the render's own output
	if err != nil {
		return fmt.Errorf("render: the output was not written: %w", err)
	}
	defer in.Close()           //nolint:errcheck // read-only
	out, err := os.Create(dst) //nolint:gosec // the caller's output path
	if err != nil {
		return fmt.Errorf("render: write output: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("render: write output: %w", err)
	}
	return out.Close()
}

// swapFilter returns a copy of args with the first occurrence of old in an argument replaced by repl.
func swapFilter(args []string, old, repl string) ([]string, bool) {
	out := make([]string, len(args))
	copy(out, args)
	for i, a := range out {
		if strings.Contains(a, old) {
			out[i] = strings.Replace(a, old, repl, 1)
			return out, true
		}
	}
	return out, false
}

// loudnormStats is the JSON loudnorm prints at the end of a measuring pass. Its numbers are strings.
type loudnormStats struct {
	InputI       string `json:"input_i"`
	InputTP      string `json:"input_tp"`
	InputLRA     string `json:"input_lra"`
	InputThresh  string `json:"input_thresh"`
	TargetOffset string `json:"target_offset"`
}

// silenceFloor is the integrated loudness below which a pass-1 measurement is treated as silence: loudnorm cannot
// normalise it and the second pass would fail on -inf.
const silenceFloor = -70.0

// measureLoudness runs the first pass and returns the loudnorm filter with the measured values, or "" when the mix is
// silent and should go through the single-pass filter as it is.
func measureLoudness(ctx context.Context, plan Plan, runner media.Runner, inputs []media.Input, work string, opts ExecuteOptions) (string, error) {
	ln := plan.Loudnorm
	if len(ln.Pass1Args) == 0 {
		return "", errors.New("render: two-pass loudnorm without pass 1 arguments")
	}
	// The measuring pass opens only the inputs whose audio it uses.
	args := append(ffmpegPrefix("info", "progress-measure.txt"), containerArgs(ln.Pass1Args, inputs)...)
	args = append(args, "-f", "null", "-")
	stderr, err := runFFmpeg(ctx, runner, args, inputs, work, plan, "measure", "progress-measure.txt", opts.Progress)
	if err != nil {
		return "", err
	}
	return measuredFilter(ln.Filter, string(stderr))
}

// measuredFilter reads the loudnorm JSON out of ffmpeg's log and builds the second-pass filter.
func measuredFilter(base, log string) (string, error) {
	end := strings.LastIndex(log, "}")
	start := strings.LastIndex(log, "{")
	if start < 0 || end < start {
		return "", fmt.Errorf("render: no loudnorm statistics in the measuring pass:\n%s", lastLines(log, 20))
	}
	var st loudnormStats
	if err := json.Unmarshal([]byte(log[start:end+1]), &st); err != nil {
		return "", fmt.Errorf("render: read loudnorm statistics: %w", err)
	}
	vals := make([]float64, 5)
	for i, s := range []string{st.InputI, st.InputTP, st.InputLRA, st.InputThresh, st.TargetOffset} {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return "", fmt.Errorf("render: loudnorm statistic %q: %w", s, err)
		}
		vals[i] = v
	}
	if math.IsInf(vals[0], 0) || math.IsNaN(vals[0]) || vals[0] < silenceFloor {
		return "", nil
	}
	return fmt.Sprintf("%s:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true",
		base, num(vals[0]), num(vals[1]), num(vals[2]), num(vals[3]), num(vals[4])), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// progressWriter parses the key=value blocks of -progress: a block ends with progress=continue or progress=end.
type progressWriter struct {
	plan  Plan
	stage string
	fn    func(Progress)
	buf   bytes.Buffer
	cur   Progress
}

func (w *progressWriter) Write(p []byte) (int, error) {
	if w.fn == nil {
		return len(p), nil
	}
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// Not a whole line yet: put it back.
			w.buf.Reset()
			w.buf.WriteString(line)
			break
		}
		w.line(strings.TrimSpace(line))
	}
	return len(p), nil
}

func (w *progressWriter) flush() {
	if w.fn == nil || w.buf.Len() == 0 {
		return
	}
	sc := bufio.NewScanner(&w.buf)
	for sc.Scan() {
		w.line(strings.TrimSpace(sc.Text()))
	}
	w.buf.Reset()
}

func (w *progressWriter) line(l string) {
	key, val, ok := strings.Cut(l, "=")
	if !ok {
		return
	}
	switch key {
	case "frame":
		w.cur.Frame, _ = strconv.ParseInt(val, 10, 64)
	case "out_time_us", "out_time_ms": // ffmpeg writes microseconds under both names
		if us, err := strconv.ParseInt(val, 10, 64); err == nil && us >= 0 {
			w.cur.OutTimeMs = us / 1000
		}
	case "speed":
		w.cur.Speed, _ = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(val), "x"), 64)
	case "progress":
		w.cur.Stage = w.stage
		w.cur.Done = val == "end"
		if w.plan.DurationMs > 0 {
			w.cur.Fraction = math.Min(1, float64(w.cur.OutTimeMs)/float64(w.plan.DurationMs))
		}
		if w.cur.Done {
			w.cur.Fraction = 1
		}
		w.fn(w.cur)
		w.cur = Progress{}
	}
}
