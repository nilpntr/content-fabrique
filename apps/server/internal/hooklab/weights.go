package hooklab

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

// Weights is how much each criterion counts in a variant's total, by criterion id.
type Weights map[string]float64

// defaultWeights are the weights without a playbook. What a viewer cannot get past counts double: a hook that lands late, and a
// text on the person's body or face (the founder's "text never on the body"). A dead or soft first frame and a line that starts
// mid-sentence count one and a half; a long text and a filler first word count once, because they weaken a hook rather than break it.
var defaultWeights = Weights{InFirst2s: 2, TextOffBody: 2, FirstFrame: 1.5, CleanStart: 1.5, Readable: 1, NoFillerStart: 1}

// DefaultWeights returns a copy of the default weights.
func DefaultWeights() Weights {
	out := make(Weights, len(defaultWeights))
	for k, v := range defaultWeights {
		out[k] = v
	}
	return out
}

// Of is the weight of a criterion: its own when it has a usable one, else the default.
func (w Weights) Of(id string) float64 {
	if v, ok := w[id]; ok && v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
		return v
	}
	return defaultWeights[id]
}

// The bounds of what a playbook may do to a weight. A finding is a hint about what works, never a rule (the research engine's
// section 6): it scales a criterion's default weight by its effect, held to MinScale..MaxScale, so no finding switches a criterion
// off or lets one drown the others.
const (
	MinScale = 0.5
	MaxScale = 2.0
)

// Confidence levels of a finding, as the research playbook grades them. A low finding is a hint for a person to read; it changes no
// weight.
const (
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

// DimensionHook is the dimension of the playbook's findings the rubric reads.
const DimensionHook = "hook"

// Finding is one "what works" finding of the research playbook as the rubric reads it.
type Finding struct {
	// ID is the finding's id in the playbook, kept for the record of which finding moved a weight.
	ID string `json:"id" yaml:"id"`
	// Dimension is the playbook's dimension; only DimensionHook findings (or ones that leave it out) are read.
	Dimension string `json:"dimension,omitempty" yaml:"dimension"`
	// Criterion is the rubric criterion the finding is about (one of Criteria).
	Criterion string `json:"criterion" yaml:"criterion"`
	// Effect is how much better posts that meet the criterion did, as a ratio to the creator's median (1.8 is 1.8 times). Under 1
	// means meeting it went with doing worse.
	Effect float64 `json:"effect" yaml:"effect"`
	// Confidence is low, medium or high.
	Confidence string `json:"confidence" yaml:"confidence"`
}

// Playbook is the part of the research playbook (docs/design/creator-research-engine.md) the hook lab reads: its version and its
// findings about hooks. The research engine will implement it over its findings; until then NoPlaybook stands in.
type Playbook interface {
	HookFindings() (version string, findings []Finding)
}

// NoPlaybook is the playbook that has no findings: the default weights apply.
type NoPlaybook struct{}

// HookFindings returns nothing.
func (NoPlaybook) HookFindings() (string, []Finding) { return "", nil }

// StaticPlaybook is a playbook held in memory or read from a file (LoadPlaybook).
type StaticPlaybook struct {
	Version  string    `json:"playbook_version" yaml:"playbook_version"`
	Findings []Finding `json:"findings" yaml:"findings"`
}

// HookFindings returns the playbook's version and findings.
func (p StaticPlaybook) HookFindings() (string, []Finding) { return p.Version, p.Findings }

// Applied is one finding that moved a weight.
type Applied struct {
	Finding   string  `json:"finding"`
	Criterion string  `json:"criterion"`
	Scale     float64 `json:"scale"`
}

// Resolved is the weights a lab runs with and where they come from.
type Resolved struct {
	Weights Weights `json:"weights"`
	// From is "defaults", or "playbook <version>" when at least one finding moved a weight.
	From string `json:"from"`
	// Applied lists the findings that moved a weight, in the order of Criteria.
	Applied []Applied `json:"applied,omitempty"`
}

// FromDefaults is Resolved.From without a playbook.
const FromDefaults = "defaults"

// Resolve works out the weights: the defaults, each scaled by the playbook's finding about its criterion. Only a medium or high
// finding of the hook dimension about a known criterion with a usable effect counts; of several about one criterion the most
// confident wins, and the later one of two equally confident. A nil playbook, or one without such findings, gives the defaults.
func Resolve(p Playbook) Resolved {
	res := Resolved{Weights: DefaultWeights(), From: FromDefaults}
	if p == nil {
		return res
	}
	version, findings := p.HookFindings()
	best := map[string]Finding{}
	for _, f := range findings {
		if f.Dimension != "" && f.Dimension != DimensionHook {
			continue
		}
		if !slices.Contains(Criteria, f.Criterion) || rank(f.Confidence) < rank(ConfidenceMedium) {
			continue
		}
		if f.Effect <= 0 || math.IsNaN(f.Effect) || math.IsInf(f.Effect, 0) {
			continue
		}
		if cur, ok := best[f.Criterion]; ok && rank(cur.Confidence) > rank(f.Confidence) {
			continue
		}
		best[f.Criterion] = f
	}
	for _, id := range Criteria {
		f, ok := best[id]
		if !ok {
			continue
		}
		scale := round3(math.Max(MinScale, math.Min(MaxScale, f.Effect)))
		res.Weights[id] = round3(defaultWeights[id] * scale)
		res.Applied = append(res.Applied, Applied{Finding: f.ID, Criterion: id, Scale: scale})
	}
	if len(res.Applied) > 0 {
		res.From = "playbook"
		if version != "" {
			res.From += " " + version
		}
	}
	return res
}

func rank(confidence string) int {
	switch confidence {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	case ConfidenceLow:
		return 1
	}
	return 0
}

// maxPlaybookBytes bounds a playbook file.
const maxPlaybookBytes = 1 << 20

// LoadPlaybook reads a playbook's findings file (YAML: playbook_version and findings, each with id, dimension, criterion, effect and
// confidence; other keys, such as the claim and the evidence ids, are ignored). It is the shape the research engine's findings.yaml is
// expected to have; Resolve ignores what it cannot use, so a file with other findings in it is fine.
func LoadPlaybook(path string) (StaticPlaybook, error) {
	info, err := os.Stat(path)
	if err != nil {
		return StaticPlaybook{}, fmt.Errorf("hook lab playbook: %w", err)
	}
	if info.Size() > maxPlaybookBytes {
		return StaticPlaybook{}, errors.New("hook lab playbook: the file is larger than 1 MB")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a path the operator names on the command line
	if err != nil {
		return StaticPlaybook{}, fmt.Errorf("hook lab playbook: %w", err)
	}
	var p StaticPlaybook
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return StaticPlaybook{}, fmt.Errorf("hook lab playbook %s: %w", path, err)
	}
	return p, nil
}
