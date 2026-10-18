package evals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
)

func sc(v float64) *float64 { return &v }

func result(mean, held float64, cases map[string]float64) *RunResult {
	r := &RunResult{Mean: sc(mean), HeldOut: sc(held)}
	for id, s := range cases {
		r.Cases = append(r.Cases, CaseResult{Case: id, Score: sc(s), HeldOut: strings.HasPrefix(id, "held")})
	}
	return r
}

func TestAcceptRule(t *testing.T) {
	before := result(70, 80, map[string]float64{"a": 60, "b": 80, "held": 80})
	for _, c := range []struct {
		name  string
		after *RunResult
		want  bool
		why   string
	}{
		{"better everywhere", result(75, 81, map[string]float64{"a": 66, "b": 84, "held": 81}), true, "rose"},
		{"too small a gain", result(70.2, 80, map[string]float64{"a": 60.4, "b": 80, "held": 80}), false, "less than"},
		{"one case regresses", result(76, 80, map[string]float64{"a": 80, "b": 72, "held": 80}), false, "case b dropped"},
		{"held out regresses", result(76, 70, map[string]float64{"a": 66, "b": 86, "held": 70}), false, "held-out mean dropped"},
	} {
		ok, reasons := Accept(before, c.after, 0.5, 5, 1)
		if ok != c.want || !strings.Contains(strings.Join(reasons, "; "), c.why) {
			t.Errorf("%s: %v %v", c.name, ok, reasons)
		}
	}
	if l := lowest(before, 1); len(l) != 1 || l[0].Case != "a" {
		t.Errorf("lowest %v", l)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAllowlistAndExcerpts(t *testing.T) {
	files, err := ReadAllowed(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, f := range files {
		kinds[f.Kind]++
		if f.Kind == KindCheckThreshold && strings.Contains(f.Content, "func (c *checker)") {
			t.Errorf("%s: the excerpt carries code", f.Path)
		}
	}
	if kinds[KindPrompt] != 1 || kinds[KindPlaybook] < 5 || kinds[KindCheckThreshold] != 2 || kinds[KindRendererDefault] != 1 || kinds[KindStyleRule] != 1 {
		t.Errorf("kinds %v", kinds)
	}
	for path, want := range map[string]bool{
		"apps/server/internal/agents/editor/prompts/editor.v2.8.md":          true,
		"apps/server/internal/agents/editor/prompts/playbooks/workout.v5.md": true,
		"apps/server/internal/agents/editor/check.go":                        true,
		"apps/server/internal/agents/editor/prompts/CHANGELOG.md":            false,
		"apps/server/internal/agents/editor/tools.go":                        false,
		"apps/server/internal/agents/editor/prompts/playbooks/../tools.go":   false,
		"/etc/passwd":              false,
		".github/workflows/ci.yml": false,
	} {
		if _, ok := allowedFor(path); ok != want {
			t.Errorf("allowedFor(%s) = %v", path, ok)
		}
	}
	blocks := goDeclBlocks("package x\n\n// a\nconst (\n\tA = 1\n)\n\nfunc f() {\n\tconst inner = 2\n}\n\n// t\nvar table = map[string]int{\n\t\"x\": 1,\n}\n")
	if !strings.Contains(blocks, "A = 1") || !strings.Contains(blocks, `"x": 1`) || strings.Contains(blocks, "inner") || !strings.Contains(blocks, "// a") {
		t.Errorf("blocks:\n%s", blocks)
	}
}

func TestValidateAndApplyProposal(t *testing.T) {
	root := t.TempDir()
	path := "apps/server/internal/agents/editor/pace.go"
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(path)), 0o750); err != nil {
		t.Fatal(err)
	}
	src := "const (\n\tlongShotMs = 4000\n\tother = 4000\n)\n"
	if err := os.WriteFile(filepath.Join(root, path), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	good := Proposal{Title: "t", Edits: []Edit{{Path: path, Old: "longShotMs = 4000", New: "longShotMs = 3500"}}}
	p, err := ValidateProposal(good, root)
	if err != nil || p.Kind != KindCheckThreshold || p.AffectsModel {
		t.Fatalf("%+v %v", p, err)
	}
	for name, bad := range map[string]Proposal{
		"ambiguous":   {Edits: []Edit{{Path: path, Old: "4000", New: "1"}}},
		"missing":     {Edits: []Edit{{Path: path, Old: "nope", New: "1"}}},
		"not allowed": {Edits: []Edit{{Path: "apps/server/internal/agents/editor/tools.go", Old: "a", New: "b"}}},
		"two files":   {Edits: []Edit{{Path: path, Old: "longShotMs = 4000", New: "x"}, {Path: "apps/server/internal/render/profile.go", Old: "a", New: "b"}}},
		"no-op":       {Edits: []Edit{{Path: path, Old: "longShotMs = 4000", New: "longShotMs = 4000"}}},
		"empty":       {},
	} {
		if _, err := ValidateProposal(bad, root); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := ApplyProposal(p, root); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, path)); !strings.Contains(string(raw), "longShotMs = 3500") || !strings.Contains(string(raw), "other = 4000") {
		t.Errorf("applied: %s", raw)
	}
}

// ---- the loop over fakes -----------------------------------------------------------------------------------------------------------

type fakeRepo struct {
	root      string
	commits   int
	dropped   int
	removed   bool
	deleted   []string
	buildFail bool
}

func (r *fakeRepo) AddWorktree(_ context.Context, dir, _ string) error {
	// The scratch worktree is a copy of the allowlist's files.
	files, err := ReadAllowed(r.root)
	if err != nil {
		return err
	}
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join(r.root, f.Path))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f.Path)), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, f.Path), src, 0o600); err != nil {
			return err
		}
	}
	return nil
}
func (r *fakeRepo) Commit(context.Context, string, string) (string, error) {
	r.commits++
	return "diff --git a/x b/x\n-old\n+new", nil
}
func (r *fakeRepo) DropLast(context.Context, string) error { r.dropped++; return nil }
func (r *fakeRepo) RemoveWorktree(context.Context, string) error {
	r.removed = true
	return nil
}
func (r *fakeRepo) DeleteBranch(_ context.Context, b string) error {
	r.deleted = append(r.deleted, b)
	return nil
}
func (r *fakeRepo) Build(context.Context, string) error {
	if r.buildFail {
		return errors.New("does not compile")
	}
	return nil
}

func TestImproveKeepsAChangeThatHelps(t *testing.T) {
	s := &Suite{Name: "s", Feedback: []FeedbackItem{{ID: "f", Text: "music only"}}}
	repo := &fakeRepo{root: repoRoot(t)}
	work := t.TempDir()
	thresholds := "apps/server/internal/styleprofile/thresholds.go"
	files, _ := ReadAllowed(repoRoot(t))
	var old string
	for _, f := range files {
		if f.Path == thresholds {
			old = strings.SplitN(f.Content, "\n", 2)[0] // its first line exists once
		}
	}
	if old == "" {
		t.Fatal("no thresholds excerpt")
	}
	var proposals int
	var shown ImproverInput
	d := ImproveDeps{
		Repo: repo, Root: repoRoot(t),
		Baseline: func(context.Context) (*RunResult, error) {
			return result(70, 80, map[string]float64{"a": 60, "b": 80, "held": 80}), nil
		},
		Propose: func(_ context.Context, in ImproverInput, budget int64) (Proposal, int64, error) {
			proposals++
			shown = in
			if budget > 400_000 {
				t.Errorf("improver budget %d", budget)
			}
			return Proposal{Title: "tune a style rule", Rationale: "r", Edits: []Edit{{Path: thresholds, Old: old, New: old + " // tuned"}}}, 50_000, nil
		},
		Evaluate: func(_ context.Context, dir, label string, live bool, left int64) (*RunResult, error) {
			if live || left <= 0 || !strings.Contains(label, "tune") {
				t.Errorf("evaluate live %v left %d label %q", live, left, label)
			}
			raw, _ := os.ReadFile(filepath.Join(dir, thresholds))
			if !strings.Contains(string(raw), "// tuned") {
				t.Error("the change is not in the scratch worktree")
			}
			r := result(74, 80, map[string]float64{"a": 68, "b": 80, "held": 80})
			r.SpentMicro = 20_000
			return r, nil
		},
		TraceURL: func(id string) string { return "http://phoenix/" + id },
	}
	res, err := Improve(context.Background(), d, ImproveOptions{Suite: s, MaxMicros: 1_000_000, ImproverMicros: 400_000, MinGain: 0.5, MaxRegression: 5,
		HeldOutSlack: 1, WorkDir: work, ReportDir: filepath.Join(work, "reports")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Kept || proposals != 1 || repo.commits != 1 || repo.dropped != 0 || !repo.removed || len(repo.deleted) != 0 || res.Spent != 70_000 {
		t.Fatalf("result %+v repo %+v", res, repo)
	}
	if len(shown.Cases) != 2 || shown.Cases[0].Case != "a" || len(shown.Files) == 0 {
		t.Errorf("the improver saw %d cases (%v) and %d files", len(shown.Cases), shown.Cases, len(shown.Files))
	}
	for _, c := range shown.Cases {
		if c.HeldOut {
			t.Error("the improver saw a held-out case")
		}
	}
	report, _ := os.ReadFile(res.Report)
	for _, want := range []string{"**Kept.**", "tune a style rule", "| a | 60.0 | 68.0 | +8.0 |", "```diff", "never pushed"} {
		if !strings.Contains(string(report), want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
}

func TestImproveRejectsAndExplains(t *testing.T) {
	s := &Suite{Name: "s"}
	work := t.TempDir()
	prompt := ""
	files, _ := ReadAllowed(repoRoot(t))
	for _, f := range files {
		if f.Kind == KindPrompt {
			prompt = f.Path
		}
	}
	for name, tc := range map[string]struct {
		proposal  Proposal
		buildFail bool
		after     *RunResult
		wantNote  string
	}{
		"prompt change without --live": {Proposal{Title: "p", Edits: []Edit{{Path: prompt, Old: "You", New: "Thou"}}}, false, nil, "Not evaluated"},
		"not on the allowlist":         {Proposal{Title: "x", Edits: []Edit{{Path: "apps/server/go.mod", Old: "module", New: "x"}}}, false, nil, "Refused"},
		"does not build":               {Proposal{Title: "b", Edits: []Edit{{Path: "apps/server/internal/styleprofile/thresholds.go", Old: "package styleprofile", New: "package broken"}}}, true, nil, "does not build"},
		"worse":                        {Proposal{Title: "w", Edits: []Edit{{Path: "apps/server/internal/styleprofile/thresholds.go", Old: "package styleprofile", New: "package styleprofile // x"}}}, false, result(60, 80, map[string]float64{"a": 50, "b": 70, "held": 80}), "Rejected"},
	} {
		repo := &fakeRepo{root: repoRoot(t), buildFail: tc.buildFail}
		if strings.Contains(tc.proposal.Edits[0].Old, "You") {
			// Use an old text that occurs once in the prompt.
			raw, _ := os.ReadFile(filepath.Join(repoRoot(t), prompt))
			tc.proposal.Edits[0].Old = strings.SplitN(string(raw), "\n", 2)[0]
		}
		d := ImproveDeps{
			Repo: repo, Root: repoRoot(t),
			Baseline: func(context.Context) (*RunResult, error) {
				return result(70, 80, map[string]float64{"a": 60, "b": 80, "held": 80}), nil
			},
			Propose: func(context.Context, ImproverInput, int64) (Proposal, int64, error) { return tc.proposal, 1000, nil },
			Evaluate: func(context.Context, string, string, bool, int64) (*RunResult, error) {
				if tc.after == nil {
					t.Errorf("%s: evaluated", name)
					return nil, errors.New("no")
				}
				return tc.after, nil
			},
		}
		res, err := Improve(context.Background(), d, ImproveOptions{Suite: s, MaxMicros: 500_000, ImproverMicros: 100_000, MinGain: 0.5, MaxRegression: 5,
			HeldOutSlack: 1, WorkDir: work, ReportDir: filepath.Join(work, "r-"+strings.ReplaceAll(name, " ", "-"))})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Kept || len(repo.deleted) != 1 {
			t.Errorf("%s: kept %v, deleted %v", name, res.Kept, repo.deleted)
		}
		report, _ := os.ReadFile(res.Report)
		if !strings.Contains(string(report), tc.wantNote) {
			t.Errorf("%s: report lacks %q:\n%s", name, tc.wantNote, report)
		}
	}
}

func TestGitRepoScratchBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(root, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(root, "add", ".")
	run(root, "commit", "-q", "-m", "init")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	g := GitRepo{Root: root}
	ctx := context.Background()
	wt := filepath.Join(root, ".eval-worktrees", "x")
	if err := g.AddWorktree(ctx, wt, "improve/x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := g.Commit(ctx, wt, "change")
	if err != nil || !strings.Contains(diff, "+b") {
		t.Fatalf("diff %q %v", diff, err)
	}
	if err := g.DropLast(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(wt, "f.txt")); string(raw) != "a\n" {
		t.Errorf("after drop: %q", raw)
	}
	if err := g.RemoveWorktree(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if err := g.DeleteBranch(ctx, "improve/x"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "f.txt")); string(raw) != "a\n" {
		t.Errorf("the main checkout changed: %q", raw)
	}
}

func TestImproverRequestAndAnswer(t *testing.T) {
	in := ImproverInput{Suite: "s", Cases: []CaseResult{{Case: "a", Score: sc(60), Checks: struct {
		Passed  int      `json:"passed"`
		Failed  int      `json:"failed"`
		Skipped int      `json:"skipped"`
		Failing []string `json:"failing,omitempty"`
	}{Passed: 3, Failed: 1, Failing: []string{"long_shot"}}}}, Files: []SourceFile{{Path: "p.md", Kind: KindPrompt, Content: "prompt text"}}, Tried: []string{"x (rejected)"}}
	req := BuildImproveRequest(in)
	var all []string
	for _, b := range req.Messages[0].Content {
		all = append(all, b.Text)
	}
	text := strings.Join(all, "\n")
	for _, want := range []string{`<untrusted_data kind="file">`, "prompt text", "long_shot", "x (rejected)"} {
		if !strings.Contains(text, want) {
			t.Errorf("request lacks %q", want)
		}
	}
	if req.ToolChoice.Name != ImproveToolName || !req.Tools[0].Strict {
		t.Error("the tool is not forced and strict")
	}
	answer, _ := json.Marshal(map[string]any{"title": "t", "rationale": "r", "expected_effect": "e", "edits": []map[string]string{{"path": "a", "old": "b", "new": "c"}}})
	fc := &fakeClient{reply: func(llm.Request) (llm.Response, error) {
		return llm.Response{StopReason: "tool_use", Content: []llm.Block{{Type: llm.BlockToolUse, Name: ImproveToolName, Input: answer}}}, nil
	}}
	im := &Improver{Models: fakeFactory{fc}, Router: fakeRouter{}}
	p, cost, err := im.Propose(context.Background(), ids.WorkspaceID{UUID: uuid.New()}, in, 200_000)
	if err != nil || p.Title != "t" || len(p.Edits) != 1 || cost != 12_345 {
		t.Errorf("proposal %+v cost %d err %v", p, cost, err)
	}
	if b, ok := fc.calls[0].Run.Budget.(*llm.RunBudget); !ok || b.Remaining() > 200_000 {
		t.Error("the improver call has no budget")
	}
}
