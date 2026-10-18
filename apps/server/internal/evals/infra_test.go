package evals

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func TestRunFailureClassification(t *testing.T) {
	for reason, infra := range map[string]bool{
		"daily_cap": true, "platform_spend_paused": true, "spend_cap_busy": true, "model_error": true, "model_access": true, "internal_error": true,
		"budget": false, "max_tokens": false, "refusal": false, "context_window_exceeded": false, "event_too_large": false, "": false,
	} {
		err := RunFailure("run_x", "failed", reason)
		if errors.Is(err, ErrInfrastructure) != infra {
			t.Errorf("%q: infrastructure %v, want %v (%v)", reason, !infra, infra, err)
		}
	}
	if err := RunFailure("run_x", "failed", "daily_cap"); !strings.Contains(err.Error(), "the workspace's daily spend cap was reached") {
		t.Errorf("message %q", err)
	}
}

func TestCheckRoomRefusesUpFront(t *testing.T) {
	if err := CheckRoom(2_000_000, SpendRoom{Workspace: 5_000_000, WorkspaceCap: 20_000_000}); err != nil {
		t.Errorf("room enough: %v", err)
	}
	err := CheckRoom(6_000_000, SpendRoom{Workspace: 5_000_000, WorkspaceCap: 20_000_000})
	if !errors.Is(err, ErrInfrastructure) || !strings.Contains(err.Error(), "daily spend cap ($20.00) has $5.00 left today, less than the estimated $6.00") {
		t.Errorf("over the workspace cap: %v", err)
	}
	if err := CheckRoom(1, SpendRoom{Workspace: 5_000_000, PlatformBilled: true, PlatformTripped: true, Platform: 9e9}); !errors.Is(err, ErrInfrastructure) ||
		!strings.Contains(err.Error(), "breaker is open") {
		t.Errorf("breaker open: %v", err)
	}
	if err := CheckRoom(3_000_000, SpendRoom{Workspace: 5_000_000, PlatformBilled: true, Platform: 1_000_000}); err == nil {
		t.Error("over the platform room")
	}
	// Test-billed (local) traffic does not count against the breaker.
	if err := CheckRoom(3_000_000, SpendRoom{Workspace: 5_000_000, Platform: 0, PlatformTripped: true}); err != nil {
		t.Errorf("test billing: %v", err)
	}
}

// The director's run: the daily cap tripped after the first case. The run stops there, unscored, instead of scoring every later sample 0.
// A failure of the edit itself still scores 0.
func TestInfrastructureStopsTheRun(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fb.yaml", "feedback: []\n")
	path := write(t, dir, "s.yaml", "suite: s\nfeedback: fb.yaml\ndefaults: {workspace: wks_x, budget_usd: 1}\ncases:\n  - id: one\n    footage: [ast_a]\n  - id: two\n    footage: [ast_b]\n  - id: three\n    footage: [ast_c]\n")
	s, err := LoadSuite(path, os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	ws := ids.WorkspaceID{UUID: uuid.New()}
	calls := 0
	d := Deps{
		Edit: func(_ context.Context, c Case, _ int64) (Target, error) {
			calls++
			tg := Target{Workspace: ws, Project: ids.ProjectID{UUID: uuid.New()}, Version: 1, RunID: uuid.New(), EditMicros: 200_000}
			switch {
			case c.ID == "one" && calls == 2:
				return tg, RunFailure("run_b", "failed", "budget") // the edit's own failure: scored 0
			case c.ID == "two":
				return tg, RunFailure("run_c", "failed", "daily_cap") // the infrastructure: stop
			}
			return tg, nil
		},
		Measure: func(context.Context, Target) (Measurement, error) {
			return Measurement{Report: editor.Report{Checks: []editor.Check{{ID: "a", Status: editor.CheckPass}}}, Spec: specOf([]timeline.VideoItem{clip("c1", 0, 2000)}, true)}, nil
		},
	}
	res, err := Run(context.Background(), s, d, Options{Live: true, Repeat: 2, MaxMicros: 5_000_000, OutDir: dir, NoJudge: true})
	if !errors.Is(err, ErrInfrastructure) || res == nil || !strings.Contains(res.Aborted, "stopped at case two") || !strings.Contains(res.Aborted, "nothing was decided") {
		t.Fatalf("err %v aborted %q", err, res.Aborted)
	}
	if calls != 3 || len(res.Cases) != 1 || res.Cases[0].Case != "one" || len(res.Cases[0].Failures) != 1 || !strings.Contains(err.Error(), "spent $0.600") {
		t.Errorf("calls %d cases %d failures %v err %v", calls, len(res.Cases), res.Cases[0].Failures, err)
	}
	back, lerr := LoadResults(res.JSONL)
	if lerr != nil || back.Aborted == "" {
		t.Errorf("the results file does not say it stopped: %v", lerr)
	}
}

// A candidate stopped by the infrastructure ends the round with nothing decided (not "rejected"), and the report says so.
func TestImproveStopsOnInfrastructure(t *testing.T) {
	s := &Suite{Name: "s"}
	work := t.TempDir()
	thresholds := "apps/server/internal/styleprofile/thresholds.go"
	files, _ := ReadAllowed(repoRoot(t))
	var old string
	for _, f := range files {
		if f.Path == thresholds {
			old = strings.SplitN(f.Content, "\n", 2)[0]
		}
	}
	repo := &fakeRepo{root: repoRoot(t)}
	evaluated := 0
	d := ImproveDeps{
		Repo: repo, Root: repoRoot(t),
		Baseline: func(context.Context) (*RunResult, bool, error) {
			return result(70, 80, map[string]float64{"a": 60, "b": 80, "held": 80}), false, nil
		},
		Propose: func(_ context.Context, _ ImproverInput, _ int64, validate func(Proposal) (Proposal, error)) (Proposal, int64, []Attempt, error) {
			p, err := validate(named(Proposal{Title: "Tune the style rule comment", Edits: []Edit{{Path: thresholds, Old: old, New: old + " // tuned"}}}))
			return p, 10_000, []Attempt{{N: 1, Title: p.Title, File: p.File()}}, err
		},
		Evaluate: func(context.Context, string, string, bool, int64) (*RunResult, error) {
			evaluated++
			return &RunResult{SpentMicro: 1_500_000, Aborted: "stopped at case two: the workspace's daily spend cap was reached; nothing was decided"},
				&InfraError{Reason: "daily_cap", Msg: "stopped at case two: the workspace's daily spend cap was reached; nothing was decided"}
		},
	}
	res, err := Improve(context.Background(), d, ImproveOptions{Suite: s, Iterations: 2, MaxMicros: 10_000_000, ImproverMicros: 100_000,
		Rule: Rule{MinGain: 0.5, MaxRegression: 5, HeldOutSlack: 1}, WorkDir: work, ReportDir: filepath.Join(work, "r")})
	if err != nil {
		t.Fatal(err)
	}
	if evaluated != 1 || len(res.Iterations) != 1 || res.Kept || res.Aborted == "" || res.Spent != 1_510_000 || repo.dropped != 1 {
		t.Errorf("evaluated %d iterations %d aborted %q spent %d dropped %d", evaluated, len(res.Iterations), res.Aborted, res.Spent, repo.dropped)
	}
	report, _ := os.ReadFile(res.Report)
	if !strings.Contains(string(report), "Stopped by the infrastructure, nothing was decided") || strings.Contains(string(report), "**Rejected.**") {
		t.Errorf("report:\n%s", report)
	}

	// A baseline stopped the same way: an error, and a report of what was spent.
	d.Baseline = func(context.Context) (*RunResult, bool, error) {
		return &RunResult{SpentMicro: 700_000}, false, &InfraError{Reason: "daily_cap", Msg: "stopped at case one: the workspace's daily spend cap was reached; nothing was decided"}
	}
	res, err = Improve(context.Background(), d, ImproveOptions{Suite: s, MaxMicros: 10_000_000, ImproverMicros: 100_000, WorkDir: work, ReportDir: filepath.Join(work, "r2")})
	if !errors.Is(err, ErrInfrastructure) || res.Spent != 700_000 || res.Report == "" {
		t.Errorf("baseline stop: %v spent %d report %q", err, res.Spent, res.Report)
	}
}
