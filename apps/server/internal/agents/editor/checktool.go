package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

// check_edit (the self-review loop, docs/design/edit-style-v1.md step 4) and the gates around it and around plan_edit.

// CheckArtifact is the kind of the artifact event a check_edit result is recorded as. Its payload is
//
//	{"kind": "edit_check", "tool_use_id": "toolu_...", "data": <CheckRecord>}
const CheckArtifact = "edit_check"

// CheckRecordVersion is the version of the CheckRecord shape.
const CheckRecordVersion = 1

// CheckRecord is the payload (data) of an edit_check artifact: the Report, with the counts and which check of the run it was.
type CheckRecord struct {
	RecordVersion int `json:"record_version"`
	Report
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	// Nth is how many times check_edit has run in the run, this one included. The editor revises after the first and finishes after
	// the second.
	Nth int `json:"nth"`
}

// AnalysisSource is what loading the analysis of a timeline's clips reads. *store.Store is the real one.
type AnalysisSource interface {
	ListAssetSpans(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]store.AssetSpanRow, error)
	ListAssetExposureSeries(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]store.ExposureSeriesRow, error)
}

// LoadAssetAnalysis reads the stored analysis of the clips a spec uses: the reps, holds and dead time, and the luma series for the
// brightness check. A clip with no analysis is left out, and the checks that need it are skipped.
func LoadAssetAnalysis(ctx context.Context, src AnalysisSource, ws ids.WorkspaceID, spec *timeline.Spec) (map[string]AssetAnalysis, error) {
	main, _, ok := timeline.MainTrack(spec)
	if !ok {
		return nil, nil
	}
	seen := map[string]bool{}
	var assets []ids.AssetID
	for _, it := range main.Items {
		if seen[it.AssetID] {
			continue
		}
		seen[it.AssetID] = true
		aid, err := ids.ParseAssetID(it.AssetID)
		if err != nil {
			continue
		}
		assets = append(assets, aid)
	}
	out := make(map[string]AssetAnalysis, len(assets))
	series, err := src.ListAssetExposureSeries(ctx, ws, assets)
	if err != nil {
		return nil, fmt.Errorf("read the exposure series: %w", err)
	}
	for _, r := range series { // an asset with a row here has been analysed
		a := AssetAnalysis{Analysed: true}
		if len(r.Luma) > 0 && len(r.Luma) == len(r.High) && r.StepMs > 0 {
			a.Series, a.HasSeries = analysis.ExposureSeries(int(r.StepMs), r.Luma, r.High), true
		}
		out[r.AssetID.String()] = a
	}
	spans, err := src.ListAssetSpans(ctx, ws, assets)
	if err != nil {
		return nil, fmt.Errorf("read the reps and the dead time: %w", err)
	}
	for _, m := range spans {
		a, ok := out[m.AssetID.String()]
		if !ok {
			continue
		}
		sp := Span{StartMs: int64(m.StartMs), EndMs: int64(m.EndMs)}
		switch m.Kind {
		case store.MomentRep:
			a.Reps = append(a.Reps, sp)
		case store.MomentHold:
			a.Holds = append(a.Holds, sp)
		case store.MomentDead:
			a.Dead = append(a.Dead, sp)
		}
		out[m.AssetID.String()] = a
	}
	return out, nil
}

// CheckVersion runs the checks over one stored version of a project: what check_edit does, and what cfctl eval editor prints.
//
// sampler measures the version's preview render for the checks that look at the rendered picture; nil, or a version with no preview,
// leaves those checks "not applicable".
func CheckVersion(ctx context.Context, projects Projects, src AnalysisSource, ws ids.WorkspaceID, pid ids.ProjectID, version int, plan *Plan, sampler RenderSampler) (Report, error) {
	vd, perr := projects.Version(ctx, actor(ws), pid, version)
	if perr != nil {
		return Report{}, projectErr(perr)
	}
	spec, err := timeline.Parse(vd.Version.Spec)
	if err != nil {
		return Report{}, fmt.Errorf("parse stored spec: %w", err)
	}
	assets, err := LoadAssetAnalysis(ctx, src, ws, spec)
	if err != nil {
		return Report{}, err
	}
	in := CheckInput{Version: version, Spec: spec, Plan: plan, Assets: assets, Issues: vd.Issues}
	if sampler != nil {
		switch series, err := sampler.Rendered(ctx, ws, pid, version); {
		case err == nil:
			in.Rendered = &series
		case errors.Is(err, ErrNoPreview):
		default:
			slog.WarnContext(ctx, "editor: the preview render could not be measured; the rendered-brightness checks do not apply", "project_id", pid.String(), "version", version, "error", err)
		}
	}
	// The ears: the same preview, listened to (its audio stem when it has a beat guide). It is a separate, small step; when it cannot
	// run the audio checks do not apply, and the picture checks are unaffected.
	if as, ok := sampler.(AudioSampler); ok && sampler != nil {
		switch res, err := as.Audio(ctx, ws, pid, version, spec); {
		case err == nil:
			in.Audio = &res
			slog.InfoContext(ctx, "editor: the preview was listened to", "project_id", pid.String(), "version", version, "profile", res.Report.Profile, "source", res.Source, "audio_ms", res.Elapsed.Milliseconds())
		case errors.Is(err, ErrNoPreview):
		case errors.Is(err, ErrNoAudioStem):
			in.AudioSkip = "the preview has no audio stem (it was rendered before stems existed, or the stem could not be stored), so its sound has the beat guide in it: call render_preview again, then check_edit; if it is the same again, say in finish that the sound was not checked"
		default:
			in.AudioSkip = "the preview's sound could not be measured"
			slog.WarnContext(ctx, "editor: the preview's sound could not be measured; the audio checks do not apply", "project_id", pid.String(), "version", version, "error", err)
		}
	}
	return RunChecks(in), nil
}

// checkEdit is the check_edit tool.
func (d Deps) checkEdit(ctx context.Context, rc runtime.RunContext, _ noArgs) (runtime.Result, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.Result{}, err
	}
	det, perr := d.Projects.Get(ctx, actor(ws), pid)
	if perr != nil {
		return runtime.Result{}, projectErr(perr)
	}
	if det.Project.CurrentTimelineVersion == nil {
		return runtime.Result{}, runtime.UserErrorf("there is no timeline to check yet; make one with edit_timeline first")
	}
	// The run's own newest version is the one finish asks about, so it is the one checked, even when a person has saved a newer one.
	v := int(*det.Project.CurrentTimelineVersion)
	made, err := d.Store.GetTimelineVersionByAuthor(ctx, ws, pid, string(ops.AuthorAgent), ids.Encode(ids.PrefixRun, rc.RunID))
	switch {
	case err == nil:
		v = int(made.Version)
	case !errors.Is(err, store.ErrNotFound):
		return runtime.Result{}, fmt.Errorf("look for the run's versions: %w", err)
	}
	var plan *Plan
	if p, ok := d.planOf(ctx, rc); ok {
		plan = &p
	}
	// The checks measure the rendered picture, so the version is previewed first (a run with no working renderer is let off, as it is
	// for finish).
	if d.previewsOn() && !d.previewOK(ctx, rc, v) {
		return runtime.Result{}, runtime.UserErrorf("version %d has not been previewed. Call render_preview first, then check_edit: the checks measure the rendered picture (a look can crush the footage), not only the timeline", v)
	}
	report, err := CheckVersion(ctx, d.Projects, d.Store, ws, pid, v, plan, d.Rendered)
	if err != nil {
		return runtime.Result{}, err
	}
	nth, seen := d.checkNth(ctx, rc)
	pass, fail, skip := report.Counts()
	if !seen { // a call that is run again after a restart has its event already
		rec := CheckRecord{RecordVersion: CheckRecordVersion, Report: report, Passed: pass, Failed: fail, Skipped: skip, Nth: nth}
		if err := rc.Artifact(ctx, CheckArtifact, rec); err != nil {
			return runtime.Result{}, fmt.Errorf("record the check: %w", err)
		}
	}
	d.previews.checkRun(rc.RunID, v, nth, rc.ToolUseID)
	d.previews.checkOutcome(rc.RunID, v, report.Failed())

	var b strings.Builder
	b.WriteString(report.Text())
	b.WriteString("\n\n")
	worst := d.worstCallMicros(ctx, rc)
	low := budgetLow(rc.Left, worst)
	switch {
	case fail == 0:
		b.WriteString("All the checks that apply pass. Call finish.")
	case nth == 1 && low:
		b.WriteString("Some checks fail, but you are close to your limits: do not start a revision. Call finish now and say in your message what is not as good as it should be.")
	case nth == 1:
		b.WriteString("Fix the failing checks in one batch (after a change to a shot's length, snap again with a small max_shift_ms, 60 or less: a snap moves an edge each time it runs), then render_preview and check_edit once more, and finish. This is your one revision pass: skip it if you are short of tool calls or time, or if the footage cannot fix a check, and say so in finish.")
	default:
		b.WriteString("This was the check after your revision pass. Do not revise again: call finish, and say in your message what is still not as it should be and why.")
	}
	b.WriteString(budgetNote(rc.Left, worst))
	return runtime.Result{Content: []llm.Block{llm.TextBlock(b.String())}}, nil
}

// budgetLow says the run is close to a limit: few tool calls, little time, or little money, left. A revision pass needs about five
// calls (an edit, a preview, a check, a finish) and the preview takes up to a minute. worst is the worst-case cost of one model call
// of the run (max_tokens at the output price; 0 when it is not known): the metered client reserves that much before it sends a call
// and refuses it when the run cannot cover it, so a run with fewer than two of them left cannot make a revision. (This counts the output side only, the figure the budget plan quotes; the input of a call is on top, so the warning comes a little late rather than early.)
func budgetLow(l runtime.BudgetLeft, worst int64) bool {
	return l.Known && (l.ToolCalls <= 7 || l.Wall < 3*time.Minute || (worst > 0 && l.Micros < 2*worst))
}

// budgetShortNote is the line added to the budget note when the run can no longer pay for a full-size call: the next answer will be cut
// to what is left, so a long plan or a long thought would be cut off.
const budgetShortNote = "Your budget is nearly used: keep this turn short and finish soon."

// budgetNote is what the run has left, for the end of a tool's answer ("" when it is not known): the model that sees its limits
// coming skips the revision and finishes.
func budgetNote(l runtime.BudgetLeft, worst int64) string {
	if !l.Known {
		return ""
	}
	dollars := fmt.Sprintf("$%.2f", float64(max(l.Micros, 0))/1e6)
	if l.Micros < 10_000 {
		dollars = "under $0.01"
	}
	s := fmt.Sprintf("\n\nLeft of your budget: %s, %d tool calls and %s.", dollars, max(l.ToolCalls, 0), approxDuration(l.Wall))
	if budgetLow(l, worst) {
		s += " That is not much: finish soon, and do not start work you cannot complete."
	}
	if worst > 0 && l.Micros < worst+worst/4 {
		// The metered client cuts the next call's max_tokens to what the budget can pay for (the input counts too, hence the margin over
		// the output's worst case), and the answer may stop short.
		s += " " + budgetShortNote
	}
	return s
}

func approxDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "no time"
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	return fmt.Sprintf("%d minutes", int(d.Round(time.Minute).Minutes()))
}

// worstCallMicros is the worst-case cost of one model call of the run: its max_tokens at the output price of its model, from the run
// row and the price table. 0 when either is not known (no price function wired, an unpriced model).
func (d Deps) worstCallMicros(ctx context.Context, rc runtime.RunContext) int64 {
	if d.OutputCost == nil || !rc.Left.Known {
		return 0
	}
	run, err := d.Store.GetAgentRun(ctx, rc.WorkspaceID, rc.RunID)
	if err != nil || run.MaxTokens <= 0 {
		slog.WarnContext(ctx, "editor: the run row could not be read; the dollar warning is off", "run_id", rc.RunID.String(), "error", err)
		return 0
	}
	c, err := d.OutputCost(run.Model, int64(run.MaxTokens))
	if err != nil {
		slog.WarnContext(ctx, "editor: the worst-case cost of a call is not known; the dollar warning is off", "model", run.Model, "error", err)
		return 0
	}
	return c
}

// lastCheckOf is the version of the run's newest check_edit and the checks that failed in it. The newest edit_check event is the
// truth (another worker may have made later checks than this one remembers); this worker's record answers when the events cannot be
// read.
func (d Deps) lastCheckOf(ctx context.Context, rc runtime.RunContext) (int, []string) {
	memV, memFailed := d.previews.lastCheck(rc.RunID)
	best, bestFailed := memV, memFailed
	for _, art := range runArtifacts(ctx, d.Store, rc.WorkspaceID, rc.RunID, CheckArtifact) {
		var rec CheckRecord
		if json.Unmarshal(art.Data, &rec) == nil && rec.Version >= best && rec.Version > 0 {
			best, bestFailed = rec.Version, rec.Report.Failed()
		}
	}
	return best, bestFailed
}

// onLimit ends a run that reached a limit gracefully when its work is usable: it made a version, and a version of it was previewed (or
// there is no renderer to preview with). The output is the one finish writes, with a plain note for the creator of what was not done,
// so the run succeeds instead of failing with "budget". A run that made no version, or never previewed one, fails as before.
func (d Deps) onLimit(ctx context.Context, rc runtime.RunContext, limit string) (json.RawMessage, bool) {
	pid, ws, err := project(rc)
	if err != nil {
		return nil, false
	}
	made, err := d.Store.GetTimelineVersionByAuthor(ctx, ws, pid, string(ops.AuthorAgent), ids.Encode(ids.PrefixRun, rc.RunID))
	if err != nil {
		return nil, false
	}
	v := int(made.Version)
	if d.previewsOn() && !d.anyPreview(ctx, rc) && !d.previewOK(ctx, rc, v) {
		return nil, false
	}
	what := map[string]string{"tool_calls": "steps", "wall_clock": "time", "dollars": "budget", "tokens": "budget"}[limit]
	if what == "" {
		what = "room"
	}
	var note strings.Builder
	fmt.Fprintf(&note, "I ran out of %s before I could finish polishing this video, so this is the version as it stood. It is saved as version %d.", what, v)
	switch {
	case d.previewsOn() && !d.previewOK(ctx, rc, v):
		note.WriteString(" My last change to it was not previewed, so look at it before you use it.")
	case !d.noCheckGate && !d.checked(ctx, rc, v):
		note.WriteString(" I did not get to the final checks of it.")
	default:
		if cv, failed := d.lastCheckOf(ctx, rc); cv == v && len(failed) > 0 {
			note.WriteString(" The final checks still found something to improve: " + strings.ReplaceAll(strings.Join(failed, ", "), "_", " ") + ".")
		}
	}
	out, _ := json.Marshal(map[string]any{"summary": note.String(), "timeline_version": v, "implicit_finish": true, "limit": limit})
	return out, true
}

// checkNth is which check of the run this call is, and whether the call has been through before (a call that is run again after a
// worker died answers as it did, and does not write a second event). It reads this worker's record; a worker with none (a takeover)
// or a call that is being repeated reads the run's edit_check events instead.
func (d Deps) checkNth(ctx context.Context, rc runtime.RunContext) (nth int, seen bool) {
	if n, ok := d.previews.checkNth(rc.RunID, rc.ToolUseID); ok {
		return n, true
	}
	_, calls := d.previews.checkState(rc.RunID)
	if calls == 0 || rc.Resumed {
		for _, art := range runArtifacts(ctx, d.Store, rc.WorkspaceID, rc.RunID, CheckArtifact) {
			var c struct {
				Nth int `json:"nth"`
			}
			if json.Unmarshal(art.Data, &c) != nil {
				continue
			}
			if rc.ToolUseID != "" && art.ToolUseID == rc.ToolUseID {
				return c.Nth, true
			}
			calls = max(calls, c.Nth)
		}
	}
	return calls + 1, false
}

// checked says check_edit has run on version (or on a later one) in this run.
func (d Deps) checked(ctx context.Context, rc runtime.RunContext, version int) bool {
	if v, _ := d.previews.checkState(rc.RunID); v >= version {
		return true
	}
	for _, art := range runArtifacts(ctx, d.Store, rc.WorkspaceID, rc.RunID, CheckArtifact) {
		var c struct {
			Version int `json:"version"`
			Nth     int `json:"nth"`
		}
		if json.Unmarshal(art.Data, &c) == nil && c.Version >= version {
			d.previews.checkRun(rc.RunID, c.Version, c.Nth, art.ToolUseID)
			return true
		}
	}
	return false
}

// artifact is the part of an artifact event the editor reads back: the call that made it and its data.
type artifact struct {
	ToolUseID string
	Data      json.RawMessage
}

// runArtifacts returns the run's artifact events of a kind, oldest first. A read that fails is no artifacts.
func runArtifacts(ctx context.Context, st EventSource, ws ids.WorkspaceID, run uuid.UUID, kind string) []artifact {
	events, err := st.AgentArtifactEvents(ctx, ws, run, kind)
	if err != nil {
		// The record of a run is rebuilt from its events when this worker has none; a read that fails leaves the gates asking the model
		// again (harmless) and the counts starting over, so it is said.
		slog.WarnContext(ctx, "editor: the run's artifact events could not be read", "run_id", run.String(), "kind", kind, "error", err)
		return nil
	}
	var out []artifact
	for _, ev := range events {
		var p struct {
			Kind      string          `json:"kind"`
			ToolUseID string          `json:"tool_use_id"`
			Data      json.RawMessage `json:"data"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Kind == kind {
			out = append(out, artifact{ToolUseID: p.ToolUseID, Data: p.Data})
		}
	}
	return out
}

// ---- the gates --------------------------------------------------------------------------------------------------------

const (
	noPlanMsg  = "you cannot edit the timeline yet: you have not written your plan. Call plan_edit first: say what the footage is, the story, the hook, the arc, the ending, the playbook, the target length, the tempo and the shot list. It answers with the playbook for that kind of video."
	noCheckMsg = "you cannot finish yet: you have not run check_edit on version %d. Call check_edit (after render_preview): it measures the edit and says which checks fail. Fix what it names once, preview and check again, then call finish."
	nudgeNoChk = "You ended your turn without calling finish, and you have not run check_edit on your latest version, version %d, yet. Call check_edit now, fix what it names (once), then call finish with your message for the creator."
)

// planGate returns the refusal for an edit when the run has no plan, or "" when it may edit. A run that already made a version has
// been through the gate (a resumed run in another worker finds its plan in its events, or its version).
func (d Deps) planGate(ctx context.Context, rc runtime.RunContext, pid ids.ProjectID, ws ids.WorkspaceID) (string, error) {
	if d.noPlanGate {
		return "", nil
	}
	if _, ok := d.previews.plan(rc.RunID); ok {
		return "", nil
	}
	_, err := d.Store.GetTimelineVersionByAuthor(ctx, ws, pid, string(ops.AuthorAgent), ids.Encode(ids.PrefixRun, rc.RunID))
	switch {
	case err == nil:
		return "", nil
	case !errors.Is(err, store.ErrNotFound):
		return "", fmt.Errorf("look for the run's versions: %w", err)
	}
	if _, ok := d.planOf(ctx, rc); ok {
		return "", nil
	}
	return noPlanMsg, nil
}

// checkGate returns the refusal for finish when check_edit has not run on version, or "".
func (d Deps) checkGate(ctx context.Context, rc runtime.RunContext, version int) string {
	if d.noCheckGate || d.checked(ctx, rc, version) {
		return ""
	}
	return fmt.Sprintf(noCheckMsg, version)
}
