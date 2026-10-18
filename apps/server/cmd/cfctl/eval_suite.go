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
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/evals"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/renderjobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

const evalSuiteUsage = `Usage:
  cfctl eval suite [--suite <file.yaml>] [--local <file.yaml>] [--live] [--max-usd N] [flags]

DEVELOPMENT ONLY. Runs an eval suite (docs/design/observability-v1.md section 7): every case is scored on a timeline version by the
check_edit checks, the distance from the creator's style, the recorded human feedback (suites/feedback.yaml, measured, it outweighs
the judge) and an AI judge (judge_edit, Sonnet 5.5, at most $0.10 a case) that looks at the render's contact sheet, strips around its
cuts and the creator's reference videos. Results go to eval-results/<suite>/<run>.jsonl and, when Phoenix is up, to Phoenix as an
experiment (the suite is the dataset, one run per case, linked to its trace).

Without --live a case is scored on its stored version (suite: stored), and costs nothing but the judge calls, which are cached by
their inputs: judging the same render again is free. With --live the cases with footage are edited again first (a project is made, the
editor runs in this process with the case's budget; a worker must be running for the previews). Nothing is spent beyond --max-usd:
editor runs and judge calls together. --max-usd 0 (the default) spends nothing: the judge answers only from its cache.

  --suite         the suite file; default suites/back-day.yaml at the repository root
  --local         the local file with the real ids; default <suite>.local.yaml next to it, when it exists
  --live          run the editor on the cases with footage (needs --max-usd)
  --max-usd       the hard total to spend on editor runs and judge calls; default 0
  --no-judge      skip the judge
  --judge-profile the routing profile the judge is resolved from; default v1-start (Sonnet 5.5), so scores stay comparable
  --cases         only these case ids, comma separated
  --out           the results directory; default eval-results at the repository root
  --phoenix       Phoenix's address; default OTEL_EXPORTER_OTLP_ENDPOINT, else http://localhost:6006 (used when it answers)
  --no-phoenix    do not log to Phoenix
  --label         a name for this run in Phoenix
  --json          print the summary as JSON
  --env-file, --yes  as for cfctl agent run (--yes is needed with --live outside plain local development)
`

// suiteFlags are the flags of eval suite, also used by eval improve.
type suiteFlags struct {
	suite, local, cases, out, phoenix, label, judgeProfile, envFile string
	live, noJudge, noPhoenix, asJSON, yes                           bool
	maxUSD                                                          float64
}

func (f *suiteFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.suite, "suite", "", "suite file")
	fs.StringVar(&f.local, "local", "", "local suite file")
	fs.BoolVar(&f.live, "live", false, "run the editor live")
	fs.Float64Var(&f.maxUSD, "max-usd", 0, "hard total budget")
	fs.BoolVar(&f.noJudge, "no-judge", false, "skip the judge")
	fs.StringVar(&f.judgeProfile, "judge-profile", "", "the judge's routing profile")
	fs.StringVar(&f.cases, "cases", "", "case ids")
	fs.StringVar(&f.out, "out", "", "results directory")
	fs.StringVar(&f.phoenix, "phoenix", "", "Phoenix address")
	fs.BoolVar(&f.noPhoenix, "no-phoenix", false, "no Phoenix")
	fs.StringVar(&f.label, "label", "", "run label")
	fs.BoolVar(&f.asJSON, "json", false, "JSON summary")
	fs.StringVar(&f.envFile, "env-file", "", "env file")
	fs.BoolVar(&f.yes, "yes", false, "confirm outside plain local development")
}

func cmdEvalSuite(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "eval suite"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, evalSuiteUsage) }
	var f suiteFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: unexpected argument %q\n", name, fs.Arg(0))
		return 2
	}
	if f.maxUSD < 0 || (f.live && f.maxUSD <= 0) {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --live needs --max-usd, the hard total it may spend (editor runs and judge calls)\n", name)
		return 2
	}
	res, _, code := runSuite(name, f, stdout, stderr, lookup, deps)
	if code != 0 || res == nil {
		return code
	}
	return 0
}

// suiteEnv is an opened suite run: its suite, the database and what Run needs.
type suiteEnv struct {
	env   *agentEnv
	st    *store.Store
	suite *evals.Suite
	deps  evals.Deps
	opts  evals.Options
	close func()
}

// runSuite loads, runs and prints a suite. It returns the result (nil when nothing ran) and an exit code.
func runSuite(name string, f suiteFlags, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) (*evals.RunResult, *evals.Suite, int) {
	se, code := openSuite(name, f, stdout, stderr, lookup, deps)
	if code != 0 {
		return nil, nil, code
	}
	defer se.close()
	ctx, stop := signalContext()
	defer stop()
	out := se.env.out
	mode := "stored versions"
	if f.live {
		mode = "live"
	}
	_, _ = fmt.Fprintf(out, "suite %s (%s): %d cases, %s, judge %s, budget $%.2f\n", se.suite.Name, displayPath(se.env.root, se.suite.Path), len(se.suite.Cases), mode,
		map[bool]string{true: "off", false: "on"}[f.noJudge], f.maxUSD)
	res, err := evals.Run(ctx, se.suite, se.deps, se.opts)
	if err != nil {
		_, _ = fmt.Fprintf(se.env.err, "cfctl %s: %v\n", name, err)
		if res == nil {
			return nil, se.suite, 1
		}
	}
	printSuite(out, res, f.asJSON)
	return res, se.suite, 0
}

func printSuite(out io.Writer, res *evals.RunResult, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"summary": res, "cases": res.Cases})
		return
	}
	_, _ = fmt.Fprintln(out)
	for _, c := range res.Cases {
		score := "  -  "
		if c.Score != nil {
			score = fmt.Sprintf("%5.1f", *c.Score)
		}
		flags := ""
		if c.HeldOut {
			flags += " (held out)"
		}
		_, _ = fmt.Fprintf(out, "%s  %s%s  [%s v%d]\n", score, c.Case, flags, c.Project, c.Version)
		if c.Skipped != "" || c.Error != "" {
			_, _ = fmt.Fprintf(out, "       %s%s\n", c.Skipped, c.Error)
			continue
		}
		_, _ = fmt.Fprintf(out, "       checks %d/%d", c.Checks.Passed, c.Checks.Passed+c.Checks.Failed)
		if len(c.Checks.Failing) > 0 {
			_, _ = fmt.Fprintf(out, " (failing: %s)", strings.Join(c.Checks.Failing, ", "))
		}
		if c.StyleScore != nil {
			_, _ = fmt.Fprintf(out, "  style %.2f", *c.StyleScore)
		}
		if c.MusicSync != nil {
			_, _ = fmt.Fprintf(out, "  music_sync %d/5", c.MusicSync.Score)
		}
		_, _ = fmt.Fprintln(out)
		for _, fb := range c.Feedback {
			if fb.Status == evals.Fail {
				_, _ = fmt.Fprintf(out, "       feedback FAIL %s: %s\n", fb.ID, fb.Detail)
			}
		}
		if c.Judge != nil {
			var dims []string
			for _, d := range evals.Dimensions {
				if s, ok := c.Judge.Scores[d]; ok {
					dims = append(dims, fmt.Sprintf("%s %d", d, s.Score))
				}
			}
			cached := ""
			if c.Judge.Cached {
				cached = ", cached"
			}
			_, _ = fmt.Fprintf(out, "       judge (%s%s): %s\n", c.Judge.Model, cached, strings.Join(dims, ", "))
		} else if c.JudgeNote != "" {
			_, _ = fmt.Fprintf(out, "       %s\n", c.JudgeNote)
		}
		if c.Calibration != nil {
			_, _ = fmt.Fprintf(out, "       calibration: agrees %v %s\n", c.Calibration.Agrees, strings.Join(c.Calibration.Off, "; "))
		}
	}
	mean := "-"
	if res.Mean != nil {
		mean = fmt.Sprintf("%.1f", *res.Mean)
	}
	held := ""
	if res.HeldOut != nil {
		held = fmt.Sprintf(", held out %.1f", *res.HeldOut)
	}
	_, _ = fmt.Fprintf(out, "\nmean %s%s, spent $%.3f\n", mean, held, float64(res.SpentMicro)/1e6)
	if res.JSONL != "" {
		_, _ = fmt.Fprintf(out, "results  %s\n", res.JSONL)
	}
	if res.Experiment != "" {
		_, _ = fmt.Fprintf(out, "phoenix  %s\n", res.Experiment)
	}
}

// openSuite loads the suite and builds what its run needs.
func openSuite(name string, f suiteFlags, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) (*suiteEnv, int) {
	env, code := guardDevCommand(name, f.envFile, f.live, f.yes, stdout, stderr, lookup)
	if code != 0 {
		return nil, code
	}
	suitePath := cmp.Or(f.suite, filepath.Join(env.root, "suites", "back-day.yaml"))
	if !filepath.IsAbs(suitePath) {
		if abs, err := filepath.Abs(suitePath); err == nil {
			suitePath = abs
		}
	}
	suite, err := evals.LoadSuite(suitePath, f.local)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return nil, 2
	}
	outDir := cmp.Or(f.out, filepath.Join(env.root, "eval-results"))
	ctx := context.Background()
	st, code := env.openDB(ctx, name, deps)
	if code != 0 {
		return nil, code
	}
	closers := []func(){st.Close}
	se := &suiteEnv{env: env, st: st, suite: suite, close: func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}}

	// Phoenix: the experiment, and the spans of the cases and of the judge's calls.
	phoenixURL := cmp.Or(f.phoenix, env.get("OTEL_EXPORTER_OTLP_ENDPOINT"), defaultPhoenix)
	var phx *evals.Phoenix
	if !f.noPhoenix {
		if p := (&evals.Phoenix{BaseURL: phoenixURL}); p.Reachable(ctx) {
			phx = p
		} else {
			_, _ = fmt.Fprintf(env.out, "phoenix: %s does not answer; the results go to the JSONL file only (task obs:up starts it)\n", phoenixURL)
		}
	}
	if env.get("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		closers = append(closers, setupCfctlTracing(env))
	} else if phx != nil {
		closers = append(closers, setupCfctlTracingTo(env, phoenixURL))
	}

	// The runtime: the model access (judge, editor), and for the pictures and live runs the storage and the media sandbox.
	opts := agentset.Options{Store: st, Getenv: env.get, Inline: true, HTTPClient: deps.httpClient}
	var objects *storage.Client
	var runner media.Runner
	var tempDir string
	if cfg := storageConfigOf(env); cfg.Configured() {
		if objects, err = storage.New(cfg); err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: object storage: %v\n", name, err)
			se.close()
			return nil, 1
		}
		opts.Objects = objects
		if opts.Preview, err = renderjobs.NewPreviewRendererFor(st, objects, nil); err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: preview renderer: %v\n", name, err)
			se.close()
			return nil, 1
		}
		provider := cmp.Or(env.get("MEDIA_SANDBOX_PROVIDER"), config.SandboxDocker)
		root := cmp.Or(env.get("MEDIA_TEMP_DIR"), filepath.Join(os.TempDir(), "cf-media"))
		if runner, tempDir, err = agentset.NewFrameRunner(provider, cmp.Or(env.get("MEDIA_TOOLS_IMAGE"), config.DefaultMediaToolsImage), root, env.get("CF_ENV")); err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: the media sandbox: %v\n", name, err)
			se.close()
			return nil, 2
		}
		opts.Frames, opts.TempDir = runner, tempDir
	} else if f.live {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: --live needs object storage (R2_*) and the media sandbox, as cfctl agent run does\n", name)
		se.close()
		return nil, 2
	}
	rt, err := agentset.Open(opts)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		se.close()
		return nil, 2
	}
	svc, err := projects.NewService(st, nil)
	if err != nil {
		se.close()
		return nil, 1
	}

	var only []string
	for _, id := range strings.Split(f.cases, ",") {
		if id = strings.TrimSpace(id); id != "" {
			only = append(only, id)
		}
	}
	se.opts = evals.Options{Live: f.live, MaxMicros: int64(f.maxUSD * 1e6), NoJudge: f.noJudge, Only: only, OutDir: outDir, Label: f.label}
	pics := &evals.Pictures{Renders: st, Objects: objects, Runner: runner, TempDir: tempDir}
	var sampler editor.RenderSampler
	if objects != nil {
		sampler = &editor.RenderMeasurer{Store: st, Objects: objects, Runner: runner, TempDir: tempDir}
	}
	workspaces := map[string]store.Workspace{}
	workspace := func(ctx context.Context, ref string) (store.Workspace, error) {
		if w, ok := workspaces[ref]; ok {
			return w, nil
		}
		wantWS, wantOrg, ok := parseWorkspaceRef(name, ref, io.Discard)
		if !ok {
			return store.Workspace{}, fmt.Errorf("the workspace %q is not a wks_... or org_... id", ref)
		}
		w, code := findWorkspace(ctx, st, name, wantWS, wantOrg, io.Discard)
		if code != 0 {
			return store.Workspace{}, fmt.Errorf("no workspace %s", ref)
		}
		workspaces[ref] = w
		return w, nil
	}
	se.deps = evals.Deps{
		Log: env.out,
		Stored: func(ctx context.Context, c evals.Case) (evals.Target, error) {
			w, err := workspace(ctx, c.Workspace)
			if err != nil {
				return evals.Target{}, err
			}
			pid, err := ids.ParseProjectID(c.Stored.Project)
			if err != nil {
				return evals.Target{}, fmt.Errorf("stored.project: %w", err)
			}
			if _, perr := svc.Version(ctx, projects.Actor{Workspace: w.ID}, pid, c.Stored.Version); perr != nil {
				return evals.Target{}, fmt.Errorf("version %d of %s: %s", c.Stored.Version, pid, perr.Message)
			}
			return evals.Target{Workspace: w.ID, Project: pid, Version: c.Stored.Version}, nil
		},
		Measure: func(ctx context.Context, t evals.Target) (evals.Measurement, error) {
			return measureVersion(ctx, st, svc, sampler, t)
		},
		Visuals: func(ctx context.Context, t evals.Target, m evals.Measurement) (*evals.Picture, []evals.Picture, error) {
			if objects == nil {
				return nil, nil, errors.New("no object storage (R2_*) to read the render from")
			}
			return pics.Render(ctx, t, m.Spec)
		},
		References: func(ctx context.Context, c evals.Case, m evals.Measurement) ([]evals.Picture, error) {
			if c.ReferencesDir == "" || strings.Contains(c.ReferencesDir, evals.Placeholder) {
				return nil, nil
			}
			return pics.References(ctx, c.ReferencesDir, m.StyleNames)
		},
		Phoenix: phx,
	}
	if !f.noJudge {
		se.deps.Judge = &evals.Judge{Models: rt.Factory, Router: rt.Router, CacheDir: filepath.Join(outDir, ".judge-cache"), Profile: f.judgeProfile}
	}
	if f.live {
		se.deps.Edit = func(ctx context.Context, c evals.Case, budget int64) (evals.Target, error) {
			return editLive(ctx, st, svc, rt, workspace, suite.Name, c, budget, name)
		}
	}
	return se, 0
}

// measureVersion runs the checks of a version and reads what the judge is told about it.
func measureVersion(ctx context.Context, st *store.Store, svc *projects.Service, sampler editor.RenderSampler, t evals.Target) (evals.Measurement, error) {
	actor := projects.Actor{Workspace: t.Workspace}
	vd, perr := svc.Version(ctx, actor, t.Project, t.Version)
	if perr != nil {
		return evals.Measurement{}, fmt.Errorf("%s: %s", perr.Code, perr.Message)
	}
	spec, err := timeline.Parse(vd.Version.Spec)
	if err != nil {
		return evals.Measurement{}, err
	}
	plan, _ := planOfVersion(ctx, st, t.Workspace, vd.Version)
	style, err := styleprofile.LoadStyle(ctx, st, t.Workspace)
	if err != nil {
		return evals.Measurement{}, fmt.Errorf("the style profile: %w", err)
	}
	report, err := editor.CheckVersion(ctx, svc, st, t.Workspace, t.Project, t.Version, plan, sampler, style)
	if err != nil {
		return evals.Measurement{}, err
	}
	assets, err := editor.LoadAssetAnalysis(ctx, st, t.Workspace, spec)
	if err != nil {
		return evals.Measurement{}, err
	}
	m := evals.Measurement{Report: report, Spec: spec, Plan: plan, Assets: assets}
	if det, perr := svc.Get(ctx, actor, t.Project); perr == nil {
		m.Brief = det.Project.Brief
	}
	if style != nil && style.Profile != nil {
		m.StyleEvidence = style.Profile.CompactBlock()
		for _, line := range style.Profile.Evidence {
			if n, _, ok := strings.Cut(line, ":"); ok {
				m.StyleNames = append(m.StyleNames, strings.TrimSpace(n))
			}
		}
	}
	return m, nil
}

// editLive makes a project from a case and runs the editor on it in this process (no credits, the given budget), and returns the
// version the run made.
func editLive(ctx context.Context, st *store.Store, svc *projects.Service, rt *agentset.Runtime, workspace func(context.Context, string) (store.Workspace, error),
	suite string, c evals.Case, budget int64, name string) (evals.Target, error) {
	w, err := workspace(ctx, c.Workspace)
	if err != nil {
		return evals.Target{}, err
	}
	who, _, code := workspaceOwner(ctx, st, w, name, io.Discard)
	if code != 0 {
		return evals.Target{}, errors.New("the workspace has no owner to make the project as")
	}
	var pid ids.ProjectID
	opts := editor.EditOptions{Vibe: c.Vibe}
	if c.NewIdea {
		// "Try another idea" on the stored project: a new version in a format none of its versions used.
		if c.Stored == nil {
			return evals.Target{}, errors.New("a new-idea case needs stored: the project to make a new idea of")
		}
		if pid, err = ids.ParseProjectID(c.Stored.Project); err != nil {
			return evals.Target{}, fmt.Errorf("stored.project: %w", err)
		}
		if opts.AvoidFormats, err = editor.ProjectFormats(ctx, st, w.ID, pid); err != nil {
			return evals.Target{}, fmt.Errorf("the formats of the project: %w", err)
		}
		opts.NewIdea = true
	} else {
		assetIDs := make([]api.AssetId, 0, len(c.Footage)+1)
		for _, a := range append(append([]string{}, c.Footage...), c.Song) {
			if a != "" {
				assetIDs = append(assetIDs, api.AssetId(a))
			}
		}
		title := fmt.Sprintf("eval %s/%s", suite, c.ID)
		brief := c.Brief
		det, perr := svc.Create(ctx, who, api.CreateProjectRequest{AssetIds: assetIDs, Brief: &brief, Format: api.ProjectFormat(store.Format9x16), Title: &title})
		if perr != nil {
			return evals.Target{}, fmt.Errorf("make the project: %s: %s", perr.Code, perr.Message)
		}
		pid = det.Project.ID
	}
	input, err := editor.NewEditInputWith(opts)
	if err != nil {
		return evals.Target{}, err
	}
	run, err := rt.Service.Start(ctx, runtime.StartParams{Agent: editor.Name, WorkspaceID: w.ID, ProjectID: pid.UUID, Input: input,
		Profile: c.Profile, Budgets: runtime.Budgets{Micros: budget}, NoCredits: true})
	if err != nil {
		return evals.Target{}, fmt.Errorf("start the editor: %w", err)
	}
	t := evals.Target{Workspace: w.ID, Project: pid, RunID: run.ID}
	for {
		outcome, err := rt.Runner.Execute(ctx, w.ID, run.ID)
		if err != nil {
			return t, fmt.Errorf("the editor run: %w", err)
		}
		if outcome != runtime.OutcomeWaiting {
			break
		}
		// An eval has nobody to answer the editor's question: the run is cancelled.
		_, _ = rt.Service.Cancel(ctx, w.ID, run.ID)
	}
	final, err := st.GetAgentRun(ctx, w.ID, run.ID)
	if err != nil {
		return t, err
	}
	t.EditMicros = final.CostMicros
	v, err := st.GetTimelineVersionByAuthor(ctx, w.ID, pid, "agent", ids.Encode(ids.PrefixRun, run.ID))
	if err != nil {
		return t, fmt.Errorf("the editor run %s (%s) made no version", ids.Encode(ids.PrefixRun, run.ID), final.Status)
	}
	t.Version = int(v.Version)
	return t, nil
}
