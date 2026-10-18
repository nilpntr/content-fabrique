package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

const renderUsage = `Usage:
  cfctl render <spec.json> --assets <dir> -o <out.mp4> [--profile preview|final] [--provider local|docker|hardened] [--dry-run]

Renders a timeline spec with ffmpeg, for local manual runs (the platform renders through its jobs, P3-06). The spec is
decoded strictly (unknown fields are errors) and compiled by internal/render; it is not checked against an asset catalog,
so run it through the validator first if it did not come from one.

  --assets     a directory with one file per asset, named <asset id> plus any extension (ast_01j3er3kw....mp4). The same
               file is used for both profiles: there is no proxy locally.
  -o           the MP4 to write (replaced if it exists)
  --profile    preview (half size, fast, with the beat guide; the default) or final (full size, two-pass loudness)
  --provider   the sandbox provider ffmpeg runs in: local (default: ffmpeg on this machine, no isolation, development only),
               docker or hardened (the media-tools image)
  --image      the media-tools image for docker and hardened (default media-tools:dev)
  --docker     the docker CLI (default docker)
  --fonts-dir  the directory with the font files (packages/media-tools/fonts), for --provider local outside a repository checkout;
               docker and hardened use the fonts in the image (default: found above the working directory)
  --ffmpeg     the ffmpeg binary used by --dry-run's printout, and ffprobe's directory (default: from PATH)
  --stem       also write the audio of a preview without its beat guide here (an M4A): what cfctl audio check should measure
  --subjects   a JSON file of subject boxes per asset id ({"ast_...": {"aspect": 0.5625, "samples": [{"at_ms": 1000, "x": 0.2,
               "y": 0.2, "w": 0.6, "h": 0.7, "head_top_y": 0.22, "head_x": 0.42, "head_w": 0.16, "shoulder_y": 0.34}]}}; head_x, head_w
               and shoulder_y are optional): titles that would cover the person are moved off them, as in the
               platform's renders, which read the boxes from the asset's quick look
  --dry-run    print the ffmpeg command line and the filter graph, and do not run it

Prints the sound cues (cue_only sounds are never mixed in) and progress while it runs.
`

func cmdRender(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("render", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, renderUsage) }
	assetsArg := flags.String("assets", "", "directory with the assets")
	outArg := flags.String("o", "", "output MP4")
	profileArg := flags.String("profile", "preview", "preview or final")
	stemArg := flags.String("stem", "", "also write the audio without the preview's beat guide here (an M4A; only a preview with a guide has one)")
	ffmpegArg := flags.String("ffmpeg", "ffmpeg", "ffmpeg binary")
	providerArg := flags.String("provider", "local", "local, docker or hardened")
	imageArg := flags.String("image", "media-tools:dev", "media-tools image")
	dockerArg := flags.String("docker", "docker", "docker CLI")
	fontsArg := flags.String("fonts-dir", "", "directory with the font files, for the local provider")
	dryRun := flags.Bool("dry-run", false, "print the command and do not run it")
	subjectsArg := flags.String("subjects", "", "a JSON file of subject boxes per asset id, for keeping titles off the head")

	// The spec path may come before or after the flags.
	var positional []string
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	if len(positional) == 1 && positional[0] == "help" {
		_, _ = fmt.Fprint(stdout, renderUsage)
		return 0
	}
	if len(positional) != 1 || *assetsArg == "" || (*outArg == "" && !*dryRun) {
		_, _ = fmt.Fprint(stderr, renderUsage)
		return 2
	}
	profile, err := render.ParseProfile(*profileArg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl render: %v\n", err)
		return 2
	}

	data, err := os.ReadFile(positional[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl render: %v\n", err)
		return 1
	}
	spec, err := timeline.Parse(data)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl render: %s: %v\n", positional[0], err)
		return 1
	}
	res := &dirResolver{dir: *assetsArg, ffmpeg: *ffmpegArg}
	if *subjectsArg != "" {
		ix, err := loadSubjectsFile(*subjectsArg)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl render: %v\n", err)
			return 1
		}
		res.subjects = ix
	}
	plan, err := render.Compile(spec, profile, res)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl render: %v\n", err)
		return 1
	}
	for _, c := range plan.SoundCues {
		_, _ = fmt.Fprintf(stderr, "sound cue (not mixed in): %s trend %s at %d ms, offset %d ms\n", c.ItemID, c.TrendID, c.AtMs, c.SourceOffsetMs)
	}
	for _, w := range plan.Warnings {
		_, _ = fmt.Fprintf(stderr, "warning: %s: %s\n", w.Code, w.Message)
	}

	if *dryRun {
		out := *outArg
		if out == "" {
			out = "<out.mp4>"
		}
		_, _ = fmt.Fprintf(stdout, "%s %s\n\nfilter_complex:\n%s\n", *ffmpegArg, strings.Join(quoteAll(plan.OutputArgs(out)), " "), render.PrettyFilter(plan.FilterComplex))
		return 0
	}

	if *providerArg == "local" {
		env, _ := os.LookupEnv("CF_ENV")
		if err := sandbox.CheckLocalAllowed(env); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl render: %v\n", err)
			return 2
		}
	}
	runner, workDir, cleanup, err := newRenderRunner(*providerArg, *dockerArg, *imageArg, *assetsArg, *outArg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl render: %v\n", err)
		return 2
	}
	defer cleanup()
	if *providerArg == "local" {
		_, _ = fmt.Fprintln(stderr, "cfctl: the local provider runs ffmpeg on this machine without isolation: development only")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lastTenth := -1
	err = render.Execute(ctx, plan, runner, render.ExecuteOptions{
		Output:     *outArg,
		StemOutput: *stemArg,
		WorkDir:    workDir,
		FontsDir:   *fontsArg,
		Progress: func(p render.Progress) {
			if tenth := int(p.Fraction * 10); tenth != lastTenth || p.Done {
				lastTenth = tenth
				_, _ = fmt.Fprintf(stderr, "%s: %3d%%  (%d frames, %.1fx)\n", p.Stage, int(p.Fraction*100), p.Frame, p.Speed)
			}
		},
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl render: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s (%dx%d, %.1f s, %s profile)\n", *outArg, plan.OutputSize.Width, plan.OutputSize.Height, float64(plan.DurationMs)/1000, plan.Profile.Name)
	return 0
}

// newRenderRunner builds the sandbox runner for a render: the provider may read the assets directory and write the work
// directory, a fresh directory next to the output (so that moving the result is a rename).
func newRenderRunner(name, dockerBin, image, assetsDir, output string) (runner media.Runner, workDir string, cleanup func(), err error) {
	assets, err := filepath.Abs(assetsDir)
	if err != nil {
		return nil, "", nil, err
	}
	if assets, err = filepath.EvalSymlinks(assets); err != nil {
		return nil, "", nil, err
	}
	outDir, err := filepath.Abs(filepath.Dir(output))
	if err != nil {
		return nil, "", nil, err
	}
	work, err := os.MkdirTemp(outDir, ".cf-render-")
	if err != nil {
		return nil, "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(work) }
	if work, err = filepath.EvalSymlinks(work); err != nil {
		cleanup()
		return nil, "", nil, err
	}
	pol := sandbox.Policy{AllowedRoots: []string{assets, work}}
	var p sandbox.Provider
	switch name {
	case "local":
		p = sandbox.NewLocal(pol)
	case "docker":
		d := sandbox.NewDocker(pol)
		d.Binary = dockerBin
		p = d
	case "hardened":
		h := sandbox.NewHardened(pol)
		h.Binary = dockerBin
		p = h
	default:
		cleanup()
		return nil, "", nil, fmt.Errorf("unknown provider %q (use local, docker or hardened)", name)
	}
	return &media.SandboxRunner{Provider: p, Image: image, Limits: render.DefaultLimits(), Labels: map[string]string{"kind": "render"}}, work, cleanup, nil
}

// quoteAll quotes the arguments that a shell would split, for the dry run's printout.
func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n'\"[]();|&$<>*?\\") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return out
}

// dirResolver resolves an asset id to the file in a directory that starts with it, and asks ffprobe whether it has audio.
type dirResolver struct {
	dir    string
	ffmpeg string
	// subjects are the subject boxes of the assets (--subjects), by asset id.
	subjects subject.Index
}

// loadSubjectsFile reads a JSON object of asset id to subject track ({"aspect": 0.5625, "samples": [...]}); a track that is not usable
// is an error, so a typo in a hand-written file is seen.
func loadSubjectsFile(path string) (subject.Index, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a developer tool reading the file the developer named
	if err != nil {
		return nil, err
	}
	var raw map[string]subject.Track
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ix := make(subject.Index, len(raw))
	for id, t := range raw {
		clean, ok := subject.Clean(t)
		if !ok {
			return nil, fmt.Errorf("%s: the subject track of %s is not usable (an aspect from %v to %v and boxes inside the frame)", path, id, subject.MinAspect, subject.MaxAspect)
		}
		ix[id] = clean
	}
	return ix, nil
}

func (r *dirResolver) Resolve(assetID string, _ render.Variant) (render.ResolvedAsset, error) {
	// The id comes from a spec: refuse anything that could leave the directory (and a glob character is never special:
	// the directory is listed and names are compared exactly).
	if assetID == "" || strings.ContainsAny(assetID, `/\`) || strings.Contains(assetID, "..") {
		return render.ResolvedAsset{}, fmt.Errorf("invalid asset id %q", assetID)
	}
	dir, err := filepath.Abs(r.dir)
	if err != nil {
		return render.ResolvedAsset{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return render.ResolvedAsset{}, err
	}
	var matches []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.TrimSuffix(name, filepath.Ext(name)) == assetID {
			matches = append(matches, filepath.Join(dir, name))
		}
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return render.ResolvedAsset{}, fmt.Errorf("no file for asset %s in %s (want %s.<ext>)", assetID, r.dir, assetID)
	}
	ra := render.ResolvedAsset{Location: matches[0], HasAudio: r.hasAudio(matches[0])}
	if t, ok := r.subjects[assetID]; ok {
		ra.Subject = &t
	}
	return ra, nil
}

// hasAudio runs ffprobe; when it cannot, it assumes the file has audio and lets ffmpeg report the problem.
func (r *dirResolver) hasAudio(path string) bool {
	probe := "ffprobe"
	if dir := filepath.Dir(r.ffmpeg); dir != "." && r.ffmpeg != "ffmpeg" {
		probe = filepath.Join(dir, "ffprobe")
	}
	out, err := exec.CommandContext(context.Background(), probe, "-v", "error", "-select_streams", "a", "-show_entries", "stream=index", "-of", "csv=p=0", path).Output() //nolint:gosec // a local developer tool; path is a file in the assets directory
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != ""
}
