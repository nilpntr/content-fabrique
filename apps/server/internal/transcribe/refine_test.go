package transcribe

import (
	"math"
	"math/rand"
	"testing"
)

// speechLike builds audio from "bursts" (start, end in ms): a 200 Hz tone with a 3 Hz envelope, over low noise, which has the
// energy dips and the silences of words.
func speechLike(totalMs int, bursts [][2]int64) []int16 {
	r := rand.New(rand.NewSource(1))
	pcm := make([]int16, totalMs*SampleRate/1000)
	for i := range pcm {
		pcm[i] = int16(r.NormFloat64() * 8)
	}
	for _, b := range bursts {
		for i := int(b[0]) * SampleRate / 1000; i < int(b[1])*SampleRate/1000 && i < len(pcm); i++ {
			t := float64(i) / SampleRate
			env := 0.6 + 0.4*math.Sin(2*math.Pi*3*t)
			pcm[i] += int16(8000 * env * math.Sin(2*math.Pi*200*t))
		}
	}
	return pcm
}

func near(t *testing.T, what string, got, want, tol int64) {
	t.Helper()
	if d := got - want; d < -tol || d > tol {
		t.Errorf("%s = %d ms, want %d ms (+-%d)", what, got, want, tol)
	}
}

func TestRefineSnapsWordsToTheSilencesBetweenThem(t *testing.T) {
	bursts := [][2]int64{{500, 900}, {1300, 1700}, {2200, 2800}}
	pcm := speechLike(3500, bursts)
	// The model's anchors are a few hundred ms late and its spans are rough.
	words := []Word{
		{Text: "one", StartMs: 100, EndMs: 1100, AnchorMs: 800},
		{Text: "two", StartMs: 1100, EndMs: 1900, AnchorMs: 1650},
		{Text: "three", StartMs: 1900, EndMs: 3000, AnchorMs: 2700, Confidence: 0.9},
	}
	got := Refine(words, pcm)
	for i, b := range bursts {
		near(t, got[i].Text+" start", got[i].StartMs, b[0], 30)
		near(t, got[i].Text+" end", got[i].EndMs, b[1], 30)
		if got[i].AnchorMs != 0 {
			t.Errorf("the anchor of %s is kept", got[i].Text)
		}
	}
	if got[2].Confidence != 0.9 || got[0].Text != "one" {
		t.Errorf("text and confidence must survive: %+v", got)
	}
	if words[0].AnchorMs != 800 {
		t.Error("the input was modified")
	}
}

func TestRefineUsesTheQuietestFrameWhenThereIsNoSilence(t *testing.T) {
	// One continuous burst with a dip at 1000 ms (a word boundary in running speech).
	pcm := speechLike(2000, [][2]int64{{200, 990}, {1010, 1900}})
	words := []Word{{Text: "a", AnchorMs: 600}, {Text: "b", AnchorMs: 1500}}
	got := Refine(words, pcm)
	near(t, "a start", got[0].StartMs, 200, 30)
	near(t, "boundary", got[0].EndMs, 1000, 40)
	if got[1].StartMs != got[0].EndMs && got[1].StartMs < got[0].EndMs {
		t.Errorf("words overlap: %+v", got)
	}
	near(t, "b end", got[1].EndMs, 1900, 30)
}

func TestRefineKeepsWordsOrderedAndNonOverlapping(t *testing.T) {
	pcm := speechLike(2000, [][2]int64{{100, 1800}})
	words := []Word{{Text: "a", AnchorMs: 500}, {Text: "b", AnchorMs: 500}, {Text: "c", AnchorMs: 400}, {Text: "d", AnchorMs: 5000}}
	got := Refine(words, pcm)
	for i := range got {
		if got[i].EndMs <= got[i].StartMs {
			t.Errorf("word %d has no length: %+v", i, got[i])
		}
		if i > 0 && got[i].StartMs < got[i-1].EndMs {
			t.Errorf("word %d overlaps word %d: %+v %+v", i, i-1, got[i], got[i-1])
		}
	}
}

func TestRefineFallsBackToTheMiddleOfTheSpan(t *testing.T) {
	pcm := speechLike(1500, [][2]int64{{300, 700}, {900, 1300}})
	got := Refine([]Word{{Text: "a", StartMs: 200, EndMs: 800}, {Text: "b", StartMs: 800, EndMs: 1400}}, pcm)
	near(t, "a start", got[0].StartMs, 300, 30)
	near(t, "b start", got[1].StartMs, 900, 30)
}

func TestRefineLeavesTooShortAudioAlone(t *testing.T) {
	in := []Word{{Text: "a", StartMs: 5, EndMs: 9, AnchorMs: 7}}
	got := Refine(in, make([]int16, 100))
	if got[0].StartMs != 5 || got[0].EndMs != 9 || got[0].AnchorMs != 0 {
		t.Errorf("%+v", got)
	}
	if len(Refine(nil, make([]int16, SampleRate))) != 0 {
		t.Error("no words in, no words out")
	}
}

func TestWithinSpeechDropsInventedWords(t *testing.T) {
	segs := []Segment{{StartMs: 1000, EndMs: 2000}}
	in := []Word{{Text: "ghost", StartMs: 100, EndMs: 300}, {Text: "real", StartMs: 1100, EndMs: 1500}, {Text: "edge", StartMs: 2100, EndMs: 2300}, {Text: "late", StartMs: 3000, EndMs: 3200}}
	got := WithinSpeech(in, segs)
	if len(got) != 2 || got[0].Text != "real" || got[1].Text != "edge" {
		t.Fatalf("%+v", got)
	}
}
