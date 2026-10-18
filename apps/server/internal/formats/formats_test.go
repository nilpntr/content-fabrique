package formats

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	configfiles "github.com/nilpntr/content-fabrique/config"
)

// Every card of config/formats is valid under the schema, its id is its file's name, and the library has the formats the design
// note promises.
func TestTheEmbeddedLibraryIsValid(t *testing.T) {
	lib, err := Load(configfiles.Formats())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(lib.Version, "formats@") || len(lib.Version) != len("formats@")+8 {
		t.Errorf("version %q", lib.Version)
	}
	formats, devices := lib.IDs(KindFormat), lib.IDs(KindDevice)
	if n := len(formats) + len(devices); n < 10 || n > 16 {
		t.Errorf("%d cards, want about 10 to 14", n)
	}
	for _, want := range []string{"tutorial_101", "numbered_listicle", "quote_reveal", "transformation", "meme_dialogue", "story_captions", "rep_counter", "pov", "myth_fact", "montage", "form_check"} {
		if !slices.Contains(formats, want) {
			t.Errorf("no format %s", want)
		}
	}
	if !slices.Contains(devices, "loop_ending") {
		t.Errorf("no loop_ending device: %v", devices)
	}
	for _, c := range lib.Cards() {
		if c.Kind == KindFormat && len(c.Detect.Rules) == 0 {
			t.Errorf("%s: a format without detection rules", c.ID)
		}
		for _, f := range c.Text.TitleForms {
			_ = TitleFormRegexp(f) // compiles
		}
	}
}

func card(t *testing.T, mutate func(string) string) error {
	t.Helper()
	raw, err := fs.ReadFile(configfiles.Formats(), "tutorial_101.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Parse([]byte(mutate(string(raw))))
	return err
}

// The schema refuses what a card must not have, each with a message that names the field.
func TestTheSchemaRefusesBadCards(t *testing.T) {
	if err := card(t, func(s string) string { return s }); err != nil {
		t.Fatalf("the unchanged card: %v", err)
	}
	for _, tc := range []struct{ name, from, to, want string }{
		{"unknown field", "kind: format", "kind: format\ncolour: red", "field colour not found"},
		{"bad kind", "kind: format", "kind: genre", "kind \"genre\""},
		{"bad signal", "needs: [reps]", "needs: [biceps]", "fits.needs: \"biceps\""},
		{"bad content type", "content_types: [workout, tutorial]", "content_types: [cooking]", "fits.content_types"},
		{"bad role", "roles: [hook]", "roles: [opener]", "beats[0].roles"},
		{"beat order", "  - name: hook", "  - name: intro", "beats[0] is \"intro\""},
		{"bad pattern", "pattern: single_title", "pattern: subtitles", "text.pattern"},
		{"lowercase form", `["<TOPIC> 101",`, `["<TOPIC> basics",`, "the fixed words are in capitals"},
		{"bad slot", `["<TOPIC> 101",`, `["<topic> 101",`, "a slot is a word in capitals"},
		{"length", "length_s: {min: 12, max: 35}", "length_s: {min: 40, max: 35}", "length_s"},
		{"bad vibe", "vibes: {calm: great, lively: great, hype: good}", "vibes: {calm: great, lively: great, hype: fine}", "vibes.hype"},
		{"missing vibe", "vibes: {calm: great, lively: great, hype: good}", "vibes: {calm: great, lively: great}", "vibes.hype"},
		{"bad cue", "all: [title_series]", "all: [title_sereis]", "detect.rules[0].all"},
		{"score", "score: 0.9", "score: 1.5", "outside 0.5 to 1"},
		{"keyword case", "keywords: [how to, tutorial, guide, technique]", "keywords: [How To]", "detect.keywords[0]"},
		{"no rules", "    - all: [title_series]\n      score: 0.9\n    - all: [keyword_hook]\n      none: [pattern_dialogue, pattern_story_captions]\n      score: 0.75\n", "    []\n", "a format must be recognisable"},
		{"payoff", "payoff: end", "payoff: middle", "check.payoff"},
		{"empty pitch", "pitch: One movement", "pitch: \"\"\nx: One movement", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := card(t, func(s string) string {
				if !strings.Contains(s, tc.from) {
					t.Fatalf("the card has no %q", tc.from)
				}
				return strings.Replace(s, tc.from, tc.to, 1)
			})
			if err == nil {
				t.Fatal("accepted")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q lacks %q", err, tc.want)
			}
		})
	}
}

func TestLoadRefusesAnIDThatIsNotTheFileName(t *testing.T) {
	raw, err := fs.ReadFile(configfiles.Formats(), "pov.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(fstest.MapFS{"other.yaml": {Data: raw}}); err == nil || !strings.Contains(err.Error(), "not the file's name") {
		t.Errorf("err = %v", err)
	}
	if _, err := Load(fstest.MapFS{}); err == nil {
		t.Error("an empty library loaded")
	}
}

func TestTitleForms(t *testing.T) {
	for _, tc := range []struct {
		title string
		forms []string
		want  bool
	}{
		{"UPPER CHEST 101", []string{"<TOPIC> 101"}, true},
		{"upper chest 101", []string{"<TOPIC> 101"}, true},
		{"UPPER\nCHEST  101", []string{"<TOPIC> 101"}, true},
		{"101", []string{"<TOPIC> 101"}, false},
		{"UPPER CHEST 102", []string{"<TOPIC> 101"}, false},
		{"3 MOVES FOR WIDTH", []string{"<N> <TOPIC> FOR <GOAL>"}, true},
		{"MOVES FOR WIDTH", []string{"<N> <TOPIC> FOR <GOAL>"}, false},
		{"POV: first set in a month", []string{"POV: <SITUATION>"}, true},
		{"80KG VS 72KG", []string{"<THEN> VS <NOW>"}, true},
		{"a (weird) [title]+?", []string{"<TOPIC>"}, true},
	} {
		if got := MatchesTitleForm(tc.title, tc.forms); got != tc.want {
			t.Errorf("%q against %v = %v, want %v", tc.title, tc.forms, got, tc.want)
		}
	}
}

// Detection on references shaped like the kinds of video the library was written from (our own words in the lines).
func TestDetect(t *testing.T) {
	lib := Default()
	shots := func(n int, each float64) ([]float64, []float64) {
		var cuts, s []float64
		for i := range n {
			s = append(s, each)
			if i > 0 {
				cuts = append(cuts, float64(i)*each)
			}
		}
		return cuts, s
	}
	cuts3, shots3 := []float64{2.1, 3.4, 4.0}, []float64{2.1, 1.3, 0.6, 9}
	cuts8, shots8 := shots(9, 1.4)
	for _, tc := range []struct {
		name string
		ref  Reference
		want string
	}{
		{"series title", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "UPPER CHEST 101", CutsS: cuts3, ShotsS: shots3}, "tutorial_101"},
		{"count title with chapters", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "3 KEYS TO WIDER LATS", Chapters: []string{"1. PULL-UPS", "2. ROWS"}, CutsS: cuts8, ShotsS: shots8}, "numbered_listicle"},
		{"chapters alone", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "WIDER LATS", Lines: []Line{{3, "1. PULL-UPS"}, {6, "2. ROWS"}}, CutsS: cuts8, ShotsS: shots8}, "numbered_listicle"},
		{"quote then the drop", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "\u201cdiscipline is doing it when nobody watches\u201d", CutsS: []float64{3.1}, ShotsS: []float64{3.1, 12}, DropS: 3.0}, "quote_reveal"},
		{"weights against each other", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "64kg vs 81kg", CutsS: []float64{2.5}, ShotsS: []float64{2.5, 6}}, "transformation"},
		{"two years", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "2021 \u2192 2025", CutsS: []float64{2.5}, ShotsS: []float64{2.5, 6}}, "transformation"},
		{"dialogue", Reference{TextRead: true, Pattern: TextDialogue, Lines: []Line{{1, "\"where have you been?\""}, {3, "\"you don't want to know\""}}, CutsS: []float64{2, 4}, ShotsS: []float64{2, 2, 5}}, "meme_dialogue"},
		{"first person story", Reference{TextRead: true, Pattern: TextStoryCaptions, Lines: []Line{{1, "what fixed my shoulders"}, {3, "I stopped pressing every day"}, {5, "and I started with raises"}}, CutsS: []float64{2, 4, 6}, ShotsS: []float64{2, 2, 2, 3}}, "story_captions"},
		{"pov", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "POV: the gym at 5 am", CutsS: []float64{2}, ShotsS: []float64{2, 6}}, "pov"},
		{"myth", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "MYTH: heavier is always better", CutsS: []float64{3}, ShotsS: []float64{3, 6}}, "myth_fact"},
		{"montage", Reference{TextRead: true, Pattern: TextNone, CutsS: cuts8, ShotsS: shots8, Handheld: true}, "montage"},
		{"counter", Reference{TextRead: true, Pattern: TextSingleTitle, Lines: []Line{{1, "rep 1"}, {2, "rep 4"}, {4, "rep 8"}}, CutsS: []float64{}, ShotsS: []float64{12}}, "rep_counter"},
		{"form check", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "not this", Lines: []Line{{3, "do this"}}, CutsS: []float64{2.5}, ShotsS: []float64{2.5, 5}}, "form_check"},
		{"one take", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "BACK DAY", ShotsS: []float64{25}}, "one_take"},
		{"weights over two shots", Reference{TextRead: true, Pattern: TextSingleTitle, Lines: []Line{{1, "92kg vs"}, {4, "78kg"}}, CutsS: []float64{3, 6}, ShotsS: []float64{3, 3, 9}}, "transformation"},
		{"a witty how-to title is a tutorial, not a quote", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "how to fold a towel bicep", CutsS: []float64{2.9}, ShotsS: []float64{2.9, 20}, DropS: 3}, "tutorial_101"},
		{"a thought in the first person is a quote", Reference{TextRead: true, Pattern: TextSingleTitle, Title: "I keep training and nothing seems to change", CutsS: []float64{2.2}, ShotsS: []float64{2.2, 6}, DropS: 2.3}, "quote_reveal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags := lib.Detect(tc.ref)
			if len(tags) == 0 || tags[0].ID != tc.want {
				t.Errorf("tags %v (cues %v), want %s first", tags, CuesOf(tc.ref), tc.want)
			}
		})
	}
	// A story in another language: its opening line in the first person makes it a first-person story.
	dutch := Reference{TextRead: true, Pattern: TextStoryCaptions, Lines: []Line{{1, "zo pak ik mijn benen aan"}, {4, "Squat variatie"}, {7, "Lunge variatie"}}, CutsS: []float64{3, 6}, ShotsS: []float64{3, 3, 5}}
	if tags := lib.Detect(dutch); len(tags) == 0 || tags[0] != (Tag{"story_captions", 0.9}) {
		t.Errorf("a Dutch first-person story: %v (cues %v)", tags, CuesOf(dutch))
	}
	// Without the text read only the cuts speak: a long take is one take, a cut-up video a montage, and nothing is a dialogue.
	if tags := lib.Detect(Reference{ShotsS: []float64{25}}); len(tags) == 0 || tags[0].ID != "one_take" {
		t.Errorf("a single take with no text read: %v", tags)
	}
	if tags := lib.Detect(Reference{TextRead: true, Pattern: TextSingleTitle, Title: "x", CutsS: []float64{4}, ShotsS: []float64{4, 4}}); len(tags) != 0 {
		t.Errorf("a plain two-shot video with a one-word title is tagged %v", tags)
	}
	// A loop is a device: never a tag.
	for _, tag := range lib.Detect(Reference{TextRead: true, Pattern: TextNone, CutsS: cuts8, ShotsS: shots8}) {
		if tag.ID == "loop_ending" {
			t.Error("a device was tagged")
		}
	}
}

func TestShares(t *testing.T) {
	got := Shares([][]Tag{{{"a", 0.9}}, {{"a", 0.8}, {"b", 0.7}}, {{"b", 0.9}}, nil})
	want := []Share{{"a", 2, 0.5}, {"b", 1, 0.25}}
	if !slices.Equal(got, want) {
		t.Errorf("shares %v, want %v", got, want)
	}
	if Shares(nil) != nil {
		t.Error("shares of nothing")
	}
}

func TestSignalsAndRank(t *testing.T) {
	lib := Default()
	gym := Footage{Clips: []Clip{
		{Kind: "video", Words: "back pose in the mirror", Pose: true, DurationMs: 6000, Analysed: true},
		{Kind: "video", Words: "seated cable row", Reps: 8, DurationMs: 30000, Analysed: true},
		{Kind: "video", Words: "lat pulldown", Reps: 6, DurationMs: 25000, Analysed: true},
		{Kind: "audio", Words: "song"},
	}}
	sig := SignalsOf(gym)
	for _, s := range []string{SignalPose, SignalReps, SignalTwoExercises, SignalBigSet, SignalSong, SignalLongClip, SignalClips3} {
		if !sig[s] {
			t.Errorf("signal %s missing: %v", s, sig)
		}
	}
	for _, s := range []string{SignalThenClip, SignalThreeExercises, SignalManyClips, SignalLifestyle} {
		if sig[s] {
			t.Errorf("signal %s holds: %v", s, sig)
		}
	}
	creator := []Share{{ID: "tutorial_101", Videos: 3, Share: 0.6}, {ID: "numbered_listicle", Videos: 2, Share: 0.4}}
	fits := lib.Rank(gym, RankOptions{ContentType: "workout", Creator: creator, Exclude: []string{"numbered_listicle"}, Vibe: "lively"})
	if fits[0].Card.ID != "tutorial_101" || !fits[0].CreatorUses {
		t.Errorf("the first fit is %s, want the creator's go-to tutorial_101", fits[0].Card.ID)
	}
	byID := map[string]Fit{}
	novel := 0
	for _, f := range fits {
		byID[f.Card.ID] = f
		if f.Novel {
			novel++
			if !f.Fits || f.CreatorUses || f.Excluded || f.Card.Kind != KindFormat {
				t.Errorf("%s is put forward as new but fits %v, used %v, excluded %v", f.Card.ID, f.Fits, f.CreatorUses, f.Excluded)
			}
		}
	}
	if novel != 2 {
		t.Errorf("%d novel formats, want 2", novel)
	}
	if f := byID["transformation"]; f.Fits || len(f.Missing) == 0 || !strings.Contains(f.Missing[0], "old clip") {
		t.Errorf("transformation fits footage with no old clip: %+v", f)
	}
	if !byID["numbered_listicle"].Excluded {
		t.Error("the excluded format is not marked")
	}
	if !byID["meme_dialogue"].Fits {
		t.Error("meme_dialogue needs a pose or an old clip or a big set: the pose is there")
	}
	// The order: fitting formats, then devices, then what does not fit.
	seenDevice, seenMisfit := false, false
	for _, f := range fits {
		switch {
		case !f.Fits:
			seenMisfit = true
		case f.Card.Kind == KindDevice:
			if seenMisfit {
				t.Error("a device after a format that does not fit")
			}
			seenDevice = true
		default:
			if seenDevice || seenMisfit {
				t.Errorf("%s comes after a device or a misfit", f.Card.ID)
			}
		}
	}
	// Without the creator's formats nothing is "new to you".
	for _, f := range lib.Rank(gym, RankOptions{}) {
		if f.New || f.Novel {
			t.Errorf("%s is new without the creator's formats", f.Card.ID)
		}
	}
	// An old photo named in the brief makes the transformation fit.
	gym.Brief = "my transformation: an old photo from before, then today"
	if f := lib.Rank(gym, RankOptions{}); !slices.ContainsFunc(f, func(x Fit) bool { return x.Card.ID == "transformation" && x.Fits }) {
		t.Error("the brief's old photo does not make the transformation fit")
	}
}

func TestDescribeHasTheCard(t *testing.T) {
	c, ok := Default().Card("transformation")
	if !ok {
		t.Fatal("no card")
	}
	d := c.Describe()
	for _, want := range []string{c.Pitch, "hook (", "payoff (", "title forms", "music:", "length", "vibes: calm", "how it plays"} {
		if !strings.Contains(d, want) {
			t.Errorf("the description lacks %q:\n%s", want, d)
		}
	}
}
