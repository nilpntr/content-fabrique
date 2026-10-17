// Package routing loads model routing profiles from apps/server/config/routing.
//
// It began as a minimal loader for `cfctl doctor` (P0-14): "which distinct models does this profile use" and "what
// is the Cloudflare name of this native model ID". P4-00a added Profile.Entry, which resolves one stage to its model,
// effort and max_tokens (internal/agents/llm.Router is the caller). P4-09 owns the rest: escalation, sub-agents and
// budgets.
//
// A profile is YAML in the shape of docs/03-architecture.md §7.5. Since the shape is still moving, the loader does
// not model it: Models collects every `model` value anywhere in the document, and Entry walks a dotted stage path
// ("editor", "research.lead") to one mapping.
package routing

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// CatalogFile is the file that maps native model IDs to Cloudflare names.
const CatalogFile = "models.yaml"

var profileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Catalog maps a native Anthropic model ID (claude-opus-5-5) to its entry.
type Catalog map[string]Model

// Model is one catalogue entry.
type Model struct {
	// Cloudflare is the provider/model name Cloudflare's REST endpoints use (anthropic/claude-opus-5.5).
	Cloudflare string `yaml:"cloudflare"`
}

// LoadCatalog reads models.yaml from the routing directory.
func LoadCatalog(fsys fs.FS) (Catalog, error) {
	data, err := fs.ReadFile(fsys, CatalogFile)
	if err != nil {
		return nil, fmt.Errorf("routing: read %s: %w", CatalogFile, err)
	}
	var doc struct {
		Models Catalog `yaml:"models"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("routing: parse %s: %w", CatalogFile, err)
	}
	if len(doc.Models) == 0 {
		return nil, fmt.Errorf("routing: %s lists no models", CatalogFile)
	}
	for id, m := range doc.Models {
		if m.Cloudflare == "" {
			return nil, fmt.Errorf("routing: %s: model %q has no cloudflare name", CatalogFile, id)
		}
	}
	return doc.Models, nil
}

// Profile is a loaded routing profile.
type Profile struct {
	Name   string
	models []string
	doc    *yaml.Node
}

// Models returns the distinct native model IDs the profile uses, sorted. A sub-agent marked `enabled: false` is
// not active, so its model is left out.
func (p Profile) Models() []string { return slices.Clone(p.models) }

// LoadProfile reads <name>.yaml from the routing directory.
func LoadProfile(fsys fs.FS, name string) (Profile, error) {
	if !profileNameRE.MatchString(name) {
		return Profile{}, fmt.Errorf("routing: invalid profile name")
	}
	data, err := fs.ReadFile(fsys, name+".yaml")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Profile{}, fmt.Errorf("routing: profile %q not found", name)
		}
		return Profile{}, fmt.Errorf("routing: read profile %q: %w", name, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Profile{}, fmt.Errorf("routing: parse profile %q: %w", name, err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return Profile{}, fmt.Errorf("routing: profile %q is not a YAML mapping", name)
	}
	doc := root.Content[0]
	if got := scalar(doc, "profile"); got != name {
		return Profile{}, fmt.Errorf("routing: file %s.yaml declares profile %q", name, got)
	}
	set := map[string]bool{}
	collectModels(doc, set)
	if len(set) == 0 {
		return Profile{}, fmt.Errorf("routing: profile %q uses no models", name)
	}
	models := make([]string, 0, len(set))
	for m := range set {
		models = append(models, m)
	}
	slices.Sort(models)
	return Profile{Name: name, models: models, doc: doc}, nil
}

// Entry is what one stage of a profile resolves to.
type Entry struct {
	// Model is the native Anthropic model ID (claude-opus-5-5).
	Model string
	// Effort is low, medium, high, xhigh or max; empty leaves the model's own default.
	Effort string
	// MaxTokens is the profile's max_tokens for the stage, or 0 when the profile does not say (the caller's default).
	MaxTokens int
	// Batch says the stage runs through the Batch API where it can (backfills).
	Batch bool
}

// Efforts are the values the Messages API accepts for output_config.effort.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Entry resolves a stage path, such as "editor" or "research.lead", to its model, effort and max_tokens.
func (p Profile) Entry(stage string) (Entry, error) {
	if p.doc == nil {
		return Entry{}, fmt.Errorf("routing: profile %q is not loaded", p.Name)
	}
	n := p.doc
	for _, part := range strings.Split(stage, ".") {
		var next *yaml.Node
		if part != "" && n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == part {
					next = n.Content[i+1]
					break
				}
			}
		}
		if next == nil {
			return Entry{}, fmt.Errorf("routing: profile %q has no stage %q", p.Name, stage)
		}
		n = next
	}
	if n.Kind != yaml.MappingNode || scalar(n, "model") == "" {
		return Entry{}, fmt.Errorf("routing: stage %q of profile %q names no model", stage, p.Name)
	}
	e := Entry{Model: scalar(n, "model"), Effort: scalar(n, "effort"), Batch: scalar(n, "batch") == "true"}
	if e.Effort != "" && !slices.Contains(Efforts, e.Effort) {
		return Entry{}, fmt.Errorf("routing: stage %q of profile %q: effort %q is not one of %s", stage, p.Name, e.Effort, strings.Join(Efforts, ", "))
	}
	if v := scalar(n, "max_tokens"); v != "" {
		mt, err := strconv.Atoi(v)
		if err != nil || mt < 1 {
			return Entry{}, fmt.Errorf("routing: stage %q of profile %q: max_tokens must be a positive integer", stage, p.Name)
		}
		e.MaxTokens = mt
	}
	return e, nil
}

// scalar returns the scalar value of key in a mapping node, or "".
func scalar(m *yaml.Node, key string) string {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key && m.Content[i+1].Kind == yaml.ScalarNode {
			return m.Content[i+1].Value
		}
	}
	return ""
}

// collectModels adds every `model` scalar below n to set, skipping blocks marked `enabled: false`.
func collectModels(n *yaml.Node, set map[string]bool) {
	switch n.Kind {
	case yaml.MappingNode:
		if scalar(n, "enabled") == "false" {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if key.Value == "model" && val.Kind == yaml.ScalarNode && val.Value != "" {
				set[val.Value] = true
				continue
			}
			collectModels(val, set)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			collectModels(c, set)
		}
	}
}
