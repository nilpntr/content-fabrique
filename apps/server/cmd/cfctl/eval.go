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
	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const evalUsage = `Usage:
  cfctl eval editor --workspace <wks_... or org_...> --project <prj_...> [--version N] [--json] [flags]

DEVELOPMENT ONLY, like cfctl project show. eval editor prints the check_edit metrics of one stored timeline version of a project:
the hook, the shot lengths, the cuts against the reps and the beat grid, dead time, effect density, text in the safe zones, the
length and the brightness jump at each cut. The checks are computed in Go from the stored timeline and the stored clip analysis:
no model is called and nothing is written, so it costs nothing and can be run over every version of a run (or of two runs) to compare
them. When the version was made by an editor run that wrote a creative plan, the plan sets the genre thresholds and the target length;
without one the video is judged as a general video of 15 to 45 seconds.

  --workspace   a wks_... id or a Clerk org_... id (required)
  --project     the project (prj_...) (required)
  --version     the timeline version to check; default the current one
  --json        print the report as JSON (the same record check_edit stores in the run's events)
  --env-file    the env file; default .env at the repository root
`

func cmdEval(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, evalUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, evalUsage)
		return 0
	case "editor":
		return cmdEvalEditor(args[1:], stdout, stderr, lookup, deps)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl eval: unknown command %q\n\n%s", args[0], evalUsage)
	return 2
}

func cmdEvalEditor(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "eval editor"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, evalUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	projectArg := flags.String("project", "", "project id")
	version := flags.Int("version", 0, "timeline version")
	asJSON := flags.Bool("json", false, "print JSON")
	envFileArg := flags.String("env-file", "", "env file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: unexpected argument %q\n", name, flags.Arg(0))
		return 2
	}
	pid, err := ids.ParseProjectID(strings.TrimSpace(*projectArg))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --project <prj_...> is required\n", name)
		return 2
	}
	if *version < 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --version is a number from 1\n", name)
		return 2
	}
	wantWS, wantOrg, ok := parseWorkspaceRef(name, *workspaceArg, stderr)
	if !ok {
		return 2
	}
	env, code := guardDevCommand(name, *envFileArg, false, false, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	out, errOut := env.out, env.err
	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, name, deps)
	if code != 0 {
		return code
	}
	defer st.Close()
	ws, code := findWorkspace(ctx, st, name, wantWS, wantOrg, errOut)
	if code != 0 {
		return code
	}
	svc, err := projects.NewService(st, nil)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}
	det, perr := svc.Get(ctx, projects.Actor{Workspace: ws.ID}, pid)
	if perr != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %s: %s\n", name, perr.Code, perr.Message)
		return 1
	}
	v := *version
	if v == 0 {
		if det.Project.CurrentTimelineVersion == nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: project %s has no timeline yet\n", name, pid)
			return 1
		}
		v = int(*det.Project.CurrentTimelineVersion)
	}
	vd, perr := svc.Version(ctx, projects.Actor{Workspace: ws.ID}, pid, v)
	if perr != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: version %d: %s: %s\n", name, v, perr.Code, perr.Message)
		return 1
	}
	plan, author := planOfVersion(ctx, st, ws.ID, vd.Version)
	sampler, note := renderSamplerFor(env, st)
	report, err := editor.CheckVersion(ctx, svc, st, ws.ID, pid, v, plan, sampler)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}
	pass, fail, skip := report.Counts()
	if *asJSON {
		rec := editor.CheckRecord{RecordVersion: editor.CheckRecordVersion, Report: report, Passed: pass, Failed: fail, Skipped: skip}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rec); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintf(out, "project %s, version %d, made by %s\n", pid, v, author)
	if plan != nil {
		_, _ = fmt.Fprintf(out, "plan: %s (%s), target %d s, %d BPM, %d shots: %s\n", plan.ContentType, plan.PlaybookVersion, plan.TargetLengthS, plan.TempoBPM, len(plan.Shots), plan.Story)
	} else {
		_, _ = fmt.Fprintln(out, "plan: none found, judged as a general video")
	}
	_, _ = fmt.Fprintln(out, report.Text())
	if note != "" {
		_, _ = fmt.Fprintln(out, note)
	}
	m := report.Metrics
	_, _ = fmt.Fprintf(out, "\nmetrics: duration %d ms, %d shots (shortest %d ms, median %d ms, longest %d ms), longest equal run %d, rep edges %d/%d, dead share %.3f, effects %d (%.2f per cut, %d ramps), cuts on beat %d/%d, brightness jump %.2f (range %.2f)\n",
		m.DurationMs, m.Shots, m.ShotMinMs, m.ShotMedianMs, m.ShotMaxMs, m.LongestEqualRun, m.RepEdgesOn, m.RepEdgesTotal, m.DeadShare,
		m.Effects, m.EffectsPerCut, m.Ramps, m.CutsOnBeat, m.Cuts, m.BrightnessJump, m.BrightnessRange)
	return 0
}

// planOfVersion finds the creative plan of the editor run that made a version: the run's TypeID is the version's author.
func planOfVersion(ctx context.Context, st *store.Store, ws ids.WorkspaceID, v store.TimelineVersion) (*editor.Plan, string) {
	author := v.AuthorKind
	if v.AuthorKind != "agent" {
		return nil, author
	}
	author += " " + v.AuthorID
	run, err := parseRunID(v.AuthorID)
	if err != nil {
		return nil, author
	}
	if p, ok := editor.PlanFromEvents(ctx, st, ws, run); ok {
		return &p, author
	}
	return nil, author
}

// renderSamplerFor builds the sampler that measures the version's preview render (the rendered-brightness checks), from the same
// settings `cfctl agent run` takes its frames with: object storage and the media sandbox. Without them the checks that need the
// render do not apply, and the note says so.
func renderSamplerFor(env *agentEnv, st *store.Store) (editor.RenderSampler, string) {
	cfg := storage.Config{
		Endpoint: env.get("R2_ENDPOINT"), Bucket: env.get("R2_BUCKET"), AccessKeyID: env.get("R2_ACCESS_KEY_ID"),
		SecretAccessKey: env.get("R2_SECRET_ACCESS_KEY"), Jurisdiction: strings.ToLower(env.get("R2_JURISDICTION")),
	}
	if !cfg.Configured() {
		return nil, "note: no object storage is configured (R2_*), so the preview render was not measured and the rendered-brightness checks do not apply"
	}
	objects, err := storage.New(cfg)
	if err != nil {
		return nil, fmt.Sprintf("note: object storage: %v; the rendered-brightness checks do not apply", err)
	}
	provider := cmp.Or(env.get("MEDIA_SANDBOX_PROVIDER"), config.SandboxDocker)
	tempDir := cmp.Or(env.get("MEDIA_TEMP_DIR"), filepath.Join(os.TempDir(), "cf-media"))
	runner, dir, err := agentset.NewFrameRunner(provider, env.get("MEDIA_TOOLS_IMAGE"), tempDir, env.get("CF_ENV"))
	if err != nil {
		return nil, fmt.Sprintf("note: the media sandbox: %v; the rendered-brightness checks do not apply", err)
	}
	return &editor.RenderMeasurer{Store: st, Objects: objects, Runner: runner, TempDir: dir}, ""
}
