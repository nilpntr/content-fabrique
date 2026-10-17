package quicklook

import (
	"encoding/json"
	"testing"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
)

// The acceptance criterion: a quick look costs at most $0.003. This works the cost out from the price table for the request
// the service really builds, counting every image at the pessimistic 1,600 tokens the llm layer uses when it cannot count
// (a 512 x 512 picture is about 350), and 120 output tokens, which is what a title, a sentence and eight tags take.
func TestACallCostsLessThanTheBudget(t *testing.T) {
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"dev-cheap", "dev-mid", "v1-start"} {
		router, err := llm.NewRouter(configfiles.Routing(), profile)
		if err != nil {
			t.Fatal(err)
		}
		route, err := router.Resolve("", Stage)
		if err != nil {
			t.Fatalf("%s: %v", profile, err)
		}
		if route.Model != "claude-haiku-5-5" || route.MaxTokens != 400 || route.Effort != "" {
			t.Errorf("%s: the quicklook stage routes to %+v, want claude-haiku-5-5 with max_tokens 400 in every profile", profile, route)
		}

		frames := make([][]byte, Frames)
		for i := range frames {
			frames[i] = fixtureFrame(i)
		}
		req := route.Apply(buildRequest(frames, []int{12, 37, 62, 87}))
		tokens, err := llm.EstimateTokens(req)
		if err != nil {
			t.Fatal(err)
		}
		if floor := int64(Frames) * 1600; tokens < floor {
			t.Fatalf("the estimate (%d tokens) must count each image as 1,600 tokens (%d)", tokens, floor)
		}
		// What a 512 px picture really takes, for the record: width x height / 750.
		if perImage := int64(512*512+749) / 750; perImage >= 1600 {
			t.Fatalf("a 512 px picture is %d tokens, the estimate no longer leans high", perImage)
		}

		cost, err := prices.Cost(route.Model, llm.Usage{InputTokens: tokens, OutputTokens: 120})
		if err != nil {
			t.Fatal(err)
		}
		worst, err := prices.EstimateMax(route.Model, tokens, int64(route.MaxTokens), llm.CacheMarks{})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d input tokens (counted pessimistically), 120 output: %d micro-dollars; worst case with max_tokens %d: %d", profile, tokens, cost, route.MaxTokens, worst)
		if cost > MaxMicros {
			t.Errorf("%s: a quick look costs %d micro-dollars, over the budget of %d", profile, cost, MaxMicros)
		}
		if worst > MaxMicros {
			t.Errorf("%s: the worst case %d micro-dollars does not fit the budget of %d, so the call would be refused", profile, worst, MaxMicros)
		}
		if cost*3 > MaxMicros {
			t.Errorf("%s: %d micro-dollars is not comfortably under the budget of %d", profile, cost, MaxMicros)
		}
	}
	if MaxMicros != 3000 {
		t.Errorf("MaxMicros = %d, the criterion is $0.003", MaxMicros)
	}
}

// Strict tool use takes a subset of JSON Schema: no length or count keywords. The limits live in the prompt and in Validate.
func TestTheToolSchemaIsStrictAndPlain(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(inputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Errorf("schema = %v", schema)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 3 {
		t.Errorf("required = %v, want title, description and tags", required)
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				switch k {
				case "minLength", "maxLength", "minItems", "maxItems", "minimum", "maximum", "pattern", "format", "enum", "$ref", "oneOf", "anyOf", "allOf":
					t.Errorf("strict tool use does not take %q", k)
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(schema)
	if tl := tool(); !tl.Strict || tl.Name != ToolName {
		t.Errorf("tool = %+v", tl)
	}
}
