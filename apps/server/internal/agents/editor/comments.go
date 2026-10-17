package editor

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/annotations"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The revision loop (architecture section 7.2): the creator's comments on a render reach the next run. Each is resolved through the
// time map of the render it was made on to the timeline items that played in its range, and the run is told which of those items are
// still in the timeline. What the creator wrote is untrusted data, as the brief is. Pinned frames with the pin drawn on them come
// later; for now the pin is given as numbers.

// ToolGetComments is the tool that lists the open comments.
const ToolGetComments = "get_comments"

// CommentSource is where the editor reads the creator's open comments from, and where it reports that a run dealt with them.
// *annotations.Source is the real one. Deps.Comments may be nil (a process without comments): the run then has none.
type CommentSource interface {
	// OpenForProject returns the open comments on the project's renders, resolved through their time maps.
	OpenForProject(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID) ([]annotations.Comment, error)
	// MarkAddressed marks the comments and instructions the run was handed as applied by the run, in the version it made (nil: none).
	MarkAddressed(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, run uuid.UUID, version *int) (int, error)
}

// timelineItems is what the comments need to know about the current timeline: its version and the ids of its items.
type timelineItems struct {
	version int
	ids     map[string]bool
}

func itemIDs(spec *timeline.Spec) map[string]bool {
	out := map[string]bool{}
	for _, t := range spec.Tracks {
		switch t := t.(type) {
		case *timeline.VideoTrack:
			for _, it := range t.Items {
				out[it.ID] = true
			}
		case *timeline.TextTrack:
			for _, it := range t.Items {
				out[it.ID] = true
			}
		case *timeline.AudioTrack:
			for _, it := range t.Items {
				out[it.Common().ID] = true
			}
		}
	}
	return out
}

// currentItems reads the project's current version and the ids of its items; version 0 and no items before the first edit.
func (d Deps) currentItems(ctx context.Context, ws ids.WorkspaceID, pid ids.ProjectID, current *int32) (timelineItems, error) {
	if current == nil {
		return timelineItems{ids: map[string]bool{}}, nil
	}
	v := int(*current)
	vd, perr := d.Projects.Version(ctx, actor(ws), pid, v)
	if perr != nil {
		return timelineItems{}, projectErr(perr)
	}
	spec, err := timeline.Parse(vd.Version.Spec)
	if err != nil {
		return timelineItems{}, fmt.Errorf("parse stored spec: %w", err)
	}
	return timelineItems{version: v, ids: itemIDs(spec)}, nil
}

// openComments reads the comments, or none without a source.
func (d Deps) openComments(ctx context.Context, ws ids.WorkspaceID, pid ids.ProjectID) ([]annotations.Comment, error) {
	if d.Comments == nil {
		return nil, nil
	}
	cs, err := d.Comments.OpenForProject(ctx, ws, pid)
	if err != nil {
		return nil, fmt.Errorf("open comments: %w", err)
	}
	return cs, nil
}

// pct formats a 0..1 position as a whole percent.
func pct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

// commentText describes one comment. bodyChars cuts the creator's words (0 or less: whole).
func commentText(c annotations.Comment, now timelineItems, bodyChars int) string {
	a := c.Annotation
	var b strings.Builder
	version := 0
	if a.TimelineVersion != nil {
		version = int(*a.TimelineVersion)
	}
	when := secs2(a.StartMs)
	if a.EndMs > a.StartMs {
		when += " to " + secs2(a.EndMs)
	}
	fmt.Fprintf(&b, "- %s | %s | made on version %d, at %s of that version's video", a.ID, a.Kind, version, when)
	if p := c.Pin; p != nil {
		if p.W != nil && p.H != nil {
			fmt.Fprintf(&b, " | pinned on the frame: a box from x %s, y %s, %s wide and %s high (from the top left)", pct(p.X), pct(p.Y), pct(*p.W), pct(*p.H))
		} else {
			fmt.Fprintf(&b, " | pinned on the frame at x %s, y %s (from the top left)", pct(p.X), pct(p.Y))
		}
	}
	b.WriteByte('\n')
	if body := strings.TrimSpace(a.Body); body != "" {
		if bodyChars > 0 {
			body = cutRunes(body, bodyChars)
		}
		fmt.Fprintf(&b, "  text: %s\n", runtime.Untrusted("comment", body))
	}
	switch {
	case !c.Mapped:
		b.WriteString("  plays there: unknown (that version has no finished render to map the time through)\n")
	case len(c.Items) == 0:
		b.WriteString("  plays there: nothing (no item is on screen in that range)\n")
	default:
		for _, s := range c.Items {
			where := "text"
			if s.AssetID != "" {
				where = "clip " + s.AssetID
				if s.SourceInMs != nil && s.SourceOutMs != nil {
					where += fmt.Sprintf(", source %s to %s", secs2(*s.SourceInMs), secs2(*s.SourceOutMs))
				}
			}
			state := "still in the current timeline"
			if !now.ids[s.ItemID] {
				state = "no longer in the current timeline"
			}
			fmt.Fprintf(&b, "  plays there: item %s (%s), %s to %s of the video, %s\n", s.ItemID, where, secs2(s.OutStartMs), secs2(s.OutEndMs), state)
		}
	}
	return b.String()
}

// standingKinds are the kinds that state a constraint on a range rather than a request: they stay open for every run.
func standing(k string) bool {
	return k == store.AnnotationInclude || k == store.AnnotationExclude || k == store.AnnotationPrefer
}

const commentsGuide = "Comments are the creator's requests for this revision. Make the edit each one asks for, using the items it points at; if one cannot be done, say so in finish. Comments of kind include, exclude and prefer are constraints on that range of the video that hold for every revision. A comment on an older version points at items by id: an item that is no longer in the timeline has been changed or removed since, so find what now plays where the creator meant."

// commentsIntro is the run context's part about comments: a count, and the first few. get_comments has all of them.
func commentsIntro(cs []annotations.Comment, now timelineItems, shown, bodyChars int) []llm.Block {
	if len(cs) == 0 {
		return nil
	}
	requests := 0
	for _, c := range cs {
		if !standing(c.Annotation.Kind) {
			requests++
		}
	}
	head := fmt.Sprintf("The creator left %d open comments on earlier versions (%d requests, %d constraints). %s", len(cs), requests, len(cs)-requests, commentsGuide)
	if shown >= len(cs) {
		head += " They follow."
	} else {
		head += " get_comments lists all of them."
	}
	blocks := []llm.Block{llm.TextBlock(head)}
	if shown > 0 {
		var b strings.Builder
		for i, c := range cs {
			if i >= shown {
				break
			}
			b.WriteString(commentText(c, now, bodyChars))
		}
		blocks = append(blocks, llm.TextBlock(strings.TrimRight(b.String(), "\n")))
	}
	return blocks
}

// ---- get_comments -----------------------------------------------------------------------------------------------

func (d Deps) getComments(ctx context.Context, rc runtime.RunContext, _ noArgs) (runtime.Result, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.Result{}, err
	}
	det, perr := d.Projects.Get(ctx, actor(ws), pid)
	if perr != nil {
		return runtime.Result{}, projectErr(perr)
	}
	cs, err := d.openComments(ctx, ws, pid)
	if err != nil {
		return runtime.Result{}, err
	}
	if len(cs) == 0 {
		return runtime.Text("There are no open comments on this project."), nil
	}
	now, err := d.currentItems(ctx, ws, pid, det.Project.CurrentTimelineVersion)
	if err != nil {
		return runtime.Result{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d open comments, oldest first. The current timeline is version %d. %s\n\n", len(cs), now.version, commentsGuide)
	for _, c := range cs {
		b.WriteString(commentText(c, now, 0))
	}
	return runtime.Text(strings.TrimRight(b.String(), "\n")), nil
}

// markCommentsAddressed is the end of a run that finished: the comments it was handed are applied by it.
func (d Deps) markCommentsAddressed(ctx context.Context, rc runtime.RunContext, ws ids.WorkspaceID, pid ids.ProjectID, version *int) error {
	if d.Comments == nil {
		return nil
	}
	if _, err := d.Comments.MarkAddressed(ctx, ws, pid, rc.RunID, version); err != nil {
		return fmt.Errorf("mark the comments addressed: %w", err)
	}
	return nil
}
