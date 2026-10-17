package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// find_moments: the clip analysis (P2-02, P2-03, deterministic P2-09) as the editor reads it. The analysis finds, without a model,
// where the reps of a set start and end (the turning points of the movement), the still holds of a pose, and the dead time between
// them, and scores each by how clean and how well exposed it is. Everything it says is computed from the pixels, so none of it is
// untrusted text; the clip's own title and tags never appear in what this tool returns.

// Limits of find_moments.
const (
	defaultMoments = 10
	maxMoments     = 20
	maxDeadSpans   = 6
)

type findMomentsArgs struct {
	AssetID  string   `json:"asset_id"`
	Kind     string   `json:"kind"`
	MinScore *float64 `json:"min_score"`
	Limit    *int     `json:"limit"`
}

// projectVideos returns the project's ready video clips, in the creator's order.
func (d Deps) projectVideos(ctx context.Context, ws ids.WorkspaceID, pid ids.ProjectID) ([]store.ProjectFootageRow, error) {
	rows, err := d.Store.ListProjectFootage(ctx, ws, pid)
	if err != nil {
		return nil, fmt.Errorf("list footage: %w", err)
	}
	return slices.DeleteFunc(rows, func(r store.ProjectFootageRow) bool { return r.Kind != store.AssetKindVideo }), nil
}

func (d Deps) findMoments(ctx context.Context, rc runtime.RunContext, in findMomentsArgs) (runtime.Result, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.Result{}, err
	}
	kinds, err := momentKinds(in.Kind)
	if err != nil {
		return runtime.Result{}, err
	}
	minScore := 0.0
	if in.MinScore != nil {
		if *in.MinScore < 0 || *in.MinScore > 1 {
			return runtime.Result{}, runtime.UserErrorf("min_score is a number from 0 to 1; scores of 0.7 and above are clean")
		}
		minScore = *in.MinScore
	}
	limit, clamped := defaultMoments, false
	if in.Limit != nil {
		limit = *in.Limit
		if limit < 1 {
			return runtime.Result{}, runtime.UserErrorf("limit must be at least 1 (and at most %d)", maxMoments)
		}
		if limit > maxMoments {
			limit, clamped = maxMoments, true
		}
	}

	videos, err := d.projectVideos(ctx, ws, pid)
	if err != nil {
		return runtime.Result{}, err
	}
	scope := videos
	if strings.TrimSpace(in.AssetID) != "" {
		aid, perr := ids.ParseAssetID(in.AssetID)
		i := slices.IndexFunc(videos, func(r store.ProjectFootageRow) bool { return perr == nil && r.AssetID == aid })
		if i < 0 {
			return runtime.Result{}, runtime.UserErrorf("%s is not one of this project's video clips. list_footage shows the clips you can use", in.AssetID)
		}
		scope = videos[i : i+1]
	}
	if len(scope) == 0 {
		return runtime.Text("This project has no video clips, so there are no moments."), nil
	}
	assets := make([]ids.AssetID, len(scope))
	for i, r := range scope {
		assets[i] = r.AssetID
	}

	sums, err := d.Store.ListAssetAnalysisSummaries(ctx, ws, assets)
	if err != nil {
		return runtime.Result{}, fmt.Errorf("find_moments: summaries: %w", err)
	}
	analysed := map[ids.AssetID]bool{}
	for _, s := range sums {
		analysed[s.AssetID] = true
	}
	var waiting []string
	for _, a := range assets {
		if !analysed[a] {
			waiting = append(waiting, a.String())
		}
	}
	if len(analysed) == 0 {
		return runtime.Text("None of these clips has been analysed yet (the analysis runs shortly after an upload is processed), so there are no moments to list. Use view_clip to choose moments by eye."), nil
	}

	moments, err := d.Store.ListMoments(ctx, ws, store.MomentFilter{Assets: assets, Kinds: kinds, MinScore: minScore, Limit: limit})
	if err != nil {
		return runtime.Result{}, fmt.Errorf("find_moments: %w", err)
	}
	var b strings.Builder
	if len(moments) == 0 {
		b.WriteString("No moments match")
		if minScore > 0 {
			fmt.Fprintf(&b, " (score at least %.2f; try a lower min_score)", minScore)
		}
		b.WriteString(". ")
	} else {
		fmt.Fprintf(&b, "%d %s, best first. Times are in the clip's own timeline, in milliseconds: use start_ms and end_ms as a clip's in and out points to cut on the rep boundaries. A rep runs from one turning point of the movement to the same turning point of the next, so cutting there never lands mid-rep. Scores run from 0 to 1; 0.7 and above are clean.\n", len(moments), plural(len(moments), "moment", "moments"))
		for i, m := range moments {
			fmt.Fprintf(&b, "%d. %s | %d to %d ms (%s to %s) | %s | score %.2f | %s\n",
				i+1, m.AssetID, m.StartMs, m.EndMs, secs(int64(m.StartMs)), secs(int64(m.EndMs)), m.Kind, m.Score, m.Description)
		}
	}
	if clamped {
		fmt.Fprintf(&b, "(limit lowered to %d.) ", maxMoments)
	}
	b.WriteString(d.deadTimeNote(ctx, ws, assets))
	if len(waiting) > 0 {
		fmt.Fprintf(&b, "\nNot analysed yet, so not covered here: %s.", strings.Join(waiting, ", "))
	}
	return runtime.Text(strings.TrimSpace(b.String())), nil
}

// momentKinds turns the tool's kind into the moment kinds to search.
func momentKinds(kind string) ([]string, error) {
	switch kind {
	case "", "any":
		return []string{store.MomentRep, store.MomentHold}, nil
	case "rep":
		return []string{store.MomentRep}, nil
	case "hold":
		return []string{store.MomentHold}, nil
	}
	return nil, runtime.UserErrorf("kind must be rep, hold or any")
}

// deadTimeNote lists the stretches to skip (dead time) of the assets: at most maxDeadSpans per clip, in time order.
func (d Deps) deadTimeNote(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) string {
	dead, err := d.Store.ListMoments(ctx, ws, store.MomentFilter{Assets: assets, Kinds: []string{store.MomentDead}, Limit: 200})
	if err != nil || len(dead) == 0 {
		return ""
	}
	byAsset := map[ids.AssetID][]store.Moment{}
	for _, m := range dead {
		byAsset[m.AssetID] = append(byAsset[m.AssetID], m)
	}
	var b strings.Builder
	b.WriteString("\nDead time to skip (nothing useful happens):")
	for _, a := range assets {
		spans := byAsset[a]
		if len(spans) == 0 {
			continue
		}
		slices.SortFunc(spans, func(x, y store.Moment) int { return int(x.StartMs) - int(y.StartMs) })
		parts := make([]string, 0, maxDeadSpans)
		for i, m := range spans {
			if i == maxDeadSpans {
				parts = append(parts, fmt.Sprintf("and %d more", len(spans)-maxDeadSpans))
				break
			}
			parts = append(parts, fmt.Sprintf("%d to %d ms", m.StartMs, m.EndMs))
		}
		fmt.Fprintf(&b, "\n- %s: %s", a, strings.Join(parts, ", "))
	}
	return b.String()
}

// analysisSummaries reads the one-line summaries of the clips for the footage listing. A clip that is not analysed has none.
func (d Deps) analysisSummaries(ctx context.Context, ws ids.WorkspaceID, rows []store.ProjectFootageRow) (map[ids.AssetID]analysis.Summary, error) {
	var assets []ids.AssetID
	for _, r := range rows {
		if r.Kind == store.AssetKindVideo {
			assets = append(assets, r.AssetID)
		}
	}
	got, err := d.Store.ListAssetAnalysisSummaries(ctx, ws, assets)
	if err != nil {
		return nil, fmt.Errorf("analysis summaries: %w", err)
	}
	out := make(map[ids.AssetID]analysis.Summary, len(got))
	for _, g := range got {
		var s analysis.Summary
		if json.Unmarshal(g.Summary, &s) == nil {
			out[g.AssetID] = s
		}
	}
	return out, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
