package editor

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/motion"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// liveShape is the shape of the live v2.9 run on Gino's footage: a title_pop "UPPER BACK 101" over the hook and two chapter cards, one
// at the start of each exercise (the fourth and the fifth shot of the cut), all in the creator's title recipe.
func liveShape(t *testing.T) (*timeline.Spec, *Plan) {
	t.Helper()
	spec := buildingLats(t)
	rec := ginoProfile(t).TitleRecipe()
	look := timeline.GraphicsParams{Style: rec.Style, Fill: rec.Fill, Shadow: rec.Shadow}
	one, two := 1, 2
	title, card1, card2 := look, look, look
	title.Text = "UPPER BACK 101"
	card1.Text, card1.Number = "SHRUGS", &one
	card2.Text, card2.Number = "FACE PULLS", &two
	var tracks []timeline.Track
	for _, tr := range spec.Tracks {
		if _, ok := tr.(*timeline.TextTrack); !ok {
			tracks = append(tracks, tr)
		}
	}
	spec.Tracks = append(tracks, &timeline.GraphicsTrack{ID: "graphics", Items: []timeline.GraphicsItem{
		{ID: "g1", Template: timeline.TemplateTitlePop, AtMs: 0, DurationMs: 2400, Anchor: "upper_third", Params: title},
		{ID: "g2", Template: timeline.TemplateChapterCard, AtMs: 3800, DurationMs: 2500, Anchor: "upper_third", Params: card1},
		{ID: "g3", Template: timeline.TemplateChapterCard, AtMs: 11800, DurationMs: 2500, Anchor: "upper_third", Params: card2},
	}})
	plan := workoutPlan()
	plan.Shots = []PlanShot{{Role: "hook"}, {Role: "build"}, {Role: "build"}, {Role: "step", Chapter: 1}, {Role: "step", Chapter: 2}}
	return spec, plan
}

// chapterSetting is the creator's style with chapter titles set (by the creator) to level.
func chapterSetting(t *testing.T, level string) *styleprofile.Resolved {
	t.Helper()
	r := styleprofile.Resolve(ginoProfile(t), nil, styleprofile.Settings{ChapterTitles: level}, VibeCalm)
	if r.ChapterTitles != level || r.Sources.ChapterTitles != styleprofile.SourceYou {
		t.Fatalf("resolved chapter titles %q from %q", r.ChapterTitles, r.Sources.ChapterTitles)
	}
	return &r
}

// The live failure: "title: 3 titles, expected one title". With chapter titles on, the two chapter cards are the creator's chapter
// titles, not more titles: one title, two chapter titles, style_match and chapter_titles pass.
func TestATitleAndTwoChapterCardsPassWithChaptersOn(t *testing.T) {
	spec, plan := liveShape(t)
	r := RunChecks(CheckInput{Version: 1, Spec: spec, Plan: plan, Style: ginoProfile(t), Settings: chapterSetting(t, styleprofile.LevelOn), Vibe: VibeCalm})
	c := wantStatus(t, r, CheckStyleMatch, CheckPass)
	if r.Style == nil {
		t.Fatal("no distance")
	}
	dims := map[string]styleprofile.Dimension{}
	for _, d := range r.Style.Dimensions {
		dims[d.ID] = d
	}
	if d := dims["title"]; !d.OK || d.Value != "1 titles" {
		t.Errorf("title: %+v (%s)", d, c.Detail)
	}
	if d := dims["chapter_titles"]; !d.OK || d.Value != "2 chapter titles" || d.Soft {
		t.Errorf("chapter titles: %+v", d)
	}
	if d, ok := dims["labels"]; ok && !d.OK {
		t.Errorf("the chapter cards counted as labels: %+v", d)
	}
	wantStatus(t, r, CheckChapterTitles, CheckPass)
	wantStatus(t, r, CheckHookTitle, CheckPass)
	wantStatus(t, r, CheckTitleCopied, CheckPass)
}

// With chapter titles off (the creator's choice), the same cards fail style_match on chapter_titles (not on the title), and the
// chapter_titles check does not ask for them.
func TestChapterCardsFailWithChaptersOff(t *testing.T) {
	spec, plan := liveShape(t)
	r := RunChecks(CheckInput{Version: 1, Spec: spec, Plan: plan, Style: ginoProfile(t), Settings: chapterSetting(t, styleprofile.LevelOff), Vibe: VibeCalm})
	c := wantStatus(t, r, CheckStyleMatch, CheckFail)
	if !strings.Contains(c.Detail, "chapter") || strings.Contains(c.Detail, "3 titles") {
		t.Errorf("style_match: %s", c.Detail)
	}
	wantStatus(t, r, CheckChapterTitles, CheckSkip)
}

// The template sizes follow the creator's measured title height: an add_graphic of a title template that names no style gets the
// creator's title recipe in edit_timeline, and then the title and the chapter cards are drawn with capitals of the creator's height
// (4.0 % of the frame for this creator), where the template's default style would draw them at about 3.2 %.
func TestTitleGraphicsGetTheCreatorsTitleSize(t *testing.T) {
	p := ginoProfile(t)
	rec := p.TitleRecipe()
	if rec == nil || rec.CapHeightPct < 3.9 || rec.CapHeightPct > 4.1 {
		t.Fatalf("recipe %+v", rec)
	}
	raw := json.RawMessage(`[{"op":"add_graphic","template":"title_pop","at_ms":0,"duration_ms":2400,"anchor":"upper_third","params":{"text":"UPPER BACK 101","accents_ms":[0,484,968]}},
		{"op":"add_graphic","template":"chapter_card","at_ms":3800,"duration_ms":2500,"anchor":"upper_third","params":{"text":"SHRUGS","number":1}},
		{"op":"add_graphic","template":"title_pop","at_ms":9000,"duration_ms":2400,"anchor":"upper_third","params":{"text":"MINE","style":"title_bold"}},
		{"op":"add_graphic","template":"counter","at_ms":6000,"duration_ms":2500,"anchor":"upper_third","params":{"text":"REPS","from":0,"to":12}},
		{"op":"add_text","text":"x","style":"caption_plain","anchor":"center","at_ms":0,"duration_ms":1000}]`)
	got, notes := applyTitleRecipe(raw, rec)
	if len(notes) != 2 || !strings.Contains(notes[0], "style "+rec.Style) || !strings.Contains(notes[0], "4.0 %") {
		t.Fatalf("notes %q", notes)
	}
	var ops []struct {
		Op       string                  `json:"op"`
		Template string                  `json:"template"`
		Params   timeline.GraphicsParams `json:"params"`
	}
	if err := json.Unmarshal(got, &ops); err != nil {
		t.Fatal(err)
	}
	if ops[0].Params.Style != rec.Style || ops[0].Params.Fill != rec.Fill || ops[0].Params.Shadow != rec.Shadow || len(ops[0].Params.AccentsMs) != 3 {
		t.Errorf("title_pop params %+v", ops[0].Params)
	}
	if ops[1].Params.Style != rec.Style || ops[1].Params.Number == nil {
		t.Errorf("chapter_card params %+v", ops[1].Params)
	}
	if ops[2].Params.Style != "title_bold" || ops[2].Params.Fill != "" {
		t.Errorf("a graphic with a style of its own was changed: %+v", ops[2].Params)
	}
	if ops[3].Params.Style != "" {
		t.Errorf("a counter got the title look: %+v", ops[3].Params)
	}
	var list []json.RawMessage
	if err := json.Unmarshal(got, &list); err != nil {
		t.Fatal(err)
	}
	var in []json.RawMessage
	_ = json.Unmarshal(raw, &in)
	for _, i := range []int{2, 3, 4} {
		if string(list[i]) != string(in[i]) {
			t.Errorf("op %d was rewritten: %s", i, list[i])
		}
	}

	// As drawn: the capitals of the recipe's title and card against the creator's measured height, and the default style's for scale.
	out := timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"}
	for i, tpl := range []string{timeline.TemplateTitlePop, timeline.TemplateChapterCard} {
		g := timeline.GraphicsItem{ID: "g", Template: tpl, AtMs: 0, DurationMs: 2500, Anchor: "upper_third", Params: ops[i].Params}
		g.Params.AccentsMs = nil
		r, err := motion.Resolve(out, g, nil)
		if err != nil {
			t.Fatal(err)
		}
		capPct := r.Placement.Style.CapHeightPct(out.ShortSide(), out.Height)
		if math.Abs(capPct-rec.CapHeightPct)/rec.CapHeightPct > 0.05 {
			t.Errorf("%s: capitals %.2f %% of the frame, the creator's %.2f %%", tpl, capPct, rec.CapHeightPct)
		}
		bare := g
		bare.Params.Style, bare.Params.Fill, bare.Params.Shadow, bare.Params.Scale = "", "", "", nil
		rb, err := motion.Resolve(out, bare, nil)
		if err != nil {
			t.Fatal(err)
		}
		if def := rb.Placement.Style.CapHeightPct(out.ShortSide(), out.Height); def >= capPct*0.9 {
			t.Errorf("%s: the default style's capitals (%.2f %%) are not smaller than the recipe's (%.2f %%): the test shows nothing", tpl, def, capPct)
		}
	}
	if same, _ := applyTitleRecipe(raw, nil); string(same) != string(raw) {
		t.Error("no recipe changed the ops")
	}
}
