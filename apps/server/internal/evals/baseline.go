package evals

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Baselines and costs (docs/design/observability-v1.md section 9): a suite run is remembered by a hash of the code that decides an edit's
// score, so the improvement loop does not pay to re-run a baseline of code that has not changed; and a run's cost is estimated from the
// recent runs before anything is spent.

// HashedPaths are the parts of the repository whose content decides how an edit is made and scored: the editor (its prompt, playbooks,
// tools and checks), the renderer and its defaults, the timeline rules, the style profile, the scoring itself and the routing (models,
// efforts, budgets). Relative to the repository root; a directory is every file under it but tests and test data.
var HashedPaths = []string{
	"apps/server/internal/agents/editor",
	"apps/server/internal/render",
	"apps/server/internal/timeline",
	"apps/server/internal/styleprofile",
	"apps/server/internal/evals",
	"apps/server/config/routing",
	// The format library (docs/design/formats-v1.md): its cards decide what list_formats offers, what format_followed checks and how a
	// creator's references are tagged.
	"apps/server/internal/formats",
	"apps/server/config/formats",
	// How a live case is made: a first edit of new footage, or a new idea on a stored project.
	"apps/server/cmd/cfctl/eval_suite.go",
}

// RunKey is what besides the code makes two suite runs comparable.
type RunKey struct {
	Live         bool     `json:"live"`
	Repeat       int      `json:"repeat"`
	Judge        bool     `json:"judge"`
	JudgeProfile string   `json:"judge_profile,omitempty"`
	Only         []string `json:"only,omitempty"`
}

// CodeHash is the hash of the HashedPaths under root, the suite's files (the suite, its local file and its feedback) and the key.
func CodeHash(root string, s *Suite, key RunKey) (string, error) {
	var files []string
	for _, p := range HashedPaths {
		err := filepath.WalkDir(filepath.Join(root, p), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if e.Name() == "testdata" || strings.HasPrefix(e.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(e.Name(), "_test.go") || !e.Type().IsRegular() {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("hash %s: %w", p, err)
		}
	}
	sort.Strings(files)
	for _, f := range []string{s.Path, s.LocalPath, s.feedbackPath()} {
		if f != "" {
			files = append(files, f)
		}
	}
	h := sha256.New()
	for _, f := range files {
		raw, err := os.ReadFile(f) // #nosec G304 -- files of the repository and the suite
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(root, f)
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(raw))
		_, _ = h.Write(raw)
	}
	k, _ := json.Marshal(key)
	_, _ = h.Write(k)
	return hex.EncodeToString(h.Sum(nil))[:24], nil
}

func (s *Suite) feedbackPath() string {
	if s.FeedbackFile == "" || s.Path == "" {
		return ""
	}
	if filepath.IsAbs(s.FeedbackFile) {
		return s.FeedbackFile
	}
	return filepath.Join(filepath.Dir(s.Path), s.FeedbackFile)
}

// baselineEntry is one remembered run.
type baselineEntry struct {
	Hash    string    `json:"hash"`
	JSONL   string    `json:"jsonl"`
	Created time.Time `json:"created"`
	Label   string    `json:"label,omitempty"`
}

func baselineIndex(outDir, suite string) string {
	return filepath.Join(outDir, suite, "baselines.json")
}

func readIndex(path string) []baselineEntry {
	raw, err := os.ReadFile(path) // #nosec G304 -- our results directory
	if err != nil {
		return nil
	}
	var es []baselineEntry
	_ = json.Unmarshal(raw, &es)
	return es
}

// RecordBaseline remembers a finished run under its code hash.
func RecordBaseline(outDir string, r *RunResult, now time.Time) error {
	if r.CodeHash == "" || r.JSONL == "" {
		return nil
	}
	path := baselineIndex(outDir, r.Suite)
	es := append(readIndex(path), baselineEntry{Hash: r.CodeHash, JSONL: r.JSONL, Created: now.UTC(), Label: r.Label})
	if len(es) > 200 {
		es = es[len(es)-200:]
	}
	raw, err := json.MarshalIndent(es, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// LookupBaseline returns the newest run of the same hash younger than maxAge, when its results are still there and complete.
func LookupBaseline(outDir, suite, hash string, maxAge time.Duration, now time.Time) (*RunResult, bool) {
	es := readIndex(baselineIndex(outDir, suite))
	for i := len(es) - 1; i >= 0; i-- {
		e := es[i]
		if e.Hash != hash || now.Sub(e.Created) > maxAge || now.Before(e.Created.Add(-time.Minute)) {
			continue
		}
		if r, err := LoadResults(e.JSONL); err == nil {
			return r, true
		}
	}
	return nil, false
}

// baselineTime is when a run was made (its id starts with the UTC time).
func baselineTime(r *RunResult) time.Time {
	if len(r.RunID) >= 15 {
		if t, err := time.Parse("20060102-150405", r.RunID[:15]); err == nil {
			return t
		}
	}
	return time.Now()
}

// RecentRuns are the newest results files of a suite (at most n), read back; unreadable ones are skipped.
func RecentRuns(outDir, suite string, n int) []*RunResult {
	dir := filepath.Join(outDir, suite)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // the names start with the time
	var out []*RunResult
	for _, name := range names {
		if len(out) >= n {
			break
		}
		if r, err := LoadResults(filepath.Join(dir, name)); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// Estimate is the expected cost of a suite run.
type Estimate struct {
	Micros int64
	Lines  []string
}

// EstimateRun is what a run of the suite with these options is expected to cost: per sample, the case's mean editor cost in the recent
// runs (its budget when there is none) for a live case and the mean judge cost (the judge's budget when there is none), times the
// samples. It leans high: a stored case's judge verdict often comes from the cache for nothing.
func EstimateRun(s *Suite, o Options, live bool, history []*RunResult) Estimate {
	edit, judge := map[string][]int64{}, map[string][]int64{}
	var allJudge []int64
	for _, r := range history {
		for _, c := range r.Cases {
			for _, smp := range c.Samples {
				if smp.EditMicros > 0 {
					edit[c.Case] = append(edit[c.Case], smp.EditMicros)
				}
				if smp.JudgeMicros > 0 {
					judge[c.Case] = append(judge[c.Case], smp.JudgeMicros)
					allJudge = append(allJudge, smp.JudgeMicros)
				}
			}
		}
	}
	only := map[string]bool{}
	for _, id := range o.Only {
		only[id] = true
	}
	var e Estimate
	for _, raw := range s.Cases {
		if len(only) > 0 && !only[raw.ID] {
			continue
		}
		c := s.Resolved(raw)
		n, editEst, src := 1, int64(0), ""
		if live && o.Live && c.Calibration == nil && len(c.Missing(true)) == 0 {
			n = max(o.Repeat, 1)
			if m, ok := meanOf(edit[c.ID]); ok {
				editEst, src = m, "recent runs"
			} else {
				editEst, src = int64(c.BudgetUSD*1e6), "its budget"
			}
		} else if len(c.Missing(false)) > 0 {
			continue
		}
		var judgeEst int64
		if !o.NoJudge {
			if m, ok := meanOf(judge[c.ID]); ok {
				judgeEst = m
			} else if m, ok := meanOf(allJudge); ok {
				judgeEst = m
			} else {
				judgeEst = MaxJudgeMicros
			}
		}
		cost := int64(n) * (editEst + judgeEst)
		e.Micros += cost
		line := fmt.Sprintf("%s: %d × (", c.ID, n)
		if editEst > 0 {
			line += fmt.Sprintf("edit $%.3f from %s + ", float64(editEst)/1e6, src)
		}
		e.Lines = append(e.Lines, line+fmt.Sprintf("judge $%.3f) = $%.3f", float64(judgeEst)/1e6, float64(cost)/1e6))
	}
	return e
}

func meanOf(v []int64) (int64, bool) {
	if len(v) == 0 {
		return 0, false
	}
	var s int64
	for _, x := range v {
		s += x
	}
	return s / int64(len(v)), true
}
