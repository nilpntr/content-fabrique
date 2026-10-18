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

	"github.com/nilpntr/content-fabrique/internal/textclean"
)

// What the model returns goes into the database and, later, into the editor agent's prompt and the library's search, so it is
// untrusted input however strict the schema was: it is parsed strictly, cleaned as model text (package textclean) and held to
// the limits. The rules differ in what a fault costs:
//
//   - a title or description that is empty, too long or not the right shape rejects the whole answer;
//   - anything that looks like a link, an address, an @handle or words aimed at a reader rejects the whole answer, wherever it
//     is, because it means the model repeated something from the footage that was meant for someone else;
//   - a single tag that does not fit the tag format is dropped and the rest are kept.
//
// A rejected answer leaves the asset's title, description and tags alone and marks the quick look failed.

// ErrInvalidOutput is wrapped by every rejection of Validate.
var ErrInvalidOutput = errors.New("quicklook: the model's answer is not acceptable")

func reject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOutput, fmt.Sprintf(format, args...))
}

// Output is a validated answer.
type Output struct {
	Title       string
	Description string
	Tags        []string
	// Subjects are the subject boxes the model gave, by frame number (1 based), as read: bounded numbers, not yet checked against the
	// clip (package subject cleans them).
	Subjects []SubjectRead
}

// SubjectRead is where the model says the person is in one frame.
type SubjectRead struct {
	Frame                int
	Present              bool
	X, Y, W, H, HeadTopY float64
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

// tagWord is one word of a tag: letters and digits, optionally joined by hyphens, apostrophes or "&".
var tagWord = regexp.MustCompile(`^[\p{L}\p{N}]+(?:['&-][\p{L}\p{N}]+)*$`)

// Validate parses the arguments of the describe_clip call and returns the cleaned answer, or an error that wraps
// ErrInvalidOutput. The rules:
//
//   - the JSON is an object with exactly title, description and tags (no unknown field, no trailing data);
//   - title and description are cleaned (textclean.Model) and must be non-empty and within 60 and 160 characters; they are
//     rejected, not cut, when they are too long;
//   - tags are lower-cased and cleaned; a tag that is empty, longer than 32 characters, more than three words, or has any
//     character other than letters, digits and "-", "'" or "&" inside a word is dropped; duplicates are dropped; at most 8 are
//     kept (the first 8 that fit); an empty list is allowed;
//   - nothing contains a link, an e-mail address, an @handle or words that address a reader (see suspicious), after folding
//     compatibility forms: that rejects the whole answer.
func Validate(raw json.RawMessage) (Output, error) {
	var in struct {
		Title       *string      `json:"title"`
		Description *string      `json:"description"`
		Tags        *[]string    `json:"tags"`
		Subjects    *[]subjectIn `json:"subjects"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return Output{}, reject("not the expected JSON object: %v", err)
	}
	if dec.More() {
		return Output{}, reject("data after the JSON object")
	}
	if in.Title == nil || in.Description == nil || in.Tags == nil {
		return Output{}, reject("title, description and tags are all required")
	}

	title, err := cleanLine("title", *in.Title, MaxTitleChars)
	if err != nil {
		return Output{}, err
	}
	desc, err := cleanLine("description", *in.Description, MaxDescriptionChars)
	if err != nil {
		return Output{}, err
	}
	seen := map[string]bool{}
	tags := make([]string, 0, MaxTags)
	for _, t := range *in.Tags {
		tag, ok, err := cleanTag(t)
		if err != nil {
			return Output{}, err
		}
		if !ok || seen[tag] || len(tags) == MaxTags {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	// The subject boxes are numbers: an entry that is not complete or not a plain number is left out (the labels are still good), never
	// repaired. package subject holds the bounds a box must meet.
	var subjects []SubjectRead
	var entries []subjectIn
	if in.Subjects != nil {
		entries = *in.Subjects
	}
	for _, s := range entries {
		if len(subjects) == MaxSubjects {
			break
		}
		if s.Frame == nil || s.Present == nil || s.X == nil || s.Y == nil || s.W == nil || s.H == nil || s.HeadTopY == nil {
			continue
		}
		subjects = append(subjects, SubjectRead{Frame: *s.Frame, Present: *s.Present, X: *s.X, Y: *s.Y, W: *s.W, H: *s.H, HeadTopY: *s.HeadTopY})
	}
	return Output{Title: title, Description: desc, Tags: tags, Subjects: subjects}, nil
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
	return clean, nil
}

// cleanTag returns the tag cleaned, ok false when it does not fit the tag format (the caller drops it), and an error when it
// holds something that rejects the whole answer.
func cleanTag(s string) (tag string, ok bool, err error) {
	if suspicious.MatchString(foldForChecks(s)) {
		return "", false, reject("a tag holds a link, an address, a handle or an instruction")
	}
	clean, ok := textclean.Model(strings.ToLower(s), 4*maxTagChars)
	if !ok || utf8.RuneCountInString(clean) > maxTagChars {
		return "", false, nil
	}
	words := strings.Fields(clean)
	if len(words) < 1 || len(words) > MaxTagWords {
		return "", false, nil
	}
	for _, w := range words {
		if !tagWord.MatchString(w) {
			return "", false, nil
		}
	}
	if suspicious.MatchString(foldForChecks(clean)) {
		return "", false, reject("a tag holds a link, an address, a handle or an instruction")
	}
	return clean, true, nil
}
