// Package formats is the format library (docs/design/formats-v1.md): the short-form video formats that work in a feed ("<TOPIC> 101"
// tutorials, numbered lists, a quote then the reveal on the drop, a transformation, a dialogue then the twist, ...), as versioned data
// in config/formats, one card per format, in our own words. Style profiles teach the editor how a creator edits; the formats give it
// ideas: which concept fits this footage, which ones the creator already uses (detected on their reference videos, no model call), and
// which one would be new to them.
//
// The package reads and validates the cards (Load), detects the formats of a finished video from its measures and its on-screen text
// (Detect), and ranks the formats that fit a set of footage (Rank). It imports nothing of the rest of the server but the embedded
// config, so the style profile, the editor and the API can all use it.
package formats

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	configfiles "github.com/nilpntr/content-fabrique/config"
)

// The kinds of card: a format is the idea of a whole video; a device (a loop ending) is mixed into a format.
const (
	KindFormat = "format"
	KindDevice = "device"
)

// The text patterns a format asks for: the creator style's four (single_title, story_captions, dialogue, none), chapter_cards (a
// count title and numbered chapter cards), and any (the format does not decide the text).
const (
	TextSingleTitle   = "single_title"
	TextStoryCaptions = "story_captions"
	TextDialogue      = "dialogue"
	TextChapterCards  = "chapter_cards"
	TextNone          = "none"
	TextAny           = "any"
)

// TextPatterns lists the text patterns of a card.
var TextPatterns = []string{TextSingleTitle, TextStoryCaptions, TextDialogue, TextChapterCards, TextNone, TextAny}

// The payoff a card's check asks for: on the song's drop (a reveal), at the end, or anywhere.
const (
	PayoffDrop = "drop"
	PayoffEnd  = "end"
	PayoffAny  = "any"
)

// Fit levels of a vibe.
const (
	VibeGreat = "great"
	VibeGood  = "good"
	VibeWeak  = "weak"
)

// The vocabularies a card is checked against. Roles and content types are the editor's (kept equal by a test in package editor), the
// vibes the style's.
var (
	Roles        = []string{"hook", "build", "peak", "detail", "step", "reveal", "payoff", "broll", "ending"}
	ContentTypes = []string{"talking", "unboxing_product", "workout", "vlog", "tutorial", "general"}
	Vibes        = []string{"calm", "lively", "hype"}
	Paces        = []string{"steady", "building", "fast"}
	BeatNames    = []string{"hook", "middle", "payoff"}
)

// Bounds of the text of a card.
const (
	MaxNameChars    = 40
	MaxPitchChars   = 200
	MaxProseChars   = 320
	MaxTitleForm    = 40
	MaxKeywordChars = 40
	MaxHowItPlays   = 3
)

// Card is one format of the library.
type Card struct {
	ID      string `yaml:"id" json:"id"`
	Version int    `yaml:"version" json:"version"`
	Kind    string `yaml:"kind" json:"kind"`
	// Name is the format in plain words, for the creator ("Transformation reveal"); Plural is how a list of the creator's formats
	// names it ("transformation reveals").
	Name   string `yaml:"name" json:"name"`
	Plural string `yaml:"plural" json:"plural"`
	Pitch  string `yaml:"pitch" json:"pitch"`
	Fits   Fits   `yaml:"fits" json:"fits"`
	// NotWhen says when the format does not fit.
	NotWhen []string `yaml:"not_when" json:"not_when"`
	// Beats are the hook, the middle and the payoff, in order.
	Beats []Beat `yaml:"beats" json:"beats"`
	Text  Text   `yaml:"text" json:"text"`
	Music string `yaml:"music" json:"music"`
	// Length is the typical length in seconds.
	Length   Length `yaml:"length_s" json:"length_s"`
	Pace     string `yaml:"pace" json:"pace"`
	PaceHint string `yaml:"pace_hint" json:"pace_hint"`
	// Vibes says how well the format plays at each vibe (great, good or weak).
	Vibes map[string]string `yaml:"vibes" json:"vibes"`
	// HowItPlays are one to three anonymised descriptions of a video in this format.
	HowItPlays []string `yaml:"how_it_plays" json:"how_it_plays"`
	Detect     Detect   `yaml:"detect" json:"detect"`
	Check      Check    `yaml:"check" json:"check"`
}

// Fits is what footage the format needs: the content types it suits, the footage signals it needs (all of Needs, at least one of
// NeedsAny) and those that help, and the same in words.
type Fits struct {
	ContentTypes []string `yaml:"content_types" json:"content_types"`
	Needs        []string `yaml:"needs" json:"needs"`
	NeedsAny     []string `yaml:"needs_any" json:"needs_any,omitempty"`
	Helps        []string `yaml:"helps" json:"helps"`
	When         []string `yaml:"when" json:"when"`
}

// Beat is one part of the structure: its name (hook, middle, payoff), the plan roles that play it, when it comes against the song's
// drop, and what it shows.
type Beat struct {
	Name   string   `yaml:"name" json:"name"`
	Roles  []string `yaml:"roles" json:"roles"`
	Timing string   `yaml:"timing" json:"timing"`
	What   string   `yaml:"what" json:"what"`
}

// Text is the text the format asks for: the pattern and the forms of its title, with slots (<TOPIC>, <N>, ...).
type Text struct {
	Pattern    string   `yaml:"pattern" json:"pattern"`
	TitleForms []string `yaml:"title_forms" json:"title_forms"`
	Notes      string   `yaml:"notes" json:"notes"`
}

// Length is a range of seconds.
type Length struct {
	Min int `yaml:"min" json:"min"`
	Max int `yaml:"max" json:"max"`
}

// Detect is how the format is recognised on a finished video: rules over the cues (Cues), and the card's own keywords, which the
// cues keyword and keyword_hook look for in the on-screen text.
type Detect struct {
	Keywords []string `yaml:"keywords" json:"keywords,omitempty"`
	Rules    []Rule   `yaml:"rules" json:"rules,omitempty"`
}

// Rule holds when every cue of All holds and none of None does; its score is how sure the tag is (0.5 to 1).
type Rule struct {
	All   []string `yaml:"all" json:"all"`
	None  []string `yaml:"none" json:"none,omitempty"`
	Score float64  `yaml:"score" json:"score"`
}

// Check is what check_edit's format_followed holds an edit in this format to, besides the text pattern: where the payoff lands, a
// loop (the last shot from the first shot's clip) and the fewest shots.
type Check struct {
	Payoff   string `yaml:"payoff" json:"payoff,omitempty"`
	Loop     bool   `yaml:"loop" json:"loop,omitempty"`
	MinShots int    `yaml:"min_shots" json:"min_shots"`
}

// Beat returns the beat of that name.
func (c *Card) Beat(name string) (Beat, bool) {
	for _, b := range c.Beats {
		if b.Name == name {
			return b, true
		}
	}
	return Beat{}, false
}

// Library is the loaded, validated cards.
type Library struct {
	// Version names the content of the library: "formats@" and a hash of the card files, so a profile tagged with an older library is
	// tagged again.
	Version string
	cards   []Card
	byID    map[string]int
}

// Cards returns the cards, sorted by id.
func (l *Library) Cards() []Card { return slices.Clone(l.cards) }

// Card returns the card of an id.
func (l *Library) Card(id string) (Card, bool) {
	i, ok := l.byID[id]
	if !ok {
		return Card{}, false
	}
	return l.cards[i], true
}

// IDs returns the ids of the cards of a kind ("" for every kind), sorted.
func (l *Library) IDs(kind string) []string {
	var out []string
	for _, c := range l.cards {
		if kind == "" || c.Kind == kind {
			out = append(out, c.ID)
		}
	}
	return out
}

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)

// slotRe is a slot of a title form: a word in capitals between angle brackets.
var slotRe = regexp.MustCompile(`<[A-Z]{1,12}>`)

// Load reads every *.yaml in dir as a card and validates it. A file that is not a valid card fails the whole library: the library is
// part of the build, and a broken card is a bug.
func Load(dir fs.FS) (*Library, error) {
	names, err := fs.Glob(dir, "*.yaml")
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, errors.New("formats: no cards")
	}
	sort.Strings(names)
	h := sha256.New()
	lib := &Library{byID: map[string]int{}}
	for _, name := range names {
		raw, err := fs.ReadFile(dir, name)
		if err != nil {
			return nil, err
		}
		_, _ = h.Write([]byte(name))
		_, _ = h.Write(raw)
		c, err := Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("formats: %s: %w", name, err)
		}
		if want := strings.TrimSuffix(path.Base(name), ".yaml"); c.ID != want {
			return nil, fmt.Errorf("formats: %s: the id %q is not the file's name", name, c.ID)
		}
		if _, dup := lib.byID[c.ID]; dup {
			return nil, fmt.Errorf("formats: %s: the id %q is used twice", name, c.ID)
		}
		lib.byID[c.ID] = len(lib.cards)
		lib.cards = append(lib.cards, c)
	}
	lib.Version = "formats@" + hex.EncodeToString(h.Sum(nil))[:8]
	return lib, nil
}

// Parse reads one card strictly (an unknown field is an error) and validates it.
func Parse(raw []byte) (Card, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var c Card
	if err := dec.Decode(&c); err != nil {
		return Card{}, err
	}
	return c, c.Validate()
}

// Validate checks a card against the schema: every field present and inside its bounds, every name from its vocabulary.
func (c *Card) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	prose := func(field, s string, limit int) {
		switch n := utf8.RuneCountInString(strings.TrimSpace(s)); {
		case n == 0:
			bad("%s is empty", field)
		case n > limit:
			bad("%s is %d characters, at most %d", field, n, limit)
		}
	}
	in := func(field string, values, allowed []string, needOne bool) {
		if needOne && len(values) == 0 {
			bad("%s is empty", field)
		}
		seen := map[string]bool{}
		for _, v := range values {
			if !slices.Contains(allowed, v) {
				bad("%s: %q is not one of %s", field, v, strings.Join(allowed, ", "))
			}
			if seen[v] {
				bad("%s: %q is listed twice", field, v)
			}
			seen[v] = true
		}
	}
	if !idRe.MatchString(c.ID) {
		bad("id %q is not lowercase letters, digits and underscores (2 to 40)", c.ID)
	}
	if c.Version < 1 {
		bad("version must be 1 or more")
	}
	if c.Kind != KindFormat && c.Kind != KindDevice {
		bad("kind %q is not format or device", c.Kind)
	}
	prose("name", c.Name, MaxNameChars)
	prose("plural", c.Plural, MaxNameChars)
	prose("pitch", c.Pitch, MaxPitchChars)
	in("fits.content_types", c.Fits.ContentTypes, ContentTypes, true)
	in("fits.needs", c.Fits.Needs, Signals, false)
	in("fits.needs_any", c.Fits.NeedsAny, Signals, false)
	in("fits.helps", c.Fits.Helps, Signals, false)
	if len(c.Fits.When) == 0 {
		bad("fits.when is empty: say when it fits")
	}
	for i, w := range c.Fits.When {
		prose(fmt.Sprintf("fits.when[%d]", i), w, MaxProseChars)
	}
	if len(c.NotWhen) == 0 {
		bad("not_when is empty: say when it does not fit")
	}
	for i, w := range c.NotWhen {
		prose(fmt.Sprintf("not_when[%d]", i), w, MaxProseChars)
	}
	if len(c.Beats) != len(BeatNames) {
		bad("beats: %d, want the %s", len(c.Beats), strings.Join(BeatNames, ", "))
	}
	for i, b := range c.Beats {
		if i < len(BeatNames) && b.Name != BeatNames[i] {
			bad("beats[%d] is %q, want %q (hook, middle, payoff in order)", i, b.Name, BeatNames[i])
		}
		in(fmt.Sprintf("beats[%d].roles", i), b.Roles, Roles, true)
		prose(fmt.Sprintf("beats[%d].timing", i), b.Timing, MaxProseChars)
		prose(fmt.Sprintf("beats[%d].what", i), b.What, MaxProseChars)
	}
	if !slices.Contains(TextPatterns, c.Text.Pattern) {
		bad("text.pattern %q is not one of %s", c.Text.Pattern, strings.Join(TextPatterns, ", "))
	}
	for i, f := range c.Text.TitleForms {
		if err := checkTitleForm(f); err != nil {
			bad("text.title_forms[%d]: %v", i, err)
		}
	}
	prose("text.notes", c.Text.Notes, MaxProseChars)
	prose("music", c.Music, MaxProseChars)
	if c.Length.Min < 3 || c.Length.Max > 180 || c.Length.Min >= c.Length.Max {
		bad("length_s %d to %d: min 3 or more, under max, max 180 or less", c.Length.Min, c.Length.Max)
	}
	if !slices.Contains(Paces, c.Pace) {
		bad("pace %q is not one of %s", c.Pace, strings.Join(Paces, ", "))
	}
	prose("pace_hint", c.PaceHint, MaxProseChars)
	for _, v := range Vibes {
		switch c.Vibes[v] {
		case VibeGreat, VibeGood, VibeWeak:
		default:
			bad("vibes.%s is %q, want great, good or weak", v, c.Vibes[v])
		}
	}
	for k := range c.Vibes {
		if !slices.Contains(Vibes, k) {
			bad("vibes: %q is not a vibe", k)
		}
	}
	if n := len(c.HowItPlays); n < 1 || n > MaxHowItPlays {
		bad("how_it_plays has %d, want 1 to %d", n, MaxHowItPlays)
	}
	for i, h := range c.HowItPlays {
		prose(fmt.Sprintf("how_it_plays[%d]", i), h, MaxProseChars)
	}
	for i, k := range c.Detect.Keywords {
		if k != strings.ToLower(strings.TrimSpace(k)) || k == "" || utf8.RuneCountInString(k) > MaxKeywordChars {
			bad("detect.keywords[%d] %q: lowercase, trimmed, 1 to %d characters", i, k, MaxKeywordChars)
		}
	}
	for i, r := range c.Detect.Rules {
		in(fmt.Sprintf("detect.rules[%d].all", i), r.All, Cues, true)
		in(fmt.Sprintf("detect.rules[%d].none", i), r.None, Cues, false)
		if r.Score < 0.5 || r.Score > 1 {
			bad("detect.rules[%d].score %.2f is outside 0.5 to 1", i, r.Score)
		}
		for _, cue := range append(slices.Clone(r.All), r.None...) {
			if (cue == CueKeyword || cue == CueKeywordHook) && len(c.Detect.Keywords) == 0 {
				bad("detect.rules[%d] uses %s but the card has no keywords", i, cue)
			}
		}
	}
	if c.Kind == KindFormat && len(c.Detect.Rules) == 0 {
		bad("detect.rules is empty: a format must be recognisable on a creator's videos")
	}
	switch c.Check.Payoff {
	case "", PayoffDrop, PayoffEnd, PayoffAny:
	default:
		bad("check.payoff %q is not drop, end or any", c.Check.Payoff)
	}
	if c.Check.MinShots < 1 || c.Check.MinShots > 16 {
		bad("check.min_shots %d is outside 1 to 16", c.Check.MinShots)
	}
	return errors.Join(errs...)
}

// checkTitleForm validates a title form: words in capitals, digits, punctuation and slots, at most MaxTitleForm characters.
func checkTitleForm(f string) error {
	if f == "" || utf8.RuneCountInString(f) > MaxTitleForm {
		return fmt.Errorf("%q: 1 to %d characters", f, MaxTitleForm)
	}
	rest := slotRe.ReplaceAllString(f, "")
	if strings.ContainsAny(rest, "<>") {
		return fmt.Errorf("%q: a slot is a word in capitals between angle brackets, such as <TOPIC>", f)
	}
	if rest != strings.ToUpper(rest) {
		return fmt.Errorf("%q: the fixed words are in capitals (the case is the creator's)", f)
	}
	return nil
}

// TitleFormRegexp turns a title form into a pattern that a title matches without case: a slot <N> is a number, any other slot one or
// more words, the fixed words as they are, and the spaces between them any run of spaces.
func TitleFormRegexp(form string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`(?i)^\s*`)
	last := 0
	for _, loc := range slotRe.FindAllStringIndex(form, -1) {
		b.WriteString(literal(form[last:loc[0]]))
		if form[loc[0]:loc[1]] == "<N>" {
			b.WriteString(`\d{1,4}`)
		} else {
			b.WriteString(`\S.*?`)
		}
		last = loc[1]
	}
	b.WriteString(literal(form[last:]))
	b.WriteString(`\s*$`)
	return regexp.MustCompile(b.String())
}

func literal(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	out := strings.Join(parts, `\s+`)
	if len(parts) > 0 {
		if strings.HasPrefix(s, " ") {
			out = `\s+` + out
		}
		if strings.HasSuffix(s, " ") {
			out += `\s+`
		}
	} else if s != "" {
		out = `\s+`
	}
	return out
}

// MatchesTitleForm says whether a title fits one of the forms (a title over several lines is one line here).
func MatchesTitleForm(title string, forms []string) bool {
	t := strings.Join(strings.Fields(title), " ")
	for _, f := range forms {
		if TitleFormRegexp(f).MatchString(t) {
			return true
		}
	}
	return false
}

var defaultLib = sync.OnceValues(func() (*Library, error) { return Load(configfiles.Formats()) })

// Default is the library embedded in the binary (config/formats). It panics when a card is invalid: the tests load it, so a broken card
// never ships.
func Default() *Library {
	lib, err := defaultLib()
	if err != nil {
		panic(err)
	}
	return lib
}
