package taxonomy

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// The chips of the founder's clip picker (2026-10-18, five clips of one workspace): the quick look's free-form tags, every distinct one.
var founderChips = []string{
	"back exercise", "back extension", "back pose", "back training", "back view", "back workout", "cable machine", "cable row",
	"dark background", "gym", "hyperextension bench", "low angle", "machine", "machine row", "physique", "posing", "pulling",
	"rear view", "red lighting", "seated row", "side view", "strength training",
}

func refs(ss ...string) Set {
	out := make(Set, 0, len(ss))
	for _, s := range ss {
		r, ok := ParseRef(s)
		if !ok {
			panic(s)
		}
		out = append(out, r)
	}
	return out
}

func TestTheEmbeddedVocabularyLoadsAndStaysSmall(t *testing.T) {
	tax := Default()
	if tax.Version != 1 {
		t.Errorf("version = %d", tax.Version)
	}
	var ids []string
	for _, fc := range tax.Facets {
		ids = append(ids, fc.ID)
		if len(fc.Values) > 11 {
			t.Errorf("facet %s has %d values: the point is a short list", fc.ID, len(fc.Values))
		}
	}
	if want := []string{"type", "body", "movement", "angle", "equipment"}; !slices.Equal(ids, want) {
		t.Errorf("facets = %v, want %v", ids, want)
	}
	if !tax.FromProbe(Ref{"type", "slow_motion"}) {
		t.Error("slow motion must come from the file (the frame rate), not from words or the model")
	}
}

func TestTheFoundersChipsCollapseToAFewValues(t *testing.T) {
	tax := Default()
	union := map[Ref]bool{}
	for _, chip := range founderChips {
		if !tax.Recognised(chip) {
			t.Errorf("%q is neither mapped nor listed as ignored", chip)
		}
		got := tax.Map(chip)
		t.Logf("%-22s -> %v", chip, got.Strings())
		for _, r := range got {
			union[r] = true
		}
	}
	var all []string
	for r := range union {
		all = append(all, r.String())
	}
	slices.Sort(all)
	t.Logf("%d chips -> %d values: %v", len(founderChips), len(all), all)
	if len(all) > 12 {
		t.Errorf("%d chips map to %d values, want at most 12: %v", len(founderChips), len(all), all)
	}

	// Near-duplicates become one value; empty words become none.
	for _, c := range []struct {
		chips []string
		want  Set
	}{
		{[]string{"back view"}, refs("angle:back")},
		{[]string{"rear view"}, refs("angle:back")},
		{[]string{"side view"}, refs("angle:side")},
		{[]string{"back training"}, refs("type:lift", "body:back")},
		{[]string{"back workout"}, refs("type:lift", "body:back")},
		{[]string{"back exercise"}, refs("type:lift", "body:back")},
		{[]string{"seated row"}, refs("type:lift", "body:back", "movement:row")},
		{[]string{"cable row"}, refs("type:lift", "body:back", "movement:row", "equipment:cable")},
		{[]string{"machine row"}, refs("type:lift", "body:back", "movement:row", "equipment:machine")},
		{[]string{"hyperextension bench"}, refs("type:lift", "body:back", "movement:hinge")},
		{[]string{"back pose"}, refs("type:pose", "body:back")},
		{[]string{"physique"}, refs("type:pose")},
		{[]string{"gym"}, nil},
		{[]string{"dark background"}, nil},
		{[]string{"red lighting"}, nil},
		{[]string{"low angle"}, nil},
	} {
		if got := tax.Map(c.chips...); !slices.Equal(got, c.want) {
			t.Errorf("Map(%q) = %v, want %v", c.chips, got.Strings(), c.want.Strings())
		}
	}
}

// One clip's old tags vote: the type, the body part and the angle each end up as one value, however many words said it.
func TestOneClipsTagsVote(t *testing.T) {
	tax := Default()
	got := tax.Map("back pose", "posing", "back view", "dark background", "physique", "rear view")
	if want := refs("type:pose", "body:back", "angle:back"); !slices.Equal(got, want) {
		t.Errorf("a back pose clip = %v, want %v", got.Strings(), want.Strings())
	}
	got = tax.Map("cable row", "seated row", "gym", "back view", "back training", "cable machine")
	if want := refs("type:lift", "body:back", "movement:row", "angle:back", "equipment:cable"); !slices.Equal(got, want) {
		t.Errorf("a cable row clip = %v, want %v", got.Strings(), want.Strings())
	}
	// A pose and a lift both said: the type has room for one, and the most voted wins.
	got = tax.Map("posing", "physique", "lifting")
	if got.Of("type")[0] != "pose" || len(got.Of("type")) != 1 {
		t.Errorf("type = %v", got.Of("type"))
	}
}

// The exercise names of the director's sanity set (the founder's seven new clips, read by the quick look on 2026-10-18).
func TestExerciseNamesMapOntoTheMovements(t *testing.T) {
	tax := Default()
	for _, c := range []struct {
		text string
		want Set
	}{
		{"seated leg press", refs("type:lift", "body:legs", "movement:squat", "equipment:machine")},
		{"incline chest press machine", refs("type:lift", "body:chest", "movement:press", "equipment:machine")},
		{"person setting up plate-loaded machine", refs("type:b_roll", "equipment:machine")},
		{"lying leg curl", refs("type:lift", "body:legs", "movement:curl")},
		{"single-arm cable triceps pushdown", refs("type:lift", "body:arms", "movement:extension", "equipment:cable")},
		{"seated chest press", refs("type:lift", "body:chest", "movement:press")},
		{"hammer curls", refs("type:lift", "body:arms", "movement:curl")},
		{"lat pulldown", refs("type:lift", "body:back", "movement:pull")},
		{"Lat Pull-Down", refs("type:lift", "body:back", "movement:pull")},
		{"back squat", refs("type:lift", "body:legs", "movement:squat", "equipment:barbell")},
		{"bench press", refs("type:lift", "body:chest", "movement:press")},
		{"side view", refs("angle:side")},
		{"close-up", refs("angle:close_up")},
	} {
		if got := tax.Map(c.text); !slices.Equal(got, c.want) {
			t.Errorf("Map(%q) = %v, want %v", c.text, got.Strings(), c.want.Strings())
		}
	}
}

func TestBuildPrefersWhatWasGivenAndFillsTheRestFromHints(t *testing.T) {
	tax := Default()
	// The model said lift, back and side; the exercise name adds the movement and the equipment, not a second body part.
	got := tax.Build(refs("type:lift", "body:back", "angle:side"), "seated cable row")
	if want := refs("type:lift", "body:back", "movement:row", "angle:side", "equipment:cable"); !slices.Equal(got, want) {
		t.Errorf("Build = %v, want %v", got.Strings(), want.Strings())
	}
	// Given values over a facet's max are cut; unknown ones and the probe's are refused.
	got = tax.Build(refs("type:pose", "type:lift", "angle:front", "angle:side", "angle:back", "type:slow_motion"))
	if want := refs("type:pose", "angle:front", "angle:side"); !slices.Equal(got, want) {
		t.Errorf("Build = %v, want %v", got.Strings(), want.Strings())
	}
	if got := tax.Build([]Ref{{"body", "elbow"}, {"colour", "red"}}); len(got) != 0 {
		t.Errorf("unknown values were kept: %v", got)
	}
	// The probe's slow motion comes on top of the type the pictures gave.
	got = tax.WithProbe(refs("type:lift", "body:arms"), Ref{"type", "slow_motion"})
	if want := refs("type:lift", "type:slow_motion", "body:arms"); !slices.Equal(got, want) {
		t.Errorf("WithProbe = %v, want %v", got.Strings(), want.Strings())
	}
	if got := tax.WithProbe(refs("type:lift"), Ref{"type", "pose"}); !slices.Equal(got, refs("type:lift")) {
		t.Errorf("WithProbe took a value the file does not set: %v", got)
	}
}

func TestNormalizeReadsStoredLabels(t *testing.T) {
	tax := Default()
	got := tax.Normalize([]string{"angle:side", "body:back", "nonsense", "body:elbow", "body:back", "type:slow_motion", "type:lift", "Body:Back", "type:pose"})
	if want := refs("type:lift", "type:slow_motion", "body:back", "angle:side"); !slices.Equal(got, want) {
		t.Errorf("Normalize = %v, want %v", got.Strings(), want.Strings())
	}
}

func TestFromLegacy(t *testing.T) {
	tax := Default()
	for _, c := range []struct {
		in       Legacy
		want     Set
		exercise string
	}{
		{Legacy{Title: "Seated cable row, back view", FromQuickLook: true, Tags: []string{"cable row", "seated row", "gym", "back view"}},
			refs("type:lift", "body:back", "movement:row", "angle:back", "equipment:cable"), "Seated cable row"},
		{Legacy{Title: "Seated machine row, side view in a gym", FromQuickLook: true, Tags: []string{"gym", "machine row", "back training", "side view"}},
			refs("type:lift", "body:back", "movement:row", "angle:side", "equipment:machine"), "Seated machine row"},
		{Legacy{Title: "Back pose, rear view against dark wall", FromQuickLook: true, Tags: []string{"back pose", "posing", "back view", "dark background"}},
			refs("type:pose", "body:back", "angle:back"), ""},
		// A title a person typed is theirs: the exercise then comes from the tags.
		{Legacy{Title: "my best set", Tags: []string{"gym", "lat pulldown"}}, refs("type:lift", "body:back", "movement:pull"), "lat pulldown"},
		// Only a title: what it names fills the facets.
		{Legacy{Title: "Lat pulldown, front view", FromQuickLook: true, Tags: []string{"gym"}}, refs("type:lift", "body:back", "movement:pull"), "Lat pulldown"},
		{Legacy{}, nil, ""},
	} {
		got, ex := tax.FromLegacy(c.in)
		if !slices.Equal(got, c.want) || ex != c.exercise {
			t.Errorf("FromLegacy(%+v) = %v %q, want %v %q", c.in, got.Strings(), ex, c.want.Strings(), c.exercise)
		}
	}
}

func TestSearchTextAndLabels(t *testing.T) {
	tax := Default()
	s := refs("type:lift", "body:back", "angle:close_up")
	if got := tax.SearchText(s); got != "Lifting lift Back back Close-up close up" {
		t.Errorf("SearchText = %q", got)
	}
	if got := strings.Join(tax.Labels(s), ", "); got != "Lifting, Back, Close-up" {
		t.Errorf("Labels = %q", got)
	}
}

func TestLoadRefusesABrokenVocabulary(t *testing.T) {
	base := "version: 1\nfacets:\n  - id: type\n    label: Type\n    max: 1\n    values:\n      - { id: lift, label: Lifting }\n      - { id: slow, label: Slow, source: probe }\n"
	for name, body := range map[string]string{
		"unknown field":         base + "colour: red\n",
		"synonym to unknown":    base + "synonyms:\n  rows: [movement:row]\n",
		"synonym to the probe":  base + "synonyms:\n  slow mo: [type:slow]\n",
		"synonym not normal":    base + "synonyms:\n  Lifting!: [type:lift]\n",
		"ignored and a synonym": base + "synonyms:\n  reps: [type:lift]\nignore: [reps]\n",
		"no facets":             "version: 1\nfacets: []\n",
		"bad id":                "version: 1\nfacets:\n  - id: Type\n    label: T\n    max: 1\n    values:\n      - { id: lift, label: L }\n",
		"value named unsure":    "version: 1\nfacets:\n  - id: type\n    label: T\n    max: 1\n    values:\n      - { id: unsure, label: L }\n",
		"implies own facet":     "version: 1\nfacets:\n  - id: type\n    label: T\n    max: 1\n    values:\n      - { id: lift, label: L, implies: [type:pose] }\n      - { id: pose, label: P }\n",
	} {
		if _, err := Load(fstest.MapFS{"v.yaml": {Data: []byte(body)}}, "v.yaml"); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if _, err := Load(fstest.MapFS{"v.yaml": {Data: []byte(base + "synonyms:\n  reps: [type:lift]\n")}}, "v.yaml"); err != nil {
		t.Errorf("a good file: %v", err)
	}
}
