package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// storyStyle is the effective style of a creator of story captions in the middle of the frame (lines made up for the tests).
func storyStyle(pattern string) *styleprofile.Resolved {
	r := styleprofile.Resolve(nil, nil, styleprofile.Settings{TextPattern: pattern, TextPosition: styleprofile.TextMiddle}, "")
	return &r
}

func line(id string, at, dur int64, text string) timeline.TextItem {
	return timeline.TextItem{ID: id, AtMs: at, DurationMs: dur, Text: text, Style: "subtitle_clean", Anchor: timeline.AnchorCenter, Avoid: timeline.AvoidFace}
}

// four shots of 2 s, the person standing with the head at 22 % of the frame.
func fourShots(t *testing.T, texts []timeline.TextItem) *timeline.Spec {
	return specOf(t, []shot{{asset: graphicsAsset, in: 0, out: 2000}, {asset: graphicsAsset, in: 2000, out: 4000},
		{asset: graphicsAsset, in: 4000, out: 6000}, {asset: graphicsAsset, in: 6000, out: 8000}}, texts, nil)
}

func standingPerson() subject.Index {
	return subject.Index{graphicsAsset: {Aspect: 0.5625, Samples: []subject.Sample{
		{AtMs: 0, X: 0.2, Y: 0.2, W: 0.6, H: 0.8, HeadTopY: 0.22},
		{AtMs: 8000, X: 0.2, Y: 0.2, W: 0.6, H: 0.8, HeadTopY: 0.22},
	}}}
}

func checksOf(r Report) map[string]Check {
	out := map[string]Check{}
	for _, c := range r.Checks {
		out[c.ID] = c
	}
	return out
}

// A story-caption edit has a line on every shot but the last, each up long enough to read, none over a face; lines that sit on the chest
// (avoid face) are this creator's, not a title creator's.
func TestStoryCaptionChecks(t *testing.T) {
	good := fourShots(t, []timeline.TextItem{
		line("l1", 0, 2000, "This is what made my back grow"), line("l2", 2000, 2000, "I stopped pulling heavy"),
		line("l3", 4000, 2000, "And started to squeeze every rep"),
	})
	r := checksOf(RunChecks(CheckInput{Spec: good, Plan: workoutPlan(), Settings: storyStyle(styleprofile.PatternStoryCaptions), Subjects: standingPerson()}))
	for _, id := range []string{CheckStoryCaptions, CheckCaptionReadable, CheckTextOverFace, CheckTextOverSubject} {
		if r[id].Status != CheckPass {
			t.Errorf("%s: %+v", id, r[id])
		}
	}

	bare := fourShots(t, []timeline.TextItem{line("l1", 0, 2000, "This is what made my back grow"), line("l3", 4000, 600, "And then")})
	r = checksOf(RunChecks(CheckInput{Spec: bare, Plan: workoutPlan(), Settings: storyStyle(styleprofile.PatternStoryCaptions), Subjects: standingPerson()}))
	if c := r[CheckStoryCaptions]; c.Status != CheckFail || !strings.Contains(c.Detail, "c2") || !strings.Contains(c.Detail, "c3") {
		t.Errorf("shots without a line: %+v", c)
	}
	if c := r[CheckCaptionReadable]; c.Status != CheckFail || !strings.Contains(c.Detail, "l3 (0.6 s)") {
		t.Errorf("a line of 0.6 s: %+v", c)
	}

	// A line placed on the face fails, wherever the renderer would otherwise put it when it is pinned there.
	pinned := line("l1", 0, 2000, "On the face")
	pinned.Anchor, pinned.Position, pinned.AvoidSubject = "", &timeline.Point{X: 0.5, Y: 0.3}, new(bool)
	r = checksOf(RunChecks(CheckInput{Spec: fourShots(t, []timeline.TextItem{pinned}), Plan: workoutPlan(), Settings: storyStyle(styleprofile.PatternDialogue), Subjects: standingPerson()}))
	if c := r[CheckTextOverFace]; c.Status != CheckFail || !strings.Contains(c.Detail, "l1") {
		t.Errorf("a line pinned on the face: %+v", c)
	}
	if c := r[CheckStoryCaptions]; c.Status != CheckSkip {
		t.Errorf("dialogue is not held to a line per shot: %+v", c)
	}

	// A title creator: avoid face is refused, and the text-pattern checks are not in the report at all.
	title := styleprofile.Resolve(nil, nil, styleprofile.Settings{}, "")
	rep := RunChecks(CheckInput{Spec: good, Plan: workoutPlan(), Settings: &title, Subjects: standingPerson()})
	r = checksOf(rep)
	if c := r[CheckTextOverSubject]; c.Status != CheckFail || !strings.Contains(c.Detail, "remove avoid face") {
		t.Errorf("avoid face for a title creator: %+v", c)
	}
	if _, ok := r[CheckStoryCaptions]; ok {
		t.Error("a title creator's report has the story-caption checks")
	}
}

// The settings say the pattern, the look and the language in our words, and tell the editor to keep the lines off the face only.
func TestTheSettingsTellTheStory(t *testing.T) {
	p := ginoProfile(t)
	p.Text.Pattern, p.Text.PatternRead, p.Text.PatternShare, p.Text.Language = styleprofile.PatternStoryCaptions, 5, 0.6, "nl"
	p.Text.LineLook = &styleprofile.TextLook{Lines: 12, Position: styleprofile.TextMiddle, HeightPct: 2.5, Case: "sentence", Colour: "#FFFFFF", OverBodyShare: 0.6, WordsLo: 3, WordsHi: 7}
	st := (&styleprofile.Style{Profile: p}).ForEdit("")
	if st.Resolved.TextPattern != styleprofile.PatternStoryCaptions || !st.FaceOnly() {
		t.Fatalf("style %+v", st.Resolved)
	}
	all := ""
	for _, b := range styleBlocks(st) {
		all += b.Text + "\n"
	}
	for _, want := range []string{"- Text: story captions (measured from their videos)", "in Dutch, the language of their text", "written as a sentence", "fill #FFFFFF",
		"3 to 7 words a line", "anchor center, in the middle of the frame", `Give each line avoid "face"`} {
		if !strings.Contains(all, want) {
			t.Errorf("the context lacks %q:\n%s", want, all)
		}
	}
	// A title creator gets neither.
	title := (&styleprofile.Style{Profile: ginoProfile(t)}).ForEdit("")
	all = ""
	for _, b := range styleBlocks(title) {
		all += b.Text + "\n"
	}
	if strings.Contains(all, "- Text: story captions") || strings.Contains(all, `avoid "face"`) {
		t.Errorf("a title creator's context:\n%s", all)
	}
}
