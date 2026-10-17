package styleprofile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// referenceMeasures are the measures of five finished videos of one creator (derived numbers only: the videos are not in the repository).
// The cuts, the music and the light were measured by this package from the videos; the title text styles are what a person read from
// their hook frames (the vision call's answer is replaced by that reading in tests, so no model is needed).
func referenceMeasures(t *testing.T) []Measures {
	t.Helper()
	raw, err := os.ReadFile("testdata/reference_measures.json")
	if err != nil {
		t.Fatal(err)
	}
	var ms []Measures
	if err := json.Unmarshal(raw, &ms); err != nil {
		t.Fatal(err)
	}
	if len(ms) != 5 {
		t.Fatalf("%d references", len(ms))
	}
	return ms
}

func TestAggregateOfTheReferenceVideos(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	if p == nil || p.Version != ProfileVersion || p.References != 5 {
		t.Fatalf("profile: %+v", p)
	}
	// Pacing: 0 to 5 cuts, two videos with none, takes that run.
	pc := p.Pacing
	if pc.Style != PaceLongTake || pc.CutsPerVideo.Min != 0 || pc.CutsPerVideo.Max != 5 || pc.ZeroCutShare != 0.4 || !pc.HasLongTakes || pc.LongestShotS.Min < 7 {
		t.Errorf("pacing: %+v", pc)
	}
	if pc.LongTakeShare < 0.8 {
		t.Errorf("the long takes are %.2f of the screen time", pc.LongTakeShare)
	}
	// The hook: the first cut at 2.1 to 2.5 s.
	if p.Hook.FirstCutS.Min != 2.1 || p.Hook.FirstCutS.Max != 2.47 {
		t.Errorf("first cut: %+v", p.Hook.FirstCutS)
	}
	// No effects, no grade, low-key footage.
	if p.Effects.Verdict != EffectsNone || p.Look.Grade != "none" || !p.Look.LowKey {
		t.Errorf("effects %s, look %+v", p.Effects.Verdict, p.Look)
	}
	// The text: a single title in three of five, no labels, no captions.
	tx := p.Text
	if tx == nil || tx.Read != 5 || tx.TitleShare != 0.6 || tx.LabelShare != 0 || tx.CaptionShare != 0 || tx.Case != "upper" || tx.Position != "upper_third" || !tx.Condensed {
		t.Errorf("text: %+v", tx)
	}
	// The sound: a quiet intro and a lift in every video, an energy that builds, no fade-out, loose cuts, music only.
	a := p.Audio
	if a.LiftShare != 1 || a.IntroS.Min < 1.8 || a.IntroS.Max > 7.5 || a.Arc != ArcBuilds || !a.NoFadeOut || a.BeatLocked || a.Content != ContentMusicOnly || a.MusicShare != 1 {
		t.Errorf("audio: %+v", a)
	}
	if a.LUFS.Min < -19.5 || a.LUFS.Max > -15 || a.TempoBPM.Min < 65 || a.TempoBPM.Max > 130 {
		t.Errorf("audio numbers: %+v", a)
	}
	// Long takes use the slower tracks, the cut-up ones the faster ones.
	if a.TempoLongTakeBPM >= a.TempoMontageBPM || a.TempoMontageBPM < 100 {
		t.Errorf("tempo by pacing: long takes %.0f, cut-up %.0f", a.TempoLongTakeBPM, a.TempoMontageBPM)
	}
	if a.FirstCutAtLiftShare < 0.5 || a.CutsWithin100ms > 0.7 {
		t.Errorf("cuts and the lift: first cut at the lift %.2f, within 100 ms %.2f", a.FirstCutAtLiftShare, a.CutsWithin100ms)
	}
	if p.Confidence < 0.8 || len(p.Evidence) != 5 {
		t.Errorf("confidence %.2f, %d evidence lines", p.Confidence, len(p.Evidence))
	}
}

func TestDescribeSaysItInPlainLanguage(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	text := p.Describe()
	for _, want := range []string{"long takes", "no cut at all", "Effects: none", "none grade", "single title", "no exercise labels", "intro, then the music lifts", "not beat-locked", "energy builds to the end", "no fade-out", "music only", "no sound effects", "BPM"} {
		if !strings.Contains(text, want) {
			t.Errorf("the description lacks %q:\n%s", want, text)
		}
	}
	block := p.CompactBlock()
	for _, want := range []string{"pacing: long_takes", "effects: none", "labels: none", "look: grade none", "title: case upper", "sound numbers"} {
		if !strings.Contains(block, want) {
			t.Errorf("the compact block lacks %q:\n%s", want, block)
		}
	}
	if len(block) > 2500 {
		t.Errorf("the compact block is %d bytes", len(block))
	}
	var nilProfile *Profile
	if nilProfile.Describe() == "" || nilProfile.CompactBlock() != "" {
		t.Error("a nil profile")
	}
}

func TestAggregateEdgeCases(t *testing.T) {
	if Aggregate(nil) != nil {
		t.Error("no references made a profile")
	}
	one := referenceMeasures(t)[:1]
	p := Aggregate(one)
	if p.Confidence > 0.3 {
		t.Errorf("one reference gives confidence %.2f", p.Confidence)
	}
	// A reference with no audio, and no text read: the profile still builds.
	m := referenceMeasures(t)
	m[0].Audio = AudioStats{}
	for i := range m {
		m[i].Text = nil
	}
	p = Aggregate(m)
	if p.Text != nil || p.Audio.MusicShare != 1 || p.Audio.Content != ContentMusicOnly {
		t.Errorf("without text: %+v %+v", p.Text, p.Audio)
	}
	for i := range m {
		m[i].Audio = AudioStats{}
	}
	if p = Aggregate(m); p.Audio.Content != ContentUnknown || p.Audio.Speech != "unknown" {
		t.Errorf("no audio at all: %+v", p.Audio)
	}
	// The profile round trips through JSON, and a profile of another shape is refused.
	raw, err := json.Marshal(Aggregate(referenceMeasures(t)))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseProfile(raw); err != nil || got.References != 5 {
		t.Errorf("round trip: %v %+v", err, got)
	}
	if _, err := ParseProfile([]byte(`{"version": 99}`)); err == nil {
		t.Error("a profile of version 99 was accepted")
	}
}
