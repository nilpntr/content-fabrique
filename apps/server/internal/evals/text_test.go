package evals

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// D-38: titles keep off the body, story captions off the face; a failure of any of the placement checks fails the feedback item.
func TestTextPlacementFeedbackReadsEveryPlacementCheck(t *testing.T) {
	for _, c := range []struct {
		checks []editor.Check
		want   string
	}{
		{[]editor.Check{{ID: editor.CheckTextOverSubject, Status: editor.CheckPass}, {ID: editor.CheckTextOverFace, Status: editor.CheckFail, Detail: "caption on the face"}}, Fail},
		{[]editor.Check{{ID: editor.CheckOverlayOverSubject, Status: editor.CheckFail, Detail: "title graphic on the torso"}}, Fail},
		{[]editor.Check{{ID: editor.CheckTextOverFace, Status: editor.CheckPass}, {ID: editor.CheckOverlayOverSubject, Status: editor.CheckSkip}}, Pass},
		{[]editor.Check{{ID: editor.CheckHookLength, Status: editor.CheckFail}}, Skip},
	} {
		if st, detail := detectTextOnBody(Measured{Report: editor.Report{Checks: c.checks}}); st != c.want {
			t.Errorf("%v: %s (%s), want %s", c.checks, st, detail, c.want)
		}
	}
}

// The judge is told every text on screen: text items and graphics (a chapter card, a title graphic) with their words and placement rule.
func TestJudgeTimelineListsTextsAndGraphics(t *testing.T) {
	spec := specOf([]timeline.VideoItem{clip("c1", 0, 3000)}, true)
	spec.Tracks = append(spec.Tracks,
		&timeline.TextTrack{ID: "text", Items: []timeline.TextItem{
			{ID: "t1", AtMs: 0, DurationMs: 1500, Text: "BACK DAY 101", Style: "title_bold", Anchor: "upper_third"},
			{ID: "t2", AtMs: 1500, DurationMs: 1200, Text: "first rows", Style: "caption_clean", Avoid: timeline.AvoidFace},
		}},
		&timeline.GraphicsTrack{ID: "graphics", Items: []timeline.GraphicsItem{
			{ID: "g1", Template: timeline.TemplateChapterCard, AtMs: 2000, DurationMs: 900, Params: timeline.GraphicsParams{Text: "1. Rows"}},
		}})
	d := describeTimeline(spec)
	for _, want := range []string{`text t1 at 0.0 s for 1.5 s, style title_bold, upper_third, keeps off the body: "BACK DAY 101"`, "keeps off the face only", "chapter card g1", `1. Rows"`, "caption_clean, its style's place, keeps off the face only"} {
		if !strings.Contains(d, want) {
			t.Errorf("timeline lacks %q:\n%s", want, d)
		}
	}
}
