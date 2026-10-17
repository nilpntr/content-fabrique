package quicklook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nilpntr/content-fabrique/internal/textclean"
)

// What the model returns goes into the database and, later, into the editor agent's prompt and the library's search, so it is
// untrusted input however strict the schema was: it is parsed strictly, cleaned like an asset title (package textclean) and
// held to the limits, and anything that does not fit is rejected as a whole, not repaired. A rejected answer leaves the
// asset's title, description and tags alone and marks the quick look failed.

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
}

// A link, an address, or a sentence that tries to steer whatever reads the text later. Footage can carry such words (a
// caption, a watermark, a sign) and a model may copy them; none of it is a label.
var suspicious = regexp.MustCompile(`(?i)(https?:|ftp:|www\.|\.(com|net|org|io|ly|me)\b|[a-z0-9._%+-]+@[a-z0-9-]+\.|` +
	`\b(ignore|disregard|forget|override)\b.{0,40}\b(instruction|prompt|rule|above|previous)s?\b|` +
	`\b(system|developer)\s+(prompt|message)\b|\bas an ai\b|\bassistant\s*:|\bsystem\s*:)`)

// tagPattern is one tag: one to three words of letters and digits, optionally joined by hyphens, apostrophes or "&".
var tagWord = regexp.MustCompile(`^[\p{L}\p{N}]+(?:['&-][\p{L}\p{N}]+)*$`)

// Validate parses the arguments of the describe_clip call and returns the cleaned answer, or an error that wraps
// ErrInvalidOutput. The rules:
//
//   - the JSON is an object with exactly title, description and tags (no unknown field, no trailing data);
//   - title and description are cleaned (textclean) and then must be non-empty and within 60 and 160 characters; they are
//     rejected, not cut, when they are too long;
//   - tags number at most 8 and are lower-cased and cleaned; each is one to three words of letters, digits and "-", "'" or "&"
//     in between, at most 32 characters; duplicates are dropped; an empty list is allowed;
//   - nothing contains a link, an e-mail address or words that address a reader (see suspicious).
func Validate(raw json.RawMessage) (Output, error) {
	var in struct {
		Title       *string   `json:"title"`
		Description *string   `json:"description"`
		Tags        *[]string `json:"tags"`
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
	if len(*in.Tags) > MaxTags {
		return Output{}, reject("%d tags, at most %d", len(*in.Tags), MaxTags)
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
	tags := make([]string, 0, len(*in.Tags))
	for _, t := range *in.Tags {
		tag, err := cleanTag(t)
		if err != nil {
			return Output{}, err
		}
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	return Output{Title: title, Description: desc, Tags: tags}, nil
}

func cleanLine(field, s string, limit int) (string, error) {
	// textclean's own cut is a safety net far above the limit: a too-long answer is rejected, not shortened.
	clean, ok := textclean.Clean(s, 4*limit)
	if !ok {
		return "", reject("%s is empty or not text", field)
	}
	if n := utf8.RuneCountInString(clean); n > limit {
		return "", reject("%s is %d characters, at most %d", field, n, limit)
	}
	if suspicious.MatchString(clean) {
		return "", reject("%s holds a link, an address or an instruction", field)
	}
	return clean, nil
}

func cleanTag(s string) (string, error) {
	clean, ok := textclean.Clean(strings.ToLower(s), 4*maxTagChars)
	if !ok {
		return "", reject("a tag is empty or not text")
	}
	if utf8.RuneCountInString(clean) > maxTagChars {
		return "", reject("tag %q is longer than %d characters", clean, maxTagChars)
	}
	words := strings.Fields(clean)
	if len(words) < 1 || len(words) > MaxTagWords {
		return "", reject("tag %q is not one to %d words", clean, MaxTagWords)
	}
	for _, w := range words {
		if !tagWord.MatchString(w) {
			return "", reject("tag %q holds characters other than letters, digits and - ' &", clean)
		}
	}
	if clean != strings.ToLower(clean) || strings.ContainsFunc(clean, unicode.IsUpper) {
		return "", reject("tag %q is not lowercase", clean)
	}
	if suspicious.MatchString(clean) {
		return "", reject("tag %q holds a link, an address or an instruction", clean)
	}
	return clean, nil
}
