package llm

import (
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// PriceFile is the price table the code loads from config/pricing. A price change is a new file and a new version:
// ledger rows name the version that priced them.
const PriceFile = "2026-10.yaml"

// UnpricedVersion is the price_table_version of a ledger row written under LLM_ALLOW_UNPRICED for a model with no
// price. Its cost is 0.
const UnpricedVersion = "unpriced"

var versionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,39}$`)

// microsPerMTok is a price in micro-dollars per million tokens, which is the same number as micro-dollars per token
// times a million: $4 per MTok is 4,000,000. Integers all the way, so a price table never rounds a ledger row.
type microsPerMTok = int64

// Prices is one set of prices, in micro-dollars per million tokens.
type Prices struct {
	Input        microsPerMTok
	CacheWrite5m microsPerMTok
	CacheWrite1h microsPerMTok
	CacheRead    microsPerMTok
	Output       microsPerMTok
}

type modelPrices struct {
	base Prices
	// long applies when the prompt is over longOver tokens (Haiku 5.5); nil means one price for any length.
	long     *Prices
	longOver int64
}

// PriceTable holds the list prices of the models it knows, by version. A model with a null price in the file is
// unpriced: Cost and Estimate fail closed for it.
type PriceTable struct {
	// Version names the file (the ledger's price_table_version).
	Version string
	// Source is the page the prices were read from, and Retrieved the day.
	Source    string
	Retrieved string

	models map[string]modelPrices // priced models only
	known  map[string]bool        // every model the file lists, priced or not
}

type priceFile struct {
	Version   string                `yaml:"version"`
	Source    string                `yaml:"source"`
	Retrieved yaml.Node             `yaml:"retrieved"`
	Models    map[string]priceModel `yaml:"models"`
}

type priceSet struct {
	Input        *float64 `yaml:"input"`
	CacheWrite5m *float64 `yaml:"cache_write_5m"`
	CacheWrite1h *float64 `yaml:"cache_write_1h"`
	CacheRead    *float64 `yaml:"cache_read"`
	Output       *float64 `yaml:"output"`
}

type priceModel struct {
	priceSet    `yaml:",inline"`
	LongContext *struct {
		OverPromptTokens int64 `yaml:"over_prompt_tokens"`
		priceSet         `yaml:",inline"`
	} `yaml:"long_context"`
}

// LoadPriceTable reads a price file (2026-10.yaml) from the pricing directory.
func LoadPriceTable(fsys fs.FS, name string) (*PriceTable, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("llm: read price table %s: %w", name, err)
	}
	var f priceFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(false)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("llm: parse price table %s: %w", name, err)
	}
	if !versionRE.MatchString(f.Version) {
		return nil, fmt.Errorf("llm: price table %s: version %q is missing or not a short plain string", name, f.Version)
	}
	if f.Version+".yaml" != name {
		return nil, fmt.Errorf("llm: price table %s declares version %q: the file name must be <version>.yaml", name, f.Version)
	}
	if len(f.Models) == 0 {
		return nil, fmt.Errorf("llm: price table %s lists no models", name)
	}
	t := &PriceTable{Version: f.Version, Source: f.Source, Retrieved: f.Retrieved.Value, models: map[string]modelPrices{}, known: map[string]bool{}}
	for id, m := range f.Models {
		t.known[id] = true
		base, ok, err := m.convert()
		if err != nil {
			return nil, fmt.Errorf("llm: price table %s: model %s: %w", name, id, err)
		}
		if !ok {
			continue // unpriced: left out, so Cost fails closed
		}
		mp := modelPrices{base: base}
		if lc := m.LongContext; lc != nil {
			long, ok, err := lc.convert()
			if err != nil {
				return nil, fmt.Errorf("llm: price table %s: model %s long_context: %w", name, id, err)
			}
			if lc.OverPromptTokens < 1 {
				return nil, fmt.Errorf("llm: price table %s: model %s long_context: over_prompt_tokens must be positive", name, id)
			}
			if !ok {
				continue // a partly priced model is unpriced as a whole
			}
			mp.long, mp.longOver = &long, lc.OverPromptTokens
		}
		t.models[id] = mp
	}
	return t, nil
}

// convert turns dollars per MTok into micro-dollars per MTok. ok is false when any of the five prices is null or
// missing (the TODO case: a price that could not be verified).
func (s priceSet) convert() (p Prices, ok bool, err error) {
	vals := []*float64{s.Input, s.CacheWrite5m, s.CacheWrite1h, s.CacheRead, s.Output}
	out := make([]int64, len(vals))
	for i, v := range vals {
		if v == nil {
			return Prices{}, false, nil
		}
		if *v < 0 || math.IsNaN(*v) || math.IsInf(*v, 0) {
			return Prices{}, false, fmt.Errorf("a price is negative or not a number")
		}
		out[i] = int64(math.Round(*v * 1e6))
	}
	return Prices{Input: out[0], CacheWrite5m: out[1], CacheWrite1h: out[2], CacheRead: out[3], Output: out[4]}, true, nil
}

// Known reports whether the file lists the model at all.
func (t *PriceTable) Known(model string) bool { return t.known[model] }

// Priced reports whether the model has a complete price.
func (t *PriceTable) Priced(model string) bool { _, ok := t.models[model]; return ok }

// pricesFor picks the set that applies to a prompt of promptTokens.
func (m modelPrices) pricesFor(promptTokens int64) Prices {
	if m.long != nil && promptTokens > m.longOver {
		return *m.long
	}
	return m.base
}

// Cost returns the list cost of a call in micro-dollars, rounded up to the next micro-dollar. It returns an
// *UnpricedError for a model with no complete price.
func (t *PriceTable) Cost(model string, u Usage) (int64, error) {
	m, ok := t.models[model]
	if !ok {
		return 0, &UnpricedError{Model: model, TableVersion: t.Version}
	}
	p := m.pricesFor(u.PromptTokens())
	w1h := u.CacheWrite1hTokens
	w5m := u.CacheWriteTokens - w1h
	if w5m < 0 { // a malformed usage: price everything at the dearer lifetime rather than under-count
		w5m, w1h = 0, u.CacheWriteTokens
	}
	return ceilDiv(u.InputTokens*p.Input+w5m*p.CacheWrite5m+w1h*p.CacheWrite1h+u.CacheReadTokens*p.CacheRead+u.OutputTokens*p.Output, 1_000_000), nil
}

// CacheMarks says which prompt-cache lifetimes a request asks for, for the worst-case estimate.
type CacheMarks struct{ Any, OneHour bool }

// EstimateMax is the worst-case cost of a request before it is sent: its counted input tokens, each priced as the
// dearest way they could be billed (a cache write of the requested lifetime when the request has cache breakpoints,
// plain input otherwise), plus maxTokens output tokens at the output price. A real call costs this much or less.
func (t *PriceTable) EstimateMax(model string, inputTokens int64, maxTokens int64, marks CacheMarks) (int64, error) {
	m, ok := t.models[model]
	if !ok {
		return 0, &UnpricedError{Model: model, TableVersion: t.Version}
	}
	p := m.pricesFor(inputTokens)
	rate := p.Input
	switch {
	case marks.OneHour:
		rate = max(rate, p.CacheWrite1h)
	case marks.Any:
		rate = max(rate, p.CacheWrite5m)
	}
	return ceilDiv(inputTokens*rate+maxTokens*p.Output, 1_000_000), nil
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }
