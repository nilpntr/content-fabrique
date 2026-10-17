package editor

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// match_exposure: brightness matching, computed in Go. Shots from different angles meet at a cut at different brightness (the posing
// clip of the founder's footage is 1.9 times as bright as the gym clips), and the editor is told never to invent an exposure_ev, so
// this tool does it from the analysis: it measures the lit part of each clip's used range (the median p90 luma, never the mean, which
// calls low-key footage dark), takes the brightness the timeline mostly has (the median of the shots, weighted by their length) as the
// target, and sets a bounded exposure hint on each clip that is brighter than it, so the render darkens it toward the target.
//
// What the hint does is in docs/design/timeline-v1.md ("Looks and exposure"): the render scales the luma by 2^-ev below the middle of
// the range and keeps the black point and the white point. A low-key shot lies entirely below that point, so its p90 scales by the
// gain and the p90 is the right number to match. A shot whose p90 is above the middle moves less than the gain says; the hint is
// bounded all the same, and `check_edit` measures the render, so a hint that did too little shows as a jump.
//
// Only the brighter shots are darkened. Lifting a dark clip would wash out the low-key footage the hints exist to protect, so a clip
// darker than the target stays as it is.

// Bounds of the hints: the render applies at most 0.7 stops and ignores a hint under 0.15, so none is written outside.
const (
	maxExposureEV = 0.7
	minExposureEV = 0.15
	// minMatchP90 is the p90 below which a clip is not matched: it is underexposed, not a clip to compare.
	minMatchP90 = 0.06
)

// ExposureHint is what match_exposure sets on one clip.
type ExposureHint struct {
	ID string
	// P90 is the clip's lit-part brightness over the range the shot plays (0 to 1) and Current its hint before this call.
	P90     float64
	Current float64
	// EV is the hint to set: 0 clears it.
	EV float64
}

// MatchExposure works out the hints of a spec's clips from the analysis. target is the p90 luma they are matched toward and
// measured how many clips could be measured; a clip with no analysis series is left alone. The hints are only those that change
// something: a clip whose hint is already right is not in the list.
func MatchExposure(spec *timeline.Spec, assets map[string]AssetAnalysis) (target float64, hints []ExposureHint, measured int) {
	main, _, ok := timeline.MainTrack(spec)
	if !ok {
		return 0, nil, 0
	}
	type shot struct {
		it     timeline.VideoItem
		p90    float64
		weight int64
	}
	var shots []shot
	for _, it := range main.Items {
		a, ok := assets[it.AssetID]
		if !ok || !a.Analysed || !a.HasSeries {
			continue
		}
		e, ok := a.Series.ExposureBetween(int(it.Source.InMs), int(it.Source.OutMs))
		if !ok || e.P90 < minMatchP90 {
			continue
		}
		shots = append(shots, shot{it, e.P90, max(timeline.OutputDuration(it), 1)})
	}
	if len(shots) < 2 {
		return 0, nil, len(shots)
	}
	// The weighted median of the shots' p90.
	sorted := slices.Clone(shots)
	slices.SortStableFunc(sorted, func(a, b shot) int { return cmp.Compare(a.p90, b.p90) })
	var total, run int64
	for _, s := range sorted {
		total += s.weight
	}
	target = sorted[len(sorted)-1].p90
	for _, s := range sorted {
		run += s.weight
		if run*2 >= total {
			target = s.p90
			break
		}
	}
	for _, s := range shots {
		ev := 0.0
		if raw := math.Log2(s.p90 / target); raw >= minExposureEV {
			ev = math.Round(math.Min(raw, maxExposureEV)*100) / 100
		}
		cur := 0.0
		if s.it.ExposureEv != nil {
			cur = *s.it.ExposureEv
		}
		if math.Abs(ev-cur) < 0.005 {
			continue
		}
		hints = append(hints, ExposureHint{ID: s.it.ID, P90: s.p90, Current: cur, EV: ev})
	}
	return target, hints, len(shots)
}

// hintOps turns the hints into set_look ops. A clip keeps the preset it has (none when it has no look), and gets match on, so the
// render applies the hint; a hint of 0 clears it.
func hintOps(spec *timeline.Spec, hints []ExposureHint) ([]map[string]any, error) {
	main, _, ok := timeline.MainTrack(spec)
	if !ok {
		return nil, fmt.Errorf("the timeline has no main track")
	}
	byID := map[string]timeline.VideoItem{}
	for _, it := range main.Items {
		byID[it.ID] = it
	}
	var out []map[string]any
	for _, h := range hints {
		preset := timeline.LookNone
		if l := byID[h.ID].Look; l != nil && l.Preset != "" {
			preset = l.Preset
		}
		op := map[string]any{"op": "set_look", "id": h.ID, "preset": preset, "exposure_ev": h.EV}
		if h.EV != 0 {
			op["match"] = true
		}
		out = append(out, op)
	}
	return out, nil
}

// matchExposure is the match_exposure tool.
func (d Deps) matchExposure(ctx context.Context, rc runtime.RunContext, _ noArgs) (runtime.Result, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.Result{}, err
	}
	det, perr := d.Projects.Get(ctx, actor(ws), pid)
	if perr != nil {
		return runtime.Result{}, projectErr(perr)
	}
	if det.Project.CurrentTimelineVersion == nil {
		return runtime.Result{}, runtime.UserErrorf("there is no timeline yet; make one with edit_timeline first")
	}
	v := int(*det.Project.CurrentTimelineVersion)
	vd, perr := d.Projects.Version(ctx, actor(ws), pid, v)
	if perr != nil {
		return runtime.Result{}, projectErr(perr)
	}
	spec, err := timeline.Parse(vd.Version.Spec)
	if err != nil {
		return runtime.Result{}, fmt.Errorf("parse stored spec: %w", err)
	}
	assets, err := LoadAssetAnalysis(ctx, d.Store, ws, spec)
	if err != nil {
		return runtime.Result{}, err
	}
	target, hints, measured := MatchExposure(spec, assets)
	switch {
	case measured < 2:
		return runtime.Text("Nothing to match: fewer than two clips of this timeline have an analysed brightness, so there is nothing to compare."), nil
	case len(hints) == 0:
		return runtime.Text(fmt.Sprintf("Nothing to change: the %d measured clips are within %.2f stops of the brightness the timeline mostly has (p90 luma %.2f).", measured, minExposureEV, target)), nil
	}
	ops, err := hintOps(spec, hints)
	if err != nil {
		return runtime.Result{}, err
	}
	raw, err := json.Marshal(ops)
	if err != nil {
		return runtime.Result{}, err
	}
	res, err := d.editTimeline(ctx, rc, editArgs{BaseVersion: v, Ops: raw})
	if err != nil {
		return runtime.Result{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Matched the brightness of %d clips toward p90 luma %.2f (the median of the timeline's shots; only the brighter ones are darkened, by at most %.1f stops, and a clip is left alone under %.2f stops):\n", measured, target, maxExposureEV, minExposureEV)
	for _, h := range hints {
		if h.EV == 0 {
			fmt.Fprintf(&b, "- %s: p90 %.2f, hint cleared\n", h.ID, h.P90)
		} else {
			fmt.Fprintf(&b, "- %s: p90 %.2f, exposure_ev %.2f\n", h.ID, h.P90, h.EV)
		}
	}
	b.WriteString("Render a preview and check_edit again: it measures the picture.\n")
	res.Content = append([]llm.Block{llm.TextBlock(b.String())}, res.Content...)
	return res, nil
}
