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

	"github.com/nilpntr/content-fabrique/internal/evals"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

const evalImproveUsage = `Usage:
  cfctl eval improve --max-usd N [--iterations N] [--live] [suite flags]

DEVELOPMENT ONLY. The improvement loop (docs/design/observability-v1.md section 8):
  1. runs the suite on the current code: the baseline;
  2. shows the lowest-scoring cases (failing checks, the judge's rationales, failed human feedback) to the improver (improve_edit,
     Opus 5.5, at most --improver-usd a call) with the files it may change;
  3. the improver proposes ONE change: exact replacements in one file of the allowlist (the editor prompt, a playbook, a check
     threshold, a renderer default, a style rule);
  4. the change is applied and committed on a scratch branch (improve/<suite>-<time>) in a scratch worktree under .eval-worktrees/,
     built, and the suite runs again on that code (go run in the worktree). A change to what the editor is told (prompt, playbook)
     needs --live: stored versions cannot show its effect; without --live it is reported, not evaluated;
  5. the change is kept when the mean rises by --min-gain, no case drops by more than --max-regression and the held-out mean drops by
     --held-out-slack at most; otherwise it is dropped from the branch;
  6. a markdown report (before and after per case, the diff, the trace links) goes to eval-results/improve/.
It never pushes: a kept change is a local branch for you to review and turn into a PR. Every iteration is a span in Phoenix, and every
suite run an experiment. Nothing is spent beyond --max-usd (improver calls, judge calls and live editor runs together).

  --max-usd         the hard total (required, more than 0)
  --iterations      changes to try, each on top of the last kept one; default 1
  --improver-usd    the budget of one improver call; default 0.50
  --min-gain        the least rise of the mean that keeps a change, in points of 100; default 0.5
  --max-regression  the most one case may drop; default 5
  --held-out-slack  the most the held-out mean may drop; default 1
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
	d := evals.ImproveDeps{
		Repo: evals.GitRepo{Root: se.env.root},
		Root: se.env.root,
		Baseline: func(ctx context.Context) (*evals.RunResult, error) {
			o := baseOpts
			o.MaxMicros = maxMicros / 2 // the candidates need room too
			return evals.Run(ctx, se.suite, se.deps, o)
		},
		Evaluate: func(ctx context.Context, dir, label string, live bool, left int64) (*evals.RunResult, error) {
			return evaluateIn(ctx, se, f, dir, label, live, left, outDir)
		},
		Propose: func(ctx context.Context, in evals.ImproverInput, budget int64) (evals.Proposal, int64, error) {
			if *proposalFile != "" {
				return readProposal(*proposalFile)
			}
			return improver.Propose(ctx, ws, in, budget)
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
		MinGain: *minGain, MaxRegression: *maxRegression, HeldOutSlack: *heldOutSlack, LowCases: *low,
		WorkDir: filepath.Join(se.env.root, ".eval-worktrees"), ReportDir: filepath.Join(outDir, "improve"),
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
	args := []string{"run", "./cmd/cfctl", "eval", "suite", "--suite", se.suite.Path, "--out", outDir, "--label", label,
		"--max-usd", strconv.FormatFloat(float64(max(left, 0))/1e6, 'f', 6, 64)}
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
	cmd := exec.CommandContext(ctx, "go", args...) // #nosec G204 -- our own command, fixed arguments
	cmd.Dir = filepath.Join(dir, "apps", "server")
	cmd.Stdout, cmd.Stderr = se.env.out, se.env.err
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("the suite on the change: %w", err)
	}
	entries, err := os.ReadDir(resultsDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !before[e.Name()] && strings.HasSuffix(e.Name(), ".jsonl") {
			return evals.LoadResults(filepath.Join(resultsDir, e.Name()))
		}
	}
	return nil, errors.New("the suite on the change wrote no results file")
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
