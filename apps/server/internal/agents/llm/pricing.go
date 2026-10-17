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
const PriceFile = "2026-10b.yaml"

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

	// The sections that are not model calls (zero means unpriced).
	workersAIPerMinuteMicros int64
	whisperLocal             WhisperLocal
	vcpuSecondMicros         int64
	storageGBMonthMicros     int64
	egressGBMicros           int64
	hasStorage               bool
}

// WhisperLocal is how local whisper.cpp is priced: compute for an estimated wall time on a number of vCPUs.
type WhisperLocal struct {
	VCPUs                     int
	WallSecondsPerAudioSecond float64
}

type priceFile struct {
	Version   string                `yaml:"version"`
	Source    string                `yaml:"source"`
	Retrieved yaml.Node             `yaml:"retrieved"`
	Models    map[string]priceModel `yaml:"models"`

	Transcription struct {
		WorkersAI struct {
			PerMinute *float64 `yaml:"per_minute"`
		} `yaml:"workers_ai"`
		WhisperCPPLocal struct {
			VCPUs                     int     `yaml:"vcpus"`
			WallSecondsPerAudioSecond float64 `yaml:"wall_seconds_per_audio_second"`
		} `yaml:"whisper_cpp_local"`
	} `yaml:"transcription"`
	Compute struct {
		VCPUSecond *float64 `yaml:"vcpu_second"`
	} `yaml:"compute"`
	Storage struct {
		GBMonth  *float64 `yaml:"gb_month"`
		EgressGB *float64 `yaml:"egress_gb"`
	} `yaml:"storage"`
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
	if t.workersAIPerMinuteMicros, err = dollarsToMicros(f.Transcription.WorkersAI.PerMinute); err != nil {
		return nil, fmt.Errorf("llm: price table %s: transcription.workers_ai.per_minute: %w", name, err)
	}
	if t.vcpuSecondMicros, err = dollarsToMicros(f.Compute.VCPUSecond); err != nil {
		return nil, fmt.Errorf("llm: price table %s: compute.vcpu_second: %w", name, err)
	}
	if t.storageGBMonthMicros, err = dollarsToMicros(f.Storage.GBMonth); err != nil {
		return nil, fmt.Errorf("llm: price table %s: storage.gb_month: %w", name, err)
	}
	if t.egressGBMicros, err = dollarsToMicros(f.Storage.EgressGB); err != nil {
		return nil, fmt.Errorf("llm: price table %s: storage.egress_gb: %w", name, err)
	}
	t.hasStorage = f.Storage.GBMonth != nil && f.Storage.EgressGB != nil
	if w := f.Transcription.WhisperCPPLocal; w.VCPUs > 0 && w.WallSecondsPerAudioSecond > 0 {
		t.whisperLocal = WhisperLocal{VCPUs: w.VCPUs, WallSecondsPerAudioSecond: w.WallSecondsPerAudioSecond}
	}
	return t, nil
}

// dollarsToMicros converts a dollar price to micro-dollars; nil (null or missing) is 0, unpriced. Prices finer than a micro-dollar
// per unit round to the nearest micro-dollar, so the unit must be chosen with that in mind (vCPU-seconds are 20).
func dollarsToMicros(v *float64) (int64, error) {
	if v == nil {
		return 0, nil
	}
	if *v < 0 || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return 0, fmt.Errorf("a price is negative or not a number")
	}
	return int64(math.Round(*v * 1e6)), nil
}

// WorkersAIMicrosPerMinute is the list price of a minute of hosted transcription in micro-dollars; 0 when the file leaves it unpriced.
func (t *PriceTable) WorkersAIMicrosPerMinute() int64 { return t.workersAIPerMinuteMicros }

// VCPUSecondMicros is the compute price of a vCPU-second in micro-dollars; 0 when unpriced.
func (t *PriceTable) VCPUSecondMicros() int64 { return t.vcpuSecondMicros }

// WhisperLocal says how local whisper.cpp is priced; the zero value when the file does not say.
func (t *PriceTable) WhisperLocal() WhisperLocal { return t.whisperLocal }

// StorageGBMonthMicros and EgressGBMicros are the R2 prices in micro-dollars, ok false when the file leaves either unpriced.
func (t *PriceTable) StorageGBMonthMicros() (storageGBMonth, egressGB int64, ok bool) {
	return t.storageGBMonthMicros, t.egressGBMicros, t.hasStorage
}

// ComputeCostMicros is the list cost of vcpuMs milliseconds of vCPU time, rounded up to the next micro-dollar. It returns an
// *UnpricedError when the file has no compute price.
func (t *PriceTable) ComputeCostMicros(vcpuMs int64) (int64, error) {
	if t.vcpuSecondMicros <= 0 {
		return 0, &UnpricedError{Model: "compute.vcpu_second", TableVersion: t.Version}
	}
	return ceilDiv(max(vcpuMs, 0)*t.vcpuSecondMicros, 1000), nil
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
//
// A model with a long-prompt tier (Haiku 5.5) is priced at the dearer tier whenever input plus max_tokens could cross
// the threshold. The count is an estimate of what will be billed, and the margin of max_tokens covers its error, so a
// request near the threshold is never under-estimated.
func (t *PriceTable) EstimateMax(model string, inputTokens int64, maxTokens int64, marks CacheMarks) (int64, error) {
	m, ok := t.models[model]
	if !ok {
		return 0, &UnpricedError{Model: model, TableVersion: t.Version}
	}
	p := m.pricesFor(inputTokens + maxTokens)
	rate := p.Input
	switch {
	case marks.OneHour:
		rate = max(rate, p.CacheWrite1h)
	case marks.Any:
		rate = max(rate, p.CacheWrite5m)
	}
	return ceilDiv(inputTokens*rate+maxTokens*p.Output, 1_000_000), nil
}

// FitOutputTokens returns the largest max_tokens, at most want, for which the worst case of a call with inputTokens of input (EstimateMax)
// is no more than room micro-dollars, and 0 when not even one token fits. EstimateMax only grows with max_tokens, so a binary search finds
// it exactly, including where a long-prompt tier starts to apply.
func (t *PriceTable) FitOutputTokens(model string, inputTokens, want int64, marks CacheMarks, room int64) (int64, error) {
	lo, hi := int64(0), want // the answer is in [lo, hi]; lo always fits (0 tokens of output is the input alone, checked below)
	if c, err := t.EstimateMax(model, inputTokens, 0, marks); err != nil {
		return 0, err
	} else if c > room {
		return 0, nil
	}
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		c, err := t.EstimateMax(model, inputTokens, mid, marks)
		if err != nil {
			return 0, err
		}
		if c <= room {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo, nil
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }
