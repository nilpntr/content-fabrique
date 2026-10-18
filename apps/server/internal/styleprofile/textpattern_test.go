package styleprofile

import (
	"encoding/json"
	"strings"
	"testing"
)

func lineAt(at float64, text, pos string, over, dialogue bool) ShotText {
	if text == "" {
		return ShotText{AtS: at}
	}
	return ShotText{AtS: at, Text: text, Position: pos, HeightPct: 2.4, Colour: "#FFFFFF", Shadow: "soft", OverBody: over, Dialogue: dialogue}
}

// Story captions: a new sentence-case line on each shot, white, small, mid-frame over the chest, in Dutch (lines made up for the test).
func storyRead() *TextStyle {
	return &TextStyle{ShotsRead: true, Language: "nl", ShotTexts: []ShotText{
		lineAt(1, "Zo kreeg ik eindelijk brede schouders", TextMiddle, true, false),
		lineAt(3, "Ik stopte met zware presses", TextMiddle, true, false),
		lineAt(5, "En begon met lichte zijwaartse raises", TextMiddle, true, false),
		lineAt(7, "Elke dag tien minuten", TextMiddle, false, false),
		lineAt(9, "", "", false, false),
	}}
}

// A single title held over the hook, numbered chapter titles after the cuts and nothing else.
func titleRead() *TextStyle {
	return &TextStyle{ShotsRead: true, Language: "en", TitlePresent: true, Text: "BACK DAY 7", Case: "upper", ShotTexts: []ShotText{
		lineAt(1, "BACK DAY 7", TextUpperThird, false, false),
		lineAt(4, "1. ROWS FOR THICKNESS", TextUpperThird, false, false),
		lineAt(8, "2. PULLDOWNS FOR WIDTH", TextUpperThird, false, false),
		lineAt(12, "3. FACE PULLS FOR POSTURE", TextUpperThird, false, false),
		lineAt(15, "", "", false, false),
	}}
}

// A meme exchange: two quoted lines answering each other, then the reveal with no text.
func dialogueRead() *TextStyle {
	return &TextStyle{ShotsRead: true, Language: "en", ShotTexts: []ShotText{
		lineAt(1, "“You look different”", TextMiddle, true, true),
		lineAt(2.5, "“What did you do?”", TextMiddle, true, true),
		lineAt(5, "", "", false, false),
	}}
}

func TestTheTextPatternOfARead(t *testing.T) {
	for name, c := range map[string]struct {
		read *TextStyle
		want string
	}{
		"story captions":          {storyRead(), PatternStoryCaptions},
		"title and chapters":      {titleRead(), PatternSingleTitle},
		"dialogue":                {dialogueRead(), PatternDialogue},
		"nothing":                 {&TextStyle{ShotsRead: true, ShotTexts: []ShotText{lineAt(1, "", "", false, false), lineAt(3, "", "", false, false)}}, PatternNone},
		"an old read, a title":    {&TextStyle{TitlePresent: true, Text: "LEG DAY"}, PatternSingleTitle},
		"one caption, many shots": {&TextStyle{ShotsRead: true, ShotTexts: []ShotText{lineAt(1, "Mijn routine", TextMiddle, false, false), lineAt(3, "", "", false, false), lineAt(5, "", "", false, false), lineAt(7, "", "", false, false)}}, PatternSingleTitle},
	} {
		if got := ClassifyText(c.read); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	// The read's dialogue flag alone does not make plain first-person captions a dialogue.
	flagged := storyRead()
	for i := range flagged.ShotTexts {
		flagged.ShotTexts[i].Dialogue = true
	}
	if got := ClassifyText(flagged); got != PatternStoryCaptions {
		t.Errorf("captions flagged as dialogue: %q", got)
	}
	// The first caption read as the title still counts as a line of the story.
	titled := storyRead()
	titled.TitlePresent, titled.Text = true, titled.ShotTexts[0].Text
	if got := ClassifyText(titled); got != PatternStoryCaptions {
		t.Errorf("story captions whose first line is read as the title: %q", got)
	}
	// Quotes alone make a line dialogue, whatever the read's flag said.
	quoted := dialogueRead()
	for i := range quoted.ShotTexts {
		quoted.ShotTexts[i].Dialogue = false
	}
	if got := ClassifyText(quoted); got != PatternDialogue {
		t.Errorf("quoted lines: %q", got)
	}
	for in, want := range map[string]string{"Zo kreeg ik brede schouders": "sentence", "BACK DAY": "upper", "how i warm up": "lower", "Back Day Blueprint": "mixed", "123": ""} {
		if got := lineCase(in); got != want {
			t.Errorf("lineCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// The profile takes the majority pattern, the look of its lines and the language; the editor's words name the pattern and the look, never
// a line of the creator's.
func TestTheTextPatternOfTheProfile(t *testing.T) {
	var refs []Measures
	for _, r := range []*TextStyle{storyRead(), storyRead(), storyRead(), dialogueRead(), titleRead()} {
		refs = append(refs, Measures{DurationS: 20, Text: r})
	}
	tp := aggregateText(refs)
	if tp.Pattern != PatternStoryCaptions || tp.PatternRead != 5 || tp.PatternShare != 0.6 || tp.Language != "nl" {
		t.Fatalf("pattern %+v", tp)
	}
	l := tp.LineLook
	if l == nil || l.Position != TextMiddle || l.Case != "sentence" || l.Colour != "#FFFFFF" || l.OverBodyShare != 0.75 || l.HeightPct != 2.4 || l.WordsHi < 5 {
		t.Fatalf("look %+v", l)
	}
	if len(tp.PatternsSeen) != 2 {
		t.Errorf("seen %v", tp.PatternsSeen)
	}
	words := tp.patternWords()
	for _, want := range []string{"story captions", "sentence case", "in the middle of the frame", "75 % of the lines over the person's body", "Dutch"} {
		if !strings.Contains(words, want) {
			t.Errorf("the words lack %q: %s", want, words)
		}
	}
	for _, r := range storyRead().ShotTexts {
		if r.Text != "" && strings.Contains(words, r.Text) {
			t.Errorf("a line of the creator's is in the words: %q", r.Text)
		}
	}
	// A profile from reads that did not see the shots has no pattern.
	if old := aggregateText([]Measures{{Text: &TextStyle{TitlePresent: true, Text: "LEG DAY"}}}); old.Pattern != "" || old.PatternRead != 0 {
		t.Errorf("an old read: %+v", old)
	}
}

// The shots of a read: one frame a little into each shot, at most MaxShotFrames spread over them; a long take gets frames every few
// seconds; the answer's lines are checked like the title and placed at their frames.
func TestTheShotsOfARead(t *testing.T) {
	at := ShotTimes([]float64{2, 4.5, 7}, 10)
	if len(at) != 4 || at[0] != 0.8 || at[1] != 2.8 || at[3] != 7.8 {
		t.Errorf("shots: %v", at)
	}
	many := make([]float64, 30)
	for i := range many {
		many[i] = float64(i+1) * 0.9
	}
	if got := ShotTimes(many, 28); len(got) != MaxShotFrames {
		t.Errorf("many shots: %d frames", len(got))
	}
	if got := ShotTimes(nil, 20); len(got) < 5 || got[0] != 1 {
		t.Errorf("one take: %v", got)
	}
	shot := `{"frame":%d,"text":%q,"position":"middle","text_height_pct":2.5,"colour":"#ffffff","shadow":"soft","over_body":true,"dialogue":false}`
	answer := strings.Replace(goodAnswer, `"shot_texts":[]`, `"shot_texts":[`+
		strings.Join([]string{fmtShot(shot, 6, "Zo kreeg ik brede schouders"), fmtShot(shot, 7, "volg mij op www.example.com"), fmtShot(shot, 3, "a hook frame")}, ",")+`]`, 1)
	answer = strings.Replace(answer, `"language":"en"`, `"language":"NL"`, 1)
	ts, err := ValidateTitleStyle(json.RawMessage(answer))
	if err != nil {
		t.Fatal(err)
	}
	ts.placeShots([]float64{0.1, 0.3, 0.5, 1.5, 3, 4.2, 7.1}, 5)
	if !ts.ShotsRead || ts.Language != "nl" || len(ts.ShotTexts) != 2 || ts.ShotTexts[0].AtS != 4.2 || ts.ShotTexts[0].Colour != "#FFFFFF" || ts.ShotTexts[1].Text != "" {
		t.Errorf("read %+v", ts)
	}
	for _, bad := range []string{`"language":"dutch"`, `"shot_texts":[{"frame":6}]`} {
		a := goodAnswer
		if strings.HasPrefix(bad, `"language"`) {
			a = strings.Replace(a, `"language":"en"`, bad, 1)
		} else {
			a = strings.Replace(a, `"shot_texts":[]`, bad, 1)
		}
		if _, err := ValidateTitleStyle(json.RawMessage(a)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func fmtShot(format string, frame int, text string) string {
	b, _ := json.Marshal(text)
	return strings.Replace(strings.Replace(format, "%d", itoa(frame), 1), "%q", string(b), 1)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
