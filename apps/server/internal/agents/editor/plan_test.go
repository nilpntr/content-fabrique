package editor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
)

func validPlanArgs(asset string) planArgs {
	return planArgs{
		ContentType: "workout", Story: "A heavy back day.", Hook: "The heaviest rep.", Arc: "Hook, build, payoff.", Ending: "Back to the hook.",
		Playbook: "workout", TargetLengthS: 20, TempoBPM: 124, Pace: "building",
		Shots: []planArgsShot{
			{Role: "hook", What: "Heaviest rep", AssetID: asset, Beats: 3, Effect: "punch_in"},
			{Role: "build", What: "The row", Beats: 6},
			{Role: "payoff", What: "Last rep", Beats: 4, Effect: "flash"},
		},
	}
}

func TestBuildPlanComputesTheBeatArithmetic(t *testing.T) {
	asset := assetA
	p, err := BuildPlan(validPlanArgs(asset), map[string]bool{asset: true}, AvailableFeatures())
	if err != nil {
		t.Fatal(err)
	}
	if p.BeatMs != 484 || p.PlaybookVersion != "workout.v1" || p.Version != PlanVersion || p.PromptVersion != PromptVersion {
		t.Errorf("plan: %+v", p)
	}
	if p.Shots[0].LengthMs != 3*484 || p.EstimatedLengthMs != 13*484 || p.Shots[1].Effect != "none" {
		t.Errorf("shots: %+v (total %d)", p.Shots, p.EstimatedLengthMs)
	}
	// No tempo: a beat counts for half a second, and the playbook defaults to the content type.
	in := validPlanArgs(asset)
	in.TempoBPM, in.Playbook, in.ContentType = 0, "", "talking"
	p, err = BuildPlan(in, map[string]bool{asset: true}, AvailableFeatures())
	if err != nil || p.BeatMs != 500 || p.Playbook != "talking" || p.PlaybookVersion != "talking.v1" {
		t.Errorf("no tempo: %+v %v", p, err)
	}
	if BeatMs(120) != 500 || BeatMs(128) != 469 || BeatMs(124) != 484 {
		t.Errorf("beat lengths: %d %d %d", BeatMs(120), BeatMs(124), BeatMs(128))
	}
}

func TestBuildPlanRefusesWhatIsNotValid(t *testing.T) {
	asset := assetA
	footage := map[string]bool{asset: true}
	long := func(n int) string { return strings.Repeat("x", n) }
	cases := []struct {
		name string
		mut  func(*planArgs)
		want string
	}{
		{"type", func(p *planArgs) { p.ContentType = "cooking" }, "content_type"},
		{"playbook", func(p *planArgs) { p.Playbook = "podcast" }, "playbook"},
		{"empty story", func(p *planArgs) { p.Story = "  " }, "story is empty"},
		{"long story", func(p *planArgs) { p.Story = long(MaxPlanStoryChars + 1) }, "story is 201 characters"},
		{"long hook", func(p *planArgs) { p.Hook = long(MaxPlanHookChars + 1) }, "hook is"},
		{"long arc", func(p *planArgs) { p.Arc = long(MaxPlanArcChars + 1) }, "arc is"},
		{"long ending", func(p *planArgs) { p.Ending = long(MaxPlanEndingChars + 1) }, "ending is"},
		{"short target", func(p *planArgs) { p.TargetLengthS = 3 }, "target_length_s is 3"},
		{"long target", func(p *planArgs) { p.TargetLengthS = 600 }, "target_length_s is 600"},
		{"tempo low", func(p *planArgs) { p.TempoBPM = 30 }, "tempo_bpm is 30"},
		{"tempo high", func(p *planArgs) { p.TempoBPM = 400 }, "tempo_bpm is 400"},
		{"pace", func(p *planArgs) { p.Pace = "frantic" }, "pace"},
		{"no shots", func(p *planArgs) { p.Shots = nil }, "0 shots"},
		{"too many shots", func(p *planArgs) { p.Shots = make([]planArgsShot, MaxPlanShots+1) }, "31 shots"},
		{"role", func(p *planArgs) { p.Shots[0].Role = "intro" }, "shot 1: role"},
		{"what", func(p *planArgs) { p.Shots[1].What = long(MaxPlanWhatChars + 1) }, "shot 2: what is"},
		{"beats", func(p *planArgs) { p.Shots[1].Beats = 0 }, "shot 2: beats is 0"},
		{"too many beats", func(p *planArgs) { p.Shots[1].Beats = 65 }, "shot 2: beats is 65"},
		{"effect", func(p *planArgs) { p.Shots[2].Effect = "glitch" }, "shot 3: effect"},
		{"no grid for a workout", func(p *planArgs) { p.TempoBPM = 0 }, "tempo_bpm 0 means no beat grid"},
		{"foreign asset", func(p *planArgs) { p.Shots[0].AssetID = assetB }, "not one of this project's clips"},
	}
	for _, c := range cases {
		in := validPlanArgs(asset)
		c.mut(&in)
		_, err := BuildPlan(in, footage, AvailableFeatures())
		var te *runtime.ToolError
		if err == nil || !errorsAs(err, &te) || !strings.Contains(te.Msg, c.want) {
			t.Errorf("%s: %v, want a message with %q", c.name, err, c.want)
		}
	}
	// An effect the build cannot make is not planned.
	in := validPlanArgs(asset)
	if _, err := BuildPlan(in, footage, NewFeatures("op:add_clip")); err == nil || !strings.Contains(err.Error(), "punch_in is not available") {
		t.Errorf("an effect without its feature: %v", err)
	}
}

func errorsAs(err error, target **runtime.ToolError) bool {
	te, ok := err.(*runtime.ToolError) //nolint:errorlint // BuildPlan returns the ToolError itself
	if ok {
		*target = te
	}
	return ok
}

func TestPlanNotes(t *testing.T) {
	in := validPlanArgs(assetA)
	in.TargetLengthS = 60
	in.Shots = []planArgsShot{{Role: "hook", What: "a", Beats: 6}, {Role: "build", What: "b", Beats: 4}, {Role: "build", What: "c", Beats: 4}, {Role: "build", What: "d", Beats: 4}, {Role: "ending", What: "e", Beats: 4}}
	p, err := BuildPlan(in, nil, AvailableFeatures())
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(planNotes(p), "\n")
	for _, want := range []string{"adds up to 10.6 s but your target is 60 s", "Shots 2 to 5 are all 4 beats long", "The hook shot is 2.9 s long"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
}

func planCall(args planArgs) string {
	raw, _ := json.Marshal(args)
	return string(raw)
}

func TestPlanEditTool(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{title: "Row"})
	prj := e.project(e.wsA, "9:16", "", a)
	var artifacts []json.RawMessage
	rc := e.runCtx(e.wsA, prj, "").WithArtifacts(func(_ context.Context, p json.RawMessage) error {
		artifacts = append(artifacts, p)
		return nil
	})
	res := e.ok(rc, ToolPlanEdit, planCall(validPlanArgs(a.String())))
	text := textOf(res)
	for _, want := range []string{"Plan saved", "workout.v1", "one beat is 484 ms", `{"bpm":124,"first_beat_ms":0}`, "1. hook: 3 beats = 1.45 s, punch_in", "# Playbook: workout and fitness", "punch_in on a beat hit"} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer lacks %q:\n%s", want, text)
		}
	}
	// Only the chosen playbook comes with it.
	if strings.Contains(text, "# Playbook: talking") || strings.Contains(text, "# Playbook: tutorial") {
		t.Error("another playbook is in the answer")
	}
	if len(artifacts) != 1 {
		t.Fatalf("%d artifacts", len(artifacts))
	}
	var env struct {
		Kind string `json:"kind"`
		Data Plan   `json:"data"`
	}
	if err := json.Unmarshal(artifacts[0], &env); err != nil || env.Kind != PlanArtifact || env.Data.ContentType != "workout" || len(env.Data.Shots) != 3 || env.Data.Version != PlanVersion {
		t.Errorf("artifact: %v %s", err, artifacts[0])
	}
	if p, ok := e.deps.previews.plan(rc.RunID); !ok || p.TargetLengthS != 20 {
		t.Errorf("the plan is not kept for the run: %+v %v", p, ok)
	}

	// A plan that is not valid, or names a clip that is not the project's, is refused with what to change.
	bad := validPlanArgs(a.String())
	bad.TargetLengthS = 2
	if msg := e.toolErr(rc, ToolPlanEdit, planCall(bad)); !strings.Contains(msg, "target_length_s is 2") {
		t.Errorf("bad plan: %q", msg)
	}
	other := validPlanArgs(e.asset(e.wsB, clip{title: "Not ours"}).String())
	if msg := e.toolErr(rc, ToolPlanEdit, planCall(other)); !strings.Contains(msg, "not one of this project's clips") {
		t.Errorf("foreign clip: %q", msg)
	}
	// The strict schema keeps the enums closed: the runtime refuses a content type that is not listed before the tool runs.
	if res, err := e.call(rc, ToolPlanEdit, strings.Replace(planCall(validPlanArgs(a.String())), `"workout"`, `"cooking"`, 1)); err != nil || !res.IsError {
		t.Errorf("a content type outside the enum: %v %v", res.IsError, err)
	}
}

// With the gate on (as in NewAgent), the first edit of a run needs a plan, and a run that already made a version does not.
func TestEditNeedsAPlan(t *testing.T) {
	e := newEnv(t)
	e.deps.noPlanGate = false
	e.rebuild()
	a := e.asset(e.wsA, clip{title: "Row"})
	prj := e.project(e.wsA, "9:16", "", a)
	rc := e.runCtx(e.wsA, prj, "")
	edit := e.opsArgs(prj, addClipOp(a, 0, 8000))
	if msg := e.toolErr(rc, ToolEditTimeline, edit); !strings.Contains(msg, "plan_edit") {
		t.Fatalf("an edit without a plan: %q", msg)
	}
	if v := e.currentVersion(prj); v != 0 {
		t.Fatalf("the refused edit made version %d", v)
	}
	e.ok(rc, ToolPlanEdit, planCall(validPlanArgs(a.String())))
	e.ok(e.nextCall(rc), ToolEditTimeline, edit)
	if v := e.currentVersion(prj); v != 1 {
		t.Fatalf("version %d after the edit", v)
	}

	// Another worker (no memory of the plan) lets the run go on, because the run already made a version.
	e.deps.previews.forget(rc.RunID)
	e.ok(e.nextCall(rc), ToolEditTimeline, e.opsArgs(prj, `{"op":"set_speed","id":"c1","speed":1.5}`))
	// A run with no plan and no version of its own is still stopped, even though the project has a timeline.
	other := e.runCtx(e.wsA, prj, "")
	if msg := e.toolErr(other, ToolEditTimeline, e.opsArgs(prj, `{"op":"set_speed","id":"c1","speed":1.2}`)); !strings.Contains(msg, "plan_edit") {
		t.Errorf("another run: %q", msg)
	}
}

func TestContentTypeHint(t *testing.T) {
	cases := map[string]string{
		"Back day: lat pulldown, rows, 3 sets of 10 reps, then posing.": TypeWorkout,
		"Unboxing the new camera: packaging, first look, my review":     TypeUnboxing,
		"How to bake bread, step by step, a simple recipe tutorial":     TypeTutorial,
		"A day in Lisbon, my weekend trip vlog":                         TypeVlog,
		"Rant to camera, my opinion on podcasts, story time":            TypeTalking,
		"Some clips": "",
		"Clips from today in the gym, a set of reps":       TypeWorkout, // "day in" is not "today in"
		"What a great day. In the gym, bench press, squat": TypeWorkout, // a phrase does not run across a sentence
		"workout and unboxing and recipe":                  "",
	}
	for text, want := range cases {
		if got := LikelyContentType(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}

func TestEverythingTheContractNamesAsATypeIsAPlaybook(t *testing.T) {
	for _, typ := range ContentTypes {
		if PlaybookVersion(typ) == "" {
			t.Errorf("no playbook version for %s", typ)
		}
	}
	defs, err := Contract()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		if d.Name != ToolPlanEdit {
			continue
		}
		var s struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(d.Schema, &s); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"content_type", "playbook"} {
			if strings.Join(s.Properties[f].Enum, ",") != strings.Join(ContentTypes, ",") {
				t.Errorf("%s enum %v, want %v", f, s.Properties[f].Enum, ContentTypes)
			}
		}
	}
}
