package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/agents/agentset"
	"github.com/nilpntr/content-fabrique/internal/audiocheck"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

const audioUsage = `Usage:
  cfctl audio check --file <render.mp4> --time-map <time_map.json> [--spec <timeline.json>] [flags]
  cfctl audio check --project <prj_...> --workspace <wks_... or org_...> [--render <ren_...>] [flags]

DEVELOPMENT AND LOCAL USE. Listens to a rendered video with ffmpeg measurements and no model (internal/audiocheck, P3-11): the
loudness (integrated, true peak, range, momentary curve), the silences, where new sounds start, how fast the sound reaches body
level, whether the sound lands on the cuts of the edit and on the planned sound effects, the density of the effects, and the usual
defects (clipping, DC offset, lopsided channels). It prints a plain-language summary and a pass/fail checklist against targets
(each with its source; estimates are marked), and can write the structured report as JSON and a PNG sheet of the sound on the
contact sheet's time axis.

  --file       the rendered MP4 (with --time-map)
  --time-map   the render's time map, a JSON list as stored with the render (the cuts come from its main-track entries)
  --spec       the timeline spec the render came from, to know the planned sound effects (optional)
  --project    a project (prj_...): checks its newest ready render (or --render); needs the database and object storage
               settings in the env file, and the media sandbox settings
  --render     with --project: a render id (ren_...) instead of the newest ready one
  --workspace  with --project: the workspace
  --profile    what the sound is held to: auto (default: from the timeline of --spec or --project: sound_ready when no clip is in full mode and
               there is no music or creator audio; without a spec it is programme), programme or sound_ready (P3-12)
  --fps        the frame rate "within a frame" is measured against (default: read from the file)
  --sheet      write the sheet PNG here
  --json       write the structured report here ("-" for standard output, after the summary)
  --provider   where ffmpeg runs for --file: local (default, no isolation, development only), docker or hardened
  --image      the media-tools image for docker and hardened (default media-tools:dev)
  --docker     the docker CLI (default docker)
  --env-file   with --project: the env file; default .env at the repository root
`

func cmdAudio(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, audioUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, audioUsage)
		return 0
	case "check":
		return cmdAudioCheck(args[1:], stdout, stderr, lookup, deps)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl audio: unknown command %q\n\n%s", args[0], audioUsage)
	return 2
}

func cmdAudioCheck(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "audio check"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, audioUsage) }
	fileArg := flags.String("file", "", "rendered MP4")
	tmArg := flags.String("time-map", "", "time map JSON")
	specArg := flags.String("spec", "", "timeline spec JSON")
	projectArg := flags.String("project", "", "project id")
	renderArg := flags.String("render", "", "render id")
	workspaceArg := flags.String("workspace", "", "workspace id")
	profileArg := flags.String("profile", "auto", "auto, programme or sound_ready")
	fpsArg := flags.Int("fps", 0, "frame rate")
	sheetArg := flags.String("sheet", "", "sheet PNG to write")
	jsonArg := flags.String("json", "", "report JSON to write (- for stdout)")
	providerArg := flags.String("provider", "local", "local, docker or hardened")
	imageArg := flags.String("image", "media-tools:dev", "media-tools image")
	dockerArg := flags.String("docker", "docker", "docker CLI")
	envFileArg := flags.String("env-file", "", "env file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*fileArg == "") == (*projectArg == "") {
		_, _ = fmt.Fprint(stderr, audioUsage)
		return 2
	}
	switch *profileArg {
	case "auto", audiocheck.ProfileProgramme, audiocheck.ProfileSoundReady:
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --profile is auto, programme or sound_ready\n", name)
		return 2
	}
	ctx, stop := signalContext()
	defer stop()

	var (
		mp4     string
		tm      []render.TimeMapEntry
		planned []audiocheck.PlannedSFX
		spec    *timeline.Spec
		runner  media.Runner
		workDir string
		cleanup = func() {}
		fps     = *fpsArg
	)
	if *fileArg != "" {
		if *tmArg == "" {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: --file needs --time-map\n", name)
			return 2
		}
		var err error
		if mp4, err = filepath.Abs(*fileArg); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 2
		}
		if tm, err = readTimeMap(*tmArg); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 1
		}
		if *specArg != "" {
			if spec, err = readSpec(*specArg); err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
				return 1
			}
		}
		if *providerArg == "local" {
			env, _ := os.LookupEnv("CF_ENV")
			if err := sandbox.CheckLocalAllowed(env); err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
				return 2
			}
			_, _ = fmt.Fprintln(stderr, "cfctl: the local provider runs ffmpeg on this machine without isolation: development only")
		}
		work, err := os.MkdirTemp("", "cf-audiocheck-")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 1
		}
		defer func() { _ = os.RemoveAll(work) }()
		if work, err = filepath.EvalSymlinks(work); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 1
		}
		dir := filepath.Dir(mp4)
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		mp4 = filepath.Join(dir, filepath.Base(mp4))
		runner, err = newCheckRunner(*providerArg, *dockerArg, *imageArg, dir, work)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 2
		}
		workDir = work
	} else {
		var code int
		mp4, tm, planned, spec, runner, workDir, cleanup, fps, code = loadProjectRender(ctx, name, *projectArg, *workspaceArg, *renderArg, *envFileArg, fps, stdout, stderr, lookup, deps)
		if code != 0 {
			return code
		}
	}
	defer cleanup()

	opts := []audiocheck.Option{audiocheck.WithSFX(planned), audiocheck.WithWorkDir(workDir)}
	if spec != nil { // the planned effects, the frame rate and the profile (sound-ready when no programme sound is exported)
		opts = []audiocheck.Option{audiocheck.WithSpec(spec), audiocheck.WithWorkDir(workDir)}
	}
	if fps > 0 {
		opts = append(opts, audiocheck.WithFPS(fps))
	}
	switch *profileArg {
	case "auto":
		if spec == nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: no timeline spec (--spec), so the profile cannot be chosen from it: the programme profile is used; pass --profile sound_ready for an export that is a bed and effects\n", name)
		}
	default:
		opts = append(opts, audiocheck.WithProfile(*profileArg))
	}
	report, err := audiocheck.Check(ctx, runner, mp4, tm, opts...)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, report.Summary)
	_, _ = fmt.Fprintln(stdout)
	for _, c := range report.Checklist {
		est := ""
		if c.Estimate {
			est = " (estimate)"
		}
		_, _ = fmt.Fprintf(stdout, "  %-4s %-48s %s  [target: %s%s]\n", strings.ToUpper(c.Status), c.Title, c.Value, c.Target, est)
	}
	if *sheetArg != "" {
		if err := os.WriteFile(*sheetArg, report.Sheet, 0o600); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "\nwrote the sheet to %s\n", *sheetArg)
	}
	if *jsonArg != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 1
		}
		if *jsonArg == "-" {
			_, _ = fmt.Fprintf(stdout, "\n%s\n", data)
		} else if err := os.WriteFile(*jsonArg, append(data, '\n'), 0o600); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 1
		} else {
			_, _ = fmt.Fprintf(stdout, "wrote the report to %s\n", *jsonArg)
		}
	}
	return 0
}

// readTimeMap reads a render's time map: a JSON list of entries (the shape of render.TimeMapEntry).
func readTimeMap(path string) ([]render.TimeMapEntry, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a developer tool reading the file it was told to
	if err != nil {
		return nil, err
	}
	var tm []render.TimeMapEntry
	if err := json.Unmarshal(data, &tm); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return tm, nil
}

// readSpec reads a timeline spec file.
func readSpec(path string) (*timeline.Spec, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a developer tool reading the file it was told to
	if err != nil {
		return nil, err
	}
	spec, err := timeline.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return spec, nil
}

// newCheckRunner builds the sandbox runner for a check: it may read the directory of the MP4 and write the work directory.
func newCheckRunner(name, dockerBin, image, inDir, workDir string) (media.Runner, error) {
	p, err := newSandboxProvider(name, dockerBin, inDir, workDir)
	if err != nil {
		return nil, err
	}
	return &media.SandboxRunner{Provider: p, Image: image, Limits: render.DefaultLimits(), Labels: map[string]string{"kind": "audio_check"}}, nil
}

// loadProjectRender downloads a project's render (its MP4 and time map) and the spec it was rendered from, and builds the sandbox
// runner from the env file's media settings. The caller removes workDir.
func loadProjectRender(ctx context.Context, name, projectArg, workspaceArg, renderArg, envFileArg string, fps int, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) (mp4 string, tm []render.TimeMapEntry, planned []audiocheck.PlannedSFX, specOut *timeline.Spec, runner media.Runner, workDir string, cleanup func(), outFPS, code int) {
	pid, err := ids.ParseProjectID(projectArg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %q is not a project id like prj_01...\n", name, projectArg)
		return "", nil, nil, nil, nil, "", nil, 0, 2
	}
	wantWS, wantOrg, ok := parseWorkspaceRef(name, workspaceArg, stderr)
	if !ok {
		return "", nil, nil, nil, nil, "", nil, 0, 2
	}
	env, code := guardDevCommand(name, envFileArg, false, false, stdout, stderr, lookup)
	if code != 0 {
		return "", nil, nil, nil, nil, "", nil, 0, code
	}
	cfg, err := config.Load(env.get)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return "", nil, nil, nil, nil, "", nil, 0, 2
	}
	if !cfg.ObjectStorageConfigured() {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: the R2_* settings are not set in %s or the environment\n", name, displayPath(env.root, env.path))
		return "", nil, nil, nil, nil, "", nil, 0, 2
	}
	st, code := env.openDB(ctx, name, deps)
	if code != 0 {
		return "", nil, nil, nil, nil, "", nil, 0, code
	}
	defer st.Close()
	ws, code := findWorkspace(ctx, st, name, wantWS, wantOrg, env.err)
	if code != 0 {
		return "", nil, nil, nil, nil, "", nil, 0, code
	}
	var row store.Render
	if renderArg != "" {
		rid, err := ids.ParseRenderID(renderArg)
		if err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: %q is not a render id like ren_01...\n", name, renderArg)
			return "", nil, nil, nil, nil, "", nil, 0, 2
		}
		if row, err = st.GetRender(ctx, ws.ID, rid); err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: no such render in this workspace: %v\n", name, err)
			return "", nil, nil, nil, nil, "", nil, 0, 1
		}
	} else {
		rows, err := st.ListRenders(ctx, ws.ID, pid, store.RenderFilter{}, 20)
		if err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: listing the renders: %v\n", name, err)
			return "", nil, nil, nil, nil, "", nil, 0, 1
		}
		found := false
		for _, r := range rows {
			if r.Status == "ready" && r.Mp4Key != nil && r.TimeMapKey != nil {
				row, found = r, true
				break
			}
		}
		if !found {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: the project has no ready render (render it first)\n", name)
			return "", nil, nil, nil, nil, "", nil, 0, 1
		}
	}
	if row.ProjectID != pid || row.Status != "ready" || row.Mp4Key == nil || row.TimeMapKey == nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: render %s is not a ready render of project %s\n", name, ids.Encode(ids.PrefixRender, row.ID.UUID), projectArg)
		return "", nil, nil, nil, nil, "", nil, 0, 1
	}
	objects, err := storage.New(cfg.Storage())
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: object storage: %v\n", name, err)
		return "", nil, nil, nil, nil, "", nil, 0, 1
	}
	runnerOut, root, err := agentset.NewFrameRunner(cfg.MediaSandboxProvider, cfg.MediaToolsImage, cmp.Or(cfg.MediaTempDir, filepath.Join(os.TempDir(), "cf-media")), cfg.Env)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: the sandbox: %v\n", name, err)
		return "", nil, nil, nil, nil, "", nil, 0, 2
	}
	work, err := os.MkdirTemp(root, "audiocheck-")
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return "", nil, nil, nil, nil, "", nil, 0, 1
	}
	// The MP4 goes in the work directory's parent (the sandbox reads it read-only), the sandbox writes in its own subdirectory.
	mp4 = filepath.Join(work, "render.mp4")
	if err := download(ctx, objects, *row.Mp4Key, mp4, 2<<30); err != nil {
		_ = os.RemoveAll(work)
		_, _ = fmt.Fprintf(env.err, "cfctl %s: downloading the render: %v\n", name, err)
		return "", nil, nil, nil, nil, "", nil, 0, 1
	}
	// A preview with a beat guide has the mix without the guide next to it (the audio stem): that is the export's sound, so it is what is
	// measured. Without one the video's own sound is.
	if stemKey, kerr := storage.RenderKey(ws.ID, pid, row.ID, storage.RenderAudioStemName); kerr == nil {
		stem := filepath.Join(work, storage.RenderAudioStemName)
		switch derr := download(ctx, objects, stemKey, stem, 256<<20); {
		case derr == nil:
			mp4 = stem
			_, _ = fmt.Fprintf(stdout, "cfctl %s: measuring the audio stem (the mix without the beat guide)\n", name)
		case !errors.Is(derr, storage.ErrNotFound):
			_, _ = fmt.Fprintf(env.err, "cfctl %s: warning: the audio stem could not be downloaded, so the video's sound (with its beat guide) is measured: %v\n", name, derr)
		}
	}
	tmPath := filepath.Join(work, "time_map.json")
	if err := download(ctx, objects, *row.TimeMapKey, tmPath, 8<<20); err != nil {
		_ = os.RemoveAll(work)
		_, _ = fmt.Fprintf(env.err, "cfctl %s: downloading the time map: %v\n", name, err)
		return "", nil, nil, nil, nil, "", nil, 0, 1
	}
	if tm, err = readTimeMap(tmPath); err != nil {
		_ = os.RemoveAll(work)
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return "", nil, nil, nil, nil, "", nil, 0, 1
	}
	out := filepath.Join(work, "out")
	if err := os.Mkdir(out, 0o750); err != nil {
		_ = os.RemoveAll(work)
		return "", nil, nil, nil, nil, "", nil, 0, 1
	}
	// The planned effects and the frame rate come from the timeline version the render was made of.
	// Without it the check still runs, but it cannot say where the effects were planned: say so.
	tv, err := st.GetTimelineVersion(ctx, ws.ID, pid, int(row.TimelineVersion))
	if err == nil {
		var spec *timeline.Spec
		if spec, err = timeline.Parse(tv.Spec); err == nil {
			planned, specOut = audiocheck.PlannedSFXFromSpec(spec), spec
			if fps == 0 {
				fps = spec.Output.FPS
			}
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: warning: could not read the timeline version %d the render was made from, so the planned sound effects and the frame rate are unknown: %v\n", name, row.TimelineVersion, err)
	}
	_, _ = fmt.Fprintf(stdout, "cfctl %s: render %s (version %d, %s profile)\n", name, ids.Encode(ids.PrefixRender, row.ID.UUID), row.TimelineVersion, row.Profile)
	return mp4, tm, planned, specOut, runnerOut, out, func() { _ = os.RemoveAll(work) }, fps, 0
}

// download copies an object to a file, refusing more than limit bytes.
func download(ctx context.Context, objects *storage.Client, key, path string, limit int64) error {
	obj, err := objects.Get(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size > limit {
		return fmt.Errorf("%d bytes is more than the limit of %d", obj.Size, limit)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a path in our own work directory
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(obj.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("the object is more than the limit of %d bytes", limit)
	}
	if err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
