package hooklab

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

// The hook lab's eval cases (suites/hook-lab.yaml): each is a small made-up project (the words of its clips, their dead time and
// moments), the variants to score or a proposal to make, and what the lab must answer. A case needs no database, no footage and no
// model, so the set runs anywhere, in replay mode included: `cfctl hooks eval`, and the package's tests.

// CaseSuite is a file of cases.
type CaseSuite struct {
	Name        string `yaml:"suite"`
	Description string `yaml:"description"`
	Cases       []Case `yaml:"cases"`
}

// Case is one eval case.
type Case struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
	// Language is the language of the words (ISO 639-1), for the filler rules; empty is "en".
	Language string `yaml:"language"`
	// NoTitle is Evidence.NoTitle for every variant.
	NoTitle bool `yaml:"no_title"`
	// Clips are the clips the variants open on, by the asset name the variants use.
	Clips []CaseClip `yaml:"clips"`
	// Variants are the variants to score. When there are none, Propose makes them from the clips and Proposal.
	Variants []CaseVariant `yaml:"variants"`
	Proposal *CaseProposal `yaml:"propose"`
	// Playbook is the research playbook the case runs with; nil is none (the default weights).
	Playbook *StaticPlaybook `yaml:"playbook"`
	Expect   Expect          `yaml:"expect"`
}

// CaseClip is a clip of a case.
type CaseClip struct {
	Asset      string `yaml:"asset"`
	DurationMs int64  `yaml:"duration_ms"`
	// Words are the clip's transcript, each word written [text, start_ms, end_ms] or [text, start_ms, end_ms, confidence].
	Words   []CaseWord `yaml:"words"`
	Moments []Moment   `yaml:"moments"`
	// Analysed says the clip has a picture analysis even when Dead, Lit and Sharpness are all empty.
	Analysed  bool     `yaml:"analysed"`
	Dead      []Span   `yaml:"dead"`
	Lit       *float64 `yaml:"lit"`
	Sharpness *float64 `yaml:"sharpness"`
}

// CaseWord is a transcribe.Word in the compact form of a case file.
type CaseWord transcribe.Word

// UnmarshalYAML reads [text, start_ms, end_ms] with an optional confidence.
func (w *CaseWord) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode || len(n.Content) < 3 || len(n.Content) > 4 {
		return fmt.Errorf("line %d: a word is [text, start_ms, end_ms] or [text, start_ms, end_ms, confidence]", n.Line)
	}
	if err := n.Content[0].Decode(&w.Text); err != nil {
		return err
	}
	if err := n.Content[1].Decode(&w.StartMs); err != nil {
		return err
	}
	if err := n.Content[2].Decode(&w.EndMs); err != nil {
		return err
	}
	if len(n.Content) == 4 {
		return n.Content[3].Decode(&w.Confidence)
	}
	return nil
}

// CaseVariant is a variant of a case with the one fact that is not derived from its clip: where its text ends up.
type CaseVariant struct {
	Variant `yaml:",inline"`
	// TextPlace is Evidence.Text (one of TextPlaces), empty for "not known".
	TextPlace string `yaml:"text_place"`
}

// CaseProposal asks the case to propose its variants.
type CaseProposal struct {
	Planned string `yaml:"planned"`
	ShotMs  int64  `yaml:"shot_ms"`
	Text    string `yaml:"text"`
	// TextPlace is where the text ends up on every proposed variant. Probe says the text is not written yet and TextPlace is where a
	// title would end up (Evidence.TitleProbe).
	TextPlace string `yaml:"text_place"`
	Probe     bool   `yaml:"probe"`
}

// Expect is what the lab must answer.
type Expect struct {
	// Pick is the id of the best variant; Order every id, best first. Either may be left out.
	Pick  string   `yaml:"pick"`
	Order []string `yaml:"order"`
	// Fails lists, per variant id, exactly the criteria that fail (an empty list: none may fail).
	Fails map[string][]string `yaml:"fails"`
	// NotApplicable lists, per variant id, criteria that must not apply.
	NotApplicable map[string][]string `yaml:"not_applicable"`
	// MinTotal and MaxTotal bound a variant's total.
	MinTotal map[string]float64 `yaml:"min_total"`
	MaxTotal map[string]float64 `yaml:"max_total"`
	// Count is how many variants come back (for a proposal); 0 does not check it. Kinds are their kinds in the order proposed.
	Count int      `yaml:"count"`
	Kinds []string `yaml:"kinds"`
	// WeightsFrom is Lab.WeightsFrom; empty does not check it.
	WeightsFrom string `yaml:"weights_from"`
}

var caseIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// maxCaseBytes bounds a case file.
const maxCaseBytes = 4 << 20

// LoadCases reads and validates a case file. Unknown keys are an error: a misspelt expectation must not pass silently.
func LoadCases(path string) (*CaseSuite, error) {
	f, err := os.Open(path) //nolint:gosec // a path the operator or a test names
	if err != nil {
		return nil, fmt.Errorf("hook lab cases: %w", err)
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil {
		return nil, fmt.Errorf("hook lab cases: %w", err)
	} else if info.Size() > maxCaseBytes {
		return nil, errors.New("hook lab cases: the file is larger than 4 MB")
	}
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var s CaseSuite
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("hook lab cases %s: %w", path, err)
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("hook lab cases %s: %w", path, err)
	}
	return &s, nil
}

func (s *CaseSuite) validate() error {
	if s.Name == "" || len(s.Cases) == 0 {
		return errors.New("a suite needs a name (suite:) and at least one case")
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		if !caseIDRE.MatchString(c.ID) || seen[c.ID] {
			return fmt.Errorf("case id %q is not a unique lowercase id", c.ID)
		}
		seen[c.ID] = true
		clips := map[string]bool{}
		for _, cl := range c.Clips {
			if cl.Asset == "" || clips[cl.Asset] {
				return fmt.Errorf("case %s: every clip needs an asset name of its own", c.ID)
			}
			clips[cl.Asset] = true
		}
		if len(c.Variants) == 0 && c.Proposal == nil {
			return fmt.Errorf("case %s: give variants, or propose", c.ID)
		}
		if len(c.Variants) > 0 && c.Proposal != nil {
			return fmt.Errorf("case %s: give variants or propose, not both", c.ID)
		}
		ids := map[string]bool{}
		for _, v := range c.Variants {
			if v.ID == "" || ids[v.ID] {
				return fmt.Errorf("case %s: every variant needs an id of its own", c.ID)
			}
			ids[v.ID] = true
			if !clips[v.AssetID] {
				return fmt.Errorf("case %s: variant %s opens on %q, which is not one of the case's clips", c.ID, v.ID, v.AssetID)
			}
			if v.TextPlace != "" && !slices.Contains(TextPlaces, v.TextPlace) {
				return fmt.Errorf("case %s: variant %s: text_place %q is not one of %s", c.ID, v.ID, v.TextPlace, strings.Join(TextPlaces, ", "))
			}
		}
		if p := c.Proposal; p != nil && p.TextPlace != "" && !slices.Contains(TextPlaces, p.TextPlace) {
			return fmt.Errorf("case %s: text_place %q is not one of %s", c.ID, p.TextPlace, strings.Join(TextPlaces, ", "))
		}
		for _, list := range []map[string][]string{c.Expect.Fails, c.Expect.NotApplicable} {
			for id, crit := range list {
				for _, k := range crit {
					if !slices.Contains(Criteria, k) {
						return fmt.Errorf("case %s: variant %s: %q is not a criterion", c.ID, id, k)
					}
				}
			}
		}
	}
	return nil
}

// CaseResult is the outcome of one case.
type CaseResult struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	// Passed is true when Problems is empty: the lab answered what the case expects.
	Passed   bool     `json:"passed"`
	Problems []string `json:"problems,omitempty"`
	Lab      Lab      `json:"lab"`
}

// RunCases runs every case of the suite.
func RunCases(s *CaseSuite) []CaseResult {
	out := make([]CaseResult, 0, len(s.Cases))
	for _, c := range s.Cases {
		out = append(out, RunCase(c))
	}
	return out
}

// RunCase runs one case and compares the lab's answer with what the case expects.
func RunCase(c Case) CaseResult {
	lang := c.Language
	if lang == "" {
		lang = "en"
	}
	speech := map[string]*Speech{}
	pictures := map[string]*Picture{}
	var clips []Clip
	for _, cl := range c.Clips {
		clip := Clip{AssetID: cl.Asset, DurationMs: cl.DurationMs, Moments: cl.Moments, Dead: cl.Dead}
		if len(cl.Words) > 0 {
			words := make([]transcribe.Word, len(cl.Words))
			for i, w := range cl.Words {
				words[i] = transcribe.Word(w)
			}
			speech[cl.Asset] = &Speech{Words: words, Structure: transcribe.Analyse(words, lang)}
			clip.Speech = speech[cl.Asset]
		}
		if cl.Analysed || len(cl.Dead) > 0 || cl.Lit != nil || cl.Sharpness != nil {
			pictures[cl.Asset] = &Picture{Dead: cl.Dead, Lit: cl.Lit, Sharpness: cl.Sharpness}
		}
		clips = append(clips, clip)
	}
	places := map[string]string{}
	var variants []Variant
	if p := c.Proposal; p != nil {
		variants = Propose(Input{Clips: clips, Planned: p.Planned, ShotMs: p.ShotMs, Text: p.Text})
		for _, v := range variants {
			places[v.ID] = p.TextPlace
		}
	}
	for _, v := range c.Variants {
		variants = append(variants, v.Variant)
		places[v.ID] = v.TextPlace
	}
	var pb Playbook
	if c.Playbook != nil {
		pb = *c.Playbook
	}
	lab := Run(variants, func(v Variant) Evidence {
		return Evidence{Speech: speech[v.AssetID], Picture: pictures[v.AssetID], Text: places[v.ID], NoTitle: c.NoTitle, TitleProbe: c.Proposal != nil && c.Proposal.Probe}
	}, Resolve(pb))
	res := CaseResult{ID: c.ID, Title: c.Title, Lab: lab}
	res.Problems = c.Expect.problems(lab, variants)
	res.Passed = len(res.Problems) == 0
	return res
}

func (e Expect) problems(lab Lab, proposed []Variant) []string {
	var out []string
	cards := map[string]Card{}
	var order []string
	for _, c := range lab.Ranked {
		cards[c.Variant.ID] = c
		order = append(order, c.Variant.ID)
	}
	if e.Pick != "" && lab.Pick != e.Pick {
		out = append(out, fmt.Sprintf("the pick is %q, want %q (ranked %s)", lab.Pick, e.Pick, strings.Join(order, ", ")))
	}
	if len(e.Order) > 0 && !slices.Equal(order, e.Order) {
		out = append(out, fmt.Sprintf("the order is %s, want %s", strings.Join(order, ", "), strings.Join(e.Order, ", ")))
	}
	if e.Count > 0 && len(lab.Ranked) != e.Count {
		out = append(out, fmt.Sprintf("%d variants, want %d", len(lab.Ranked), e.Count))
	}
	if len(e.Kinds) > 0 {
		var kinds []string
		for _, v := range proposed {
			kinds = append(kinds, v.Kind)
		}
		if !slices.Equal(kinds, e.Kinds) {
			out = append(out, fmt.Sprintf("the kinds are %s, want %s", strings.Join(kinds, ", "), strings.Join(e.Kinds, ", ")))
		}
	}
	if e.WeightsFrom != "" && lab.WeightsFrom != e.WeightsFrom {
		out = append(out, fmt.Sprintf("the weights come from %q, want %q", lab.WeightsFrom, e.WeightsFrom))
	}
	card := func(id string) (Card, bool) {
		c, ok := cards[id]
		if !ok {
			out = append(out, fmt.Sprintf("there is no variant %q", id))
		}
		return c, ok
	}
	for _, id := range sortedKeys(e.Fails) {
		c, ok := card(id)
		if !ok {
			continue
		}
		got, want := c.FailedIDs(), slices.Clone(e.Fails[id])
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			out = append(out, fmt.Sprintf("variant %s fails [%s], want [%s]", id, strings.Join(got, ", "), strings.Join(want, ", ")))
		}
	}
	for _, id := range sortedKeys(e.NotApplicable) {
		c, ok := card(id)
		if !ok {
			continue
		}
		for _, k := range e.NotApplicable[id] {
			if i := slices.IndexFunc(c.Results, func(r Result) bool { return r.ID == k }); i < 0 || c.Results[i].Status != StatusNA {
				out = append(out, fmt.Sprintf("variant %s: %s applies, want not applicable", id, k))
			}
		}
	}
	for _, id := range sortedKeys(e.MinTotal) {
		if c, ok := card(id); ok && c.Total < e.MinTotal[id] {
			out = append(out, fmt.Sprintf("variant %s totals %.3f, want at least %.3f", id, c.Total, e.MinTotal[id]))
		}
	}
	for _, id := range sortedKeys(e.MaxTotal) {
		if c, ok := card(id); ok && c.Total > e.MaxTotal[id] {
			out = append(out, fmt.Sprintf("variant %s totals %.3f, want at most %.3f", id, c.Total, e.MaxTotal[id]))
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
