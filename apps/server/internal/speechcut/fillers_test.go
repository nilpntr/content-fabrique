package speechcut

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

func TestWeakFillersAreCutOnlyWhenAPauseSetsThemOff(t *testing.T) {
	for _, spec := range [][]tok{
		{w("That's", 0), w("right,", 60), w("you", 60), w("see.", 40)},
		{w("Things", 0), w("like,", 150), w("this", 60), w("matter.", 40)},
	} {
		words := build(spec)
		st := transcribe.Analyse(words, "en")
		if len(Actionable(words, st)) != 0 {
			t.Errorf("%v: weak filler acted on", spec)
		}
		if p := Compute(words, st, Options{}); len(p.Keep) != 1 || p.Keep[0].LastWord != len(words)-1 {
			t.Errorf("%v: keep = %+v", spec, p.Keep)
		}
	}
	words := build([]tok{w("It", 0), w("is", 40), w("actually", 600), w("fine.", 40)})
	st := transcribe.Analyse(words, "en")
	if len(st.Fillers) != 1 || len(Actionable(words, st)) != 1 {
		t.Fatalf("fixture: %+v", st.Fillers)
	}
	for _, k := range Compute(words, st, Options{}).Keep {
		if k.FirstWord <= 2 && k.LastWord >= 2 {
			t.Errorf("set-off weak filler kept: %+v", k)
		}
	}
}

func TestSentenceStartSkipsLeadingFillers(t *testing.T) {
	words := build([]tok{w("Um,", 0), w("here", 80), w("we", 40), w("go.", 40)})
	st := transcribe.Analyse(words, "en")
	if got := SentenceStart(words, st, st.Sentences[0]); got != 1 {
		t.Errorf("start = %d, want 1", got)
	}
}

func TestLeadAndTailSilenceHonourLowConfidence(t *testing.T) {
	words := build([]tok{{text: "Hello", gap: 600, conf: 0.2}, w("there.", 60)})
	st := transcribe.Analyse(words, "en")
	f, u := Audit(words, st, Range{InMs: 0, OutMs: words[1].EndMs + 50})
	if len(f) != 0 || u == 0 {
		t.Errorf("uncertain lead: findings %v, uncertain %d", f, u)
	}
	words[0].Confidence, words[1].Confidence = 0.9, 0.2
	f, u = Audit(words, st, Range{InMs: 0, OutMs: words[1].EndMs + 900})
	if len(f) != 1 || f[0].Kind != FindingPause || u == 0 {
		t.Errorf("clean lead, uncertain tail: findings %v, uncertain %d", f, u)
	}
}
