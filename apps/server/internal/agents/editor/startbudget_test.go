package editor

import (
	"testing"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/llm/routing"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/spend"
)

// liveFirstPromptTokens is the prompt of an editor run's first call on the live test's project (2026-11-20): five 4K clips and a song, the
// system prompt, the tools and the run context. Its first call read 57,352 tokens from the cache and wrote 3,639, so about 61,000 in all.
const liveFirstPromptTokens = 61_000

// The live test of 2026-11-20 (section 4.3): a standard revision on Opus on the creator plan failed "budget" after 335 ms with $0.00 spent,
// every time. A run's first call has no cache of its own, so its worst case prices the whole prompt as a cache write: about 61,000 tokens
// at Opus's $5/MTok is $0.31 before any answer, and the revision's budget is $0.32. Every edit, on every profile and plan that offers it,
// must be able to start and go on: the first call (the prompt as a cache write and the smallest answer the client allows) and two more
// (the prompt read from the cache and the smallest answer) fit the plan's budget for the action.
func TestEveryEditCanStartOnItsPlanBudget(t *testing.T) {
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	stages := map[credits.Action]string{
		credits.EditFirst: "editor", credits.EditRevision: "revision", credits.EditFirstMax: "editor_max", credits.EditRevisionMax: "revision_max",
	}
	for _, profile := range []string{"v1-start", "dev-mid", "dev-cheap"} {
		p, err := routing.LoadProfile(configfiles.Routing(), profile)
		if err != nil {
			t.Fatal(err)
		}
		for plan, pp := range pl.Plans {
			for action, stage := range stages {
				budget := pp.COGSBudgetMicros[action]
				if budget == 0 || ((action == credits.EditFirstMax || action == credits.EditRevisionMax) && !pp.QualityMax) {
					continue // the plan does not offer it
				}
				e, err := p.Entry(stage)
				if err != nil {
					t.Fatalf("%s %s: %v", profile, stage, err)
				}
				minOut := int64(e.MinOutputTokens)
				if minOut == 0 {
					minOut = llm.DefaultMinOutputTokens
				}
				first, err := prices.EstimateMax(e.Model, liveFirstPromptTokens, minOut, llm.CacheMarks{Any: true})
				if err != nil {
					t.Fatal(err)
				}
				next, err := prices.EstimateMax(e.Model, liveFirstPromptTokens, minOut, llm.CacheMarks{Any: true, ReadTokens: liveFirstPromptTokens})
				if err != nil {
					t.Fatal(err)
				}
				if first+2*next > budget {
					t.Errorf("%s on %s (%s, %s): the first call's worst case is %s and two more are %s, over the budget of %s: the edit cannot start, or stops after one step",
						action, plan, profile, e.Model, spend.USD(first), spend.USD(2*next), spend.USD(budget))
				}
			}
		}
	}
}
