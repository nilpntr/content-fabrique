// Package evals runs an eval suite over the editor (docs/design/observability-v1.md section 7): a set of cases, each a project to edit
// (or a stored timeline version to judge again), scored by the deterministic checks, the distance from the creator's style, the
// recorded human feedback and an AI judge, with every result written to a JSONL file and to Phoenix as an experiment.
package evals

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Suite is a suite file (suites/<name>.yaml) merged with its local file (suites/<name>.local.yaml, gitignored), which holds the real
// ids: the checked-in file has placeholders, because a creator's footage, references and renders are never committed.
type Suite struct {
	Name        string `yaml:"suite"`
	Description string `yaml:"description"`
	// FeedbackFile is the recorded human feedback, relative to the suite file.
	FeedbackFile string   `yaml:"feedback"`
	Defaults     Defaults `yaml:"defaults"`
	// Rubric are rubric items of every case but the calibration ones (a case's own item of the same id wins): what every edit is
	// judged on besides the dimensions, such as a clear concept.
	Rubric []Expectation `yaml:"rubric"`
	Cases  []Case        `yaml:"cases"`

	// Feedback is FeedbackFile, loaded.
	Feedback []FeedbackItem `yaml:"-"`
	// Path is where the suite was read from, and LocalPath its local file ("" when there is none).
	Path, LocalPath string `yaml:"-"`
}

// Defaults are what a case leaves unset.
type Defaults struct {
	Workspace string  `yaml:"workspace"`
	Profile   string  `yaml:"profile"`
	Vibe      string  `yaml:"vibe"`
	BudgetUSD float64 `yaml:"budget_usd"`
	Format    string  `yaml:"format"`
	// ReferencesDir is a local directory of the creator's reference videos, for the judge's reference contact sheets.
	ReferencesDir string `yaml:"references_dir"`
}

// Case is one row of the suite.
type Case struct {
	ID string `yaml:"id"`
	// Title is a label for the report.
	Title     string   `yaml:"title"`
	Workspace string   `yaml:"workspace"`
	Footage   []string `yaml:"footage"`
	Song      string   `yaml:"song"`
	Brief     string   `yaml:"brief"`
	Vibe      string   `yaml:"vibe"`
	Profile   string   `yaml:"profile"`
	BudgetUSD float64  `yaml:"budget_usd"`
	// Stored is a timeline version that exists: judged as it is when the suite is not run live (no model call for the edit).
	Stored *Stored `yaml:"stored"`
	// Expectations are this case's rubric items, scored by the judge.
	Expectations []Expectation `yaml:"expectations"`
	// HeldOut keeps the case out of what the improvement loop is shown: it only guards against regressions.
	HeldOut       bool   `yaml:"held_out"`
	ReferencesDir string `yaml:"references_dir"`
	// Calibration marks a case whose human verdict is known: the judge's scores are compared with Expect, and the case does not count
	// in the suite's mean.
	Calibration *Calibration `yaml:"calibration"`
	// NewIdea makes the live edit a "Try another idea" run on the stored project (a new version in a format none of its versions
	// used) instead of a first edit of new footage; judged without --live, it is the stored version as it is.
	NewIdea bool `yaml:"new_idea"`
	// IdeaFrom is the version a live new idea rethinks: the formats of the versions up to it are avoided, so that every sample of a
	// --repeat avoids the same formats (not those of the samples before it). 0 is stored.version.
	IdeaFrom int `yaml:"idea_from"`
}

// IdeaBase is the version a live new idea rethinks (IdeaFrom, else the stored version), 0 without a stored version.
func (c Case) IdeaBase() int {
	switch {
	case c.IdeaFrom > 0:
		return c.IdeaFrom
	case c.Stored != nil:
		return c.Stored.Version
	}
	return 0
}

// Stored names a project's timeline version.
type Stored struct {
	Project string `yaml:"project"`
	Version int    `yaml:"version"`
}

// Expectation is a rubric item of a case.
type Expectation struct {
	ID     string  `yaml:"id"`
	Text   string  `yaml:"text"`
	Weight float64 `yaml:"weight"`
}

// Calibration is a recorded human verdict on a stored version: what the founder said, and the range each judge dimension should fall
// in when the judge agrees.
type Calibration struct {
	Note   string         `yaml:"note"`
	Expect map[string]Bnd `yaml:"expect"`
}

// Bnd is an inclusive range of scores, written [lo, hi].
type Bnd [2]int

// Placeholder is the mark of an id that the local file must fill.
const Placeholder = "PLACEHOLDER"

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// LoadSuite reads a suite file, merges its local file (local; "" means <name>.local.yaml next to it, when that exists) and loads the
// feedback file.
func LoadSuite(path, local string) (*Suite, error) {
	s, err := readSuite(path)
	if err != nil {
		return nil, err
	}
	s.Path = path
	if local == "" {
		guess := strings.TrimSuffix(path, filepath.Ext(path)) + ".local" + filepath.Ext(path)
		if _, err := os.Stat(guess); err == nil {
			local = guess
		}
	}
	if local != "" {
		over, err := readSuite(local)
		if err != nil {
			return nil, fmt.Errorf("local file: %w", err)
		}
		s.merge(over)
		s.LocalPath = local
	}
	if s.FeedbackFile != "" {
		fp := s.FeedbackFile
		if !filepath.IsAbs(fp) {
			fp = filepath.Join(filepath.Dir(path), fp)
		}
		if s.Feedback, err = LoadFeedback(fp); err != nil {
			return nil, err
		}
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func readSuite(path string) (*Suite, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a suite file the developer names
	if err != nil {
		return nil, err
	}
	var s Suite
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil && !errors.Is(err, io.EOF) { // an empty file is an empty suite
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// merge lays the local file over the suite: its defaults and, case by case (by id), every field it sets. A case only in the local file
// is added.
func (s *Suite) merge(o *Suite) {
	d := &s.Defaults
	str := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	str(&d.Workspace, o.Defaults.Workspace)
	str(&d.Profile, o.Defaults.Profile)
	str(&d.Vibe, o.Defaults.Vibe)
	str(&d.Format, o.Defaults.Format)
	str(&d.ReferencesDir, o.Defaults.ReferencesDir)
	if o.Defaults.BudgetUSD > 0 {
		d.BudgetUSD = o.Defaults.BudgetUSD
	}
	if o.FeedbackFile != "" {
		s.FeedbackFile = o.FeedbackFile
	}
	if len(o.Rubric) > 0 {
		s.Rubric = o.Rubric
	}
	for _, oc := range o.Cases {
		i := s.caseIndex(oc.ID)
		if i < 0 {
			s.Cases = append(s.Cases, oc)
			continue
		}
		c := &s.Cases[i]
		str(&c.Title, oc.Title)
		str(&c.Workspace, oc.Workspace)
		str(&c.Song, oc.Song)
		str(&c.Brief, oc.Brief)
		str(&c.Vibe, oc.Vibe)
		str(&c.Profile, oc.Profile)
		str(&c.ReferencesDir, oc.ReferencesDir)
		if len(oc.Footage) > 0 {
			c.Footage = oc.Footage
		}
		if oc.BudgetUSD > 0 {
			c.BudgetUSD = oc.BudgetUSD
		}
		if oc.Stored != nil {
			c.Stored = oc.Stored
		}
		if len(oc.Expectations) > 0 {
			c.Expectations = oc.Expectations
		}
		if oc.HeldOut {
			c.HeldOut = true
		}
		if oc.Calibration != nil {
			c.Calibration = oc.Calibration
		}
		if oc.NewIdea {
			c.NewIdea = true
		}
		if oc.IdeaFrom > 0 {
			c.IdeaFrom = oc.IdeaFrom
		}
	}
}

func (s *Suite) caseIndex(id string) int {
	for i, c := range s.Cases {
		if c.ID == id {
			return i
		}
	}
	return -1
}

func (s *Suite) validate() error {
	var errs []error
	if !idRE.MatchString(s.Name) {
		errs = append(errs, fmt.Errorf("suite: the name %q must be lowercase letters, digits, - and _", s.Name))
	}
	if len(s.Cases) == 0 {
		errs = append(errs, errors.New("suite: no cases"))
	}
	for _, e := range s.Rubric {
		if !idRE.MatchString(e.ID) || strings.TrimSpace(e.Text) == "" || e.Weight < 0 {
			errs = append(errs, fmt.Errorf("suite rubric: item %q needs an id, a text and a weight of 0 or more", e.ID))
		}
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		if !idRE.MatchString(c.ID) || seen[c.ID] {
			errs = append(errs, fmt.Errorf("case %q: the id must be unique, lowercase letters, digits, - and _", c.ID))
		}
		seen[c.ID] = true
		for _, e := range c.Expectations {
			if !idRE.MatchString(e.ID) || strings.TrimSpace(e.Text) == "" || e.Weight < 0 {
				errs = append(errs, fmt.Errorf("case %s: expectation %q needs an id, a text and a weight of 0 or more", c.ID, e.ID))
			}
		}
		if c.Calibration != nil {
			for dim, b := range c.Calibration.Expect {
				if !knownDimension(dim) || b[0] < 1 || b[1] > 5 || b[0] > b[1] {
					errs = append(errs, fmt.Errorf("case %s: calibration %s %v is not a judge dimension with a range in 1..5", c.ID, dim, b))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// Resolved is a case with the defaults applied.
func (s *Suite) Resolved(c Case) Case {
	or := func(v, d string) string {
		if v != "" {
			return v
		}
		return d
	}
	c.Workspace = or(c.Workspace, s.Defaults.Workspace)
	c.Profile = or(c.Profile, s.Defaults.Profile)
	c.Vibe = or(c.Vibe, s.Defaults.Vibe)
	c.ReferencesDir = or(c.ReferencesDir, s.Defaults.ReferencesDir)
	if c.BudgetUSD <= 0 {
		c.BudgetUSD = s.Defaults.BudgetUSD
	}
	// The suite's rubric, under the case's own items (an item of the same id is the case's); a calibration case keeps its own.
	if c.Calibration == nil && len(s.Rubric) > 0 {
		items := slices.Clone(c.Expectations)
		for _, e := range s.Rubric {
			if !slices.ContainsFunc(items, func(x Expectation) bool { return x.ID == e.ID }) {
				items = append(items, e)
			}
		}
		c.Expectations = items
	}
	return c
}

// Missing lists what a case still has as a placeholder (or lacks) for the way it would run: live needs a workspace and footage, a
// stored run needs a workspace and the version.
func (c Case) Missing(live bool) []string {
	var out []string
	ph := func(name, v string) {
		if v == "" || strings.Contains(v, Placeholder) {
			out = append(out, name)
		}
	}
	ph("workspace", c.Workspace)
	if live && c.NewIdea { // a new idea is made on the stored project
		if c.Stored == nil {
			return append(out, "stored")
		}
		ph("stored.project", c.Stored.Project)
		return out
	}
	if live {
		if len(c.Footage) == 0 {
			out = append(out, "footage")
		}
		for _, f := range c.Footage {
			ph("footage", f)
		}
		if c.Song != "" {
			ph("song", c.Song)
		}
		return out
	}
	if c.Stored == nil {
		return append(out, "stored")
	}
	ph("stored.project", c.Stored.Project)
	if c.Stored.Version < 1 {
		out = append(out, "stored.version")
	}
	return out
}
