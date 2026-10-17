package editor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// saveAnalysis stores an analysis for the asset: nReps reps of 3 s from 6 s on (scores rising by a tenth), a hold, dead time.
func (e *env) saveAnalysis(ws store.Workspace, asset ids.AssetID, nReps int) {
	e.t.Helper()
	e.saveAnalysisSeries(ws, asset, nReps, nil)
}

// flatSeries is the stored series of a clip whose brightness never changes: the mean luma and the 90th percentile luma (8-bit, limited
// range) of every sample of 60 s.
func flatSeries(t *testing.T, luma, high float64) *analysis.Series {
	t.Helper()
	n := 600
	s := analysis.Series{StepMs: 100, Motion: make([]float64, n), Luma: make([]float64, n), Contrast: make([]float64, n), High: make([]float64, n), Sharp: make([]float64, n), Scene: make([]float64, n)}
	for i := range n {
		s.Luma[i], s.High[i] = luma, high
	}
	return &s
}

// saveAnalysisSeries stores the analysis of the standard test clip, with its series when one is given.
func (e *env) saveAnalysisSeries(ws store.Workspace, asset ids.AssetID, nReps int, series *analysis.Series) {
	e.t.Helper()
	res := store.AnalysisResult{Version: analysis.Version, StepMs: 100, DurationMs: 60000, Series: store.PackedSeries{}}
	if series != nil {
		packed, err := analysis.Pack(*series)
		if err != nil {
			e.t.Fatal(err)
		}
		res.Series = packed
	}
	sum := analysis.Summary{Version: analysis.Version, DurMs: 60000, Shots: 1, Sets: 1, Reps: nReps, CleanReps: nReps, PeriodMs: 3000, DeadMs: 12000, Level: "dark", MeanY: 0.1, Gain: 2.5}
	for i := 0; i < nReps; i++ {
		m := store.NewMoment{
			Kind: store.MomentRep, StartMs: 6000 + i*3000, EndMs: 9000 + i*3000, Score: 0.4 + 0.5*float64(i)/float64(max(nReps-1, 1)),
			Attrs: []byte(`{}`), Description: "rep " + string(rune('1'+i)) + " of " + string(rune('0'+nReps)) + ", clean, full range",
		}
		res.Moments = append(res.Moments, m)
	}
	res.Moments = append(res.Moments,
		store.NewMoment{Kind: store.MomentHold, StartMs: 50000, EndMs: 56000, Score: 0.55, Attrs: []byte(`{}`), Description: "steady for 6.0 s, sharp, dark"},
		store.NewMoment{Kind: store.MomentDead, StartMs: 0, EndMs: 5000, Score: 0, Attrs: []byte(`{}`), Description: "dead time, 5.0 s: still between sets"},
		store.NewMoment{Kind: store.MomentDead, StartMs: 40000, EndMs: 47000, Score: 0, Attrs: []byte(`{}`), Description: "dead time, 7.0 s: still between sets"},
		store.NewMoment{Kind: store.MomentShot, StartMs: 0, EndMs: 60000, Score: 0.6, Attrs: []byte(`{}`), Description: "shot 1"},
	)
	res.Shots = []store.NewShot{{Idx: 0, StartMs: 0, EndMs: 60000, Signals: []byte(`{}`)}}
	b, err := json.Marshal(sum)
	if err != nil {
		e.t.Fatal(err)
	}
	res.Summary = b
	if err := e.st.SaveAnalysis(context.Background(), ws.ID, asset, res); err != nil {
		e.t.Fatal(err)
	}
}

func TestFindMomentsRanksAndFilters(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{title: "Row"})
	prj := e.project(e.wsA, "9:16", "", a)
	e.saveAnalysis(e.wsA, a, 5)
	rc := e.runCtx(e.wsA, prj, "")

	text := textOf(e.ok(rc, ToolFindMoments, `{}`))
	// best first: the last rep (score 0.9) leads; the hold is in; dead time and the shot are not moments but the dead time is noted
	lines := strings.Split(text, "\n")
	var ranked []string
	for _, l := range lines {
		if len(l) > 2 && l[0] >= '1' && l[0] <= '9' && l[1] == '.' {
			ranked = append(ranked, l)
		}
	}
	if len(ranked) != 6 {
		t.Fatalf("%d ranked lines, want 5 reps and a hold:\n%s", len(ranked), text)
	}
	if !strings.Contains(ranked[0], "rep 5 of 5") || !strings.Contains(ranked[0], "score 0.90") || !strings.Contains(ranked[0], "18000 to 21000 ms") || !strings.Contains(ranked[0], a.String()) {
		t.Errorf("the best moment: %s", ranked[0])
	}
	for _, l := range ranked {
		if strings.Contains(l, "| shot |") || strings.Contains(l, "| dead |") {
			t.Errorf("shots and dead time are not moments to cut: %s", l)
		}
	}
	for _, want := range []string{"Dead time to skip", "0 to 5000 ms", "40000 to 47000 ms", "cutting there never lands mid-rep"} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer lacks %q:\n%s", want, text)
		}
	}

	reps := textOf(e.ok(rc, ToolFindMoments, `{"kind":"rep","limit":2}`))
	if strings.Count(reps, " | rep | ") != 2 || strings.Contains(reps, "| hold |") {
		t.Errorf("kind rep, limit 2:\n%s", reps)
	}
	hold := textOf(e.ok(rc, ToolFindMoments, `{"kind":"hold"}`))
	if strings.Count(hold, " | hold | ") != 1 || strings.Contains(hold, "| rep |") {
		t.Errorf("kind hold:\n%s", hold)
	}
	clean := textOf(e.ok(rc, ToolFindMoments, `{"min_score":0.7}`))
	if strings.Count(clean, " | score ") != 2 || strings.Contains(clean, "score 0.40") || strings.Contains(clean, "score 0.65") {
		t.Errorf("min_score 0.7:\n%s", clean)
	}
	none := textOf(e.ok(rc, ToolFindMoments, `{"min_score":0.99}`))
	if !strings.Contains(none, "No moments match") {
		t.Errorf("min_score 0.99:\n%s", none)
	}
	clamped := textOf(e.ok(rc, ToolFindMoments, `{"limit":500}`))
	if !strings.Contains(clamped, "limit lowered to 20") {
		t.Errorf("limit 500:\n%s", clamped)
	}
	for in, want := range map[string]string{
		`{"limit":0}`:           "limit must be at least 1",
		`{"min_score":1.5}`:     "min_score",
		`{"kind":"everything"}`: "must be one of",
	} {
		if msg := e.toolErr(rc, ToolFindMoments, in); !strings.Contains(msg, want) {
			t.Errorf("%s: %q", in, msg)
		}
	}
}

func TestFindMomentsOnlySeesTheProjectsClips(t *testing.T) {
	e := newEnv(t)
	mine := e.asset(e.wsA, clip{title: "Mine"})
	other := e.asset(e.wsA, clip{title: "In the library only"})
	foreign := e.asset(e.wsB, clip{title: "Someone else's"})
	audio := e.asset(e.wsA, clip{title: "Voice", kind: store.AssetKindAudio})
	prj := e.project(e.wsA, "9:16", "", mine, audio)
	for _, a := range []ids.AssetID{mine, other} {
		e.saveAnalysis(e.wsA, a, 3)
	}
	e.saveAnalysis(e.wsB, foreign, 3)
	rc := e.runCtx(e.wsA, prj, "")

	all := textOf(e.ok(rc, ToolFindMoments, `{}`))
	if !strings.Contains(all, mine.String()) || strings.Contains(all, other.String()) || strings.Contains(all, foreign.String()) {
		t.Errorf("a search without asset_id must cover this project's clips only:\n%s", all)
	}
	for _, id := range []string{other.String(), foreign.String(), audio.String(), "ast_nonsense", ""} {
		if id == "" {
			continue
		}
		msg := e.toolErr(rc, ToolFindMoments, `{"asset_id":"`+id+`"}`)
		if !strings.Contains(msg, "not one of this project's video clips") {
			t.Errorf("asset %q: %q", id, msg)
		}
		if strings.Contains(msg, "Someone else") {
			t.Errorf("the refusal leaks: %q", msg)
		}
	}
	one := textOf(e.ok(rc, ToolFindMoments, `{"asset_id":"`+mine.String()+`"}`))
	if !strings.Contains(one, mine.String()) {
		t.Errorf("one clip:\n%s", one)
	}
	// a run with no project
	if msg := e.toolErr(e.runCtx(e.wsA, ids.ProjectID{}, ""), ToolFindMoments, `{}`); !strings.Contains(msg, "no project") {
		t.Errorf("no project: %q", msg)
	}
}

func TestFindMomentsBeforeTheAnalysisExists(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{title: "New"})
	b := e.asset(e.wsA, clip{title: "Older"})
	prj := e.project(e.wsA, "9:16", "", a, b)
	rc := e.runCtx(e.wsA, prj, "")
	if text := textOf(e.ok(rc, ToolFindMoments, `{}`)); !strings.Contains(text, "None of these clips has been analysed yet") {
		t.Errorf("nothing analysed:\n%s", text)
	}
	e.saveAnalysis(e.wsA, b, 4)
	text := textOf(e.ok(rc, ToolFindMoments, `{}`))
	if !strings.Contains(text, "Not analysed yet") || !strings.Contains(text, a.String()) {
		t.Errorf("one clip waits:\n%s", text)
	}
	empty := e.project(e.wsA, "9:16", "")
	if text := textOf(e.ok(e.runCtx(e.wsA, empty, ""), ToolFindMoments, `{}`)); !strings.Contains(text, "no video clips") {
		t.Errorf("empty project: %s", text)
	}
}

func TestFootageListsCarryTheAnalysisLine(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{title: "Row"})
	b := e.asset(e.wsA, clip{title: "Not yet"})
	prj := e.project(e.wsA, "9:16", "Make a short.", a, b)
	e.saveAnalysis(e.wsA, a, 8)

	ctx, _ := contextText(t, e, prj)
	list := textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolListFootage, `{}`))
	for name, text := range map[string]string{"run context": ctx, "list_footage": list} {
		// No series is stored for the clip, so the line says only what the stored key brightness says, and not that it is a problem.
		for _, want := range []string{"analysis: 1 set, 8 reps", "12 s dead time", "dark overall (key brightness 10%; may be low-key footage, which is fine)", "not ready yet"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, text)
			}
		}
		if strings.Contains(text, "gain") {
			t.Errorf("%s suggests a gain:\n%s", name, text)
		}
	}
	// the analysis line sits under its own clip
	i, j := strings.Index(ctx, a.String()), strings.Index(ctx, "analysis: ")
	if i < 0 || j < i || (strings.Index(ctx, b.String()) > 0 && j > strings.Index(ctx, b.String())) {
		t.Errorf("the line is not under its clip:\n%s", ctx)
	}
}

func TestContextWithAnalysisLinesStillFits(t *testing.T) {
	e := newEnv(t)
	title := strings.Repeat("背", 250)
	desc := strings.Repeat("広い体育館のラットプルダウン。", 400)
	var assets []ids.AssetID
	for i := 0; i < store.MaxProjectAssets; i++ {
		a := e.asset(e.wsA, clip{title: title, desc: desc, durMs: int64(5000 + i)})
		e.saveAnalysis(e.wsA, a, 9)
		assets = append(assets, a)
	}
	prj := e.project(e.wsA, "9:16", strings.Repeat("背中の日のショート動画。", 333), assets...)
	_, size := contextText(t, e, prj)
	if size > maxContextBytes {
		t.Fatalf("the context is %d bytes, over the budget of %d", size, maxContextBytes)
	}
}

// The exposure note is judged by the percentiles of the stored series, never by the mean: a low-key clip (a lit lifter against a dark
// wall) is not a clip to brighten, and no line suggests a gain.
func TestFootageExposureNoteUsesPercentiles(t *testing.T) {
	e := newEnv(t)
	lowKey := e.asset(e.wsA, clip{title: "Low key"})
	black := e.asset(e.wsA, clip{title: "Black"})
	lit := e.asset(e.wsA, clip{title: "Lit"})
	prj := e.project(e.wsA, "9:16", "", lowKey, black, lit)
	// 8-bit limited range: mean 24 and p90 70 is 0.04 and 0.25; mean 17 and p90 20 is 0 and 0.02; mean 120 and p90 180 is 0.47 and 0.75.
	e.saveAnalysisSeries(e.wsA, lowKey, 8, flatSeries(t, 24, 70))
	e.saveAnalysisSeries(e.wsA, black, 8, flatSeries(t, 17, 20))
	e.saveAnalysisSeries(e.wsA, lit, 8, flatSeries(t, 120, 180))

	list := textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolListFootage, `{}`))
	for _, want := range []string{"low-key (p90 luma 0.25)", "underexposed (p90 luma 0.02)"} {
		if !strings.Contains(list, want) {
			t.Errorf("list_footage lacks %q:\n%s", want, list)
		}
	}
	// The well-lit clip gets no exposure note at all.
	litLine := list[strings.Index(list, lit.String()):]
	if i := strings.Index(litLine, "\n  title"); i > 0 {
		litLine = litLine[:i]
	}
	if strings.Contains(litLine, "p90") || strings.Contains(litLine, "low-key") {
		t.Errorf("a well-lit clip has an exposure note: %s", litLine)
	}
	if strings.Contains(list, "gain") || strings.Contains(list, "would be needed") {
		t.Errorf("a line suggests a gain:\n%s", list)
	}
}
