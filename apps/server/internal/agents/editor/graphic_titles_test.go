package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// asGraphicTitle is the spec with its text title made a graphic (title_pop unless template says otherwise) carrying the same recipe
// (style, fill, scale, shadow, align), as a lively v2.8 edit writes it.
func asGraphicTitle(t *testing.T, spec *timeline.Spec, template, text string) *timeline.Spec {
	t.Helper()
	var tracks []timeline.Track
	var g timeline.GraphicsItem
	for _, tr := range spec.Tracks {
		tt, ok := tr.(*timeline.TextTrack)
		if !ok {
			tracks = append(tracks, tr)
			continue
		}
		it := tt.Items[0]
		g = timeline.GraphicsItem{ID: "g1", Template: template, AtMs: it.AtMs, DurationMs: max(it.DurationMs, 2400), Anchor: it.Anchor,
			Params: timeline.GraphicsParams{Text: text, Style: it.Style, Fill: it.Fill, Scale: it.Scale, Shadow: it.Shadow, Align: it.Align}}
	}
	if g.ID == "" {
		t.Fatal("the spec has no text title")
	}
	if template == timeline.TemplateCounter {
		from, to := 0.0, 12.0
		g.Params.From, g.Params.To, g.Params.Style = &from, &to, ""
	}
	cp := *spec
	cp.Tracks = append(tracks, &timeline.GraphicsTrack{ID: "graphics", Items: []timeline.GraphicsItem{g}})
	return &cp
}

// A live v2.8 run made the hook title a title_pop and style_match failed "title: 0 titles, expected one title". A title graphic is a
// title for style_match (its words, look and place, as the renderer draws them), hook_title, title_copied and text_safe.
func TestAGraphicTitleIsATitle(t *testing.T) {
	spec := asGraphicTitle(t, buildingLats(t), timeline.TemplateTitlePop, "BUILDING BACK 101")
	r := RunChecks(CheckInput{Version: 1, Spec: spec, Plan: workoutPlan(), Style: ginoProfile(t), Vibe: VibeCalm})
	c := wantStatus(t, r, CheckStyleMatch, CheckPass)
	if strings.Contains(c.Detail, "0 titles") || r.Style == nil || r.Style.Score != 1 {
		t.Fatalf("style match with a title graphic: %s / %+v\n%s", c.Detail, r.Style, r.Text())
	}
	wantStatus(t, r, CheckHookTitle, CheckPass)
	wantStatus(t, r, CheckTitleCopied, CheckPass)
	wantStatus(t, r, CheckTextSafe, CheckPass)

	copied := asGraphicTitle(t, buildingLats(t), timeline.TemplateTitlePop, "BUILDING LATS 101")
	r = RunChecks(CheckInput{Version: 1, Spec: copied, Plan: workoutPlan(), Style: ginoProfile(t), Vibe: VibeCalm})
	if c := wantStatus(t, r, CheckTitleCopied, CheckFail); !strings.Contains(c.Detail, "g1") {
		t.Errorf("title_copied: %s", c.Detail)
	}

	// A counter is a label, not a title: the creator's one title is missing.
	counter := asGraphicTitle(t, buildingLats(t), timeline.TemplateCounter, "REPS")
	r = RunChecks(CheckInput{Version: 1, Spec: counter, Plan: workoutPlan(), Style: ginoProfile(t), Vibe: VibeCalm})
	if c := wantStatus(t, r, CheckStyleMatch, CheckFail); !strings.Contains(c.Detail, "title") {
		t.Errorf("a counter counted as the title: %s", c.Detail)
	}
}

// At lively a graphic needs no animation or exit (its template enters and leaves), and it counts as a text that does.
func TestAGraphicEntersAndLeaves(t *testing.T) {
	spec := asGraphicTitle(t, buildingLats(t), timeline.TemplateTitlePop, "BUILDING BACK 101")
	r := RunChecks(CheckInput{Version: 1, Spec: spec, Plan: workoutPlan(), Vibe: VibeLively})
	if c := wantStatus(t, r, CheckTextWithoutEntrance, CheckPass); !strings.Contains(c.Detail, "all 1 texts") {
		t.Errorf("text_without_entrance: %s", c.Detail)
	}
}
