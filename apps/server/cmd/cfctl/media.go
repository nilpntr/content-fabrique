package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

const mediaUsage = `Usage:
  cfctl media probe <file> [--provider local|docker|hardened] [--json] [flags]
  cfctl media bench [--providers docker,hardened] [--runs 3] [flags]

probe prints a file's normalised metadata: container, duration, display size (after rotation), frame rate, codecs,
colour (HDR is flagged) and every audio stream with the one that would be used. Nothing from the file's tags is shown.
The file is only read, inside the sandbox provider, and never copied.

bench transcodes a generated 1080p 30 s clip (H.264 to H.264, CRF 23, veryfast) with each provider several times and prints
a Markdown table of timings: how much the hardened provider (gVisor) costs compared with plain docker.

  --provider   probe: local (ffprobe on this machine, development only: it isolates nothing), docker (default) or
               hardened (docker under gVisor runsc, which must be registered with the Docker daemon)
  --json       probe: print the metadata as JSON
  --image      the media-tools image (default media-tools:dev; docker build -t media-tools:dev packages/media-tools)
  --docker     the docker CLI (default docker)
  --timeout    probe: wall-clock limit (default 60s)
  --providers  bench: comma-separated list of docker, hardened, local (default docker,hardened)
  --runs       bench: runs per provider and step (default 3)
  --seconds    bench: length of the generated clip (default 30)
  --cpus       bench: CPU limit per job (default 2)
  --memory     bench: memory limit in MiB per job (default 2048)
`

// mediaDeps are the things cmdMedia needs from outside, replaced in tests.
type mediaDeps struct {
	// newProvider builds a sandbox provider rooted at the allowed host directory.
	newProvider func(name, dockerBin, root string) (sandbox.Provider, error)
	// lookupEnv reads the process environment (CF_ENV).
	lookupEnv func(string) (string, bool)
}

func defaultMediaDeps() mediaDeps {
	return mediaDeps{newProvider: newMediaProvider, lookupEnv: os.LookupEnv}
}

// checkProviderAllowed refuses the local provider (no isolation) in staging and production.
func (d mediaDeps) checkProviderAllowed(name string) error {
	if name != "local" || d.lookupEnv == nil {
		return nil
	}
	env, _ := d.lookupEnv("CF_ENV")
	return sandbox.CheckLocalAllowed(env)
}

func newMediaProvider(name, dockerBin, root string) (sandbox.Provider, error) {
	pol := sandbox.Policy{AllowedRoots: []string{root}}
	switch name {
	case "local":
		return sandbox.NewLocal(pol), nil
	case "docker":
		d := sandbox.NewDocker(pol)
		d.Binary = dockerBin
		return d, nil
	case "hardened":
		h := sandbox.NewHardened(pol)
		h.Binary = dockerBin
		return h, nil
	}
	return nil, fmt.Errorf("unknown provider %q (use local, docker or hardened)", name)
}

func cmdMedia(args []string, stdout, stderr io.Writer, deps mediaDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, mediaUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, mediaUsage)
		return 0
	case "probe":
		return cmdMediaProbe(args[1:], stdout, stderr, deps)
	case "bench":
		return cmdMediaBench(args[1:], stdout, stderr, deps)
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl media: unknown command %q\n\n%s", args[0], mediaUsage)
		return 2
	}
}

// parseInterspersed parses flags that may come before or after the positional arguments, which flag.Parse alone does
// not (it stops at the first positional).
func parseInterspersed(fs *flag.FlagSet, args []string) (positional []string, err error) {
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func cmdMediaProbe(args []string, stdout, stderr io.Writer, deps mediaDeps) int {
	fs := flag.NewFlagSet("media probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, mediaUsage) }
	provider := fs.String("provider", "docker", "local, docker or hardened")
	asJSON := fs.Bool("json", false, "print JSON")
	image := fs.String("image", "media-tools:dev", "media-tools image")
	dockerBin := fs.String("docker", "docker", "docker CLI")
	timeout := fs.Duration("timeout", 60*time.Second, "wall-clock limit")
	files, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(files) != 1 {
		_, _ = fmt.Fprintf(stderr, "cfctl media probe: want exactly one file, got %d\n", len(files))
		return 2
	}
	if err := deps.checkProviderAllowed(*provider); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media probe: %v\n", err)
		return 2
	}
	abs, err := filepath.Abs(files[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media probe: %v\n", err)
		return 2
	}
	// The sandbox may see the file the argument points to, after symlinks, and the directory it lives in is the allowed root:
	// a symlink cannot lead the mount anywhere else.
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media probe: %v\n", err)
		return 1
	}
	p, err := deps.newProvider(*provider, *dockerBin, filepath.Dir(target))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media probe: %v\n", err)
		return 2
	}
	if p.Name() == "local" {
		_, _ = fmt.Fprintln(stderr, "cfctl: the local provider runs ffprobe on this machine without isolation: development only")
	}
	limits := sandbox.DefaultLimits()
	limits.Timeout = *timeout
	runner := &media.SandboxRunner{Provider: p, Image: *image, Limits: limits, Labels: map[string]string{"kind": "probe"}}

	ctx, stop := signalContext()
	defer stop()
	m, err := media.Probe(ctx, runner, target)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media probe: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(m); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl media probe: %v\n", err)
			return 1
		}
		return 0
	}
	writeMetadataTable(stdout, filepath.Base(files[0]), m)
	return 0
}

// writeMetadataTable prints the human view of the metadata.
func writeMetadataTable(w io.Writer, name string, m media.Metadata) {
	row := func(label, format string, a ...any) {
		_, _ = fmt.Fprintf(w, "%-10s "+format+"\n", append([]any{label}, a...)...)
	}
	row("file", "%s", name)
	row("container", "%s (%s)", m.Container, m.FormatName)
	row("duration", "%.3f s", float64(m.DurationMs)/1000)
	row("size", "%s", humanBytes(m.SizeBytes))
	if m.HasVideo {
		row("video", "%s %s", m.Codecs.Video, m.PixFmt)
		rot := "none"
		if m.Rotation != 0 {
			rot = fmt.Sprintf("%d deg, turn %d deg clockwise", m.Rotation, m.ClockwiseDegrees())
		}
		row("display", "%dx%d (stored %dx%d, rotation %s)", m.Width, m.Height, m.CodedWidth, m.CodedHeight, rot)
		row("fps", "%d/%d (%.3f)", m.FPS.Num, m.FPS.Den, m.FPS.Value)
		dyn := "SDR"
		if m.HDR {
			dyn = "HDR " + strings.ToUpper(m.HDRFormat)
		}
		row("colour", "%s / %s / %s (%s)", orDash(m.ColorSpace), orDash(m.ColorTransfer), orDash(m.ColorPrimaries), dyn)
	} else {
		row("video", "none")
	}
	if !m.HasAudio {
		row("audio", "none")
		return
	}
	for i, a := range m.AudioStreams {
		label := ""
		if i == 0 {
			label = "audio"
		}
		note := "ignored"
		switch {
		case !a.Decodable:
			note = "undecodable, skipped"
		case m.SelectedAudio != nil && *m.SelectedAudio == a.Index:
			note = "selected"
		}
		tag := ""
		if a.CodecTag != "" && a.CodecTag != a.Codec {
			tag = " [" + a.CodecTag + "]"
		}
		row(label, "#%d %s%s, %d ch, %d Hz: %s", a.Index, a.Codec, tag, a.Channels, a.SampleRate, note)
	}
	if m.SelectedAudio == nil {
		row("", "no decodable audio stream")
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
