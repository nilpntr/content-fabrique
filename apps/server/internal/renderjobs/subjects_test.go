package renderjobs

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

var titlePos = regexp.MustCompile(`\\pos\([0-9.]+,([0-9.]+)\)\}AAA`)

// titleY is the y of the title "AAA\NBBB" in the subtitle scripts of a render.
func titleY(t *testing.T, scripts string) float64 {
	t.Helper()
	m := titlePos.FindStringSubmatch(scripts)
	if m == nil {
		t.Fatalf("no title in the script:\n%s", scripts)
	}
	y, _ := strconv.ParseFloat(m[1], 64)
	return y
}

func subjectSpec(a, b store.Asset) *timeline.Spec {
	spec := shortSpec(a, b)
	spec.Tracks = append(spec.Tracks, &timeline.TextTrack{ID: "text", Items: []timeline.TextItem{
		{ID: "t1", AtMs: 0, DurationMs: 1200, Text: "AAA\nBBB", Style: "title_series", Anchor: "upper_third"},
	}})
	return spec
}

// The render job reads the project's subject boxes from the quick looks and keeps a title off the head: the same timeline renders the
// title lower with a subject at the top of the frame than without one. A database error reading the boxes fails the attempt (the job is
// retried) instead of rendering without them, so the final cannot differ from the preview.
func TestTheRenderKeepsTheTitleOffTheHeadAndRetriesWhenTheBoxesCannotBeRead(t *testing.T) {
	e := newEnv(t)
	clips := testClips(t)
	w := e.newWorkspace(t, "org_render_subjects")
	a, b := e.readyAsset(t, w, "a", clips[0]), e.readyAsset(t, w, "b", clips[1])
	p := e.newProject(t, w, a, b)
	version := e.addVersion(t, w, p, subjectSpec(a, b))
	ctx := context.Background()

	// Without boxes the title is where its anchor puts it.
	plain := &scriptRecorder{Runner: e.runner}
	r1, _ := e.request(t, w, p, version, "preview")
	if out, err := e.newPipeline(t, e.objs, plain).Run(ctx, w.ID, r1.ID); err != nil || out.Status != store.RenderReady {
		t.Fatalf("render: %+v %v", out, err)
	}
	yPlain := titleY(t, plain.joined())

	// With the head under it (a quick look's subject boxes, the head at 22 % of the frame) it is moved off the person.
	detail := []byte(`{"prompt_version":"quicklook.v2","subjects":{"aspect":0.5625,"samples":[{"at_ms":500,"x":0.2,"y":0.2,"w":0.6,"h":0.75,"head_top_y":0.22}]}}`)
	if _, err := e.st.ApplyQuickLook(ctx, w.ID, a.ID, store.QuickLookResult{Title: "Row", Description: "A row.", Facets: []string{"movement:row"}, Detail: detail}); err != nil {
		t.Fatal(err)
	}
	moved := &scriptRecorder{Runner: e.runner}
	r2, _ := e.request(t, w, p, version, "final")
	if out, err := e.newPipeline(t, e.objs, moved).Run(ctx, w.ID, r2.ID); err != nil || out.Status != store.RenderReady {
		t.Fatalf("render with boxes: %+v %v", out, err)
	}
	if yMoved := titleY(t, moved.joined()); math.Abs(yMoved-yPlain) < 20 {
		t.Errorf("the title was not moved off the head: y %v with the boxes, %v without", yMoved, yPlain)
	}
	if !strings.Contains(moved.joined(), "AAA") {
		t.Error("the title is gone")
	}

	// A database error reading the boxes: the attempt fails with an error that is retried, and nothing is rendered without them.
	pipe := e.newPipeline(t, e.objs, &scriptRecorder{Runner: e.runner})
	pipe.cfg.subjects = func(context.Context, ids.WorkspaceID, []ids.AssetID) (subject.Index, error) {
		return nil, errors.New("the database is away")
	}
	spec3 := subjectSpec(a, b)
	txt, _ := spec3.Tracks[len(spec3.Tracks)-1].(*timeline.TextTrack)
	txt.Items[0].DurationMs = 1100 // another version, so a new render
	r3, _ := e.request(t, w, p, e.addVersion(t, w, p, spec3), "final")
	_, err := pipe.Run(ctx, w.ID, r3.ID)
	var fe *fileError
	if err == nil || errors.As(err, &fe) || errors.Is(err, errGone) || !strings.Contains(err.Error(), "subject boxes") {
		t.Fatalf("Run = %v, want an error that is retried", err)
	}
	if status, _, _ := e.row(t, r3.ID); status != store.RenderRendering {
		t.Errorf("status %s, want rendering until the last attempt", status)
	}
	// The retry, with the database back, renders it.
	pipe.cfg.subjects = nil
	if out, err := pipe.Run(ctx, w.ID, r3.ID); err != nil || out.Status != store.RenderReady {
		t.Fatalf("retry: %+v %v", out, err)
	}
}

// A timeline with no title that keeps off the subject does not read the boxes at all: a database error there does not touch its render.
func TestTheBoxesAreReadOnlyForATimelineThatNeedsThem(t *testing.T) {
	e := newEnv(t)
	clips := testClips(t)
	w := e.newWorkspace(t, "org_render_nosubjects")
	a, b := e.readyAsset(t, w, "a", clips[0]), e.readyAsset(t, w, "b", clips[1])
	p := e.newProject(t, w, a, b)
	ctx := context.Background()
	reads := 0
	pipe := e.newPipeline(t, e.objs, &scriptRecorder{Runner: e.runner})
	pipe.cfg.subjects = func(context.Context, ids.WorkspaceID, []ids.AssetID) (subject.Index, error) {
		reads++
		return nil, errors.New("the database is away")
	}
	plain := e.addVersion(t, w, p, shortSpec(a, b))
	r1, _ := e.request(t, w, p, plain, "preview")
	if out, err := pipe.Run(ctx, w.ID, r1.ID); err != nil || out.Status != store.RenderReady || reads != 0 {
		t.Fatalf("no text: %+v %v (%d reads)", out, err, reads)
	}
	// A title that opts out of avoiding the subject does not need them either.
	spec := subjectSpec(a, b)
	txt, _ := spec.Tracks[len(spec.Tracks)-1].(*timeline.TextTrack)
	off := false
	txt.Items[0].AvoidSubject = &off
	optOut := e.addVersion(t, w, p, spec)
	r2, _ := e.request(t, w, p, optOut, "preview")
	if out, err := pipe.Run(ctx, w.ID, r2.ID); err != nil || out.Status != store.RenderReady || reads != 0 {
		t.Fatalf("avoid_subject false: %+v %v (%d reads)", out, err, reads)
	}
}
