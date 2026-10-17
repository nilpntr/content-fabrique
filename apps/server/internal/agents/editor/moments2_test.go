package editor

import (
	"context"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func saveRaw(t *testing.T, e *env, a ids.AssetID, moments []store.NewMoment) {
	t.Helper()
	res := store.AnalysisResult{
		Version: analysis.Version, StepMs: 100, DurationMs: 60000, Series: store.PackedSeries{}, Summary: []byte(`{}`),
		Shots:   []store.NewShot{{Idx: 0, StartMs: 0, EndMs: 60000, Signals: []byte(`{}`)}},
		Moments: moments,
	}
	if err := e.st.SaveAnalysis(context.Background(), e.wsA.ID, a, res); err != nil {
		t.Fatal(err)
	}
}

// A clip with many dead spans does not crowd out another clip's: each clip lists its longest ones and says how many it left out.
func TestFindMomentsDeadTimeIsLimitedPerClip(t *testing.T) {
	e := newEnv(t)
	busy := e.asset(e.wsA, clip{title: "Busy"})
	calm := e.asset(e.wsA, clip{title: "Calm"})
	prj := e.project(e.wsA, "9:16", "", busy, calm)
	e.saveAnalysis(e.wsA, calm, 3)
	ms := []store.NewMoment{{Kind: store.MomentRep, StartMs: 1000, EndMs: 4000, Score: 0.9, Attrs: []byte(`{}`), Description: "rep"}}
	for i := 0; i < 9; i++ {
		ms = append(ms, store.NewMoment{Kind: store.MomentDead, StartMs: 5000 + i*5000, EndMs: 5000 + i*5000 + 1000 + i*100, Attrs: []byte(`{}`), Description: "dead"})
	}
	saveRaw(t, e, busy, ms)
	text := textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolFindMoments, `{}`))
	var busyLine, calmLine string
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(l, "- "+busy.String()):
			busyLine = l
		case strings.HasPrefix(l, "- "+calm.String()):
			calmLine = l
		}
	}
	if strings.Count(busyLine, " ms") != 6 || !strings.Contains(busyLine, "and 3 shorter ones") || strings.Contains(busyLine, "5000 to 6000 ms") {
		t.Errorf("the busy clip's dead time: %q", busyLine)
	}
	if !strings.Contains(calmLine, "0 to 5000 ms") || !strings.Contains(calmLine, "40000 to 47000 ms") {
		t.Errorf("the calm clip's dead time is missing: %q", calmLine)
	}
}

// Slow reps next to a set are searched with the reps (below them) and are not in the dead time the editor is told to skip.
func TestFindMomentsReturnsRepCandidatesBelowTheReps(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{title: "Tired set"})
	prj := e.project(e.wsA, "9:16", "", a)
	saveRaw(t, e, a, []store.NewMoment{
		{Kind: store.MomentRep, StartMs: 1000, EndMs: 4000, Score: 0.8, Attrs: []byte(`{}`), Description: "rep 1 of 1, clean, full range"},
		{Kind: store.MomentRepCandidate, StartMs: 4000, EndMs: 12000, Score: 0.3, Attrs: []byte(`{}`), Description: "possible slow reps, 8.0 s: moves as far as the set next to it but does not repeat clearly"},
	})
	rc := e.runCtx(e.wsA, prj, "")
	text := textOf(e.ok(rc, ToolFindMoments, `{}`))
	if i, j := strings.Index(text, "| rep |"), strings.Index(text, "| rep_candidate |"); i < 0 || j < i || strings.Contains(text, "Dead time") {
		t.Errorf("candidates come after the reps and are not dead time:\n%s", text)
	}
	if only := textOf(e.ok(rc, ToolFindMoments, `{"kind":"rep"}`)); strings.Contains(only, "rep_candidate") {
		t.Errorf("kind rep returned a candidate:\n%s", only)
	}
}

// When the dead time cannot be read the answer says so, rather than leaving the editor to think there is none.
type deadSpansFail struct{ Store }

func (deadSpansFail) ListDeadSpans(context.Context, ids.WorkspaceID, []ids.AssetID, int) ([]store.DeadSpan, error) {
	return nil, context.DeadlineExceeded
}

func TestFindMomentsSaysWhenDeadTimeCannotBeRead(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{title: "Row"})
	prj := e.project(e.wsA, "9:16", "", a)
	e.saveAnalysis(e.wsA, a, 3)
	e.deps.Store = deadSpansFail{e.deps.Store}
	e.rebuild()
	text := textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolFindMoments, `{}`))
	if !strings.Contains(text, "could not be read just now") || !strings.Contains(text, "rep 1 of 3") {
		t.Errorf("the moments come back and the missing dead time is said:\n%s", text)
	}
}
