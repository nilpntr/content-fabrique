package speechcut

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

// tok is one spoken word and the silence before it, in ms; conf 0 means the provider reports none.
type tok struct {
	text string
	gap  int64
	dur  int64
	conf float64
}

func build(toks []tok) []transcribe.Word {
	var out []transcribe.Word
	var t int64 = 300
	for _, k := range toks {
		t += k.gap
		d := k.dur
		if d == 0 {
			d = 260
		}
		out = append(out, transcribe.Word{Text: k.text, StartMs: t, EndMs: t + d, Confidence: k.conf})
		t += d
	}
	return out
}

func w(text string, gap int64) tok { return tok{text: text, gap: gap} }

// fixture: filler words, pauses of 300, 450 and 900 ms, and two sentences.
func fixture() []transcribe.Word {
	return build([]tok{
		w("Um,", 0), w("so", 120), w("today", 100), w("I", 60), w("want", 40), w("to", 30), w("show", 40), w("you", 40), // 0..7
		w("uh", 200), w("three", 150), w("things.", 40), // 8..10 (a hesitation, then a 150 ms gap)
		w("First", 900), w("is", 30), w("the", 30), w("grip,", 40), // 11..14 (a 900 ms pause between sentences)
		w("you", 450), w("know,", 60), w("it", 300), w("matters.", 40), // 15..18 (a 450 ms pause inside a sentence; a 300 ms gap kept)
	})
}

func TestComputeDropsFillersAndLongPauses(t *testing.T) {
	words := fixture()
	st := transcribe.Analyse(words, "en")
	if len(st.Fillers) == 0 || len(st.Pauses) != 2 {
		t.Fatalf("fixture: %d fillers, %d pauses; want some fillers and 2 pauses", len(st.Fillers), len(st.Pauses))
	}
	p := Compute(words, st, Options{})
	if len(p.Keep) < 3 {
		t.Fatalf("keep = %+v", p.Keep)
	}
	for _, f := range st.Fillers {
		for _, k := range p.Keep {
			if f.StartMs < k.EndMs && f.EndMs > k.StartMs {
				t.Errorf("filler %q (%d-%d) is inside keep %d-%d", f.Text, f.StartMs, f.EndMs, k.StartMs, k.EndMs)
			}
		}
	}
	for _, pa := range st.Pauses {
		for _, k := range p.Keep {
			if pa.StartMs+MaxPauseMs/2 < k.EndMs && pa.EndMs-MaxPauseMs/2 > k.StartMs && k.StartMs < pa.StartMs-DefaultPadMs && k.EndMs > pa.EndMs+DefaultPadMs {
				t.Errorf("pause %d-%d is inside keep %d-%d", pa.StartMs, pa.EndMs, k.StartMs, k.EndMs)
			}
		}
	}
	// The 300 ms gap stays: "it matters." is one range.
	last := p.Keep[len(p.Keep)-1]
	if last.FirstWord != 18-1 || last.LastWord != 18 {
		t.Errorf("last keep = %+v, want words 17..18 (it matters.)", last)
	}
}

func TestComputeRespectsWordsAndPadding(t *testing.T) {
	words := fixture()
	st := transcribe.Analyse(words, "en")
	p := Compute(words, st, Options{PadMs: 100})
	for _, k := range p.Keep {
		for i, wd := range words {
			if k.StartMs > wd.StartMs && k.StartMs < wd.EndMs {
				t.Errorf("keep %d-%d starts inside word %d %q", k.StartMs, k.EndMs, i, wd.Text)
			}
			if k.EndMs > wd.StartMs && k.EndMs < wd.EndMs {
				t.Errorf("keep %d-%d ends inside word %d %q", k.StartMs, k.EndMs, i, wd.Text)
			}
		}
		if k.StartMs > words[k.FirstWord].StartMs || k.EndMs < words[k.LastWord].EndMs {
			t.Errorf("keep %+v does not cover its words", k)
		}
	}
	// A cut after a 900 ms pause leaves the full 100 ms pad into the gap on both sides.
	var found bool
	for i := 0; i+1 < len(p.Keep); i++ {
		a, b := p.Keep[i], p.Keep[i+1]
		if a.LastWord == 10 && b.FirstWord == 11 {
			found = true
			if a.EndMs != words[10].EndMs+100 || b.StartMs != words[11].StartMs-100 {
				t.Errorf("the sentence break is cut at %d..%d, want %d..%d", a.EndMs, b.StartMs, words[10].EndMs+100, words[11].StartMs-100)
			}
		}
	}
	if !found {
		t.Errorf("no cut between words 10 and 11: %+v", p.Keep)
	}
}

func TestComputePadIsClamped(t *testing.T) {
	words := build([]tok{w("hello", 0), w("there.", 1000)})
	st := transcribe.Analyse(words, "en")
	for _, c := range []struct{ in, want int64 }{{0, DefaultPadMs}, {10, MinPadMs}, {80, 80}, {400, MaxPadMs}} {
		p := Compute(words, st, Options{PadMs: c.in})
		if len(p.Keep) != 2 {
			t.Fatalf("keep = %+v", p.Keep)
		}
		if got := p.Keep[0].EndMs - words[0].EndMs; got != c.want {
			t.Errorf("pad %d: end padding = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestComputeNeverPadsIntoANeighbour(t *testing.T) {
	// "um" sits 20 ms from both neighbours: the padding is at most the gap, so the cut is at the filler's edge.
	words := build([]tok{w("this", 0), w("um", 20), w("okay.", 20)})
	st := transcribe.Analyse(words, "en")
	p := Compute(words, st, Options{})
	if len(p.Keep) != 2 {
		t.Fatalf("keep = %+v", p.Keep)
	}
	if p.Keep[0].EndMs != words[1].StartMs || p.Keep[1].StartMs != words[1].EndMs {
		t.Errorf("cut around the filler = %d..%d, want %d..%d", p.Keep[0].EndMs, p.Keep[1].StartMs, words[1].StartMs, words[1].EndMs)
	}
}

func TestComputeRefusesCutsNextToLowConfidenceWords(t *testing.T) {
	words := build([]tok{w("this", 0), w("is", 30), tok{text: "um", gap: 60, conf: 0.2}, w("fine.", 60), w("Then", 900), w("we", 30), w("go.", 30)})
	st := transcribe.Analyse(words, "en")
	p := Compute(words, st, Options{})
	unc := p.Uncertain()
	if len(unc) != 1 || unc[0].Kind != KindFiller || !strings.Contains(unc[0].Reason, "not certain") {
		t.Fatalf("uncertain = %+v", unc)
	}
	// The filler stays in a keep range, and the audit does not call it out: its time cannot be trusted.
	if p.Keep[0].FirstWord != 0 || p.Keep[0].LastWord < 3 {
		t.Errorf("keep[0] = %+v", p.Keep[0])
	}
	f, u := Audit(words, st, Range{InMs: p.Keep[0].StartMs, OutMs: p.Keep[0].EndMs})
	if len(f) != 0 || u == 0 {
		t.Errorf("audit = %v, %d uncertain", f, u)
	}
}

func TestComputeWindowKeepsWordsWhole(t *testing.T) {
	words := fixture()
	st := transcribe.Analyse(words, "en")
	// A window that starts in the middle of "show": that word and the ones before it are left out.
	in, out := words[7].StartMs-50, words[10].EndMs+300
	p := Compute(words, st, Options{InMs: in, OutMs: out})
	if len(p.Keep) == 0 {
		t.Fatal("no keep")
	}
	for _, k := range p.Keep {
		if k.StartMs < in || k.EndMs > out {
			t.Errorf("keep %d-%d is outside the window %d-%d", k.StartMs, k.EndMs, in, out)
		}
		if k.FirstWord < 7 {
			t.Errorf("keep %+v starts before the window", k)
		}
	}
	// A window that begins inside word 7 leaves it out whole.
	p = Compute(words, st, Options{InMs: words[7].StartMs + 100, OutMs: out})
	for _, k := range p.Keep {
		if k.FirstWord <= 7 {
			t.Errorf("keep %+v includes the word the window cuts", k)
		}
	}
}

func TestComputeEmptyAndInvalid(t *testing.T) {
	if p := Compute(nil, transcribe.Structure{}, Options{}); len(p.Keep) != 0 {
		t.Errorf("nil words: %+v", p)
	}
	bad := []transcribe.Word{{Text: "a", StartMs: 100, EndMs: 200}, {Text: "b", StartMs: 150, EndMs: 300}}
	if p := Compute(bad, transcribe.Analyse(bad, "en"), Options{}); len(p.Keep) != 0 {
		t.Errorf("overlapping words: %+v", p)
	}
	only := build([]tok{w("um", 0)})
	if p := Compute(only, transcribe.Analyse(only, "en"), Options{}); len(p.Keep) != 0 {
		t.Errorf("only a filler: %+v", p)
	}
}

// The planner and the audit agree on random transcripts: what the planner keeps has no filler, no pause over 400 ms and no cut in a
// word, and every word that is not a filler is kept, so sentences stay whole.
func TestComputeProperties(t *testing.T) {
	vocab := []string{"the", "grip", "um", "uh", "like,", "so", "you", "know,", "press.", "Then", "hold", "it?", "bar", "and", "weight"}
	rng := rand.New(rand.NewSource(7))
	for n := 0; n < 300; n++ {
		var toks []tok
		for i := 0; i < 5+rng.Intn(40); i++ {
			gap := int64(rng.Intn(120))
			switch rng.Intn(6) {
			case 0:
				gap = int64(300 + rng.Intn(1200))
			case 1:
				gap = 380 + int64(rng.Intn(60)) // around the 400 ms line
			}
			toks = append(toks, tok{text: vocab[rng.Intn(len(vocab))], gap: gap, dur: int64(80 + rng.Intn(400))})
		}
		words := build(toks)
		st := transcribe.Analyse(words, "en")
		p := Compute(words, st, Options{PadMs: int64(60 + rng.Intn(41))})

		fillerWord := map[int]bool{}
		for _, f := range st.Fillers {
			for i := f.FirstWord; i <= f.LastWord; i++ {
				fillerWord[i] = true
			}
		}
		covered := map[int]bool{}
		for _, k := range p.Keep {
			for i := k.FirstWord; i <= k.LastWord; i++ {
				covered[i] = true
			}
			for i, wd := range words {
				if (k.StartMs > wd.StartMs && k.StartMs < wd.EndMs) || (k.EndMs > wd.StartMs && k.EndMs < wd.EndMs) {
					t.Fatalf("case %d: keep %+v cuts inside word %d %+v", n, k, i, wd)
				}
			}
			f, _ := Audit(words, st, Range{InMs: k.StartMs, OutMs: k.EndMs, LastOfEdit: true})
			if len(f) != 0 {
				t.Fatalf("case %d: keep %+v: %+v", n, k, f)
			}
		}
		for i := range words {
			if fillerWord[i] && covered[i] {
				t.Fatalf("case %d: filler word %d %q is kept", n, i, words[i].Text)
			}
			if !fillerWord[i] && !covered[i] {
				t.Fatalf("case %d: word %d %q was cut: sentences must stay whole", n, i, words[i].Text)
			}
		}
	}
}

func TestAuditFindsWhatTheRulesForbid(t *testing.T) {
	words := fixture()
	st := transcribe.Analyse(words, "en")
	kinds := func(r Range) map[string]int {
		f, _ := Audit(words, st, r)
		m := map[string]int{}
		for _, x := range f {
			m[x.Kind]++
		}
		return m
	}
	// The whole clip: fillers, a pause over 400 ms (two of them), and nothing cut.
	all := kinds(Range{InMs: 0, OutMs: words[len(words)-1].EndMs + 50})
	if all[FindingFiller] == 0 || all[FindingPause] != 2 || all[FindingInsideWord] != 0 {
		t.Errorf("whole clip: %v", all)
	}
	// A cut through the middle of "today" is a word cut.
	mid := (words[2].StartMs + words[2].EndMs) / 2
	if k := kinds(Range{InMs: mid, OutMs: words[10].EndMs + 60}); k[FindingInsideWord] != 1 {
		t.Errorf("start inside a word: %v", k)
	}
	if k := kinds(Range{InMs: words[1].StartMs - 40, OutMs: mid}); k[FindingInsideWord] != 1 {
		t.Errorf("end inside a word: %v", k)
	}
	// A cut within the tolerance of a word edge is not a word cut.
	if k := kinds(Range{InMs: words[3].StartMs + CutToleranceMs - 1, OutMs: words[4].EndMs + 30}); k[FindingInsideWord] != 0 {
		t.Errorf("tolerated cut: %v", k)
	}
	// Silence before the first word counts; at the end of the edit it does not.
	lead := Range{InMs: words[11].StartMs - 700, OutMs: words[14].EndMs + 60}
	if k := kinds(lead); k[FindingPause] != 1 {
		t.Errorf("leading silence: %v", k)
	}
	tail := Range{InMs: words[9].StartMs - 60, OutMs: words[10].EndMs + 800}
	if k := kinds(tail); k[FindingPause] != 1 {
		t.Errorf("trailing silence: %v", k)
	}
	tail.LastOfEdit = true
	if k := kinds(tail); k[FindingPause] != 0 {
		t.Errorf("trailing silence at the end of the edit: %v", k)
	}
	// Speed 2 halves a pause: 900 ms of source silence plays as 450, still over the limit; 700 plays as 350, under it.
	f, _ := Audit(words, st, Range{InMs: words[9].StartMs - 60, OutMs: words[10].EndMs + 700, Speed: 2})
	for _, x := range f {
		if x.Kind == FindingPause {
			t.Errorf("a 700 ms tail at 2x is 350 ms: %+v", x)
		}
	}
	// A range with no speech is a cutaway.
	if f, _ := Audit(words, st, Range{InMs: 0, OutMs: 200}); len(f) != 0 {
		t.Errorf("silent range: %+v", f)
	}
}
