package transcribe

import (
	"slices"
	"strings"
	"unicode"
)

// Structure is the speech structure of a transcript (D-28): where sentences begin and end, which words are fillers and where the
// speaker pauses. The editor cuts on it (a jump cut removes a filler or shortens a pause) and the captions break on it. It is
// computed from the words alone, so it is the same for every provider, and cheap to recompute when the rules improve.
type Structure struct {
	Sentences []Sentence `json:"sentences"`
	Fillers   []Filler   `json:"fillers"`
	Pauses    []Pause    `json:"pauses"`
}

// Sentence is a run of words from FirstWord to LastWord (indexes into the transcript's words, both included).
type Sentence struct {
	FirstWord int    `json:"first_word"`
	LastWord  int    `json:"last_word"`
	StartMs   int64  `json:"start_ms"`
	EndMs     int64  `json:"end_ms"`
	Text      string `json:"text"`
}

// Filler kinds and strengths.
const (
	// FillerHesitation is a sound that carries no meaning: "um", "uh", "eh", "äh". Always safe to cut.
	FillerHesitation = "hesitation"
	// FillerDiscourse is a word or phrase that is empty here but a word elsewhere ("like", "you know", "zeg maar").
	FillerDiscourse = "discourse"

	// StrengthStrong fillers are nearly always cuttable; weak ones depend on context and are for an editor that listens to more
	// than the transcript.
	StrengthStrong = "strong"
	StrengthWeak   = "weak"
)

// Filler is one filler: the words FirstWord to LastWord (a phrase such as "you know" has two).
type Filler struct {
	FirstWord int    `json:"first_word"`
	LastWord  int    `json:"last_word"`
	StartMs   int64  `json:"start_ms"`
	EndMs     int64  `json:"end_ms"`
	Text      string `json:"text"`
	Kind      string `json:"kind"`
	Strength  string `json:"strength"`
}

// Pause is silence between two words longer than PauseMinMs: AfterWord is the index of the word before it.
type Pause struct {
	AfterWord  int   `json:"after_word"`
	StartMs    int64 `json:"start_ms"`
	EndMs      int64 `json:"end_ms"`
	DurationMs int64 `json:"duration_ms"`
	// SentenceBreak is true when the pause falls between two sentences, false when the speaker stopped mid-sentence.
	SentenceBreak bool `json:"sentence_break"`
}

const (
	// PauseMinMs is the shortest gap between words that counts as a pause: longer than this (D-28: pauses > 400 ms).
	PauseMinMs = 400
	// sentenceGapMs ends a sentence that has no closing punctuation (a model that writes none, a speaker who stops).
	sentenceGapMs = 1200
	// contextGapMs is the gap that makes a word like "like" a filler: it is set off by silence.
	contextGapMs = 150
)

// Analyse computes the speech structure of words in the given language (ISO 639-1; empty or unknown uses the language-neutral
// rules only).
func Analyse(words []Word, language string) Structure {
	st := Structure{Sentences: []Sentence{}, Fillers: []Filler{}, Pauses: []Pause{}}
	if len(words) == 0 {
		return st
	}
	// Sentences.
	first := 0
	breaks := make(map[int]bool) // index of the last word of each sentence
	for i, w := range words {
		last := i == len(words)-1
		end := last || endsSentence(w.Text) || (words[i+1].StartMs-w.EndMs >= sentenceGapMs && !strings.HasSuffix(strings.TrimSpace(w.Text), ","))
		if !end {
			continue
		}
		parts := make([]string, 0, i-first+1)
		for _, x := range words[first : i+1] {
			parts = append(parts, x.Text)
		}
		st.Sentences = append(st.Sentences, Sentence{FirstWord: first, LastWord: i, StartMs: words[first].StartMs, EndMs: w.EndMs, Text: strings.Join(parts, " ")})
		breaks[i] = true
		first = i + 1
	}
	starts := make(map[int]bool, len(st.Sentences))
	for _, s := range st.Sentences {
		starts[s.FirstWord] = true
	}

	// Pauses.
	for i := 0; i+1 < len(words); i++ {
		gap := words[i+1].StartMs - words[i].EndMs
		if gap > PauseMinMs {
			st.Pauses = append(st.Pauses, Pause{AfterWord: i, StartMs: words[i].EndMs, EndMs: words[i+1].StartMs, DurationMs: gap, SentenceBreak: breaks[i]})
		}
	}

	// Fillers.
	rules := fillerRulesFor(language)
	norm := make([]string, len(words))
	for i, w := range words {
		norm[i] = normWord(w.Text)
	}
	for i := 0; i < len(words); {
		if f, n := matchFiller(words, norm, i, starts, rules); n > 0 {
			f.FirstWord, f.LastWord = i, i+n-1
			f.StartMs, f.EndMs = words[i].StartMs, words[i+n-1].EndMs
			parts := make([]string, 0, n)
			for _, x := range words[i : i+n] {
				parts = append(parts, strings.TrimFunc(x.Text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
			}
			f.Text = strings.Join(parts, " ")
			st.Fillers = append(st.Fillers, f)
			i += n
			continue
		}
		i++
	}
	return st
}

// endsSentence reports whether a word's text closes a sentence: it ends in ".", "?", "!" or "…" (closing quotes and brackets
// allowed after it), and it is not an abbreviation or a lone initial.
func endsSentence(text string) bool {
	t := strings.TrimRight(text, `"'”’)]»`)
	if t == "" {
		return false
	}
	r := []rune(t)
	switch r[len(r)-1] {
	case '?', '!', '…':
		return true
	case '.':
	default:
		return false
	}
	w := strings.ToLower(strings.TrimLeft(string(r[:len(r)-1]), `"'“‘([«`))
	if strings.Contains(w, ".") { // e.g., i.e., U.S.
		return false
	}
	if len([]rune(w)) == 1 && unicode.IsLetter([]rune(w)[0]) && w != "i" && w != "a" { // an initial
		return false
	}
	return !abbreviations[w]
}

var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "sr": true, "jr": true, "st": true, "vs": true, "etc": true,
	"hr": true, "mw": true, "ir": true, "nr": true, "bv": true,
}

// normWord lowercases a word and strips the punctuation around it.
func normWord(s string) string {
	return strings.ToLower(strings.TrimFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
}

// fillerRules are the filler words and phrases of one language.
type fillerRules struct {
	hesitations map[string]bool
	// phrases are discourse fillers of one or more words, longest first.
	phrases []phrase
}

type phrase struct {
	words    []string
	strength string
	// when restricts where the phrase counts: "" anywhere, "initial" first word of a sentence, "set-off" only when the speaker
	// sets it off with a comma or a pause.
	when string
}

// hesitations are the same in the languages we meet; a language adds its own.
var commonHesitations = []string{"um", "umm", "uh", "uhh", "uhm", "erm", "hmm", "hm", "mm", "mmm", "mhm", "eh", "ehm", "ehh", "euh", "äh", "ähm", "öh", "heu", "hum"}

var phrasesByLanguage = map[string][]phrase{
	"en": {
		{[]string{"you", "know"}, StrengthStrong, ""},
		{[]string{"i", "mean"}, StrengthStrong, ""},
		{[]string{"sort", "of"}, StrengthWeak, "set-off"},
		{[]string{"kind", "of"}, StrengthWeak, "set-off"},
		{[]string{"basically"}, StrengthWeak, ""},
		{[]string{"literally"}, StrengthWeak, "set-off"},
		{[]string{"actually"}, StrengthWeak, "set-off"},
		{[]string{"like"}, StrengthWeak, "set-off"},
		{[]string{"so"}, StrengthWeak, "initial"},
		{[]string{"well"}, StrengthWeak, "initial"},
		{[]string{"right"}, StrengthWeak, "set-off"},
	},
	"nl": {
		{[]string{"weet", "je"}, StrengthStrong, "set-off"},
		{[]string{"zeg", "maar"}, StrengthStrong, ""},
		{[]string{"eigenlijk"}, StrengthWeak, ""},
		{[]string{"gewoon"}, StrengthWeak, "set-off"},
		{[]string{"dus"}, StrengthWeak, "initial"},
		{[]string{"nou"}, StrengthWeak, "initial"},
		{[]string{"ja"}, StrengthWeak, "initial"},
		{[]string{"zeg"}, StrengthWeak, "set-off"},
	},
	"de": {
		{[]string{"weißt", "du"}, StrengthStrong, "set-off"},
		{[]string{"sozusagen"}, StrengthStrong, ""},
		{[]string{"quasi"}, StrengthWeak, ""},
		{[]string{"eigentlich"}, StrengthWeak, ""},
		{[]string{"halt"}, StrengthWeak, "set-off"},
		{[]string{"also"}, StrengthWeak, "initial"},
		{[]string{"ja"}, StrengthWeak, "initial"},
	},
	"fr": {
		{[]string{"tu", "sais"}, StrengthStrong, "set-off"},
		{[]string{"en", "fait"}, StrengthWeak, "set-off"},
		{[]string{"genre"}, StrengthWeak, "set-off"},
		{[]string{"bah"}, StrengthWeak, "initial"},
		{[]string{"ben"}, StrengthWeak, "initial"},
		{[]string{"du", "coup"}, StrengthWeak, "set-off"},
	},
	"es": {
		{[]string{"o", "sea"}, StrengthStrong, ""},
		{[]string{"tú", "sabes"}, StrengthStrong, "set-off"},
		{[]string{"pues"}, StrengthWeak, "initial"},
		{[]string{"bueno"}, StrengthWeak, "initial"},
		{[]string{"como"}, StrengthWeak, "set-off"},
	},
}

func fillerRulesFor(language string) fillerRules {
	r := fillerRules{hesitations: map[string]bool{}}
	for _, h := range commonHesitations {
		r.hesitations[h] = true
	}
	lang := strings.ToLower(strings.TrimSpace(language))
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	r.phrases = slices.Clone(phrasesByLanguage[lang])
	slices.SortStableFunc(r.phrases, func(a, b phrase) int { return len(b.words) - len(a.words) })
	return r
}

// matchFiller reports the filler that starts at word i and how many words it spans (0 when none does).
func matchFiller(words []Word, norm []string, i int, sentenceStarts map[int]bool, rules fillerRules) (Filler, int) {
	if rules.hesitations[norm[i]] {
		return Filler{Kind: FillerHesitation, Strength: StrengthStrong}, 1
	}
	for _, p := range rules.phrases {
		n := len(p.words)
		if i+n > len(words) {
			continue
		}
		ok := true
		for k, w := range p.words {
			if norm[i+k] != w {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		switch p.when {
		case "initial":
			if !sentenceStarts[i] {
				continue
			}
		case "set-off":
			if !setOff(words, i, i+n-1) {
				continue
			}
		}
		return Filler{Kind: FillerDiscourse, Strength: p.strength}, n
	}
	return Filler{}, 0
}

// setOff reports whether the words first..last are set apart from their neighbours by a comma or a short silence: how a speaker
// says "like" as a filler and not as a verb or a comparison.
func setOff(words []Word, first, last int) bool {
	if strings.HasSuffix(strings.TrimSpace(words[last].Text), ",") {
		return true
	}
	if first > 0 {
		if strings.HasSuffix(strings.TrimSpace(words[first-1].Text), ",") || words[first].StartMs-words[first-1].EndMs >= contextGapMs {
			return true
		}
	}
	if last+1 < len(words) && words[last+1].StartMs-words[last].EndMs >= contextGapMs {
		return true
	}
	return false
}
