package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	ListAssetMoments(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) ([]store.Moment, error)
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
	for _, aid := range assets {
		a, ok := out[aid.String()]
		if !ok {
			continue
		}
		moments, err := src.ListAssetMoments(ctx, ws, aid)
		if err != nil {
			return nil, fmt.Errorf("read the moments of %s: %w", aid, err)
		}
		for _, m := range moments {
			sp := Span{StartMs: int64(m.StartMs), EndMs: int64(m.EndMs)}
			switch m.Kind {
			case store.MomentRep:
				a.Reps = append(a.Reps, sp)
			case store.MomentHold:
				a.Holds = append(a.Holds, sp)
			case store.MomentDead:
				a.Dead = append(a.Dead, sp)
			}
		}
		out[aid.String()] = a
	}
	return out, nil
}

// CheckVersion runs the checks over one stored version of a project: what check_edit does, and what cfctl eval editor prints.
func CheckVersion(ctx context.Context, projects Projects, src AnalysisSource, ws ids.WorkspaceID, pid ids.ProjectID, version int, plan *Plan) (Report, error) {
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
	return RunChecks(CheckInput{Version: version, Spec: spec, Plan: plan, Assets: assets, Issues: vd.Issues}), nil
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
	v := int(*det.Project.CurrentTimelineVersion)
	var plan *Plan
	if p, ok := d.planOf(ctx, rc); ok {
		plan = &p
	}
	report, err := CheckVersion(ctx, d.Projects, d.Store, ws, pid, v, plan)
	if err != nil {
		return runtime.Result{}, err
	}
	nth := d.checksRun(ctx, rc) + 1
	pass, fail, skip := report.Counts()
	rec := CheckRecord{RecordVersion: CheckRecordVersion, Report: report, Passed: pass, Failed: fail, Skipped: skip, Nth: nth}
	if err := rc.Artifact(ctx, CheckArtifact, rec); err != nil {
		return runtime.Result{}, fmt.Errorf("record the check: %w", err)
	}
	d.previews.checkRun(rc.RunID, v, nth)

	var b strings.Builder
	b.WriteString(report.Text())
	b.WriteString("\n\n")
	switch {
	case fail == 0:
		b.WriteString("All the checks that apply pass. Call finish.")
	case nth == 1:
		b.WriteString("Fix the failing checks in one batch (a change to a shot's length is followed by a new snap_cuts_to_beats), then render_preview and check_edit once more, and finish. This is your one revision pass: skip it if you are short of tool calls or time, or if the footage cannot fix a check, and say so in finish.")
	default:
		b.WriteString("This was the check after your revision pass. Do not revise again: call finish, and say in your message what is still not as it should be and why.")
	}
	return runtime.Result{Content: []llm.Block{llm.TextBlock(b.String())}}, nil
}

// checksRun is how many times check_edit has run in this run: this worker's record, or (for a run that resumed in another worker)
// the edit_check artifacts of the run.
func (d Deps) checksRun(ctx context.Context, rc runtime.RunContext) int {
	if _, n := d.previews.checkState(rc.RunID); n > 0 {
		return n
	}
	n := 0
	for _, rec := range runArtifacts(ctx, d.Store, rc.WorkspaceID, rc.RunID, CheckArtifact) {
		var c struct {
			Nth int `json:"nth"`
		}
		if json.Unmarshal(rec, &c) == nil {
			n = max(n, c.Nth)
		}
	}
	return n
}

// checked says check_edit has run on version (or on a later one) in this run.
func (d Deps) checked(ctx context.Context, rc runtime.RunContext, version int) bool {
	if v, _ := d.previews.checkState(rc.RunID); v >= version {
		return true
	}
	for _, rec := range runArtifacts(ctx, d.Store, rc.WorkspaceID, rc.RunID, CheckArtifact) {
		var c struct {
			Version int `json:"version"`
			Nth     int `json:"nth"`
		}
		if json.Unmarshal(rec, &c) == nil && c.Version >= version {
			d.previews.checkRun(rc.RunID, c.Version, c.Nth)
			return true
		}
	}
	return false
}

// runArtifacts returns the data of the run's artifact events of a kind, oldest first. A read that fails is no artifacts.
func runArtifacts(ctx context.Context, st EventSource, ws ids.WorkspaceID, run uuid.UUID, kind string) []json.RawMessage {
	events, err := st.AgentEvents(ctx, ws, run, 0, 0)
	if err != nil {
		return nil
	}
	var out []json.RawMessage
	for _, ev := range events {
		if ev.Type != store.EventArtifact {
			continue
		}
		var p struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Kind == kind {
			out = append(out, p.Data)
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
