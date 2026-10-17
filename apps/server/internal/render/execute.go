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
	"os/exec"
	"strconv"
	"strings"
)

// ErrFFmpegNotFound is returned by LocalRunner when the ffmpeg binary is not on PATH.
var ErrFFmpegNotFound = errors.New("render: ffmpeg not found")

// Command is one ffmpeg invocation.
type Command struct {
	// Args are the arguments, without the program name.
	Args []string
	// Stdout receives ffmpeg's standard output (the -progress stream) and Stderr its log. Either may be nil.
	Stdout io.Writer
	Stderr io.Writer
}

// Runner starts ffmpeg. LocalRunner runs it as a child process of this one; the sandbox providers of P1-03 implement
// the same interface to run it in an isolated container, with the plan's inputs and the output path mapped to what
// the sandbox can see.
type Runner interface {
	Run(ctx context.Context, cmd Command) error
}

// LocalRunner runs ffmpeg from PATH, or from Binary when it is set.
type LocalRunner struct {
	Binary string
}

// Run implements Runner.
func (r LocalRunner) Run(ctx context.Context, cmd Command) error {
	bin := r.Binary
	if bin == "" {
		bin = "ffmpeg"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFFmpegNotFound, err)
	}
	c := exec.CommandContext(ctx, path, cmd.Args...) //nolint:gosec // the arguments come from Compile, not from a shell
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	return c.Run()
}

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
	// Progress, if set, is called for every progress block ffmpeg prints (about twice a second).
	Progress func(Progress)
}

// Execute renders plan to opts.Output. The final profile first runs the loudness measuring pass and swaps the measured
// values into the graph; every other profile is a single run.
func Execute(ctx context.Context, plan Plan, runner Runner, opts ExecuteOptions) error {
	if opts.Output == "" {
		return errors.New("render: no output path")
	}
	args := plan.Args
	if ln := plan.Loudnorm; ln != nil && ln.TwoPass {
		measured, err := measureLoudness(ctx, plan, runner, opts)
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
	full := append(append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-progress", "pipe:1"}, args...), "-y", opts.Output)
	return run(ctx, runner, full, plan, "render", opts.Progress)
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

// run starts one ffmpeg process, feeds its progress stream to the callback and wraps a failure with the tail of its log.
func run(ctx context.Context, runner Runner, args []string, plan Plan, stage string, onProgress func(Progress)) error {
	var stderr tailBuffer
	pw := &progressWriter{plan: plan, stage: stage, fn: onProgress}
	err := runner.Run(ctx, Command{Args: args, Stdout: pw, Stderr: &stderr})
	pw.flush()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("render: %s: %w", stage, ctx.Err())
		}
		if errors.Is(err, ErrFFmpegNotFound) {
			return err
		}
		return fmt.Errorf("render: ffmpeg (%s) failed: %w\n%s", stage, err, strings.TrimSpace(stderr.String()))
	}
	return nil
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
func measureLoudness(ctx context.Context, plan Plan, runner Runner, opts ExecuteOptions) (string, error) {
	ln := plan.Loudnorm
	if len(ln.Pass1Args) == 0 {
		return "", errors.New("render: two-pass loudnorm without pass 1 arguments")
	}
	args := append(append([]string{"-hide_banner", "-nostdin", "-loglevel", "info", "-nostats", "-progress", "pipe:1"}, ln.Pass1Args...), "-f", "null", "-")
	var log bytes.Buffer
	pw := &progressWriter{plan: plan, stage: "measure", fn: opts.Progress}
	err := runner.Run(ctx, Command{Args: args, Stdout: pw, Stderr: &log})
	pw.flush()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("render: measure: %w", ctx.Err())
		}
		if errors.Is(err, ErrFFmpegNotFound) {
			return "", err
		}
		return "", fmt.Errorf("render: ffmpeg (measure) failed: %w\n%s", err, lastLines(log.String(), 20))
	}
	return measuredFilter(ln.Filter, log.String())
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

// tailBuffer keeps the last 8 KiB written to it.
type tailBuffer struct {
	b []byte
}

const tailSize = 8 << 10

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > tailSize {
		t.b = t.b[len(t.b)-tailSize:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.b) }
