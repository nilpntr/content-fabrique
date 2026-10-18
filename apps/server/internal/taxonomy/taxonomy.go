// Package taxonomy is the fixed vocabulary clips are labelled with (config/tags/v1.yaml): a few facets (the type of clip, the body
// part, the movement, the camera angle, the equipment), each with at most about ten values, instead of free-form tags. A clip's labels
// are a Set of facet values, stored as "facet:value" strings (assets.facets); the library filters on them and shows their labels.
//
// Free words become facet values through the synonym map (Map): the quick look's old free-form tags when they are backfilled, a model's
// answer outside the lists, and an exercise name ("seated cable row" is a row, with a cable, of the back). A word that is not in the map
// carries nothing, so near-duplicates ("back view", "rear view") and empty words ("gym", "dark background") cannot turn into labels.
//
// The vocabulary is versioned by its file: a value is never renamed in place, because stored labels and links (`?body=back`) name it.
package taxonomy

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"

	configfiles "github.com/nilpntr/content-fabrique/config"
)

// File is the vocabulary this build uses, in config/tags.
const File = "v1.yaml"

// Unsure is what a model answers for a facet it cannot tell from the pictures; it is never stored.
const Unsure = "unsure"

// SourceProbe marks a value that is set from the file itself (the frame rate), never from words or a model.
const SourceProbe = "probe"

// MaxValuesPerFacet bounds the vocabulary: a facet with more values is a tag list again.
const MaxValuesPerFacet = 12

// Ref names one value of one facet.
type Ref struct {
	Facet string
	Value string
}

// String is the stored form, "facet:value".
func (r Ref) String() string { return r.Facet + ":" + r.Value }

// ParseRef reads "facet:value". It does not check that the value exists (Taxonomy.Known does).
func ParseRef(s string) (Ref, bool) {
	f, v, ok := strings.Cut(s, ":")
	if !ok || !idPattern.MatchString(f) || !idPattern.MatchString(v) {
		return Ref{}, false
	}
	return Ref{Facet: f, Value: v}, true
}

// Value is one value of a facet.
type Value struct {
	ID    string
	Label string
	// Source is SourceProbe for a value read from the file, "" for one read from the pictures or words.
	Source string
	// Implies are the values a clip with this one has in the facets it has nothing in yet ("row" is a lift of the back).
	Implies []Ref
}

// Facet is one dimension of the labels.
type Facet struct {
	ID    string
	Label string
	// Max is how many values (other than the probe's) a clip may have in this facet.
	Max    int
	Values []Value
}

// Taxonomy is a loaded vocabulary. It is read-only after Load and safe to share.
type Taxonomy struct {
	Version int
	Facets  []Facet

	facet    map[string]int
	value    map[Ref]int // the position of the value in its facet
	synonyms map[string][]Ref
	ignore   map[string]bool
	maxWords int
}

var idPattern = regexp.MustCompile(`^[a-z][a-z_]{0,23}$`)

type fileFormat struct {
	Version int `yaml:"version"`
	Facets  []struct {
		ID     string `yaml:"id"`
		Label  string `yaml:"label"`
		Max    int    `yaml:"max"`
		Values []struct {
			ID      string   `yaml:"id"`
			Label   string   `yaml:"label"`
			Source  string   `yaml:"source"`
			Implies []string `yaml:"implies"`
		} `yaml:"values"`
	} `yaml:"facets"`
	Synonyms map[string][]string `yaml:"synonyms"`
	Ignore   []string            `yaml:"ignore"`
}

// Load reads and checks a vocabulary file.
func Load(fsys fs.FS, name string) (*Taxonomy, error) {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("taxonomy: %w", err)
	}
	var f fileFormat
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("taxonomy: %s: %w", name, err)
	}
	t := &Taxonomy{Version: f.Version, facet: map[string]int{}, value: map[Ref]int{}, synonyms: map[string][]Ref{}, ignore: map[string]bool{}}
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if f.Version < 1 {
		bad("version must be at least 1")
	}
	if len(f.Facets) == 0 {
		bad("no facets")
	}
	implies := map[Ref][]string{}
	for i, fc := range f.Facets {
		if !idPattern.MatchString(fc.ID) {
			bad("facet %q: the id must be lowercase letters and _", fc.ID)
		}
		if _, dup := t.facet[fc.ID]; dup {
			bad("facet %q is listed twice", fc.ID)
		}
		if strings.TrimSpace(fc.Label) == "" {
			bad("facet %q has no label", fc.ID)
		}
		if fc.Max < 1 {
			bad("facet %q: max must be at least 1", fc.ID)
		}
		if len(fc.Values) == 0 || len(fc.Values) > MaxValuesPerFacet {
			bad("facet %q has %d values; between 1 and %d", fc.ID, len(fc.Values), MaxValuesPerFacet)
		}
		t.facet[fc.ID] = i
		out := Facet{ID: fc.ID, Label: fc.Label, Max: fc.Max}
		for j, v := range fc.Values {
			r := Ref{Facet: fc.ID, Value: v.ID}
			if !idPattern.MatchString(v.ID) || v.ID == Unsure {
				bad("%s: the id must be lowercase letters and _, and not %q", r, Unsure)
			}
			if _, dup := t.value[r]; dup {
				bad("%s is listed twice", r)
			}
			if strings.TrimSpace(v.Label) == "" {
				bad("%s has no label", r)
			}
			if v.Source != "" && v.Source != SourceProbe {
				bad("%s: source must be %q or absent", r, SourceProbe)
			}
			t.value[r] = j
			implies[r] = v.Implies
			out.Values = append(out.Values, Value{ID: v.ID, Label: v.Label, Source: v.Source})
		}
		t.Facets = append(t.Facets, out)
	}
	// Implications and synonyms name values, so they are read once every value is known.
	refs := func(where string, list []string) []Ref {
		var out []Ref
		for _, s := range list {
			r, ok := ParseRef(s)
			switch {
			case !ok || !t.Known(r):
				bad("%s: %q is not a facet value", where, s)
			case t.probe(r):
				bad("%s: %s is read from the file, never from words", where, s)
			default:
				out = append(out, r)
			}
		}
		return out
	}
	for fi := range t.Facets {
		for vi := range t.Facets[fi].Values {
			r := Ref{Facet: t.Facets[fi].ID, Value: t.Facets[fi].Values[vi].ID}
			im := refs(r.String()+" implies", implies[r])
			for _, x := range im {
				if x.Facet == r.Facet {
					bad("%s implies %s, a value of its own facet", r, x)
				}
			}
			t.Facets[fi].Values[vi].Implies = im
		}
	}
	for phrase, list := range f.Synonyms {
		key := Normalize(phrase)
		if key == "" || key != phrase {
			bad("synonym %q must be written as it is matched: %q", phrase, key)
			continue
		}
		rs := refs("synonym "+phrase, list)
		if len(rs) == 0 {
			bad("synonym %q maps to nothing (list it under ignore)", phrase)
		}
		t.synonyms[key] = rs
	}
	for _, phrase := range f.Ignore {
		key := Normalize(phrase)
		if key == "" || key != phrase {
			bad("ignored %q must be written as it is matched: %q", phrase, key)
			continue
		}
		if _, ok := t.synonyms[key]; ok {
			bad("%q is both a synonym and ignored", phrase)
		}
		t.ignore[key] = true
	}
	// The ids and labels of the values are synonyms too, unless the file says otherwise (an explicit entry or an ignored word wins;
	// "back" is the body part, "back view" the angle). A probe value has none: words never set it.
	for _, fc := range t.Facets {
		for _, v := range fc.Values {
			if v.Source == SourceProbe {
				continue
			}
			for _, k := range []string{Normalize(strings.ReplaceAll(v.ID, "_", " ")), Normalize(v.Label)} {
				if _, ok := t.synonyms[k]; !ok && !t.ignore[k] && k != "" {
					t.synonyms[k] = []Ref{{Facet: fc.ID, Value: v.ID}}
				}
			}
		}
	}
	for k := range t.synonyms {
		t.maxWords = max(t.maxWords, len(strings.Fields(k)))
	}
	for k := range t.ignore {
		t.maxWords = max(t.maxWords, len(strings.Fields(k)))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("taxonomy: %s: %w", name, errors.Join(errs...))
	}
	return t, nil
}

var (
	defaultOnce sync.Once
	defaultTax  *Taxonomy
)

// Default is the vocabulary this build embeds (config/tags/v1.yaml). The file is checked by the tests, so a broken one never ships; it
// panics if it is broken all the same.
func Default() *Taxonomy {
	defaultOnce.Do(func() {
		t, err := Load(configfiles.Tags(), File)
		if err != nil {
			panic(err)
		}
		defaultTax = t
	})
	return defaultTax
}

// Facet returns the facet with this id.
func (t *Taxonomy) Facet(id string) (Facet, bool) {
	i, ok := t.facet[id]
	if !ok {
		return Facet{}, false
	}
	return t.Facets[i], true
}

// Known says whether the value exists.
func (t *Taxonomy) Known(r Ref) bool { _, ok := t.value[r]; return ok }

// Value returns the value r names.
func (t *Taxonomy) Value(r Ref) (Value, bool) {
	i, ok := t.value[r]
	if !ok {
		return Value{}, false
	}
	return t.Facets[t.facet[r.Facet]].Values[i], true
}

// Label is the creator's word for the value; "" for an unknown one.
func (t *Taxonomy) Label(r Ref) string {
	v, _ := t.Value(r)
	return v.Label
}

func (t *Taxonomy) probe(r Ref) bool {
	v, ok := t.Value(r)
	return ok && v.Source == SourceProbe
}

// FromProbe says whether the value is set from the file (the frame rate) and never by a model or a word.
func (t *Taxonomy) FromProbe(r Ref) bool { return t.probe(r) }

// ---- Sets ---------------------------------------------------------------------------------------------------------------

// Set is the labels of one clip: known values, at most Max per facet (plus the probe's), in the vocabulary's order.
type Set []Ref

// Strings is the stored form.
func (s Set) Strings() []string {
	out := make([]string, len(s))
	for i, r := range s {
		out[i] = r.String()
	}
	return out
}

// Has says whether the set holds r.
func (s Set) Has(r Ref) bool { return slices.Contains(s, r) }

// Of lists the values of one facet.
func (s Set) Of(facet string) []string {
	var out []string
	for _, r := range s {
		if r.Facet == facet {
			out = append(out, r.Value)
		}
	}
	return out
}

// builder collects values in order of preference and makes a Set of them.
type builder struct {
	t    *Taxonomy
	refs []Ref
}

// add takes r when it is known, not taken already, and its facet has room (the probe's values do not count against Max).
func (b *builder) add(r Ref, allowProbe bool) bool {
	if !b.t.Known(r) || slices.Contains(b.refs, r) {
		return false
	}
	probe := b.t.probe(r)
	if probe && !allowProbe {
		return false
	}
	if !probe {
		fc, _ := b.t.Facet(r.Facet)
		if b.count(r.Facet) >= fc.Max {
			return false
		}
	}
	b.refs = append(b.refs, r)
	return true
}

// count is how many values of the facet were taken, not counting the probe's.
func (b *builder) count(facet string) int {
	n := 0
	for _, r := range b.refs {
		if r.Facet == facet && !b.t.probe(r) {
			n++
		}
	}
	return n
}

// imply adds what the taken values imply, for the facets that have nothing yet (one pass: an implied value implies nothing further).
func (b *builder) imply() {
	taken := slices.Clone(b.refs)
	empty := map[string]bool{}
	for _, fc := range b.t.Facets {
		empty[fc.ID] = b.count(fc.ID) == 0
	}
	for _, r := range b.t.sorted(taken) {
		v, _ := b.t.Value(r)
		for _, x := range v.Implies {
			if empty[x.Facet] {
				b.add(x, false)
			}
		}
	}
}

func (b *builder) set() Set { return Set(b.t.sorted(b.refs)) }

// sorted puts refs in the vocabulary's order: by facet, then by value.
func (t *Taxonomy) sorted(refs []Ref) []Ref {
	out := slices.Clone(refs)
	slices.SortStableFunc(out, func(a, b Ref) int {
		if fa, fb := t.facet[a.Facet], t.facet[b.Facet]; fa != fb {
			return fa - fb
		}
		return t.value[a] - t.value[b]
	})
	return out
}

// Normalize reads stored labels: malformed and unknown ones are dropped, and so are repeats and values over a facet's Max (the first
// ones are kept). Probe values are kept: they were set from the file.
func (t *Taxonomy) Normalize(stored []string) Set {
	b := builder{t: t}
	for _, s := range stored {
		if r, ok := ParseRef(s); ok {
			b.add(r, true)
		}
	}
	return b.set()
}

// Build makes a clip's labels from values given outright (a model's answer, already in the vocabulary) and hints (free text such as
// an exercise name): a facet the given values leave empty takes what the hints map to, then the implications fill the rest. Probe
// values are never taken from either; add them with WithProbe.
func (t *Taxonomy) Build(given []Ref, hints ...string) Set {
	b := builder{t: t}
	for _, r := range given {
		b.add(r, false)
	}
	hinted := t.vote(hints)
	for _, fc := range t.Facets {
		if b.count(fc.ID) > 0 {
			continue
		}
		for _, r := range hinted {
			if r.Facet == fc.ID {
				b.add(r, false)
			}
		}
	}
	b.imply()
	return b.set()
}

// WithProbe adds a value that the file itself says (slow motion from the frame rate).
func (t *Taxonomy) WithProbe(s Set, r Ref) Set {
	if !t.probe(r) {
		return s
	}
	b := builder{t: t, refs: slices.Clone(s)}
	b.add(r, true)
	return b.set()
}

// Map turns free texts (tags, words) into labels: every text votes for the values its phrases map to, each facet keeps its most voted
// values up to its Max (the first one seen wins a tie), and the implications fill the facets that are still empty.
func (t *Taxonomy) Map(texts ...string) Set {
	b := builder{t: t}
	for _, r := range t.vote(texts) {
		b.add(r, false)
	}
	b.imply()
	return b.set()
}

// vote lists the values the texts map to, most votes first, ties in order of first appearance.
func (t *Taxonomy) vote(texts []string) []Ref {
	votes := map[Ref]int{}
	var order []Ref
	for _, text := range texts {
		for _, r := range t.Phrases(text) {
			if votes[r] == 0 {
				order = append(order, r)
			}
			votes[r]++
		}
	}
	slices.SortStableFunc(order, func(a, b Ref) int { return votes[b] - votes[a] })
	return order
}

// Phrases reads one text, the longest listed phrase first at each word, and returns the values it maps to (repeats removed, in order).
// Ignored and unknown words map to nothing.
func (t *Taxonomy) Phrases(text string) []Ref {
	words := strings.Fields(Normalize(text))
	var out []Ref
	for i := 0; i < len(words); {
		n, refs := t.match(words[i:])
		if n == 0 {
			i++
			continue
		}
		for _, r := range refs {
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
		i += n
	}
	return out
}

// Recognised says whether a text maps to a value or is an ignored word as a whole (every word of it is accounted for).
func (t *Taxonomy) Recognised(text string) bool {
	words := strings.Fields(Normalize(text))
	if len(words) == 0 {
		return false
	}
	for i := 0; i < len(words); {
		n, _ := t.match(words[i:])
		if n == 0 {
			return false
		}
		i += n
	}
	return true
}

// match finds the longest listed phrase at the start of words: how many words it takes and what it maps to (nil for an ignored one).
func (t *Taxonomy) match(words []string) (int, []Ref) {
	for n := min(t.maxWords, len(words)); n >= 1; n-- {
		phrase := strings.Join(words[:n], " ")
		for _, p := range append([]string{phrase}, singulars(phrase)...) {
			if refs, ok := t.synonyms[p]; ok {
				return n, refs
			}
			if t.ignore[p] {
				return n, nil
			}
		}
	}
	return 0, nil
}

// singulars are the phrase with a plural ending of its last word dropped ("cable rows" is "cable row", "pushes" is "push").
func singulars(phrase string) []string {
	if strings.HasSuffix(phrase, "ss") {
		return nil
	}
	var out []string
	for _, suffix := range []string{"s", "es"} {
		if strings.HasSuffix(phrase, suffix) && len(phrase) > len(suffix)+2 {
			out = append(out, strings.TrimSuffix(phrase, suffix))
		}
	}
	return out
}

// Normalize is how a text is compared: NFKC, lower case, and every run of characters that are not letters or digits one space.
func Normalize(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

// SearchText is the words a search finds the labels by: each value's label and id ("Back view back").
func (t *Taxonomy) SearchText(s Set) string {
	var parts []string
	for _, r := range s {
		if v, ok := t.Value(r); ok {
			parts = append(parts, v.Label, strings.ReplaceAll(v.ID, "_", " "))
		}
	}
	return strings.Join(parts, " ")
}

// Labels lists the labels of the set, in its order.
func (t *Taxonomy) Labels(s Set) []string {
	out := make([]string, 0, len(s))
	for _, r := range s {
		out = append(out, t.Label(r))
	}
	return out
}
