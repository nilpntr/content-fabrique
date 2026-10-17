package transcribe

import (
	"reflect"
	"strings"
	"testing"
)

// words builds a transcript from "text:startMs-endMs" items.
func ws(items ...string) []Word {
	out := make([]Word, 0, len(items))
	for _, it := range items {
		var w Word
		text, span, _ := strings.Cut(it, ":")
		w.Text = text
		a, b, _ := strings.Cut(span, "-")
		w.StartMs, w.EndMs = atoi(a), atoi(b)
		out = append(out, w)
	}
	return out
}

func atoi(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}

func TestSentences(t *testing.T) {
	w := ws("Hello:0-300", "there.:350-700", "How:800-1000", "are:1050-1200", "you?:1250-1500", "Good:1600-1900", "Mr.:1950-2100", "Smith:2150-2500")
	st := Analyse(w, "en")
	var got []string
	for _, s := range st.Sentences {
		got = append(got, s.Text)
	}
	want := []string{"Hello there.", "How are you?", "Good Mr. Smith"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sentences %q, want %q", got, want)
	}
	if s := st.Sentences[1]; s.FirstWord != 2 || s.LastWord != 4 || s.StartMs != 800 || s.EndMs != 1500 {
		t.Errorf("second sentence %+v", s)
	}
}

func TestSentenceEndsAtALongSilenceWithoutPunctuation(t *testing.T) {
	w := ws("one:0-300", "two:350-600", "three:2100-2400", "four:2450-2700")
	st := Analyse(w, "en")
	if len(st.Sentences) != 2 || st.Sentences[0].LastWord != 1 {
		t.Fatalf("%+v", st.Sentences)
	}
	// A comma means the speaker is not done.
	w = ws("one:0-300", "two,:350-600", "three:2100-2400")
	if st := Analyse(w, "en"); len(st.Sentences) != 1 {
		t.Fatalf("%+v", st.Sentences)
	}
}

func TestAbbreviationsAndInitialsDoNotEndASentence(t *testing.T) {
	for _, s := range []string{"Mr.", "Dr.", "e.g.", "U.S.", "J.", "etc."} {
		if endsSentence(s) {
			t.Errorf("%q ends a sentence", s)
		}
	}
	for _, s := range []string{"done.", "really?", "no!", `said."`, "wait…", "I."} {
		if !endsSentence(s) {
			t.Errorf("%q does not end a sentence", s)
		}
	}
}

func TestPausesLongerThan400ms(t *testing.T) {
	w := ws("a:0-300", "b:700-900", "see.:1301-1500", "d:2500-2700")
	st := Analyse(w, "en")
	// 300->700 is exactly 400: not a pause. 900->1301 is 401: a pause. 1500->2500 is a pause between sentences.
	if len(st.Pauses) != 2 {
		t.Fatalf("%+v", st.Pauses)
	}
	if p := st.Pauses[0]; p.AfterWord != 1 || p.DurationMs != 401 || p.SentenceBreak {
		t.Errorf("first pause %+v", p)
	}
	if p := st.Pauses[1]; p.AfterWord != 2 || p.DurationMs != 1000 || !p.SentenceBreak {
		t.Errorf("second pause %+v", p)
	}
}

func TestHesitationFillers(t *testing.T) {
	w := ws("So:0-200", "um,:250-500", "this:550-700", "is:750-850", "uh:900-1100", "great.:1150-1500", "Eh:1600-1800", "ja:1850-2000")
	for _, lang := range []string{"en", "nl", "", "xx"} {
		st := Analyse(w, lang)
		var texts []string
		for _, f := range st.Fillers {
			if f.Kind == FillerHesitation {
				if f.Strength != StrengthStrong {
					t.Errorf("%s: hesitation %+v is not strong", lang, f)
				}
				texts = append(texts, f.Text)
			}
		}
		if want := []string{"um", "uh", "Eh"}; !reflect.DeepEqual(texts, want) {
			t.Errorf("%s: hesitations %q, want %q", lang, texts, want)
		}
	}
}

func TestDiscourseFillersEnglish(t *testing.T) {
	w := ws(
		"So:0-200", "you:250-400", "know:420-600", "I:650-700", "like:720-900", "pizza:950-1300", // "like" as a verb: not a filler
		"It:1400-1500", "was,:1520-1700", "like,:1720-1900", "huge.:1950-2300", // "like," set off by commas: filler
	)
	st := Analyse(w, "en")
	var got []string
	for _, f := range st.Fillers {
		got = append(got, f.Text+"/"+f.Strength)
	}
	want := []string{"So/weak", "you know/strong", "like/weak"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fillers %q, want %q", got, want)
	}
	if f := st.Fillers[1]; f.FirstWord != 1 || f.LastWord != 2 || f.StartMs != 250 || f.EndMs != 600 {
		t.Errorf("phrase filler %+v", f)
	}
}

func TestSoIsOnlyAFillerAtTheStartOfASentence(t *testing.T) {
	w := ws("That:0-200", "is:250-350", "so:400-500", "good.:550-800", "So:900-1000", "we:1050-1150", "go.:1200-1400")
	st := Analyse(w, "en")
	if len(st.Fillers) != 1 || st.Fillers[0].FirstWord != 4 {
		t.Fatalf("%+v", st.Fillers)
	}
}

func TestDutchFillers(t *testing.T) {
	w := ws("Dus:0-200", "ehm:250-500", "ik:550-650", "ben,:700-900", "zeg:950-1100", "maar:1120-1300", "moe.:1350-1600")
	st := Analyse(w, "nl")
	var got []string
	for _, f := range st.Fillers {
		got = append(got, f.Text)
	}
	if want := []string{"Dus", "ehm", "zeg maar"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fillers %q, want %q", got, want)
	}
	// "dus" is English noise; English rules must not see it.
	if st := Analyse(ws("dus:0-200"), "en"); len(st.Fillers) != 0 {
		t.Errorf("%+v", st.Fillers)
	}
}

func TestEmptyTranscript(t *testing.T) {
	st := Analyse(nil, "en")
	if st.Sentences == nil || st.Fillers == nil || st.Pauses == nil {
		t.Fatalf("slices must be empty, not nil (they are JSON arrays): %+v", st)
	}
}
