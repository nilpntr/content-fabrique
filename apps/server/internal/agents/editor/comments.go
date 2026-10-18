package editor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	// OpenForProject returns a page of the open comments on the project's renders (offset skips that many; a page is at most
	// annotations.MaxEditorComments), resolved through their time maps, and how many open comments there are in all.
	OpenForProject(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, offset int) ([]annotations.Comment, int, error)
	// RecordHandoff notes that the run was shown these comments: only they are marked applied when the run ends well.
	RecordHandoff(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, run uuid.UUID, comments []annotations.Comment) error
	// RunEnded marks the comments the run was handed as applied by it, in the version it made, if the run succeeded. The worker calls
	// it once the run's result is stored (Agent.OnEnd).
	RunEnded(ctx context.Context, run uuid.UUID) (int, error)
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
		case *timeline.GraphicsTrack:
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

// itemsOf is what the comments need to know about a spec.
func itemsOf(version int, spec *timeline.Spec) timelineItems {
	return timelineItems{version: version, ids: itemIDs(spec)}
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
	return itemsOf(v, spec), nil
}

// openComments reads a page of the comments, or none without a source.
func (d Deps) openComments(ctx context.Context, ws ids.WorkspaceID, pid ids.ProjectID, offset int) ([]annotations.Comment, int, error) {
	if d.Comments == nil {
		return nil, 0, nil
	}
	cs, total, err := d.Comments.OpenForProject(ctx, ws, pid, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("open comments: %w", err)
	}
	return cs, total, nil
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

// commentsIntro is the run context's part about comments: how many there are, and the first few (shown of them). get_comments has
// all of them, a page at a time; only the ones the run is shown here, or that get_comments lists, are the run's to deal with.
func commentsIntro(cs []annotations.Comment, total int, now timelineItems, shown, bodyChars int) []llm.Block {
	if len(cs) == 0 {
		return nil
	}
	shown = min(shown, len(cs))
	requests := 0
	for _, c := range cs {
		if !standing(c.Annotation.Kind) {
			requests++
		}
	}
	head := fmt.Sprintf("The creator left %d open comments on earlier versions. %s", total, commentsGuide)
	if shown >= total {
		head += fmt.Sprintf(" They follow (%d requests, %d constraints).", requests, len(cs)-requests)
	} else {
		head += fmt.Sprintf(" The first %d follow; %d more are not shown here. Call get_comments to read them: it lists %d at a time (offset 0 is the first page), and a comment you have not read is not one you will be taken to have dealt with.", shown, total-shown, annotations.MaxEditorComments)
	}
	blocks := []llm.Block{llm.TextBlock(head)}
	if shown > 0 {
		var b strings.Builder
		for _, c := range cs[:shown] {
			b.WriteString(commentText(c, now, bodyChars))
		}
		blocks = append(blocks, llm.TextBlock(strings.TrimRight(b.String(), "\n")))
	}
	return blocks
}

// ---- get_comments -----------------------------------------------------------------------------------------------

type getCommentsArgs struct {
	// Offset is how many of the open comments to skip: 0 (or none) for the first page.
	Offset int `json:"offset"`
}

func (d Deps) getComments(ctx context.Context, rc runtime.RunContext, in getCommentsArgs) (runtime.Result, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.Result{}, err
	}
	det, perr := d.Projects.Get(ctx, actor(ws), pid)
	if perr != nil {
		return runtime.Result{}, projectErr(perr)
	}
	if in.Offset < 0 {
		return runtime.Result{}, runtime.UserErrorf("offset cannot be negative")
	}
	cs, total, err := d.openComments(ctx, ws, pid, in.Offset)
	if err != nil {
		return runtime.Result{}, err
	}
	if total == 0 {
		return runtime.Text("There are no open comments on this project."), nil
	}
	if len(cs) == 0 {
		return runtime.Text(fmt.Sprintf("There are %d open comments and none starts at offset %d. Use an offset below %d.", total, in.Offset, total)), nil
	}
	now, err := d.currentItems(ctx, ws, pid, det.Project.CurrentTimelineVersion)
	if err != nil {
		return runtime.Result{}, err
	}
	// What this call lists is what the run has been handed.
	if err := d.Comments.RecordHandoff(ctx, ws, pid, rc.RunID, cs); err != nil {
		return runtime.Result{}, err
	}
	end := in.Offset + len(cs)
	var b strings.Builder
	fmt.Fprintf(&b, "Open comments %d to %d of %d, oldest first. The current timeline is version %d. %s\n", in.Offset+1, end, total, now.version, commentsGuide)
	if end < total {
		fmt.Fprintf(&b, "%d more remain: call get_comments again with offset %d for the next page.\n", total-end, end)
	}
	b.WriteByte('\n')
	for _, c := range cs {
		b.WriteString(commentText(c, now, 0))
	}
	return runtime.Text(strings.TrimRight(b.String(), "\n")), nil
}

// endRun is the agent's OnEnd: the run has ended and its result is stored, so the comments it was handed are applied by it (if it
// succeeded). It cannot fail the run, which is over; a failure is logged, and the comments stay open for the next run.
func (d Deps) endRun(run uuid.UUID) {
	if d.Comments == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.Comments.RunEnded(ctx, run); err != nil {
		slog.ErrorContext(ctx, "editor: marking the comments of an ended run failed; they stay open", "run", run.String(), "error", err)
	}
}
