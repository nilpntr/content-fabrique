package hooklab

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

const repoSuite = "../../../../suites/hook-lab.yaml"

// The checked-in case set passes: it is the lab's regression suite, and it needs nothing but this package.
func TestRepoCasesPass(t *testing.T) {
	s, err := LoadCases(repoSuite)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "hook-lab" || len(s.Cases) < 12 {
		t.Fatalf("suite %q with %d cases", s.Name, len(s.Cases))
	}
	covered := map[string]bool{}
	for _, r := range RunCases(s) {
		if !r.Passed {
			t.Errorf("case %s: %s", r.ID, strings.Join(r.Problems, "; "))
		}
		for _, c := range r.Lab.Ranked {
			for _, id := range c.FailedIDs() {
				covered[id] = true
			}
		}
	}
	// Every criterion fails somewhere in the set: a criterion no case can fail is not tested.
	for _, id := range Criteria {
		if !covered[id] {
			t.Errorf("no case has a variant that fails %s", id)
		}
	}
}

// The same input gives the same lab, byte for byte.
func TestRunIsDeterministic(t *testing.T) {
	s, err := LoadCases(repoSuite)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(RunCases(s))
	for range 5 {
		if b, _ := json.Marshal(RunCases(s)); string(a) != string(b) {
			t.Fatal("two runs of the cases differ")
		}
	}
}

func talk() *Speech {
	words := []transcribe.Word{
		{Text: "Um,", StartMs: 0, EndMs: 200}, {Text: "so", StartMs: 260, EndMs: 400}, {Text: "today", StartMs: 460, EndMs: 760},
		{Text: "we", StartMs: 800, EndMs: 900}, {Text: "train", StartMs: 940, EndMs: 1240}, {Text: "back.", StartMs: 1280, EndMs: 1600},
		{Text: "Stop", StartMs: 2100, EndMs: 2400}, {Text: "skipping", StartMs: 2440, EndMs: 2900}, {Text: "your", StartMs: 2940, EndMs: 3080},
		{Text: "warm-up!", StartMs: 3120, EndMs: 3700},
	}
	return &Speech{Words: words, Structure: transcribe.Analyse(words, "en")}
}

// What a card says about a variant never quotes the speaker or the text hook: it reaches the model outside a data block.
func TestDetailsNeverQuote(t *testing.T) {
	sp := talk()
	lit, soft := 0.02, 0.1
	for _, v := range []Variant{
		{ID: "a", AssetID: "x", InMs: 0, OutMs: 1700, Text: "ZANZIBAR QUOKKA", TextAtMs: 2500, TextDurationMs: 200},
		{ID: "b", AssetID: "x", InMs: 2250, OutMs: 3780, Text: strings.Repeat("ZANZIBAR ", 20)},
		{ID: "c", AssetID: "x", InMs: 2420, OutMs: 3780},
	} {
		for _, place := range append([]string{""}, TextPlaces...) {
			card := Score(v, Evidence{Speech: sp, Picture: &Picture{Dead: []Span{{0, 500}}, Lit: &lit, Sharpness: &soft}, Text: place}, DefaultWeights())
			if len(card.Results) != len(Criteria) {
				t.Fatalf("%d results", len(card.Results))
			}
			for i, r := range card.Results {
				if r.ID != Criteria[i] {
					t.Errorf("result %d is %s, want %s", i, r.ID, Criteria[i])
				}
				low := strings.ToLower(r.Detail)
				for _, secret := range []string{"zanzibar", "quokka", "skipping", "warm-up", "today"} {
					if strings.Contains(low, secret) {
						t.Errorf("%s quotes %q: %s", r.ID, secret, r.Detail)
					}
				}
				if r.Detail == "" || r.Score < 0 || r.Score > 1 || (r.Status == StatusNA && r.Score != 0) {
					t.Errorf("%s: %+v", r.ID, r)
				}
			}
		}
	}
	// The line itself is on the card, for a caller that shows it as data.
	if c := Score(Variant{ID: "b", AssetID: "x", InMs: 2020, OutMs: 3780}, Evidence{Speech: sp}, nil); c.Line == nil || !strings.Contains(c.Line.Text, "warm-up") || c.Line.Words != 4 {
		t.Errorf("line %+v", c.Line)
	}
}

func TestScoreBoundsAndSpeed(t *testing.T) {
	sp := talk()
	// Nothing known: nothing applies but the missing hook, and the total is 0.
	c := Score(Variant{ID: "a", AssetID: "x", InMs: 0, OutMs: 2000}, Evidence{}, nil)
	if c.Applied != 1 || c.Failed != 1 || c.Total != 0 {
		t.Errorf("nothing known: %+v", c)
	}
	if c := Score(Variant{ID: "a", AssetID: "x", InMs: 0, OutMs: 2000}, Evidence{NoTitle: true}, nil); c.Applied != 0 || c.Total != 0 {
		t.Errorf("no title, nothing known: %+v", c)
	}
	// A shot played at double speed reaches its first word in half the time: 4000 ms of silence is 2000 ms on screen.
	late := &Speech{Words: []transcribe.Word{{Text: "Go.", StartMs: 4000, EndMs: 4300}}}
	late.Structure = transcribe.Analyse(late.Words, "en")
	slow := Score(Variant{ID: "a", AssetID: "x", InMs: 0, OutMs: 4400}, Evidence{Speech: late}, nil)
	fast := Score(Variant{ID: "a", AssetID: "x", InMs: 0, OutMs: 4400, Speed: 2}, Evidence{Speech: late}, nil)
	if slow.Results[0].Status != StatusFail || fast.Results[0].Status != StatusPass {
		t.Errorf("speed: slow %+v, fast %+v", slow.Results[0], fast.Results[0])
	}
	// Words that are not valid (they overlap) are no speech.
	bad := &Speech{Words: []transcribe.Word{{Text: "a", StartMs: 100, EndMs: 500}, {Text: "b", StartMs: 300, EndMs: 600}}}
	if c := Score(Variant{ID: "a", AssetID: "x", InMs: 0, OutMs: 2000}, Evidence{Speech: bad, NoTitle: true}, nil); c.Applied != 0 || c.Line != nil {
		t.Errorf("invalid words: %+v", c)
	}
	// A structure that points past the words is not followed.
	off := &Speech{Words: sp.Words[:2], Structure: sp.Structure}
	_ = Score(Variant{ID: "a", AssetID: "x", InMs: 0, OutMs: 2000}, Evidence{Speech: off}, nil)
	_ = Propose(Input{Clips: []Clip{{AssetID: "x", Speech: off}}})
}

func TestRunRanksAndBreaksTies(t *testing.T) {
	clear := func(v Variant) Evidence { return Evidence{Text: TextClear} }
	vs := []Variant{
		{ID: "late", AssetID: "x", InMs: 0, OutMs: 2000, Text: "BACK DAY", TextAtMs: 2500},
		{ID: "one", AssetID: "x", InMs: 3000, OutMs: 5000, Text: "BACK DAY"},
		{ID: "two", AssetID: "x", InMs: 6000, OutMs: 8000, Text: "BACK DAY"},
	}
	lab := Run(vs, clear, Resolve(nil))
	if lab.Pick != "one" || lab.Ranked[1].Variant.ID != "two" || lab.Ranked[2].Variant.ID != "late" {
		t.Errorf("ranked %v", ids(lab))
	}
	if !lab.Close() || lab.Margin != 0 || lab.WeightsFrom != FromDefaults {
		t.Errorf("margin %v from %q", lab.Margin, lab.WeightsFrom)
	}
	if best, ok := lab.Best(); !ok || best.Variant.ID != "one" {
		t.Errorf("best %+v", best)
	}
	if lab := Run(nil, clear, Resolve(nil)); lab.Pick != "" || lab.Close() {
		t.Errorf("empty lab %+v", lab)
	}
	if _, ok := (Lab{}).Best(); ok {
		t.Error("an empty lab has a best")
	}
	// No more than MaxVariants are scored, and a nil evidence function is no evidence.
	many := make([]Variant, MaxVariants+3)
	for i := range many {
		many[i] = Variant{ID: string(rune('a' + i)), AssetID: "x", InMs: int64(i) * 1000, OutMs: int64(i)*1000 + 900}
	}
	if lab := Run(many, nil, Resolve(nil)); len(lab.Ranked) != MaxVariants {
		t.Errorf("%d ranked", len(lab.Ranked))
	}
}

func ids(l Lab) []string {
	var out []string
	for _, c := range l.Ranked {
		out = append(out, c.Variant.ID)
	}
	return out
}

func TestCheckAndDistinct(t *testing.T) {
	a := Variant{ID: "a", AssetID: "x", InMs: 0, OutMs: 2000, Text: "Back day!"}
	b := Variant{ID: "b", AssetID: "x", InMs: 300, OutMs: 2000, Text: "BACK  DAY"}
	c := Variant{ID: "c", AssetID: "y", InMs: 0, OutMs: 2000, Text: "Back day!"}
	d := Variant{ID: "d", AssetID: "x", InMs: 0, OutMs: 2000, Text: "Leg day"}
	if !Same(a, b) || Same(a, c) || Same(a, d) {
		t.Error("Same is wrong")
	}
	if got := Distinct([]Variant{a, b, c, d}); len(got) != 3 || got[0].ID != "a" || got[1].ID != "c" {
		t.Errorf("distinct %v", got)
	}
	if msg := Check([]Variant{a, c, d}); msg != "" {
		t.Errorf("three distinct variants: %s", msg)
	}
	for name, vs := range map[string][]Variant{
		"two":        {a, c},
		"same":       {a, b, c},
		"no id":      {a, c, {AssetID: "z", OutMs: 1}},
		"twice":      {a, c, {ID: "a", AssetID: "z", OutMs: 1}},
		"no clip":    {a, c, {ID: "e", OutMs: 1}},
		"no range":   {a, c, {ID: "e", AssetID: "z", InMs: 5, OutMs: 5}},
		"text times": {a, c, {ID: "e", AssetID: "z", OutMs: 5, TextAtMs: -1}},
	} {
		if Check(vs) == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestResolveWeights(t *testing.T) {
	def := DefaultWeights()
	for _, id := range Criteria {
		if def[id] <= 0 {
			t.Errorf("%s has no default weight", id)
		}
	}
	def[InFirst2s] = 99 // a copy: the defaults do not change
	if Resolve(nil).Weights[InFirst2s] != 2 || Resolve(NoPlaybook{}).From != FromDefaults {
		t.Error("the defaults changed")
	}
	// A weight that is not usable takes the default.
	if w := (Weights{InFirst2s: -1, Readable: math.NaN(), TextOffBody: math.Inf(1), FirstFrame: 3}); w.Of(InFirst2s) != 2 || w.Of(Readable) != 1 || w.Of(TextOffBody) != 2 || w.Of(FirstFrame) != 3 || w.Of("nope") != 0 {
		t.Error("Of is wrong")
	}
	res := Resolve(StaticPlaybook{Version: "2026-47", Findings: []Finding{
		{ID: "f1", Dimension: "hook", Criterion: InFirst2s, Effect: 1.8, Confidence: "medium"},
		{ID: "f2", Dimension: "hook", Criterion: InFirst2s, Effect: 1.2, Confidence: "high"},   // the more confident wins
		{ID: "f3", Criterion: TextOffBody, Effect: 9, Confidence: "high"},                      // held to MaxScale; no dimension is read as hook
		{ID: "f4", Dimension: "hook", Criterion: Readable, Effect: 0.01, Confidence: "high"},   // held to MinScale
		{ID: "f5", Dimension: "hook", Criterion: FirstFrame, Effect: 3, Confidence: "low"},     // a hint only
		{ID: "f6", Dimension: "caption", Criterion: CleanStart, Effect: 3, Confidence: "high"}, // another dimension
		{ID: "f7", Dimension: "hook", Criterion: "made_up", Effect: 3, Confidence: "high"},
		{ID: "f8", Dimension: "hook", Criterion: NoFillerStart, Effect: math.NaN(), Confidence: "high"},
		{ID: "f9", Dimension: "hook", Criterion: NoFillerStart, Effect: -2, Confidence: "high"},
		{ID: "f10", Dimension: "hook", Criterion: NoFillerStart, Effect: 1.5, Confidence: "certain"},
	}})
	want := Weights{InFirst2s: 2.4, TextOffBody: 4, Readable: 0.5, FirstFrame: 1.5, CleanStart: 1.5, NoFillerStart: 1}
	if !reflect.DeepEqual(res.Weights, want) {
		t.Errorf("weights %v, want %v", res.Weights, want)
	}
	if res.From != "playbook 2026-47" || len(res.Applied) != 3 || res.Applied[0] != (Applied{Finding: "f2", Criterion: InFirst2s, Scale: 1.2}) {
		t.Errorf("from %q, applied %+v", res.From, res.Applied)
	}
	for _, id := range Criteria { // no finding switches a criterion off or lets it drown the rest
		if r := res.Weights[id] / defaultWeights[id]; r < MinScale || r > MaxScale {
			t.Errorf("%s is scaled by %v", id, r)
		}
	}
	if Resolve(StaticPlaybook{Findings: []Finding{{Criterion: Readable, Effect: 2, Confidence: "high"}}}).From != "playbook" {
		t.Error("a playbook without a version")
	}
}

func TestLoadPlaybook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "findings.yaml")
	body := "playbook_version: \"2026-47\"\nfindings:\n  - id: fnd_1\n    dimension: hook\n    criterion: text_off_body\n    effect: 1.6\n    confidence: medium\n    claim: \"ignored\"\n    n_posts: 34\n    evidence_ids: [ev_1]\n  - id: fnd_2\n    dimension: length\n    effect: 2\n    confidence: high\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPlaybook(path)
	if err != nil {
		t.Fatal(err)
	}
	if res := Resolve(p); res.Weights[TextOffBody] != 3.2 || res.From != "playbook 2026-47" || len(res.Applied) != 1 {
		t.Errorf("%+v", res)
	}
	if _, err := LoadPlaybook(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("a missing file loaded")
	}
	if err := os.WriteFile(path, []byte("findings: {"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPlaybook(path); err == nil {
		t.Error("broken YAML loaded")
	}
}

func TestLoadCasesRefuses(t *testing.T) {
	dir := t.TempDir()
	ok := "suite: s\ncases:\n  - id: one\n    clips: [{asset: x, duration_ms: 1000}]\n    variants: [{id: a, asset_id: x, in_ms: 0, out_ms: 500}]\n"
	for name, body := range map[string]string{
		"good":            ok,
		"unknown key":     strings.Replace(ok, "id: one", "id: one\n    expcet: {pick: a}", 1),
		"no cases":        "suite: s\ncases: []\n",
		"bad id":          strings.Replace(ok, "id: one", "id: One Two", 1),
		"unknown clip":    strings.Replace(ok, "asset_id: x", "asset_id: y", 1),
		"bad word":        strings.Replace(ok, "duration_ms: 1000", "words: [[hi, 0]]", 1),
		"bad place":       strings.Replace(ok, "out_ms: 500", "out_ms: 500, text_place: nowhere", 1),
		"bad criterion":   ok + "    expect: {fails: {a: [made_up]}}\n",
		"both":            ok + "    propose: {shot_ms: 1000}\n",
		"neither":         "suite: s\ncases:\n  - id: one\n    clips: [{asset: x}]\n",
		"variant no id":   strings.Replace(ok, "{id: a, ", "{", 1),
		"clip named once": strings.Replace(ok, "clips: [{asset: x, duration_ms: 1000}]", "clips: [{asset: x}, {asset: x}]", 1),
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadCases(path)
		if (err == nil) != (name == "good") {
			t.Errorf("%s: err %v", name, err)
		}
	}
	// A case that does not get what it expects says so.
	s, err := LoadCases(filepath.Join(dir, "good.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s.Cases[0].Expect = Expect{Pick: "z", Order: []string{"z"}, Count: 2, Kinds: []string{KindPlanned}, WeightsFrom: "playbook",
		Fails: map[string][]string{"a": {Readable}, "q": nil}, NotApplicable: map[string][]string{"a": {InFirst2s}},
		MinTotal: map[string]float64{"a": 0.9}, MaxTotal: map[string]float64{"a": -1}}
	if r := RunCase(s.Cases[0]); r.Passed || len(r.Problems) != 10 {
		t.Errorf("%d problems: %v", len(r.Problems), r.Problems)
	}
}

func TestPropose(t *testing.T) {
	sp := talk()
	clips := []Clip{
		{AssetID: "talk", DurationMs: 6000, Speech: sp},
		{AssetID: "set", DurationMs: 12000, Dead: []Span{{0, 2000}, {8000, 12000}}, Moments: []Moment{{2000, 5000, 0.81}, {5000, 8000, 0.88}, {0, 1500, 0.99}}},
	}
	got := Propose(Input{Clips: clips, Planned: "set", ShotMs: 1500, Text: "BACK DAY", TextDurationMs: 2000})
	if len(got) != 3 || Check(got) != "" {
		t.Fatalf("proposed %+v: %s", got, Check(got))
	}
	// The bold line, in the gap before its first word; the best moment that is not in dead time; the planned clip's next opening.
	if got[0].Kind != KindSpokenLine || got[0].InMs != 2020 || got[0].OutMs != 3780 || got[0].ID != "a" {
		t.Errorf("line %+v", got[0])
	}
	if got[1].Kind != KindBestMoment || got[1].AssetID != "set" || got[1].InMs != 5000 || got[1].OutMs != 6500 {
		t.Errorf("moment %+v", got[1])
	}
	if got[2].Kind != KindPlanned || got[2].AssetID != "set" || got[2].InMs != 2000 {
		t.Errorf("planned %+v", got[2])
	}
	for _, v := range got {
		if v.Text != "BACK DAY" || v.TextDurationMs != 2000 {
			t.Errorf("text of %+v", v)
		}
	}
	if !reflect.DeepEqual(got, Propose(Input{Clips: clips, Planned: "set", ShotMs: 1500, Text: "BACK DAY", TextDurationMs: 2000})) {
		t.Error("two proposals differ")
	}
	// A filler-led sentence opens after the filler; a line next to a word of low confidence is not proposed.
	lines := lineCandidates(clips[0])
	if len(lines) != 2 || lines[0].v.InMs != 200 || lines[0].merit >= lines[1].merit {
		t.Errorf("lines %+v", lines)
	}
	low := talk()
	low.Words[6].Confidence = 0.1
	if l := lineCandidates(Clip{AssetID: "talk", Speech: low}); len(l) != 1 {
		t.Errorf("lines next to a low word: %+v", l)
	}
	// A clip that is all dead time, or has no length, gives no opening; no clips give none.
	if got := Propose(Input{Clips: []Clip{{AssetID: "rest", DurationMs: 3000, Dead: []Span{{0, 1000}, {1000, 3000}}}}}); len(got) != 0 {
		t.Errorf("all dead: %+v", got)
	}
	if got := Propose(Input{}); len(got) != 0 {
		t.Errorf("no clips: %+v", got)
	}
	// A shot never runs past the end of its clip.
	if got := Propose(Input{Clips: []Clip{{AssetID: "short", DurationMs: 900}}}); len(got) != 1 || got[0].OutMs != 900 || got[0].Kind != KindClipStart {
		t.Errorf("short clip: %+v", got)
	}
}
