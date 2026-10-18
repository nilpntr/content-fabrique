package styleprofile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/media"
)

func shapes(titles ...string) []titleShape {
	out := make([]titleShape, len(titles))
	for i, t := range titles {
		out[i] = titleShape{text: t, lines: 1}
	}
	return out
}

// A template keeps the words that come back and makes slots of the rest: a series number that recurs stays, a counting number at the
// start is a slot, and no topic of one title survives.
func TestTemplatesAreLearntFromTheTitles(t *testing.T) {
	series := templatesOf(shapes("ROW POWER 7", "PULL WORK 7", "LEG DAY 7"))
	if len(series) != 1 || series[0].Template != "<TOPIC> 7" || series[0].Share != 1 || series[0].Lines != 1 {
		t.Fatalf("series: %+v", series)
	}
	list := templatesOf(shapes("3 MOVES FOR WIDE SHOULDERS", "4 MOVES FOR BIG ARMS", "5 MOVES FOR A STRONG CORE"))
	if len(list) != 1 || list[0].Template != "<N> MOVES FOR <TOPIC>" {
		t.Fatalf("list: %+v", list)
	}
	chapters := templatesOf(shapes("1. SQUATS FOR THE QUADS", "2. LUNGES FOR THE GLUTES"))
	if len(chapters) != 1 || chapters[0].Template != "<N>. <TOPIC> FOR THE <GOAL>" {
		t.Fatalf("chapters: %+v", chapters)
	}
	mixed := templatesOf(shapes("ROW POWER 7", "PULL WORK 7", "how i warm up"))
	if mixed[0].Template != "<TOPIC> 7" || mixed[0].Share < 0.6 || len(mixed) != 2 {
		t.Fatalf("mixed: %+v", mixed)
	}
	for _, tpl := range mixed {
		if strings.Contains(tpl.Template, "ROW") || strings.Contains(tpl.Template, "warm") {
			t.Errorf("a topic of one title is in the template: %q", tpl.Template)
		}
	}
	if !TemplateMatches("<N> MOVES FOR <TOPIC>", "6 moves for better posture") || TemplateMatches("<N> MOVES FOR <TOPIC>", "MOVES FOR posture") || !TemplateMatches("<TOPIC> 7", "BACK DAY 7") {
		t.Error("matching")
	}
	for tpl, ok := range map[string]bool{"<TOPIC> 7": true, "<N>. <TOPIC>": true, "": false, "<TOPIC": false, strings.Repeat("A", 81): false, "<script>alert(1)</script>": false} {
		if ValidTemplate(tpl) != ok {
			t.Errorf("ValidTemplate(%q) != %v", tpl, ok)
		}
	}
}

// The transition of each cut, from the thumbnails and the scene scores: a plain cut, a flash, a blend.
func TestTransitionsAtTheCuts(t *testing.T) {
	fps := media.StyleThumbFPS
	frame := func(v byte) []byte {
		f := make([]byte, media.StyleThumbPixels)
		for i := range f {
			f[i] = v + byte(i%7)
		}
		return f
	}
	var frames [][]byte
	for i := range 10 * fps {
		v := byte(40)
		if i >= 3*fps {
			v = 120 // a plain cut at 3 s
		}
		if i == 6*fps {
			v = 250 // a flash at 6 s
		}
		frames = append(frames, frame(v))
	}
	var points []scorePoint
	for i := range 10 * media.StyleSceneFPS {
		ts := float64(i) / float64(media.StyleSceneFPS)
		s := 0.0
		switch {
		case i == 3*media.StyleSceneFPS || i == 6*media.StyleSceneFPS:
			s = 0.3
		case ts > 8.7 && ts < 9.3:
			s = 0.08 // a blend over several frames at 9 s
		}
		points = append(points, scorePoint{ts, s})
	}
	ts := transitionsOf([]float64{3, 6, 9}, points, frames)
	if ts.Cuts != 3 || ts.Hard != 1 || ts.Flash != 1 || ts.Blend != 1 {
		t.Fatalf("%+v", ts)
	}
	if got := transitionsOf(nil, points, frames); got.Cuts != 0 {
		t.Errorf("no cuts: %+v", got)
	}
}

// Where the cuts sit against the reps: at the end of a rep, in the middle of one, and inside a set (reps at the same pace on both sides).
func TestCutsAgainstTheMovement(t *testing.T) {
	rep := func(a, b int) analysis.Moment { return analysis.Moment{Kind: "rep", StartMs: a, EndMs: b} }
	moments := []analysis.Moment{
		// shot 1: reps of 2 s up to 8 s; a cut at 8.0 s on the end of a rep
		rep(2000, 4000), rep(4000, 6000), rep(6000, 8000),
		// shot 2: reps of 2 s from 8 s; a cut at 13.0 s, a second after the last full rep ended (mid-rep)
		rep(8000, 10000), rep(10000, 12000),
		// shot 3: no reps after 13 s
	}
	ms := cutMovement([]float64{8.0, 13.0, 20.0}, moments)
	if ms.CutsMeasured != 2 || ms.OnBoundary != 1 || ms.MidRep != 1 || ms.InsideSet != 1 {
		t.Fatalf("%+v", ms)
	}
}

// The new measures aggregate, say themselves in plain words, and set the measured defaults of the settings; a version 1 profile is read.
func TestTheStructureOfTheVideosIsAggregated(t *testing.T) {
	ms := referenceMeasures(t)
	moves := []string{"SQUATS", "ROWS", "CURLS", "PRESSES", "LUNGES", "DIPS", "PULLS", "RAISES", "SHRUGS", "FLYES"}
	for i := range ms {
		ms[i].Transitions = &TransitionStats{Cuts: len(ms[i].CutsS), Hard: len(ms[i].CutsS)}
		ms[i].Movement = &MovementStats{CutsMeasured: 2, OnBoundary: 0, MidRep: 2, InsideSet: 1}
		if ms[i].Text != nil {
			ms[i].Text.Entrance, ms[i].Text.ChaptersRead = "pop", true
			if len(ms[i].CutsS) >= 2 {
				ms[i].Text.Chapters = []string{"1. " + moves[2*i] + " FOR STRENGTH", "2. " + moves[2*i+1] + " FOR STRENGTH"}
			}
		}
		if len(ms[i].ShotsS) > 1 {
			ms[i].ShotsS[len(ms[i].ShotsS)-1] = 6 // a long hold at the end
		}
	}
	p := Aggregate(ms)
	if p.Version != ProfileVersion || p.Transitions.Used != TransitionsCutOnly || !p.Movement.Known() || p.Movement.MidRepShare != 1 {
		t.Fatalf("profile %+v %+v", p.Transitions, p.Movement)
	}
	if p.Text.Entrance != "pop" || !p.Text.NumberedChapters() || len(p.Text.ChapterTemplates) == 0 || p.Text.ChapterTemplates[0].Template != "<N>. <TOPIC> FOR STRENGTH" {
		t.Fatalf("text %+v", p.Text)
	}
	if !p.Pacing.LongEndingHold() || p.Pacing.ShotS.P90 == 0 {
		t.Errorf("pacing %+v camera %+v", p.Pacing, p.Camera)
	}
	r := Resolve(p, nil, Settings{}, "")
	if r.ChapterTitles != LevelOn || r.Sources.ChapterTitles != SourceMeasured || r.CutTiming != CutsMidRep || r.AngleSwitches != LevelOn || r.EndingHold != HoldLong {
		t.Errorf("measured defaults %+v %+v", r.Settings, r.Sources)
	}
	// The creator wins over the measures.
	mine := Resolve(p, nil, Settings{ChapterTitles: LevelOff, CutTiming: CutsOnFullReps, TitleTemplate: "<TOPIC> FOR <GOAL>"}, "")
	if mine.ChapterTitles != LevelOff || mine.CutTiming != CutsOnFullReps || mine.TitleTemplate != "<TOPIC> FOR <GOAL>" || mine.Sources.TitleTemplate != SourceYou {
		t.Errorf("mine %+v", mine.Settings)
	}
	plain := map[string]string{}
	for _, l := range p.PlainLines() {
		plain[l.ID] = l.Value
	}
	for id, want := range map[string]string{"ending": "long hold", "chapters": "Numbered chapter titles", "cuts": "mid-rep", "transitions": "Plain cuts only", "title_entrance": "pops in"} {
		if !strings.Contains(plain[id], want) {
			t.Errorf("%s = %q, want %q", id, plain[id], want)
		}
	}
	// The summary call sees the facts but none of the creator's words.
	facts := p.SummaryFacts()
	if !strings.Contains(facts, "middle of a rep") || !strings.Contains(facts, "umbered chapter titles") || strings.Contains(facts, "FOR STRENGTH") {
		t.Errorf("facts:\n%s", facts)
	}
	// The editor's block has the forms, as forms to fill.
	if b := p.CompactBlock(); !strings.Contains(b, "never copy a title of theirs") || !strings.Contains(b, "a long hold at the end") {
		t.Errorf("block:\n%s", b)
	}

	// A version 1 profile (before these measures) is still read, with them unknown.
	old := Aggregate(referenceMeasures(t))
	old.Version = 1
	raw, _ := json.Marshal(old)
	got, err := ParseProfile(raw)
	if err != nil || got.Movement.Known() || got.Transitions.Measured != 0 {
		t.Fatalf("version 1: %v %+v", err, got)
	}
	if r := Resolve(got, nil, Settings{}, ""); r.Sources.CutTiming != SourceDefault {
		t.Errorf("version 1 defaults: %+v", r.Sources)
	}
}
