package llm

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestRepoPriceTable(t *testing.T) {
	pt, err := LoadPriceTable(pricingFS, PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	if pt.Version != "2026-10b" || pt.Source == "" || pt.Retrieved != "2026-10-17" {
		t.Fatalf("table header = %q %q %q", pt.Version, pt.Source, pt.Retrieved)
	}
	for _, m := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
		if !pt.Priced(m) {
			t.Errorf("%s is not priced", m)
		}
	}
	if pt.Priced("claude-unknown-9") || pt.Known("claude-unknown-9") {
		t.Error("an unlisted model is priced")
	}
	// Every model the routing profiles use must be priced, or its calls would be refused.
	cat, err := LoadRouterModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range cat {
		if !pt.Priced(m) {
			t.Errorf("routing uses %s, which the price table does not price", m)
		}
	}
}

// LoadRouterModels lists every model in the routing catalogue (a test helper).
func LoadRouterModels() ([]string, error) {
	r, err := NewRouter(routingFS, "dev-cheap")
	if err != nil {
		return nil, err
	}
	var out []string
	for m := range r.catalog {
		out = append(out, m)
	}
	return out, nil
}

func TestCost(t *testing.T) {
	pt, err := LoadPriceTable(pricingFS, PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	// Opus 5.5: input $4, 5m write $5, 1h write $8, read $0.20, output $20 per MTok.
	u := Usage{InputTokens: 1000, CacheWriteTokens: 2000, CacheWrite1hTokens: 500, CacheReadTokens: 10000, OutputTokens: 500}
	// 1000*4 + 1500*5 + 500*8 + 10000*0.2 + 500*20 = 4000+7500+4000+2000+10000 = 27500 micro-dollars.
	if got, err := pt.Cost("claude-opus-5-5", u); err != nil || got != 27500 {
		t.Fatalf("opus cost = %d, %v; want 27500", got, err)
	}
	// Sonnet 5.5: $2, $2.50, $4, $0.10, $10. 1000*2+1500*2.5+500*4+10000*0.1+500*10 = 2000+3750+2000+1000+5000.
	if got, _ := pt.Cost("claude-sonnet-5-5", u); got != 13750 {
		t.Fatalf("sonnet cost = %d, want 13750", got)
	}
	// Haiku 5.5 short prompt: $0.10, $0.125, $0.20, $0.01, $0.50: 100+187.5+100+100+250 = 737.5, rounded up.
	if got, _ := pt.Cost("claude-haiku-5-5", u); got != 738 {
		t.Fatalf("haiku cost = %d, want 738", got)
	}
	// Rounding is up: one input token on Haiku is 0.1 micro-dollar, billed as 1.
	if got, _ := pt.Cost("claude-haiku-5-5", Usage{InputTokens: 1}); got != 1 {
		t.Fatalf("one token = %d, want 1", got)
	}
	if got, _ := pt.Cost("claude-haiku-5-5", Usage{}); got != 0 {
		t.Fatalf("no tokens = %d, want 0", got)
	}
}

func TestHaikuLongContextTier(t *testing.T) {
	pt, err := LoadPriceTable(pricingFS, PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	// A prompt of exactly 100,000 tokens (cache reads count) is the short tier; one more is the long tier.
	short := Usage{InputTokens: 50_000, CacheReadTokens: 50_000, OutputTokens: 1000}
	// 50000*0.10 + 50000*0.01 + 1000*0.50 = 5000+500+500 = 6000.
	if got, _ := pt.Cost("claude-haiku-5-5", short); got != 6000 {
		t.Fatalf("100k prompt = %d, want 6000", got)
	}
	long := Usage{InputTokens: 50_001, CacheReadTokens: 50_000, OutputTokens: 1000}
	// 50001*0.50 + 50000*0.05 + 1000*2.50 = 25000.5+2500+2500 = 30000.5 -> 30001.
	if got, _ := pt.Cost("claude-haiku-5-5", long); got != 30001 {
		t.Fatalf("100,001 prompt = %d, want 30001", got)
	}
	// Opus has no such tier: a 900k prompt is billed at the same rate.
	if got, _ := pt.Cost("claude-opus-5-5", Usage{InputTokens: 900_000}); got != 3_600_000 {
		t.Fatalf("opus 900k = %d, want 3600000", got)
	}
}

func TestEstimateMax(t *testing.T) {
	pt, err := LoadPriceTable(pricingFS, PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	// Opus, 100k input tokens plain, 16k output: 100000*4 + 16000*20 = 400000+320000 = 720000.
	if got, _ := pt.EstimateMax("claude-opus-5-5", 100_000, 16_000, CacheMarks{}); got != 720_000 {
		t.Fatalf("plain = %d", got)
	}
	// With a 5m breakpoint the input could be billed as a cache write at $5, with 1h at $8.
	if got, _ := pt.EstimateMax("claude-opus-5-5", 100_000, 16_000, CacheMarks{Any: true}); got != 820_000 {
		t.Fatalf("5m = %d", got)
	}
	if got, _ := pt.EstimateMax("claude-opus-5-5", 100_000, 16_000, CacheMarks{Any: true, OneHour: true}); got != 1_120_000 {
		t.Fatalf("1h = %d", got)
	}
	if _, err := pt.EstimateMax("nope", 1, 1, CacheMarks{}); !errors.Is(err, ErrUnpriced) {
		t.Fatalf("unknown model: %v", err)
	}
}

func TestUnpricedModelFailsClosed(t *testing.T) {
	fsys := fstest.MapFS{"2030-01.yaml": {Data: []byte(`
version: "2030-01"
models:
  full:    {input: 1, cache_write_5m: 1.25, cache_write_1h: 2, cache_read: 0.1, output: 5}
  nullout: {input: 1, cache_write_5m: 1.25, cache_write_1h: 2, cache_read: 0.1, output: null}  # TODO: unverified
  missing: {input: 1}
  partlong:
    {input: 1, cache_write_5m: 1.25, cache_write_1h: 2, cache_read: 0.1, output: 5, long_context: {over_prompt_tokens: 10, input: 2, output: null}}
`)}}
	pt, err := LoadPriceTable(fsys, "2030-01.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !pt.Priced("full") {
		t.Fatal("full is unpriced")
	}
	for _, m := range []string{"nullout", "missing", "partlong", "ghost"} {
		if pt.Priced(m) {
			t.Errorf("%s counts as priced", m)
		}
		if _, err := pt.Cost(m, Usage{InputTokens: 1}); !errors.Is(err, ErrUnpriced) {
			t.Errorf("%s: Cost err = %v, want ErrUnpriced", m, err)
		}
		if _, err := pt.EstimateMax(m, 1, 1, CacheMarks{}); !errors.Is(err, ErrUnpriced) {
			t.Errorf("%s: EstimateMax err = %v, want ErrUnpriced", m, err)
		}
	}
	if !pt.Known("nullout") || pt.Known("ghost") {
		t.Error("Known is wrong")
	}
}

func TestLoadPriceTableErrors(t *testing.T) {
	bad := map[string]string{
		"2030-01.yaml": "version: \"2030-02\"\nmodels: {a: {input: 1}}\n", // name and version disagree
		"nover.yaml":   "models: {a: {input: 1}}\n",
		"nomod.yaml":   "version: nomod\nmodels: {}\n",
		"neg.yaml":     "version: neg\nmodels: {a: {input: -1, cache_write_5m: 1, cache_write_1h: 1, cache_read: 1, output: 1}}\n",
		"junk.yaml":    "version: [\n",
	}
	fsys := fstest.MapFS{}
	for n, d := range bad {
		fsys[n] = &fstest.MapFile{Data: []byte(d)}
	}
	for n := range bad {
		if _, err := LoadPriceTable(fsys, n); err == nil {
			t.Errorf("%s loaded", n)
		}
	}
	if _, err := LoadPriceTable(fsys, "absent.yaml"); err == nil {
		t.Error("absent file loaded")
	}
}
