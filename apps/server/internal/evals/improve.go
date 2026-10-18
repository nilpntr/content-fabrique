package evals

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

// The improvement loop (docs/design/observability-v1.md section 8): run the suite as the baseline; show the lowest-scoring cases to the
// improver; apply its one change on a scratch branch in a scratch worktree; run the suite again with that code; keep the change only
// when the mean improves, no case regresses by more than the limit and the held-out cases do not regress; write a report. It never
// pushes: the output is a branch and a report for the director.

// Repo is the git side of the loop. GitRepo is the real one.
type Repo interface {
	// AddWorktree makes a worktree at dir on a new branch from the current HEAD.
	AddWorktree(ctx context.Context, dir, branch string) error
	// Commit commits everything in the worktree and returns the commit's diff.
	Commit(ctx context.Context, dir, message string) (diff string, err error)
	// DropLast removes the worktree's last commit (a rejected change).
	DropLast(ctx context.Context, dir string) error
	// RemoveWorktree removes the worktree (the branch stays); DeleteBranch deletes a branch.
	RemoveWorktree(ctx context.Context, dir string) error
	DeleteBranch(ctx context.Context, branch string) error
	// Build checks that the code in dir still builds.
	Build(ctx context.Context, dir string) error
}

// ImproveDeps are what the loop needs.
type ImproveDeps struct {
	Repo Repo
	// Root is the repository root of the current checkout (the baseline's code).
	Root string
	// Baseline runs the suite on the current code, or returns a recent run of the same code (baseline.go); cached says it did.
	Baseline func(ctx context.Context) (res *RunResult, cached bool, err error)
	// Evaluate runs the suite on the code in the scratch worktree dir, with a label and a spending limit.
	Evaluate func(ctx context.Context, dir, label string, live bool, maxMicros int64) (*RunResult, error)
	// Propose asks the improver for a change that passes validate (asking again after a refusal); it returns every attempt.
	Propose func(ctx context.Context, in ImproverInput, budget int64, validate func(Proposal) (Proposal, error)) (Proposal, int64, []Attempt, error)
	// TraceURL makes a case's trace link ("" when there is none).
	TraceURL func(traceID string) string
	Log      func(format string, a ...any)
}

// ImproveOptions are the loop's limits.
type ImproveOptions struct {
	Suite *Suite
	// Iterations is how many changes to try (each on top of the last accepted one).
	Iterations int
	// MaxMicros is the hard total of the loop: the improver calls and every suite run's spending.
	MaxMicros int64
	// ImproverMicros is the budget of one improver call.
	ImproverMicros int64
	// Live lets a change that affects the model be judged with live editor runs (inside MaxMicros); without it such a change is
	// reported as not evaluated.
	Live bool
	// Rule is the acceptance rule (stats.go): a noise-aware gain, no case regression beyond the noise, the held-out cases and the failure
	// rate holding.
	Rule Rule
	// LowCases is how many of the lowest-scoring cases the improver is shown.
	LowCases int
	// WorkDir holds the scratch worktree; ReportDir the report.
	WorkDir, ReportDir string
	Now                func() time.Time
}

// Iteration is one tried change.
type Iteration struct {
	N        int
	Proposal Proposal
	Invalid  string // why the proposal was refused before it was tried
	NotRun   string // why it was not evaluated
	Before   *RunResult
	After    *RunResult
	Accepted bool
	Reasons  []string
	// Attempts are the improver's proposals in this iteration, with why each refused one was refused.
	Attempts   []Attempt
	Decision   *Decision
	Diff       string
	CostMicros int64
}

// ImproveResult is the loop's outcome.
type ImproveResult struct {
	// BaselineCached says the baseline was a recent run of the same code, reused.
	BaselineCached bool
	Branch         string
	Kept           bool
	Iterations     []Iteration
	Report         string
	Spent          int64
}

// lowest are the n lowest-scoring cases that the improver may see (scored, not held out, not calibration).
func lowest(r *RunResult, n int) []CaseResult {
	var out []CaseResult
	for _, c := range r.Cases {
		if c.Score != nil && !c.HeldOut && c.Calibration == nil {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return *out[i].Score < *out[j].Score })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Improve runs the loop.
func Improve(ctx context.Context, d ImproveDeps, o ImproveOptions) (*ImproveResult, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Iterations < 1 {
		o.Iterations = 1
	}
	if o.LowCases < 1 {
		o.LowCases = 2
	}
	logf := func(format string, a ...any) {
		if d.Log != nil {
			d.Log(format, a...)
		}
	}
	stamp := o.Now().UTC().Format("20060102-150405")
	res := &ImproveResult{Branch: "improve/" + o.Suite.Name + "-" + stamp}
	ctx = telemetry.WithTraceID(ctx, telemetry.DerivedTraceID("improve/"+res.Branch))
	ctx, span := tracer().Start(ctx, "eval.improve "+o.Suite.Name, trace.WithNewRoot(), trace.WithAttributes(
		telemetry.Kind(telemetry.KindChain), attribute.String("cf.suite", o.Suite.Name), attribute.String("cf.branch", res.Branch)))
	defer span.End()

	before, cached, err := d.Baseline(ctx)
	if err != nil {
		span.SetStatus(codes.Error, "baseline")
		return res, fmt.Errorf("baseline: %w", err)
	}
	if cached {
		logf("baseline: reusing %s (the same code, %s old): no new runs", cmpOr(before.JSONL, before.RunID), o.Now().Sub(baselineTime(before)).Round(time.Minute))
	} else {
		res.Spent += before.SpentMicro
	}
	res.BaselineCached = cached
	if before.Mean == nil {
		return res, fmt.Errorf("the baseline scored no case; fill the local suite file (or judge first) before improving")
	}
	dir := filepath.Join(o.WorkDir, strings.ReplaceAll(res.Branch, "/", "-"))
	if err := d.Repo.AddWorktree(ctx, dir, res.Branch); err != nil {
		return res, fmt.Errorf("the scratch worktree: %w", err)
	}
	defer func() {
		if err := d.Repo.RemoveWorktree(context.WithoutCancel(ctx), dir); err != nil {
			logf("could not remove the scratch worktree %s: %v", dir, err)
		}
		if !res.Kept {
			if err := d.Repo.DeleteBranch(context.WithoutCancel(ctx), res.Branch); err != nil {
				logf("could not delete the branch %s: %v", res.Branch, err)
			}
		}
	}()
	files, err := ReadAllowed(dir)
	if err != nil {
		return res, err
	}
	var tried []string
	for n := 1; n <= o.Iterations; n++ {
		it := Iteration{N: n, Before: before}
		left := o.MaxMicros - res.Spent
		budget := min(o.ImproverMicros, left)
		if budget <= 0 {
			it.NotRun = "the --max-usd budget is used up"
			res.Iterations = append(res.Iterations, it)
			break
		}
		low := lowest(before, o.LowCases)
		logf("iteration %d: asking the improver about %d low cases", n, len(low))
		validate := func(p Proposal) (Proposal, error) {
			if err := CheckProposalText(p); err != nil {
				return p, err
			}
			return ValidateProposal(p, dir)
		}
		p, cost, attempts, err := d.Propose(ctx, ImproverInput{Suite: o.Suite.Name, Cases: low, Files: files, Tried: tried, Feedback: o.Suite.Feedback}, budget, validate)
		it.CostMicros += cost
		res.Spent += cost
		it.Attempts = attempts
		for _, a := range attempts {
			if a.Error != "" {
				logf("iteration %d: proposal %d refused: %s", n, a.N, a.Error)
			}
		}
		if errors.Is(err, ErrNoChange) {
			it.NotRun = err.Error()
			res.Iterations = append(res.Iterations, it)
			traceIteration(ctx, it)
			continue
		}
		if err != nil {
			it.Invalid = err.Error()
			res.Iterations = append(res.Iterations, it)
			traceIteration(ctx, it)
			continue
		}
		it.Proposal = p
		logf("iteration %d: %s (%s, %s)", n, p.Title, p.Kind, p.File())
		if p.AffectsModel && !o.Live {
			it.NotRun = "this change is to what the editor is told: only live editor runs can show its effect. Run again with --live and a --max-usd that covers them"
			it.Diff = editsDiff(p)
			tried = append(tried, p.Title+" (not evaluated: needs --live)")
			res.Iterations = append(res.Iterations, it)
			traceIteration(ctx, it)
			continue
		}
		if err := ApplyProposal(p, dir); err != nil {
			it.Invalid = err.Error()
			res.Iterations = append(res.Iterations, it)
			continue
		}
		if err := d.Repo.Build(ctx, dir); err != nil {
			it.Invalid = "the change does not build: " + err.Error()
			_ = d.Repo.DropLast(ctx, dir) // nothing committed yet: restore the file
			res.Iterations = append(res.Iterations, it)
			tried = append(tried, p.Title+" (did not build)")
			traceIteration(ctx, it)
			continue
		}
		msg := fmt.Sprintf("Eval improve (%s): %s\n\n%s\n\nExpected: %s\n\nCo-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>", p.Kind, p.Title, p.Rationale, p.ExpectedEffect)
		if it.Diff, err = d.Repo.Commit(ctx, dir, msg); err != nil {
			return res, fmt.Errorf("commit the change: %w", err)
		}
		after, err := d.Evaluate(ctx, dir, fmt.Sprintf("candidate %d: %s", n, p.Title), o.Live, o.MaxMicros-res.Spent) // live when the baseline was: like against like
		if err != nil {
			it.NotRun = "the suite did not run on the change: " + err.Error()
			_ = d.Repo.DropLast(ctx, dir)
			res.Iterations = append(res.Iterations, it)
			traceIteration(ctx, it)
			continue
		}
		it.After = after
		it.CostMicros += after.SpentMicro
		res.Spent += after.SpentMicro
		dec := Accept(before, after, o.Rule)
		it.Decision, it.Accepted, it.Reasons = &dec, dec.Accepted, dec.Reasons
		for _, r := range dec.Reasons {
			logf("  %s", r)
		}
		if it.Accepted {
			res.Kept = true
			before = after
			logf("iteration %d: kept", n)
		} else {
			_ = d.Repo.DropLast(ctx, dir)
			tried = append(tried, p.Title+" (rejected: "+strings.Join(it.Reasons, "; ")+")")
			logf("iteration %d: rejected", n)
		}
		res.Iterations = append(res.Iterations, it)
		traceIteration(ctx, it)
	}
	span.SetAttributes(attribute.Bool("cf.kept", res.Kept), attribute.Int64("cf.spent_micros", res.Spent), attribute.Int("cf.iterations", len(res.Iterations)))
	report, err := writeReport(res, o, d)
	if err != nil {
		return res, err
	}
	res.Report = report
	return res, nil
}

// editsDiff shows a proposal's replacements when there is no commit to diff.
func editsDiff(p Proposal) string {
	var b strings.Builder
	for _, e := range p.Edits {
		fmt.Fprintf(&b, "--- %s\n", e.Path)
		for _, l := range strings.Split(e.Old, "\n") {
			b.WriteString("-" + l + "\n")
		}
		for _, l := range strings.Split(e.New, "\n") {
			b.WriteString("+" + l + "\n")
		}
	}
	return b.String()
}

// traceIteration records an iteration as a span of the loop's trace.
func traceIteration(ctx context.Context, it Iteration) {
	_, s := tracer().Start(ctx, fmt.Sprintf("eval.improve.iteration %d", it.N), trace.WithAttributes(
		telemetry.Kind(telemetry.KindChain), attribute.String("cf.change.kind", it.Proposal.Kind), attribute.String("cf.change.file", it.Proposal.File()),
		attribute.String("cf.change.title", it.Proposal.Title), attribute.Bool("cf.accepted", it.Accepted), attribute.Int64("cf.cost_micros", it.CostMicros),
		attribute.String("cf.invalid", it.Invalid), attribute.String("cf.not_run", it.NotRun), attribute.StringSlice("cf.reasons", it.Reasons),
		attribute.Int("cf.attempts", len(it.Attempts))))
	for _, a := range it.Attempts {
		s.AddEvent("proposal", trace.WithAttributes(attribute.Int("cf.attempt", a.N), attribute.String("cf.change.title", a.Title),
			attribute.String("cf.change.file", a.File), attribute.String("cf.refused", a.Error)))
	}
	if it.Before != nil && it.Before.Mean != nil {
		s.SetAttributes(attribute.Float64("cf.before.mean", *it.Before.Mean))
	}
	if it.After != nil && it.After.Mean != nil {
		s.SetAttributes(attribute.Float64("cf.after.mean", *it.After.Mean))
	}
	if it.Diff != "" {
		s.SetAttributes(telemetry.Text(telemetry.OutputValue, it.Diff)...)
	}
	s.End()
}

// writeReport writes the loop's report (markdown) and returns its path.
func writeReport(res *ImproveResult, o ImproveOptions, d ImproveDeps) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Eval improve: %s\n\n", o.Suite.Name)
	fmt.Fprintf(&b, "- Branch: `%s` (%s)\n", res.Branch, map[bool]string{true: "kept: review it and open a PR; it was never pushed", false: "nothing was kept; the branch was deleted"}[res.Kept])
	fmt.Fprintf(&b, "- Spent: $%.3f of $%.2f\n", float64(res.Spent)/1e6, float64(o.MaxMicros)/1e6)
	fmt.Fprintf(&b, "- Rule (paired by case, noise-aware, docs/design/observability-v1.md section 9): the mean gain is at least %.1f and more than twice its standard error; no case drops by more than max(%.1f, twice its noise); the held-out mean drops by at most max(%.1f, twice its noise); the failure rate does not rise. A live case scored once is given a noise sd of %.1f\n", o.Rule.MinGain, o.Rule.MaxRegression, o.Rule.HeldOutSlack, cmpF(o.Rule.NoiseSD, DefaultNoiseSD))
	if res.BaselineCached {
		b.WriteString("- The baseline is a recent run of the same code, reused (no new runs)\n")
	}
	b.WriteString("\n")
	for _, it := range res.Iterations {
		fmt.Fprintf(&b, "## Iteration %d: %s\n\n", it.N, cmpOr(it.Proposal.Title, "(no proposal)"))
		for _, a := range it.Attempts {
			if a.Error != "" {
				fmt.Fprintf(&b, "- Proposal %d (%q, `%s`) refused: %s\n", a.N, a.Title, a.File, a.Error)
			}
		}
		if len(it.Attempts) > 0 {
			b.WriteString("\n")
		}
		switch {
		case it.Invalid != "":
			fmt.Fprintf(&b, "**Refused before it was tried:** %s\n\n", it.Invalid)
		case it.NotRun != "":
			fmt.Fprintf(&b, "**Not evaluated:** %s\n\n", it.NotRun)
		case it.Accepted:
			b.WriteString("**Kept.**\n\n")
		default:
			b.WriteString("**Rejected.**\n\n")
		}
		if it.Proposal.Kind != "" {
			fmt.Fprintf(&b, "- Kind: %s, file `%s`, affects the model: %v\n- Rationale: %s\n- Expected: %s\n- Cost: $%.3f\n\n", it.Proposal.Kind, it.Proposal.File(),
				it.Proposal.AffectsModel, it.Proposal.Rationale, it.Proposal.ExpectedEffect, float64(it.CostMicros)/1e6)
		}
		if it.Decision != nil {
			fmt.Fprintf(&b, "- Mean gain %+.2f ± %.2f over %d cases\n", it.Decision.Gain, it.Decision.GainSE, it.Decision.Cases)
		}
		for _, r := range it.Reasons {
			fmt.Fprintf(&b, "- %s\n", r)
		}
		if it.After != nil {
			b.WriteString("\n| case | before | after | change | trace before | trace after |\n|---|---|---|---|---|---|\n")
			for _, bc := range it.Before.Cases {
				ac, _ := it.After.ByCase(bc.Case)
				delta := "-"
				if bc.Score != nil && ac.Score != nil {
					delta = fmt.Sprintf("%+.1f", *ac.Score-*bc.Score)
				}
				name := bc.Case
				if bc.HeldOut {
					name += " (held out)"
				}
				if bc.Calibration != nil {
					name += " (calibration)"
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", name, ScoreText(bc), ScoreText(ac), delta, link(d, bc.TraceID), link(d, ac.TraceID))
			}
			fmt.Fprintf(&b, "| **mean** | %s | %s | | | |\n| held out | %s | %s | | | |\n| failure rate | %.0f %% (%d/%d) | %.0f %% (%d/%d) | | | |\n\n",
				MeanText(it.Before.Mean, it.Before.MeanSE), MeanText(it.After.Mean, it.After.MeanSE), MeanText(it.Before.HeldOut, it.Before.HeldOutSE),
				MeanText(it.After.HeldOut, it.After.HeldOutSE), 100*it.Before.FailureRate, it.Before.Failures, it.Before.Samples,
				100*it.After.FailureRate, it.After.Failures, it.After.Samples)
			for _, r := range []*RunResult{it.Before, it.After} {
				for _, f := range failureList(r) {
					fmt.Fprintf(&b, "- Failed sample (%s, scored 0): %s\n", cmpOr(r.Label, r.RunID), f)
				}
			}
			for _, r := range []*RunResult{it.Before, it.After} {
				if r.Experiment != "" {
					fmt.Fprintf(&b, "- Phoenix experiment (%s): %s\n", cmpOr(r.Label, r.RunID), r.Experiment)
				}
				if r.JSONL != "" {
					fmt.Fprintf(&b, "- Results (%s): `%s`\n", cmpOr(r.Label, r.RunID), r.JSONL)
				}
			}
			b.WriteString("\n")
		}
		if it.Diff != "" {
			fmt.Fprintf(&b, "```diff\n%s\n```\n\n", strings.TrimRight(it.Diff, "\n"))
		}
	}
	if err := os.MkdirAll(o.ReportDir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(o.ReportDir, strings.ReplaceAll(res.Branch, "/", "-")+".md")
	return path, os.WriteFile(path, []byte(b.String()), 0o600)
}

func link(d ImproveDeps, traceID string) string {
	if d.TraceURL == nil || traceID == "" {
		return "-"
	}
	if u := d.TraceURL(traceID); u != "" {
		return "[trace](" + u + ")"
	}
	return "-"
}

// GitRepo is Repo over the git command, in the repository at Root.
type GitRepo struct{ Root string }

func (g GitRepo) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed git subcommands
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// AddWorktree implements Repo.
func (g GitRepo) AddWorktree(ctx context.Context, dir, branch string) error {
	_, err := g.git(ctx, g.Root, "worktree", "add", "-b", branch, dir, "HEAD")
	return err
}

// Commit implements Repo.
func (g GitRepo) Commit(ctx context.Context, dir, message string) (string, error) {
	if _, err := g.git(ctx, dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := g.git(ctx, dir, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	return g.git(ctx, dir, "show", "--format=", "HEAD")
}

// DropLast implements Repo: the last commit goes, and any uncommitted change with it (only the scratch branch is touched).
func (g GitRepo) DropLast(ctx context.Context, dir string) error {
	out, _ := g.git(ctx, dir, "status", "--porcelain")
	if strings.TrimSpace(out) != "" {
		_, err := g.git(ctx, dir, "checkout", "--", ".")
		return err
	}
	_, err := g.git(ctx, dir, "reset", "-q", "--hard", "HEAD~1")
	return err
}

// RemoveWorktree implements Repo.
func (g GitRepo) RemoveWorktree(ctx context.Context, dir string) error {
	_, err := g.git(ctx, g.Root, "worktree", "remove", "--force", dir)
	return err
}

// DeleteBranch implements Repo.
func (g GitRepo) DeleteBranch(ctx context.Context, branch string) error {
	_, err := g.git(ctx, g.Root, "branch", "-q", "-D", branch)
	return err
}

// Build implements Repo: go build and go vet of the server module.
func (g GitRepo) Build(ctx context.Context, dir string) error {
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./internal/agents/editor/", "./internal/render/", "./internal/styleprofile/"}} {
		cmd := exec.CommandContext(ctx, "go", args...) // #nosec G204 -- fixed go subcommands
		cmd.Dir = filepath.Join(dir, "apps", "server")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go %s: %w: %s", args[0], err, telemetry.Cap(strings.TrimSpace(string(out)), 2000))
		}
	}
	return nil
}

func cmpF(a, b float64) float64 {
	if a > 0 {
		return a
	}
	return b
}
