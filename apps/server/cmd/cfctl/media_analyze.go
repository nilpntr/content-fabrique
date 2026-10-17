package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

const mediaAnalyzeUsage = `Usage:
  cfctl media analyze <proxy.mp4> [--provider local|docker|hardened] [--json] [flags]

Development only. Runs the clip analysis (shots, motion, exposure, reps, holds, dead time) on a video file on this machine, with
no database and no object storage, and prints what it found. Give it a 720 px proxy (what the ingest makes: portrait 720x1280,
30 fps); the numbers are tuned for that. The file is copied into a temporary job directory and read by ffmpeg in the sandbox.

  --provider   local (ffmpeg on this machine, development only: it isolates nothing), docker (default) or hardened
  --json       print the shots, moments and summary as JSON
  --image      the media-tools image (default media-tools:dev)
  --docker     the docker CLI (default docker)
`

func cmdMediaAnalyze(args []string, stdout, stderr io.Writer, deps mediaDeps) int {
	fs := flag.NewFlagSet("media analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, mediaAnalyzeUsage) }
	provider := fs.String("provider", "docker", "local, docker or hardened")
	asJSON := fs.Bool("json", false, "print JSON")
	image := fs.String("image", "media-tools:dev", "media-tools image")
	dockerBin := fs.String("docker", "docker", "docker CLI")
	files, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(files) != 1 {
		_, _ = fmt.Fprintf(stderr, "cfctl media analyze: want exactly one file, got %d\n", len(files))
		return 2
	}
	if err := deps.checkProviderAllowed(*provider); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media analyze: %v\n", err)
		return 2
	}
	root, err := os.MkdirTemp("", "cf-analyze-")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media analyze: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()
	if root, err = filepath.EvalSymlinks(root); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media analyze: %v\n", err)
		return 1
	}
	p, err := deps.newProvider(*provider, *dockerBin, root)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media analyze: %v\n", err)
		return 2
	}
	if p.Name() == "local" {
		_, _ = fmt.Fprintln(stderr, "cfctl: the local provider runs ffmpeg on this machine without isolation: development only")
	}
	runner := &media.SandboxRunner{Provider: p, Image: *image, Limits: sandbox.Limits{CPU: 2, MemoryMB: 2048, Timeout: 10 * time.Minute, PIDs: 256}}

	ctx, stop := signalContext()
	defer stop()
	start := time.Now()
	an, err := analysis.AnalyzeFile(ctx, runner, root, files[0], 0, analysis.DefaultParams(), analysis.DefaultShotParams())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl media analyze: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(an); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl media analyze: %v\n", err)
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "%s: %d shot(s) in %s\n%s\n", filepath.Base(files[0]), len(an.Shots), time.Since(start).Round(time.Millisecond), an.Summary.Line())
	for _, sh := range an.Shots {
		_, _ = fmt.Fprintf(stdout, "shot %d  %.1f-%.1f s  brightness %.0f%% (%s, gain x%.2f)  sharpness %.2f (%.0f%% of the clip's best)  motion %.2f\n",
			sh.Idx+1, float64(sh.StartMs)/1000, float64(sh.EndMs)/1000, sh.Exposure.MeanY*100, sh.Exposure.Level, sh.Exposure.Gain, sh.Sharpness, sh.SharpnessRel*100, sh.Motion)
	}
	for _, m := range an.Moments {
		_, _ = fmt.Fprintf(stdout, "%-5s %7.1f-%7.1f s  score %.2f  %s\n", m.Kind, float64(m.StartMs)/1000, float64(m.EndMs)/1000, m.Score, m.Description)
	}
	return 0
}
