package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/music"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

const musicUsage = `Usage:
  cfctl music analyze --file <song or video> [--json] [--provider local|docker|hardened]
  cfctl music extract --file <video.mp4> -o <song.m4a> [--provider local|docker|hardened]
  cfctl music synth -o <track.m4a> [--bpm 124] [--drop 3] [--seconds 30]

DEVELOPMENT ONLY (P6-06, D-34). The music analysis of a creator's song, as the analyze job makes it for an audio asset (package music):
the tempo and the beat grid, the downbeats and the phrases, the strong onsets, the drop and the loudness. analyze measures a local file
(a song, or the sound of a video) in the sandbox and prints the line the editor reads (--json: the whole analysis). extract writes the
sound of a video (a reference whose song the creator wants) as an AAC file, in the sandbox, to upload as the project's song. synth
writes a synthetic test track (a four-on-the-floor kick with a quiet intro and a drop on the beat nearest --drop): no asset, no
licence, for local renders. The files are only ever inputs of the sandbox; nothing is stored.

  --provider   where ffmpeg runs: local (default, no isolation, development only), docker or hardened
  --image      the media-tools image for docker and hardened (default media-tools:dev)
  --docker     the docker CLI (default docker)
`

func cmdMusic(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, musicUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, musicUsage)
		return 0
	case "analyze", "extract":
		return cmdMusicSandbox(args[0], args[1:], stdout, stderr)
	case "synth":
		return cmdMusicSynth(args[1:], stdout, stderr)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl music: unknown command %q\n\n%s", args[0], musicUsage)
	return 2
}

func cmdMusicSandbox(sub string, args []string, stdout, stderr io.Writer) int {
	name := "music " + sub
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, musicUsage) }
	fileArg := flags.String("file", "", "input file")
	outArg := flags.String("o", "", "output file (extract)")
	asJSON := flags.Bool("json", false, "print JSON (analyze)")
	providerArg := flags.String("provider", "local", "local, docker or hardened")
	imageArg := flags.String("image", "media-tools:dev", "media-tools image")
	dockerArg := flags.String("docker", "docker", "docker CLI")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *fileArg == "" || flags.NArg() != 0 || (sub == "extract") != (*outArg != "") {
		_, _ = fmt.Fprint(stderr, musicUsage)
		return 2
	}
	if *providerArg == "local" {
		env, _ := os.LookupEnv("CF_ENV")
		if err := sandbox.CheckLocalAllowed(env); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 2
		}
	}
	in, err := filepath.Abs(*fileArg)
	if err == nil {
		in, err = filepath.EvalSymlinks(in)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 2
	}
	work, err := os.MkdirTemp("", "cf-music-")
	if err == nil {
		work, err = filepath.EvalSymlinks(work)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(work) }()
	runner, err := newCheckRunner(*providerArg, *dockerArg, *imageArg, filepath.Dir(in), work)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 2
	}
	ctx, stop := signalContext()
	defer stop()
	if sub == "analyze" {
		a, err := music.Measure(ctx, runner, in, work)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 1
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(a)
			return 0
		}
		_, _ = fmt.Fprintln(stdout, a.Line())
		return 0
	}
	// extract: the first audio stream of the video, as AAC in an M4A, written by the sandbox and copied out.
	res, err := runner.Run(ctx, media.Invocation{
		Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", media.InputPath(0), "-map", "0:a:0", "-vn", "-sn", "-dn",
			"-t", fmt.Sprint(music.MaxSeconds), "-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-y", media.OutputPath("song.m4a")},
		Inputs: []media.Input{{Host: in, Container: media.InputPath(0)}}, OutputDir: work, Timeout: 5 * time.Minute,
	})
	if err != nil || res.ExitCode != 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: ffmpeg failed (%v): %s\n", name, err, media.StderrSnippet(res.Stderr))
		return 1
	}
	data, err := os.ReadFile(filepath.Join(work, "song.m4a")) // #nosec G304 -- our job dir
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 1
	}
	if err := os.WriteFile(*outArg, data, 0o600); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", *outArg, len(data))
	return 0
}

func cmdMusicSynth(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("music synth", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, musicUsage) }
	outArg := flags.String("o", "", "output file")
	bpm := flags.Float64("bpm", 124, "tempo")
	drop := flags.Float64("drop", 3, "the drop, in seconds (the nearest beat)")
	seconds := flags.Float64("seconds", 30, "length")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *outArg == "" || *bpm < 60 || *bpm > 200 || *seconds <= 0 || *seconds > 120 || *drop < 0 || *drop >= *seconds {
		_, _ = fmt.Fprint(stderr, musicUsage)
		return 2
	}
	cmd := exec.Command("ffmpeg", music.SyntheticTrackArgs(*bpm, *drop, *seconds, *outArg)...) //nolint:gosec // fixed arguments, development only
	if b, err := cmd.CombinedOutput(); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl music synth: %v\n%s", err, b)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s: %g BPM, the drop at %d ms\n", *outArg, *bpm, music.SyntheticDropMs(*bpm, *drop))
	return 0
}
