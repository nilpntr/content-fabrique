package editor

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/ops"
)

func TestFeaturesComeFromTheOpSchema(t *testing.T) {
	f := AvailableFeatures()
	for _, want := range []string{
		"op:add_clip", "op:set_motion", "op:set_speed_ramp", "op:snap_cuts_to_beats", "op:set_transition",
		"transition:whip_left", "transition:whip_right", "transition:zoom_through", "transition:flash", "transition:crossfade",
		"motion:punch_in", "motion:push_in", "motion:shake", "look:gym_moody", "look:auto", "op:add_sfx", "sfx", "looks",
	} {
		if !f.Has(want) {
			t.Errorf("the build lacks the feature %s: %v", want, f.List())
		}
	}
	// Every op of the registry is a feature, and the other way round: nothing is listed that the schema does not have.
	for _, name := range ops.Names {
		if !f.Has("op:" + name) {
			t.Errorf("op %s is not a feature", name)
		}
	}
	for _, name := range f.List() {
		if op, ok := strings.CutPrefix(name, "op:"); ok && !slices.Contains(ops.Names, op) {
			t.Errorf("the feature %s is not an op of the registry", name)
		}
	}
	// A schema without sound effects and the new looks has neither feature.
	old := `{"oneOf":[{"$ref":"#/$defs/add_clip"},{"$ref":"#/$defs/set_look"}],"$defs":{"set_look":{"properties":{"preset":{"enum":["none","punchy"]}}}}}`
	of, err := FeaturesFromSchema([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if of.Has("sfx") || of.Has("looks") || of.Has("op:set_motion") || !of.Has("op:add_clip") || !of.Has("look:punchy") {
		t.Errorf("features of the old schema: %v", of.List())
	}
	if _, err := FeaturesFromSchema([]byte(`{}`)); err == nil {
		t.Error("a schema with no ops was accepted")
	}
}

// The prompt never offers what the build does not have: with the features of a build before the looks and the sound effects, the
// sections about them are gone; with them, they are there.
func TestPromptOffersOnlyWhatExists(t *testing.T) {
	all := AvailableFeatures()
	without := NewFeatures(filterOut(all.List(), "sfx", "looks", "op:add_sfx", "look:auto", "look:gym_moody")...)
	off, err := SystemPromptFor(without)
	if err != nil {
		t.Fatal(err)
	}
	on, err := SystemPromptFor(all)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"add_sfx", "gym_moody", "exposure_ev"} {
		if strings.Contains(off, word) {
			t.Errorf("the prompt of a build without looks and sfx mentions %s", word)
		}
		if !strings.Contains(on, word) {
			t.Errorf("the prompt of this build does not mention %s", word)
		}
	}
	if SystemPrompt() != on {
		t.Error("SystemPrompt is not the prompt of the build's features")
	}
	// A build without the effects at all does not teach them.
	bare, err := SystemPromptFor(NewFeatures("op:add_clip", "op:edit_timeline"))
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"set_motion", "set_speed_ramp", "zoom_through", "whip_left", "flash", "punch_in"} {
		if strings.Contains(bare, word) {
			t.Errorf("a build without effects is taught %s", word)
		}
	}
	for _, p := range []string{off, on, bare} {
		if strings.Contains(p, "{{") || strings.Contains(p, "}}\n") || strings.Contains(p, "\r") || p != strings.TrimSpace(p) || strings.Contains(p, "\n\n\n") || strings.Contains(p, " \n") {
			t.Error("a rendered prompt has a template leftover or stray whitespace")
		}
	}
}

func filterOut(list []string, drop ...string) []string {
	var out []string
	for _, s := range list {
		if !slices.Contains(drop, s) {
			out = append(out, s)
		}
	}
	return out
}

var opWord = regexp.MustCompile(`\b(?:add|set|remove|move|trim|replace|snap)_[a-z_]+\b`)

// Every op the prompt or a playbook names is an op of the registry (the words that look like ops but are fields are listed).
func TestPromptAndPlaybooksNameRealOps(t *testing.T) {
	notOps := map[string]bool{"snap_cuts_to_beats_": true}
	check := func(name, text string) {
		for _, w := range opWord.FindAllString(text, -1) {
			if !slices.Contains(ops.Names, w) && !notOps[w] {
				t.Errorf("%s names %q, which is not an op", name, w)
			}
		}
	}
	check("the prompt", SystemPrompt())
	for _, typ := range ContentTypes {
		book, err := Playbook(typ, AvailableFeatures())
		if err != nil {
			t.Fatal(err)
		}
		check("playbook "+typ, book)
	}
	// And every tool the prompt names is a tool of the contract.
	defs, err := Contract()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	for _, w := range regexp.MustCompile(`\b(?:get|list|view|find|plan|render|check)_[a-z]+\b`).FindAllString(SystemPrompt(), -1) {
		if !slices.Contains(names, w) {
			t.Errorf("the prompt names the tool %q, which is not in the contract", w)
		}
	}
}

// The example speed ramp in the prompt plays for as long as the prompt says it does, and its ops are valid.
func TestPromptRampExampleIsRight(t *testing.T) {
	m := regexp.MustCompile(`\{"op":"set_speed_ramp","id":"c3","points":(\[.*?\])\}`).FindStringSubmatch(SystemPrompt())
	if m == nil {
		t.Fatal("no speed ramp example in the prompt")
	}
	var pts []timeline.SpeedPoint
	if err := json.Unmarshal([]byte(m[1]), &pts); err != nil {
		t.Fatal(err)
	}
	it := timeline.VideoItem{Source: timeline.Source{InMs: 41200, OutMs: 44800}, SpeedRamp: pts}
	if d := timeline.OutputDuration(it); d < 2400 || d > 2600 {
		t.Errorf("the example ramp plays for %d ms, the prompt says about 2.5 seconds", d)
	}
	if parsed, res := ops.ParseBatch([]byte(`[{"op":"set_speed_ramp","id":"c3","points":` + m[1] + `}]`)); len(parsed) != 1 {
		t.Errorf("the example ramp is not a valid op: %+v", res.Issues)
	}
	// The snap example is a valid op too.
	snap := regexp.MustCompile(`\{"op":"snap_cuts_to_beats".*?"max_shift_ms":80\}\}`).FindString(SystemPrompt())
	if snap == "" {
		t.Fatal("no snap example in the prompt")
	}
	if parsed, res := ops.ParseBatch([]byte(`[` + snap + `]`)); len(parsed) != 1 {
		t.Errorf("the snap example is not a valid op: %+v", res.Issues)
	}
}

func TestPlaybooks(t *testing.T) {
	for _, typ := range ContentTypes {
		for name, f := range map[string]Features{"all": AvailableFeatures(), "none": NewFeatures()} {
			book, err := Playbook(typ, f)
			if err != nil {
				t.Fatalf("%s: %v", typ, err)
			}
			if !strings.HasPrefix(book, "# Playbook: ") || !strings.Contains(book, "("+PlaybookVersion(typ)+")") || strings.Contains(book, "{{") || strings.Contains(book, "\n\n\n") {
				t.Errorf("playbook %s (%s features):\n%s", typ, name, book)
			}
		}
	}
	if _, err := Playbook("podcast", AvailableFeatures()); err == nil {
		t.Error("a playbook that does not exist was returned")
	}
	// The looks and sound effects of the workout playbook turn on with the features.
	on, _ := Playbook(TypeWorkout, AvailableFeatures())
	off, _ := Playbook(TypeWorkout, NewFeatures())
	if !strings.Contains(on, "gym_moody") || !strings.Contains(on, "add_sfx") || strings.Contains(off, "gym_moody") || strings.Contains(off, "add_sfx") {
		t.Error("the workout playbook does not follow the features")
	}
	// The talking playbook says where the transcript read path is missing.
	talking, _ := Playbook(TypeTalking, AvailableFeatures())
	if !strings.Contains(talking, "TODO") {
		t.Error("the talking playbook has no note about transcripts")
	}
}

// The cached prefix is the same whatever the project is: the prompt has no per-run text, and the tools are the contract's.
func TestSystemPromptIsStable(t *testing.T) {
	if strings.Contains(SystemPrompt(), "prj_") || strings.Contains(SystemPrompt(), "ast_") {
		t.Error("the system prompt carries per-run text")
	}
	if !strings.Contains(string(embeddedPromptHead()), "You are the editor") {
		t.Error("the prompt file does not open as expected")
	}
}

func embeddedPromptHead() []byte { return []byte(systemPromptTemplate[:40]) }

// The prompt teaches every sound effect category and variant name of the build, the three clip sound modes and the audio checks, and
// the playbooks keep the bed policy (P3-12).
func TestPromptTeachesTheSoundReadyExport(t *testing.T) {
	p := SystemPrompt()
	for _, c := range timeline.SFXCategories() {
		if !strings.Contains(p, c+" (") {
			t.Errorf("the prompt does not teach the sound effect category %s", c)
		}
		vs, _ := timeline.SFXVariants(c)
		for _, v := range vs {
			if !strings.Contains(p, v) {
				t.Errorf("the prompt does not name the variant %s of %s", v, c)
			}
		}
	}
	for _, want := range []string{`"auto"`, "mute", "bed", "full", "audio_bed", "audio_effects", "audio_level", "-17 LUFS", `{"op":"set_clip_audio","mode":"bed"}`} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	for typ, want := range map[string]string{TypeWorkout: "mode bed", TypeTalking: "mode full", TypeUnboxing: "full"} {
		book, err := Playbook(typ, AvailableFeatures())
		if err != nil || !strings.Contains(book, want) {
			t.Errorf("playbook %s lacks %q: %v", typ, want, err)
		}
	}
	book, _ := Playbook(TypeWorkout, AvailableFeatures())
	for _, want := range []string{"sub_drop", "riser", "sting", "whoosh"} {
		if !strings.Contains(book, want) {
			t.Errorf("the workout playbook lacks %s", want)
		}
	}
}
