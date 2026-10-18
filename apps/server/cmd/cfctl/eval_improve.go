package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/evals"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

const evalImproveUsage = `Usage:
  cfctl eval improve --max-usd N [--iterations N] [--live] [suite flags]

DEVELOPMENT ONLY. The improvement loop (docs/design/observability-v1.md section 8):
  0. estimates the cost (the baseline unless it is cached, and per iteration the improver plus a candidate run, from the recent
     runs' costs) and refuses to start when it is over --max-usd;
  1. runs the suite on the current code: the baseline. A run of the same code (a hash of the editor, renderer, timeline, style,
     scoring and routing code, the suite files and the run options) younger than --baseline-max-age is reused instead;
  2. shows the lowest-scoring cases (failing checks, the judge's rationales, failed human feedback) to the improver (improve_edit,
     Opus 5.5, at most --improver-usd a call) with the files it may change;
  3. the improver proposes ONE change: exact replacements in one file of the allowlist (the editor prompt, a playbook, a check
     threshold, a renderer default, a style rule);
  4. the change is applied and committed on a scratch branch (improve/<suite>-<time>) in a scratch worktree under .eval-worktrees/,
     built, and the suite runs again on that code (go run in the worktree). A change to what the editor is told (prompt, playbook)
     needs --live: stored versions cannot show its effect; without --live it is reported, not evaluated;
  5. the change is kept only when, case by case, the mean gain is at least --min-gain and more than twice its standard error, no case
     drops by more than --max-regression or twice its noise, the held-out mean drops by no more than --held-out-slack or twice its
     noise, and the failure rate does not rise (a failed sample scores 0); otherwise it is dropped from the branch. Live cases run
     --repeat times (default 2), so each has a mean and a spread;
  6. a markdown report (before and after per case, the diff, the trace links) goes to eval-results/improve/.
It never pushes: a kept change is a local branch for you to review and turn into a PR. Every iteration is a span in Phoenix, and every
suite run an experiment. Nothing is spent beyond --max-usd (improver calls, judge calls and live editor runs together).

  --max-usd         the hard total (required, more than 0)
  --iterations      changes to try, each on top of the last kept one; default 1
  --improver-usd    the budget of the improver in one iteration, every attempt included (a refused proposal is sent back with the
                    exact error and asked again, up to 3 proposals); default 0.50
  --min-gain        the least rise of the mean that keeps a change, in points of 100, whatever the noise; default 0.5
  --max-regression  the most one case may drop beyond its noise; default 5
  --held-out-slack  the most the held-out mean may drop beyond its noise; default 1
  --noise-sd        the standard deviation of a live case scored once (with --repeat 1); default 4
  --baseline-max-age  how old a cached baseline of the same code may be; default 24h, 0 always runs it
  --low             how many low cases the improver sees; default 2
  --proposal        a change of your own to try instead of asking the improver: a JSON file {"title", "rationale", "expected_effect",
                    "edits": [{"path", "old", "new"}]} with the same rules (one file of the allowlist, each old text once)
  and the flags of cfctl eval suite (--suite, --local, --live, --no-judge, --judge-profile, --cases, --out, --phoenix, --env-file, --yes)
`

func cmdEvalImprove(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "eval improve"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, evalImproveUsage) }
	var f suiteFlags
	f.register(fs)
	iterations := fs.Int("iterations", 1, "changes to try")
	improverUSD := fs.Float64("improver-usd", 0.5, "budget of one improver call")
	minGain := fs.Float64("min-gain", 0.5, "least rise of the mean")
	maxRegression := fs.Float64("max-regression", 5, "most one case may drop")
	heldOutSlack := fs.Float64("held-out-slack", 1, "most the held-out mean may drop")
	low := fs.Int("low", 2, "low cases shown")
	noiseSD := fs.Float64("noise-sd", evals.DefaultNoiseSD, "sd of a live case scored once")
	maxAge := fs.Duration("baseline-max-age", 24*time.Hour, "how old a cached baseline may be")
	proposalFile := fs.String("proposal", "", "a proposal (JSON) to try instead of asking the improver")
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
	if f.maxUSD <= 0 || *improverUSD <= 0 || *iterations < 1 || *iterations > 10 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --max-usd (more than 0, the hard total), --improver-usd (more than 0) and --iterations (1 to 10) are required\n", name)
		return 2
	}
	se, code := openSuite(name, f, stdout, stderr, lookup, deps)
	if code != 0 {
		return code
	}
	defer se.close()
	if se.deps.Judge == nil {
		_, _ = fmt.Fprintf(se.env.err, "cfctl %s: the loop needs the judge (drop --no-judge)\n", name)
		return 2
	}
	ctx, stop := signalContext()
	defer stop()
	out, errOut := se.env.out, se.env.err
	ws, err := workspaceForImprover(ctx, se)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 2
	}
	judge, _ := se.deps.Judge.(*evals.Judge)
	improver := &evals.Improver{Models: judge.Models, Router: judge.Router}
	outDir := se.opts.OutDir
	maxMicros := int64(f.maxUSD * 1e6)
	baseOpts := se.opts
	baseOpts.Label = "baseline"
	baseOpts.Live = f.live // with --live, the baseline edits again too, so a change to the prompt is compared live against live

	// The cost guard: the baseline (unless a recent run of the same code can stand in for it), then per iteration the improver call and a
	// candidate run.
	history := evals.RecentRuns(outDir, se.suite.Name, 10)
	cachedBase, cached := (*evals.RunResult)(nil), false
	if *maxAge > 0 && se.opts.CodeHash != "" {
		cachedBase, cached = evals.LookupBaseline(outDir, se.suite.Name, se.opts.CodeHash, *maxAge, time.Now())
	}
	run := evals.EstimateRun(se.suite, baseOpts, se.deps.Edit != nil, history)
	var lines []string
	improverMicros := int64(*improverUSD * 1e6)
	if *proposalFile != "" {
		improverMicros = 0 // your own change: no improver call
	}
	total := int64(*iterations) * (improverMicros + run.Micros)
	if cached {
		lines = append(lines, "baseline: a run of the same code from "+cachedBase.RunID+" is reused: $0")
	} else {
		total += run.Micros
		lines = append(lines, fmt.Sprintf("baseline: $%.2f", float64(run.Micros)/1e6))
	}
	lines = append(lines, fmt.Sprintf("per iteration: improver up to $%.2f + candidate run $%.2f, × %d", float64(improverMicros)/1e6, float64(run.Micros)/1e6, *iterations))
	lines = append(lines, run.Lines...)
	if !guardCost(out, errOut, name, total, f.maxUSD, lines) {
		return 2
	}
	if f.live {
		if err := se.room(ctx, total); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: not started: %v\n", name, err)
			return exitStopped
		}
	}

	d := evals.ImproveDeps{
		Repo: evals.GitRepo{Root: se.env.root},
		Root: se.env.root,
		Baseline: func(ctx context.Context) (*evals.RunResult, bool, error) {
			if cached {
				return cachedBase, true, nil
			}
			o := baseOpts
			o.MaxMicros = maxMicros - int64(*iterations)*(improverMicros+run.Micros) // the candidates keep their share
			r, err := evals.Run(ctx, se.suite, se.deps, o)
			if err == nil && r != nil && complete(r) {
				_ = evals.RecordBaseline(outDir, r, time.Now())
			}
			return r, false, err
		},
		Evaluate: func(ctx context.Context, dir, label string, live bool, left int64) (*evals.RunResult, error) {
			return evaluateIn(ctx, se, f, dir, label, live, left, outDir)
		},
		Propose: func(ctx context.Context, in evals.ImproverInput, budget int64, validate func(evals.Proposal) (evals.Proposal, error)) (evals.Proposal, int64, []evals.Attempt, error) {
			if *proposalFile != "" {
				// Your own change: checked once, nobody to ask again.
				p, _, err := readProposal(*proposalFile)
				if err == nil {
					p, err = validate(p)
				}
				a := evals.Attempt{N: 1, Title: p.Title, File: p.File()}
				if err != nil {
					a.Error = err.Error()
					return p, 0, []evals.Attempt{a}, fmt.Errorf("--proposal: %w", err)
				}
				return p, 0, []evals.Attempt{a}, nil
			}
			return improver.Propose(ctx, ws, in, budget, validate, evals.MaxImproverAttempts)
		},
		TraceURL: func(id string) string {
			if se.deps.Phoenix == nil {
				return ""
			}
			return se.deps.Phoenix.TraceURL(ctx, telemetry.PhoenixProject, id)
		},
		Log: func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) },
	}
	res, err := evals.Improve(ctx, d, evals.ImproveOptions{
		Suite: se.suite, Iterations: *iterations, MaxMicros: maxMicros, ImproverMicros: int64(*improverUSD * 1e6), Live: f.live,
		Rule:     evals.Rule{MinGain: *minGain, MaxRegression: *maxRegression, HeldOutSlack: *heldOutSlack, NoiseSD: *noiseSD},
		LowCases: *low,
		WorkDir:  filepath.Join(se.env.root, ".eval-worktrees"), ReportDir: filepath.Join(outDir, "improve"),
	})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		if res == nil || res.Report == "" {
			return 1
		}
	}
	verdict := "nothing kept (the branch was deleted)"
	if res.Kept {
		verdict = "kept on branch " + res.Branch + " (local only: review it, then open a PR)"
	}
	_, _ = fmt.Fprintf(out, "\n%s; spent $%.3f\nreport  %s\n", verdict, float64(res.Spent)/1e6, res.Report)
	return 0
}

// workspaceForImprover is the workspace the improver's calls are metered to: the suite's default (or its first case's).
func workspaceForImprover(ctx context.Context, se *suiteEnv) (ids.WorkspaceID, error) {
	ref := se.suite.Defaults.Workspace
	if ref == "" || strings.Contains(ref, evals.Placeholder) {
		for _, c := range se.suite.Cases {
			if c.Workspace != "" && !strings.Contains(c.Workspace, evals.Placeholder) {
				ref = c.Workspace
				break
			}
		}
	}
	wantWS, wantOrg, ok := parseWorkspaceRef("eval improve", ref, io.Discard)
	if !ok {
		return ids.WorkspaceID{}, fmt.Errorf("the suite names no workspace to meter the improver's calls to (defaults.workspace in the local file)")
	}
	w, code := findWorkspace(ctx, se.st, "eval improve", wantWS, wantOrg, io.Discard)
	if code != 0 {
		return ids.WorkspaceID{}, fmt.Errorf("no workspace %s", ref)
	}
	return w.ID, nil
}

// evaluateIn runs the suite on the code of a scratch worktree: cfctl eval suite through go run there, with the same suite, local file,
// env file and results directory (so the judge's cache is shared), and reads its results file.
func evaluateIn(ctx context.Context, se *suiteEnv, f suiteFlags, dir, label string, live bool, left int64, outDir string) (*evals.RunResult, error) {
	resultsDir := filepath.Join(outDir, se.suite.Name)
	before := map[string]bool{}
	if entries, err := os.ReadDir(resultsDir); err == nil {
		for _, e := range entries {
			before[e.Name()] = true
		}
	}
	args := []string{"eval", "suite", "--suite", se.suite.Path, "--out", outDir, "--label", label,
		"--max-usd", strconv.FormatFloat(float64(max(left, 0))/1e6, 'f', 6, 64), "--repeat", strconv.Itoa(se.opts.Repeat)}
	if _, err := os.Stat(se.env.path); err == nil {
		args = append(args, "--env-file", se.env.path)
	}
	if se.suite.LocalPath != "" {
		args = append(args, "--local", se.suite.LocalPath)
	}
	if live {
		args = append(args, "--live", "--yes")
	}
	if f.judgeProfile != "" {
		args = append(args, "--judge-profile", f.judgeProfile)
	}
	if f.cases != "" {
		args = append(args, "--cases", f.cases)
	}
	if se.deps.Phoenix != nil {
		args = append(args, "--phoenix", se.deps.Phoenix.BaseURL)
	} else {
		args = append(args, "--no-phoenix")
	}
	// Built, then run: go run would turn the suite's exit code (exitStopped) into 1.
	server := filepath.Join(dir, "apps", "server")
	tmp, err := os.MkdirTemp("", "cfctl-candidate-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	bin := filepath.Join(tmp, "cfctl")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/cfctl") // #nosec G204 -- our own command, fixed arguments
	build.Dir = server
	build.Stdout, build.Stderr = se.env.out, se.env.err
	if err := build.Run(); err != nil {
		return nil, fmt.Errorf("the suite on the change: building cfctl: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- our own command, fixed arguments
	cmd.Dir = server
	cmd.Stdout, cmd.Stderr = se.env.out, se.env.err
	runErr := cmd.Run()
	var res *evals.RunResult
	if entries, err := os.ReadDir(resultsDir); err == nil {
		for _, e := range entries {
			if !before[e.Name()] && strings.HasSuffix(e.Name(), ".jsonl") {
				res, _ = evals.LoadResults(filepath.Join(resultsDir, e.Name()))
				break
			}
		}
	}
	var exit *exec.ExitError
	switch {
	case errors.As(runErr, &exit) && exit.ExitCode() == exitStopped, res != nil && res.Aborted != "":
		// The infrastructure stopped the candidate's run (or refused to start it): nothing to decide.
		msg := "the suite on the change was stopped by the infrastructure"
		if res != nil && res.Aborted != "" {
			msg = res.Aborted
		}
		return res, &evals.InfraError{Reason: "candidate", Msg: msg}
	case runErr != nil:
		return res, fmt.Errorf("the suite on the change: %w", runErr)
	case res == nil:
		return nil, errors.New("the suite on the change wrote no results file")
	}
	return res, nil
}

// readProposal reads a proposal written by hand (--proposal): no model call, no cost.
func readProposal(path string) (evals.Proposal, int64, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a file the developer names
	if err != nil {
		return evals.Proposal{}, 0, err
	}
	var p evals.Proposal
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return evals.Proposal{}, 0, fmt.Errorf("--proposal %s: %w", path, err)
	}
	return p, 0, nil
}
