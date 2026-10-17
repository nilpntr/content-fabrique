package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// The benchmark steps, in table order.
const (
	stepStartup   = "start-up (ffprobe -version)"
	stepTranscode = "transcode 1080p H.264 CRF 23 veryfast"
	stepCopy      = "copy 0.9 GB (read + write, no encode)"
)

var benchSteps = []string{stepStartup, stepTranscode, stepCopy}

// benchResult holds the timings of one provider, per step.
type benchResult struct {
	provider string
	steps    map[string][]time.Duration
}

func cmdMediaBench(args []string, stdout, stderr io.Writer, deps mediaDeps) int {
	fs := flag.NewFlagSet("media bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, mediaUsage) }
	providers := fs.String("providers", "docker,hardened", "comma-separated providers")
	runs := fs.Int("runs", 3, "runs per provider and step")
	seconds := fs.Int("seconds", 30, "length of the generated clip")
	cpus := fs.Float64("cpus", 2, "CPU limit per job")
	memory := fs.Int("memory", 2048, "memory limit per job in MiB")
	image := fs.String("image", "media-tools:dev", "media-tools image")
	dockerBin := fs.String("docker", "docker", "docker CLI")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(rest) > 0 || *runs < 1 || *runs > 20 {
		_, _ = fmt.Fprint(stderr, "cfctl media bench: unexpected argument or --runs outside 1..20\n")
		return 2
	}
	names := strings.Split(*providers, ",")
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
	}

	work, err := os.MkdirTemp("", "cf-bench-")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media bench: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(work) }()
	// Resolve symlinks (macOS /var -> /private/var) so the allowed root and the mounts agree.
	if work, err = filepath.EvalSymlinks(work); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media bench: %v\n", err)
		return 1
	}

	limits := sandbox.DefaultLimits()
	limits.CPU, limits.MemoryMB = *cpus, *memory
	runners := map[string]*media.SandboxRunner{}
	for _, n := range names {
		p, err := deps.newProvider(n, *dockerBin, work)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl media bench: %v\n", err)
			return 2
		}
		runners[n] = &media.SandboxRunner{Provider: p, Image: *image, Limits: limits, Labels: map[string]string{"kind": "bench"}}
	}

	ctx, stop := signalContext()
	defer stop()
	b := &bencher{ctx: ctx, work: work, stderr: stderr, seconds: *seconds}

	// The clip is generated once, by the first provider, and every provider transcodes the same file.
	clip, err := b.generate(runners[names[0]])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media bench: generate clip: %v\n", err)
		return 1
	}
	raw, err := b.generateRaw(runners[names[0]], rawSeconds)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media bench: generate raw clip: %v\n", err)
		return 1
	}
	var results []benchResult
	for _, n := range names {
		r := benchResult{provider: n, steps: map[string][]time.Duration{}}
		for i := 0; i < *runs; i++ {
			d, err := b.startup(runners[n])
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl media bench: %s start-up: %v\n", n, err)
				return 1
			}
			r.steps[stepStartup] = append(r.steps[stepStartup], d)
		}
		for i := 0; i < *runs; i++ {
			d, err := b.transcode(runners[n], clip, fmt.Sprintf("%s-t%d", n, i))
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl media bench: %s transcode: %v\n", n, err)
				return 1
			}
			r.steps[stepTranscode] = append(r.steps[stepTranscode], d)
		}
		for i := 0; i < *runs; i++ {
			d, err := b.copyFile(runners[n], raw, fmt.Sprintf("%s-c%d", n, i))
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl media bench: %s copy: %v\n", n, err)
				return 1
			}
			r.steps[stepCopy] = append(r.steps[stepCopy], d)
		}
		results = append(results, r)
	}
	writeBenchTable(stdout, results, benchInfo{image: *image, cpus: *cpus, memory: *memory, seconds: *seconds, runs: *runs})
	return 0
}

type bencher struct {
	ctx     context.Context
	work    string
	stderr  io.Writer
	seconds int
}

func (b *bencher) mkdir(name string) (string, error) {
	d := filepath.Join(b.work, name)
	if err := os.Mkdir(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func (b *bencher) generate(r *media.SandboxRunner) (string, error) {
	dir, err := b.mkdir("gen")
	if err != nil {
		return "", err
	}
	args, err := media.TestClipArgs(media.OutputPath("clip.mp4"), b.seconds)
	if err != nil {
		return "", err
	}
	if err := b.run(r, media.Invocation{Args: args, OutputDir: dir}); err != nil {
		return "", err
	}
	return filepath.Join(dir, "clip.mp4"), nil
}

// startup times the smallest possible job: it measures container (and sandbox) start, not ffmpeg's work.
func (b *bencher) startup(r *media.SandboxRunner) (time.Duration, error) {
	args, err := media.VersionArgs("ffprobe")
	if err != nil {
		return 0, err
	}
	res, err := r.Run(b.ctx, media.Invocation{Args: args})
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, &media.ToolError{Tool: "ffprobe", ExitCode: res.ExitCode, Stderr: string(res.Stderr)}
	}
	return res.Duration, nil
}

func (b *bencher) transcode(r *media.SandboxRunner, clip, name string) (time.Duration, error) {
	dir, err := b.mkdir(name)
	if err != nil {
		return 0, err
	}
	args, err := media.TranscodeH264Args(media.InputPath(0), media.OutputPath("out.mp4"), media.TranscodeOptions{Preset: "veryfast", CRF: 23})
	if err != nil {
		return 0, err
	}
	res, err := r.Run(b.ctx, media.Invocation{Args: args, Inputs: []media.Input{{Host: clip, Container: media.InputPath(0)}}, OutputDir: dir})
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: string(res.Stderr)}
	}
	// A fast wrong answer is not a result: check what came out, through the same provider.
	m, err := media.Probe(b.ctx, r, filepath.Join(dir, "out.mp4"))
	if err != nil {
		return 0, fmt.Errorf("probe of the output: %w", err)
	}
	if m.Width != 1920 || m.Height != 1080 || m.Codecs.Video != "h264" || !m.HasAudio ||
		m.DurationMs < int64(b.seconds)*1000-1000 || m.DurationMs > int64(b.seconds)*1000+1000 {
		return 0, fmt.Errorf("unexpected output: %dx%d %s, %d ms, audio=%v", m.Width, m.Height, m.Codecs.Video, m.DurationMs, m.HasAudio)
	}
	return res.Duration, nil
}

// rawSeconds of uncompressed 1080p30 video make the copy step's file (about 0.9 GB).
const rawSeconds = 10

func (b *bencher) generateRaw(r *media.SandboxRunner, seconds int) (string, error) {
	dir, err := b.mkdir("raw")
	if err != nil {
		return "", err
	}
	args, err := media.RawClipArgs(media.OutputPath("raw.avi"), seconds)
	if err != nil {
		return "", err
	}
	if err := b.run(r, media.Invocation{Args: args, OutputDir: dir}); err != nil {
		return "", err
	}
	return filepath.Join(dir, "raw.avi"), nil
}

// copyFile remuxes the raw file without encoding, so the time is reads and writes through the sandbox's filesystem.
func (b *bencher) copyFile(r *media.SandboxRunner, raw, name string) (time.Duration, error) {
	dir, err := b.mkdir(name)
	if err != nil {
		return 0, err
	}
	res, err := r.Run(b.ctx, media.Invocation{
		Args:      media.CopyArgs(media.InputPath(0), media.OutputPath("out.avi")),
		Inputs:    []media.Input{{Host: raw, Container: media.InputPath(0)}},
		OutputDir: dir,
	})
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: string(res.Stderr)}
	}
	in, err1 := os.Stat(raw)
	out, err2 := os.Stat(filepath.Join(dir, "out.avi"))
	if err := errors.Join(err1, err2); err != nil {
		return 0, fmt.Errorf("the copy is missing: %w", err)
	}
	if out.Size() < in.Size()*9/10 {
		return 0, fmt.Errorf("the copy is short: %d of %d bytes", out.Size(), in.Size())
	}
	_ = os.Remove(filepath.Join(dir, "out.avi")) // keep the disk use bounded across runs
	return res.Duration, nil
}

func (b *bencher) run(r *media.SandboxRunner, inv media.Invocation) error {
	res, err := r.Run(b.ctx, inv)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &media.ToolError{Tool: inv.Args[0], ExitCode: res.ExitCode, Stderr: string(res.Stderr)}
	}
	return nil
}

type benchInfo struct {
	image   string
	cpus    float64
	memory  int
	seconds int
	runs    int
}

func median(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func secs(d time.Duration) string { return fmt.Sprintf("%.2f", d.Seconds()) }

func secList(ds []time.Duration) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = secs(d)
	}
	return strings.Join(parts, " / ")
}

// writeBenchTable prints the results as Markdown, one row per step and provider. The last column compares each provider
// with the first one.
func writeBenchTable(w io.Writer, results []benchResult, info benchInfo) {
	_, _ = fmt.Fprintf(w, "Host: %s/%s, %d CPUs. Image %s. Limits per job: %g CPUs, %d MiB. Clip: %d s 1080p30 testsrc2 + sine. %d runs each.\n\n",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), info.image, info.cpus, info.memory, info.seconds, info.runs)
	_, _ = fmt.Fprintln(w, "| step | provider | runs (s) | median (s) | vs "+results[0].provider+" |")
	_, _ = fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, step := range benchSteps {
		base := median(results[0].steps[step])
		for _, r := range results {
			ds := r.steps[step]
			med := median(ds)
			ratio := fmt.Sprintf("%.2fx (%+.1f%%)", float64(med)/float64(base), (float64(med)/float64(base)-1)*100)
			_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n", step, r.provider, secList(ds), secs(med), ratio)
		}
	}
}
