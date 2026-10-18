package quicklook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/nilpntr/content-fabrique/internal/taxonomy"
	"github.com/nilpntr/content-fabrique/internal/textclean"
)

// What the model returns goes into the database and, later, into the editor agent's prompt and the library's search, so it is
// untrusted input however strict the schema was: it is parsed strictly, cleaned as model text (package textclean) and held to
// the limits. The rules differ in what a fault costs:
//
//   - a title or description that is empty, too long, not the right shape or placeholder text ("placeholder", "n/a") rejects the
//     whole answer: such a title is never stored;
//   - anything that looks like a link, an address, an @handle or words aimed at a reader rejects the whole answer, wherever it
//     is, because it means the model repeated something from the footage that was meant for someone else;
//   - a label outside the vocabulary is mapped through the synonym map onto its facet, or dropped; "unsure" is dropped; an exercise
//     name that does not fit (too long, placeholder text) is dropped. The rest of the answer is kept.
//
// A rejected answer leaves the asset's title, description and labels alone and marks the quick look failed.

// ErrInvalidOutput is wrapped by every rejection of Validate.
var ErrInvalidOutput = errors.New("quicklook: the model's answer is not acceptable")

// ErrPlaceholder is wrapped (with ErrInvalidOutput) by the rejection of an answer whose title or description is placeholder text: the
// model filled the fields without describing the frames. The service asks once more before it gives up.
var ErrPlaceholder = errors.New("quicklook: the answer is placeholder text")

func reject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOutput, fmt.Sprintf(format, args...))
}

// Output is a validated answer.
type Output struct {
	Title       string
	Description string
	// Exercise is the exercise's or pose's name, lowercase; "" when the model gave none (or one that did not fit).
	Exercise string
	// Facets are the labels, in the vocabulary (the exercise name has filled the facets the model left unsure).
	Facets taxonomy.Set
	// Subjects are the subject boxes the model gave, by frame number (1 based), as read: bounded numbers, not yet checked against the
	// clip (package subject cleans them).
	Subjects []SubjectRead
}

// SubjectRead is where the model says the person is in one frame.
type SubjectRead struct {
	Frame                int
	Present              bool
	X, Y, W, H, HeadTopY float64
	// HeadX, HeadW and ShoulderY are the head alone and the shoulder line (quicklook.v3); 0 when the model did not give them.
	HeadX, HeadW, ShoulderY float64
}

// subjectIn is one subject entry of the answer, every member a pointer so that a missing one is told from a zero.
type subjectIn struct {
	Frame    *int     `json:"frame"`
	Present  *bool    `json:"present"`
	X        *float64 `json:"x"`
	Y        *float64 `json:"y"`
	W        *float64 `json:"w"`
	H        *float64 `json:"h"`
	HeadTopY *float64 `json:"head_top_y"`
	// The head alone and the shoulder line are optional: an answer of quicklook.v2 has none, and a missing one is 0 (not read).
	HeadX     *float64 `json:"head_x"`
	HeadW     *float64 `json:"head_w"`
	ShoulderY *float64 `json:"shoulder_y"`
}

// MaxSubjects is the most subject entries read: one per frame, with room.
const MaxSubjects = 8

// suspicious matches, on text that has been folded (foldForChecks), a link, an e-mail address, an @handle, a bare domain, or
// a sentence that tries to steer whatever reads the text later. Footage can carry such words (a caption, a watermark, a sign)
// and a model may copy them; none of it is a label.
var suspicious = regexp.MustCompile(`(?i)(` +
	`\b(https?|ftp|javascript)\s*:|\bwww\s*\.|` +
	`\b[a-z0-9-]+\.(com|net|org|io|ly|me|co|app|dev|xyz|link|gl|be|tv|info|biz)\b|` +
	`\b[a-z0-9-]+\s+dot\s+(com|net|org|io|ly|me|co|app|dev|xyz|link|gl|be|tv|info|biz)\b|` +
	`[a-z0-9._%+-]+@[a-z0-9-]+\.[a-z]{2,}|` +
	`(^|[^\p{L}\p{N}_])@[\p{L}\p{N}_.]{2,}|` +
	`\b(ignore|disregard|forget|override)\b.{0,40}\b(instruction|prompt|rule|above|previous)s?\b|` +
	`\b(system|developer)\s+(prompt|message)\b|\bas an ai\b|\bassistant\s*:|\bsystem\s*:)`)

// foldForChecks folds text the way a reader would see it before the suspicious-content checks run: compatibility forms
// become plain ones (full-width "ｈｔｔｐ：／／" is "http://", "ﬁ" is "fi"), then lower case. Look-alike letters from other
// scripts are not folded; the checks look at what the text says to a person typing it, not at every possible disguise.
func foldForChecks(s string) string { return strings.ToLower(norm.NFKC.String(s)) }

// Validate parses the arguments of the describe_clip call against the vocabulary and returns the cleaned answer, or an error that
// wraps ErrInvalidOutput. The rules:
//
//   - the JSON is an object with title, description, exercise, a member per facet of the vocabulary and subjects (no unknown member,
//     no trailing data); a facet that is missing counts as unsure;
//   - title and description are cleaned (textclean.Model) and must be non-empty, within 60 and 160 characters, and not placeholder
//     text (ErrPlaceholder); they are rejected, not cut;
//   - a facet's values are kept when the vocabulary has them; any other word is mapped through the synonym map onto that facet, or
//     dropped; "unsure" is dropped; at most the facet's max are kept; a value the file sets (slow motion) is never taken from the model;
//   - the exercise is cleaned and lower-cased; at most 40 characters of words, else it is dropped; what it names fills the facets the
//     model left empty ("seated cable row": a row, with a cable);
//   - nothing contains a link, an e-mail address, an @handle or words that address a reader (see suspicious), after folding
//     compatibility forms: that rejects the whole answer.
func Validate(raw json.RawMessage, tax *taxonomy.Taxonomy) (Output, error) {
	var members map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&members); err != nil || members == nil {
		return Output{}, reject("not the expected JSON object: %v", err)
	}
	if dec.More() {
		return Output{}, reject("data after the JSON object")
	}
	for k := range members {
		if _, facet := tax.Facet(k); !facet && k != "title" && k != "description" && k != "exercise" && k != "subjects" {
			return Output{}, reject("unknown member %q", k)
		}
	}
	str := func(k string) (*string, error) {
		v, ok := members[k]
		if !ok || string(v) == "null" {
			return nil, nil
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return nil, reject("%s is not a string", k)
		}
		return &s, nil
	}
	titleIn, err := str("title")
	if err != nil {
		return Output{}, err
	}
	descIn, err := str("description")
	if err != nil {
		return Output{}, err
	}
	if titleIn == nil || descIn == nil {
		return Output{}, reject("title and description are both required")
	}
	exIn, err := str("exercise")
	if err != nil {
		return Output{}, err
	}
	// The checks that reject the whole answer for what it says run first, so a link anywhere is reported as a link.
	title, terr := cleanLine("title", *titleIn, MaxTitleChars)
	desc, derr := cleanLine("description", *descIn, MaxDescriptionChars)
	for _, e := range []error{terr, derr} {
		if e != nil && !errors.Is(e, ErrPlaceholder) {
			return Output{}, e
		}
	}
	exercise := ""
	if exIn != nil {
		if exercise, err = cleanExercise(*exIn); err != nil {
			return Output{}, err
		}
	}

	var given []taxonomy.Ref
	for _, fc := range tax.Facets {
		v, ok := members[fc.ID]
		if !ok || string(v) == "null" {
			continue
		}
		var list []string
		if err := json.Unmarshal(v, &list); err != nil {
			var one string
			if err := json.Unmarshal(v, &one); err != nil {
				return Output{}, reject("%s is not a value or a list of values", fc.ID)
			}
			list = []string{one}
		}
		for _, word := range list[:min(len(list), 8)] {
			if suspicious.MatchString(foldForChecks(word)) {
				return Output{}, reject("a label holds a link, an address, a handle or an instruction")
			}
			given = append(given, facetValues(tax, fc, word)...)
		}
	}
	subjects, err := readSubjects(members["subjects"])
	if err != nil {
		return Output{}, err
	}
	if terr != nil {
		return Output{}, terr
	}
	if derr != nil {
		return Output{}, derr
	}
	var hints []string
	if exercise != "" {
		hints = append(hints, exercise)
	}
	return Output{Title: title, Description: desc, Exercise: exercise, Facets: tax.Build(given, hints...), Subjects: subjects}, nil
}

// facetValues reads one label the model gave for a facet: the value itself when the vocabulary has it (and the model may set it), else
// what the synonym map makes of the word within that facet; nothing for "unsure" or a word that maps to nothing there.
func facetValues(tax *taxonomy.Taxonomy, fc taxonomy.Facet, word string) []taxonomy.Ref {
	w := strings.TrimSpace(strings.ToLower(word))
	if w == "" || w == taxonomy.Unsure {
		return nil
	}
	r := taxonomy.Ref{Facet: fc.ID, Value: w}
	if tax.Known(r) {
		if tax.FromProbe(r) {
			return nil
		}
		return []taxonomy.Ref{r}
	}
	var out []taxonomy.Ref
	for _, m := range tax.Phrases(word) {
		if m.Facet == fc.ID {
			out = append(out, m)
		}
	}
	return out
}

// readSubjects reads the subject boxes. The boxes are numbers: an entry that is not complete or not a plain number is left out (the
// labels are still good), never repaired. package subject holds the bounds a box must meet.
func readSubjects(raw json.RawMessage) ([]SubjectRead, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var entries []subjectIn
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, reject("subjects is not a list of boxes: %v", err)
	}
	var subjects []SubjectRead
	for _, s := range entries {
		if len(subjects) == MaxSubjects {
			break
		}
		if s.Frame == nil || s.Present == nil || s.X == nil || s.Y == nil || s.W == nil || s.H == nil || s.HeadTopY == nil {
			continue
		}
		r := SubjectRead{Frame: *s.Frame, Present: *s.Present, X: *s.X, Y: *s.Y, W: *s.W, H: *s.H, HeadTopY: *s.HeadTopY}
		if s.HeadX != nil && s.HeadW != nil {
			r.HeadX, r.HeadW = *s.HeadX, *s.HeadW
		}
		if s.ShoulderY != nil {
			r.ShoulderY = *s.ShoulderY
		}
		subjects = append(subjects, r)
	}
	return subjects, nil
}

// placeholders are what a model writes when it fills a field instead of describing the frames (as taxonomy.Normalize folds them).
var placeholders = map[string]bool{
	"placeholder": true, "placeholder text": true, "placeholder title": true, "placeholder description": true, "n a": true, "na": true,
	"none": true, "null": true, "nil": true, "undefined": true, "unknown": true, "untitled": true, "no title": true, "title": true,
	"description": true, "clip": true, "video": true, "video clip": true, "tbd": true, "todo": true, "example": true, "sample": true,
	"string": true, "test": true, "text": true, "lorem ipsum": true, "xxx": true, "unsure": true, "not sure": true, "exercise": true,
	"describe this clip": true, "your title here": true, "title here": true,
}

// IsPlaceholder says whether a text is placeholder words and not a description: empty, one of the placeholders as a whole, or a text
// that says "placeholder" or "lorem ipsum" anywhere.
func IsPlaceholder(s string) bool {
	f := taxonomy.Normalize(s)
	return f == "" || placeholders[f] || strings.Contains(f, "placeholder") || strings.Contains(f, "lorem ipsum")
}

// exerciseWord is one word of an exercise name: letters and digits, optionally joined by hyphens, apostrophes, "&" or "/".
var exerciseWord = regexp.MustCompile(`^[\p{L}\p{N}]+(?:['&/-][\p{L}\p{N}]+)*$`)

// cleanExercise returns the exercise name cleaned and lower-cased, "" when it does not fit (too long, odd characters, placeholder text),
// and an error when it holds something that rejects the whole answer.
func cleanExercise(s string) (string, error) {
	if suspicious.MatchString(foldForChecks(s)) {
		return "", reject("the exercise holds a link, an address, a handle or an instruction")
	}
	clean, ok := textclean.Model(strings.ToLower(s), 4*MaxExerciseChars)
	if !ok || utf8.RuneCountInString(clean) > MaxExerciseChars || IsPlaceholder(clean) {
		return "", nil
	}
	words := strings.Fields(strings.NewReplacer(",", " ", "(", " ", ")", " ").Replace(clean))
	if len(words) == 0 || len(words) > 6 {
		return "", nil
	}
	for _, w := range words {
		if !exerciseWord.MatchString(w) {
			return "", nil
		}
	}
	return strings.Join(words, " "), nil
}

func cleanLine(field, s string, limit int) (string, error) {
	// textclean's own cut is a safety net far above the limit: a too-long answer is rejected, not shortened.
	clean, ok := textclean.Model(s, 4*limit)
	if !ok {
		return "", reject("%s is empty or not text", field)
	}
	if n := utf8.RuneCountInString(clean); n > limit {
		return "", reject("%s is %d characters, at most %d", field, n, limit)
	}
	if suspicious.MatchString(foldForChecks(s)) || suspicious.MatchString(foldForChecks(clean)) {
		return "", reject("%s holds a link, an address, a handle or an instruction", field)
	}
	if IsPlaceholder(clean) {
		return "", fmt.Errorf("%w: %w: %s is %q", ErrInvalidOutput, ErrPlaceholder, field, clean)
	}
	return clean, nil
}
