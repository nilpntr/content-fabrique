package editor

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/formats"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The contract's enums of plan_edit are the library's ids, and its roles and content types the formats package's vocabularies.
func TestThePlanEnumsAreTheLibrary(t *testing.T) {
	def, err := contractTool(ToolPlanEdit)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	lib := formats.Default()
	if got, want := schema.Properties["format"].Enum, lib.IDs(formats.KindFormat); !slices.Equal(got, want) {
		t.Errorf("format enum %v, want the library's formats %v", got, want)
	}
	if got, want := schema.Properties["format_mix"].Enum, append([]string{"none"}, append(lib.IDs(formats.KindDevice), lib.IDs(formats.KindFormat)...)...); !slices.Equal(got, want) {
		t.Errorf("format_mix enum %v, want %v", got, want)
	}
	for _, f := range []string{"format", "format_why"} {
		if !slices.Contains(schema.Required, f) {
			t.Errorf("%s is not required", f)
		}
	}
	if !slices.Equal(formats.Roles, PlanRoles) || !slices.Equal(formats.ContentTypes, ContentTypes) || !slices.Equal(formats.Vibes, Vibes) || !slices.Equal(formats.Paces, PlanPaces) {
		t.Error("the formats package's vocabularies differ from the editor's")
	}
}

func TestNewIdeaInput(t *testing.T) {
	raw, err := NewEditInputWith(EditOptions{Instructions: " more energy ", NewIdea: true, AvoidFormats: []string{"tutorial_101", "loop_ending", "nope", "tutorial_101", "pov"}})
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !in.NewIdea || !slices.Equal(in.AvoidFormats, []string{"tutorial_101", "pov"}) || in.Instructions != "more energy" {
		t.Errorf("input %+v", in)
	}
	// Without a new idea the avoided formats are not kept.
	raw, _ = NewEditInputWith(EditOptions{AvoidFormats: []string{"pov"}})
	if in, _ := parseInput(raw); in.NewIdea || len(in.AvoidFormats) != 0 {
		t.Errorf("input %+v", in)
	}
	// A stored input with an id the library does not know (a card removed since) drops it when it is read.
	if in, err := parseInput(json.RawMessage(`{"prompt_version":"x","new_idea":true,"avoid_formats":["gone","pov"]}`)); err != nil || !slices.Equal(in.AvoidFormats, []string{"pov"}) {
		t.Errorf("input %+v %v", in, err)
	}
	blocks, err := opening(json.RawMessage(`{"prompt_version":"x","new_idea":true,"avoid_formats":["pov"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, b := range blocks {
		all.WriteString(b.Text)
	}
	if !strings.Contains(all.String(), "asked for a new idea") || !strings.Contains(all.String(), `"POV: ..." (pov)`) {
		t.Errorf("the opening: %s", all.String())
	}
}

// formatMeasures are references of a creator who posts 101 tutorials and numbered lists (our words, not a creator's).
func formatMeasures() []styleprofile.Measures {
	ref := func(name, title string, chapters []string, cuts []float64) styleprofile.Measures {
		shots := []float64{}
		prev := 0.0
		for _, c := range cuts {
			shots = append(shots, c-prev)
			prev = c
		}
		shots = append(shots, 20-prev)
		return styleprofile.Measures{Name: name, DurationS: 20, CutsS: cuts, ShotsS: shots,
			Text: &styleprofile.TextStyle{TitlePresent: true, Text: title, Case: "upper", Chapters: chapters, ChaptersRead: true}}
	}
	return []styleprofile.Measures{
		ref("a.mp4", "UPPER CHEST 101", nil, []float64{2.5, 9}),
		ref("b.mp4", "REAR DELTS 101", nil, []float64{2.2, 12}),
		ref("c.mp4", "3 KEYS TO WIDER LATS", []string{"1. PULL-UPS", "2. ROWS"}, []float64{2, 5, 8, 11, 14}),
	}
}

func TestListFormatsRanksAndMarks(t *testing.T) {
	e := newEnv(t)
	pose := e.asset(e.wsA, clip{title: "Back pose in the mirror"})
	row := e.asset(e.wsA, clip{title: "Seated cable row", durMs: 60000})
	pull := e.asset(e.wsA, clip{title: "Lat pulldown", durMs: 60000})
	e.saveAnalysis(e.wsA, row, 6)
	e.saveAnalysis(e.wsA, pull, 5)
	prj := e.project(e.wsA, "9:16", "A back day video.", pose, row, pull)

	// Without the creator's formats: by fit alone, nothing new, a transformation does not fit (no old clip).
	text := textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolListFormats, `{}`))
	for _, want := range []string{"Formats for this footage", "tutorial_101", "numbered_listicle", "The creator's own formats are not known", "Do not fit this footage", "transformation (needs an old clip or photo"} {
		if !strings.Contains(text, want) {
			t.Errorf("list_formats lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "NEW TO THE CREATOR:") {
		t.Error("a format is new without the creator's formats")
	}

	// With the creator's references: their formats rank first, two fitting formats they never used are new, and all lists misfits.
	if _, err := styleprofile.Save(context.Background(), e.st, e.wsA.ID, formatMeasures()); err != nil {
		t.Fatal(err)
	}
	text = textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolListFormats, `{"all":true}`))
	first := strings.Index(text, "1. ")
	if first < 0 || !strings.HasPrefix(text[first:], "1. tutorial_101") || !strings.Contains(text, "the creator's own: 67 % of their videos") {
		t.Errorf("the creator's go-to format is not first:\n%s", text)
	}
	if n := strings.Count(text, "NEW TO THE CREATOR: a fresh idea"); n != 2 {
		t.Errorf("%d novel formats, want 2:\n%s", n, text)
	}
	if !strings.Contains(text, "DOES NOT FIT: needs an old clip or photo") || !strings.Contains(text, "a DEVICE to mix in") {
		t.Errorf("all true does not list the misfits and the device:\n%s", text)
	}

	// A new idea: the earlier versions' formats are not for this edit.
	in := `{"prompt_version":"x","new_idea":true,"avoid_formats":["tutorial_101"]}`
	text = textOf(e.ok(e.runCtx(e.wsA, prj, in), ToolListFormats, `{}`))
	if !strings.Contains(text, "NEW IDEA: the earlier versions used Tutorial 101 (tutorial_101)") || !strings.Contains(text, "Not for this edit") || strings.Contains(text, "1. tutorial_101") {
		t.Errorf("a new idea's list:\n%s", text)
	}
}

func TestPlanEditRecordsTheFormat(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{title: "Seated cable row", durMs: 60000})
	e.saveAnalysis(e.wsA, a, 6)
	prj := e.project(e.wsA, "9:16", "", a)
	if _, err := styleprofile.Save(context.Background(), e.st, e.wsA.ID, formatMeasures()); err != nil {
		t.Fatal(err)
	}
	var artifacts []json.RawMessage
	rc := e.runCtx(e.wsA, prj, "").WithArtifacts(func(_ context.Context, p json.RawMessage) error {
		artifacts = append(artifacts, p)
		return nil
	})
	args := validPlanArgs(a.String())
	args.Format, args.FormatWhy, args.FormatMix = "rep_counter", "One long clean set to count.", "loop_ending"
	text := textOf(e.ok(rc, ToolPlanEdit, planCall(args)))
	for _, want := range []string{"format rep_counter", "The format: Rep counter to a PR (rep_counter), new to this creator, with Seamless loop mixed in", "hook (", "payoff (", "format_followed"} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer lacks %q:\n%s", want, text)
		}
	}
	var env struct {
		Data Plan `json:"data"`
	}
	if err := json.Unmarshal(artifacts[len(artifacts)-1], &env); err != nil || env.Data.Format == nil {
		t.Fatalf("the plan's format: %v %s", err, artifacts[len(artifacts)-1])
	}
	if f := env.Data.Format; f.ID != "rep_counter" || f.Name != "Rep counter to a PR" || !f.NewToCreator || f.Mix != "loop_ending" || f.Why != "One long clean set to count." || f.Library != formats.Default().Version {
		t.Errorf("format %+v", f)
	}

	// Two new formats at once: a note, not a refusal.
	args.Format, args.FormatMix = "rep_counter", "pov"
	if text := textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolPlanEdit, planCall(args))); !strings.Contains(text, "Both rep_counter and pov are new to the creator") {
		t.Errorf("no note on two new formats:\n%s", text)
	}
	// A format the footage does not show what it needs: a note to look again.
	args.Format, args.FormatMix = "transformation", ""
	if text := textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolPlanEdit, planCall(args))); !strings.Contains(text, "list_formats does not see what transformation needs") {
		t.Errorf("no note on a misfit:\n%s", text)
	}
	// Refused: no reason, a device as the format, the format as its own mix, and a new idea's avoided format.
	for _, tc := range []struct{ format, why, mix, input, want string }{
		{"tutorial_101", " ", "", "", "format_why is empty"},
		{"loop_ending", "x", "", "", "is not one of the formats"},
		{"pov", "x", "pov", "", "format_mix is the format itself"},
		{"tutorial_101", "x", "", `{"prompt_version":"x","new_idea":true,"avoid_formats":["tutorial_101"]}`, "asked for a new idea"},
	} {
		bad := validPlanArgs(a.String())
		bad.Format, bad.FormatWhy, bad.FormatMix = tc.format, tc.why, tc.mix
		// The strict schema refuses an id outside the enum before the tool runs; the tool refuses what the schema lets through.
		if msg := e.toolErr(e.runCtx(e.wsA, prj, tc.input), ToolPlanEdit, planCall(bad)); tc.format != "loop_ending" && !strings.Contains(msg, tc.want) {
			t.Errorf("%s/%s: %s", tc.format, tc.mix, msg)
		}
	}
	if _, _, err := planFormat(planArgs{Format: "loop_ending", FormatWhy: "x"}, Input{}, nil, false); err == nil || !strings.Contains(err.Error(), "is not one of the formats") {
		t.Errorf("a device as the format: %v", err)
	}
}

// ---- format_followed ---------------------------------------------------------------------------------------------------

func formatPlan(id, mix string, roles ...string) *Plan {
	p := &Plan{ContentType: TypeWorkout, TargetLengthS: 20, TempoBPM: 124, Format: &PlanFormat{ID: id, Mix: mix}}
	for _, r := range roles {
		p.Shots = append(p.Shots, PlanShot{Role: r, What: r, Beats: 4})
	}
	return p
}

type tx struct {
	text    string
	at, dur int64
}

func texts(items ...tx) []timeline.TextItem {
	var out []timeline.TextItem
	for i, it := range items {
		out = append(out, timeline.TextItem{ID: "t" + string(rune('1'+i)), Text: it.text, AtMs: it.at, DurationMs: it.dur, Style: "title_bold", Anchor: "upper_third"})
	}
	return out
}

func TestFormatFollowed(t *testing.T) {
	three := []shot{{asset: assetA, in: 0, out: 3000}, {asset: assetB, in: 0, out: 4000}, {asset: assetA, in: 5000, out: 9000}}
	run := func(plan *Plan, tx []timeline.TextItem, music *TimelineMusic, set *styleprofile.Resolved) Check {
		t.Helper()
		r := RunChecks(CheckInput{Spec: specOf(t, three, tx, nil), Plan: plan, Music: music, Settings: set})
		for _, c := range r.Checks {
			if c.ID == CheckFormatFollowed {
				return c
			}
		}
		t.Fatal("no format_followed")
		return Check{}
	}
	if c := run(&Plan{ContentType: TypeWorkout, TargetLengthS: 10}, nil, nil, nil); c.Status != CheckSkip {
		t.Errorf("no format: %+v", c)
	}
	// A 101 tutorial: a hook title and a payoff; a title out of the forms is advice.
	if c := run(formatPlan("tutorial_101", "", "hook", "build", "payoff"), texts(tx{"UPPER BACK 101", 0, 2000}), nil, nil); c.Status != CheckPass || strings.Contains(c.Detail, "borderline") {
		t.Errorf("a 101 tutorial: %+v", c)
	}
	if c := run(formatPlan("tutorial_101", "", "hook", "build", "payoff"), texts(tx{"BACK DAY", 0, 2000}), nil, nil); c.Status != CheckPass || !strings.HasPrefix(c.Detail, "borderline") {
		t.Errorf("a title out of the forms: %+v", c)
	}
	if c := run(formatPlan("tutorial_101", "", "hook", "build", "payoff"), nil, nil, nil); c.Status != CheckFail || !strings.Contains(c.Detail, "no hook title") {
		t.Errorf("no title: %+v", c)
	}
	if c := run(formatPlan("tutorial_101", "", "hook", "build", "build"), texts(tx{"UPPER BACK 101", 0, 2000}), nil, nil); c.Status != CheckFail || !strings.Contains(c.Detail, "no payoff") {
		t.Errorf("no payoff: %+v", c)
	}
	// A numbered list needs its chapter cards.
	list := formatPlan("numbered_listicle", "", "hook", "step", "payoff")
	if c := run(list, texts(tx{"2 MOVES FOR WIDTH", 0, 2000}), nil, nil); c.Status != CheckFail || !strings.Contains(c.Detail, "numbered chapter cards") {
		t.Errorf("a list without chapters: %+v", c)
	}
	if c := run(list, texts(tx{"2 MOVES FOR WIDTH", 0, 2000}, tx{"1. ROWS", 3000, 2000}, tx{"2. PULLDOWNS", 7000, 2000}), nil, nil); c.Status != CheckPass {
		t.Errorf("a list with chapters: %+v", c)
	}
	// A reveal lands on the drop: a cut there passes, none fails with the time.
	reveal := formatPlan("quote_reveal", "", "hook", "reveal", "payoff")
	quote := texts(tx{"\"one more year\"", 0, 2800})
	if c := run(reveal, quote, &TimelineMusic{HasDrop: true, DropMs: 3030}, nil); c.Status != CheckPass {
		t.Errorf("a reveal on the drop: %+v", c)
	}
	if c := run(reveal, quote, &TimelineMusic{HasDrop: true, DropMs: 4000}, nil); c.Status != CheckFail || !strings.Contains(c.Detail, "no cut on the song's drop at 4.0 s") {
		t.Errorf("a reveal off the drop: %+v", c)
	}
	// Dialogue: two lines, then a bare twist.
	dialogue := formatPlan("meme_dialogue", "", "hook", "build", "reveal")
	if c := run(dialogue, texts(tx{"\"where were you?\"", 0, 2900}, tx{"\"training.\"", 3000, 3900}), &TimelineMusic{HasDrop: true, DropMs: 7000}, nil); c.Status != CheckPass {
		t.Errorf("dialogue: %+v", c)
	}
	if c := run(dialogue, texts(tx{"\"where were you?\"", 0, 2900}, tx{"\"training.\"", 3000, 3900}, tx{"\"ha\"", 7100, 1000}), &TimelineMusic{HasDrop: true, DropMs: 7000}, nil); c.Status != CheckFail || !strings.Contains(c.Detail, "a line on the twist") {
		t.Errorf("a line on the twist: %+v", c)
	}
	// A loop mixed in: the last shot from the opening clip.
	if c := run(formatPlan("tutorial_101", "loop_ending", "hook", "build", "ending"), texts(tx{"UPPER BACK 101", 0, 2000}), nil, nil); c.Status != CheckPass || !strings.Contains(c.Detail, "a loop") {
		t.Errorf("a loop: %+v", c)
	}
	notLoop := []shot{{asset: assetA, in: 0, out: 3000}, {asset: assetB, in: 0, out: 4000}}
	r := RunChecks(CheckInput{Spec: specOf(t, notLoop, texts(tx{"UPPER BACK 101", 0, 2000}), nil), Plan: formatPlan("tutorial_101", "loop_ending", "hook", "ending")})
	if c := findCheck(r, CheckFormatFollowed); c.Status != CheckFail || !strings.Contains(c.Detail, "no loop") {
		t.Errorf("no loop: %+v", c)
	}
	// The creator's own text pattern wins over the format's: story captions chosen by the creator, a 101 tutorial without a title.
	mine := styleprofile.Resolve(nil, nil, styleprofile.Settings{TextPattern: styleprofile.PatternStoryCaptions}, "")
	if c := run(formatPlan("tutorial_101", "", "hook", "build", "payoff"), texts(tx{"this is my back day", 0, 3000}, tx{"rows first", 3000, 4000}), nil, &mine); c.Status != CheckPass || !strings.Contains(c.Detail, "the creator's own choice") {
		t.Errorf("the creator's own pattern: %+v", c)
	}
}

func findCheck(r Report, id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

// The format's text pattern replaces a measured one for the text checks, and never the creator's own.
func TestFormatSettings(t *testing.T) {
	measured := styleprofile.Resolved{Settings: styleprofile.Settings{TextPattern: styleprofile.PatternStoryCaptions}, Sources: styleprofile.Settings{TextPattern: styleprofile.SourceMeasured}}
	if got := formatSettings(formatPlan("tutorial_101", ""), &measured); got.TextPattern != styleprofile.PatternSingleTitle || got.Lines() {
		t.Errorf("a measured caption style in a 101 tutorial: %+v", got)
	}
	if measured.TextPattern != styleprofile.PatternStoryCaptions {
		t.Error("the settings were changed in place")
	}
	mine := measured
	mine.Sources.TextPattern = styleprofile.SourceYou
	if got := formatSettings(formatPlan("tutorial_101", ""), &mine); got.TextPattern != styleprofile.PatternStoryCaptions {
		t.Errorf("the creator's own pattern was replaced: %+v", got)
	}
	if got := formatSettings(formatPlan("story_captions", ""), nil); got == nil || got.TextPattern != styleprofile.PatternStoryCaptions {
		t.Errorf("a caption format without settings: %+v", got)
	}
	if got := formatSettings(formatPlan("tutorial_101", ""), nil); got != nil {
		t.Errorf("a title format without settings: %+v", got)
	}
	if got := formatSettings(formatPlan("montage", ""), &measured); got != &measured {
		t.Error("a format of any text changed the settings")
	}
	// In the report: a caption creator's measured pattern does not make a 101 tutorial fail story_captions.
	three := []shot{{asset: assetA, in: 0, out: 3000}, {asset: assetB, in: 0, out: 4000}, {asset: assetA, in: 5000, out: 9000}}
	r := RunChecks(CheckInput{Spec: specOf(t, three, texts(tx{"UPPER BACK 101", 0, 2000}), nil), Plan: formatPlan("tutorial_101", "", "hook", "build", "payoff"), Settings: &measured})
	for _, c := range r.Checks {
		if c.ID == CheckStoryCaptions {
			t.Errorf("story_captions ran for a 101 tutorial: %+v", c)
		}
	}
}
