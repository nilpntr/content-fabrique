package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// A call's max_tokens is cut to what the run's budget can pay for (P7-01a): the budget stays a hard cap, and a call that cannot be made
// even at the minimum is refused with the run-budget error.

func opusRequest(maxTokens int) Request {
	r := simpleRequest()
	r.Model, r.MaxTokens = "claude-opus-5-5", maxTokens
	return r
}

// sentMaxTokens is the max_tokens of the n-th message request the fake API saw.
func sentMaxTokens(t *testing.T, rg *rig, n int) int {
	t.Helper()
	calls := rg.api.messageCalls()
	if n >= len(calls) {
		t.Fatalf("only %d message calls", len(calls))
	}
	v, ok := calls[n].json(t)["max_tokens"].(float64)
	if !ok {
		t.Fatalf("no max_tokens in %s", calls[n].Body)
	}
	return int(v)
}

// countAt makes the fake count_tokens answer n input tokens, as the real one would for the request the test sends (the usage the fake
// messages endpoint reports must not be more than that: the client reserves what the count says).
func (rg *rig) countAt(n int) {
	rg.api.countTokens = func(int, recordedCall) fakeReply {
		return fakeReply{Status: 200, Body: fmt.Sprintf(`{"input_tokens": %d}`, n)}
	}
}

// replyWithin answers like the real API: the answer never has more output tokens than max_tokens allowed.
func replyWithin(call recordedCall, input, output int64) fakeReply {
	var body struct {
		MaxTokens int64 `json:"max_tokens"`
	}
	_ = json.Unmarshal(call.Body, &body)
	return okReply("ok", apiUsage{Input: input, Output: min(output, body.MaxTokens)})
}

func TestFitOutputTokensIsExact(t *testing.T) {
	prices, err := LoadPriceTable(pricingFS, PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	marks := CacheMarks{Any: true}
	for _, room := range []int64{100_000, 250_000, 320_000, 480_000, 960_000} {
		for _, input := range []int64{2_500, 20_000, 60_000} {
			fit, err := prices.FitOutputTokens("claude-opus-5-5", input, 24_000, marks, room)
			if err != nil {
				t.Fatal(err)
			}
			if inputOnly, _ := prices.EstimateMax("claude-opus-5-5", input, 0, marks); inputOnly > room {
				if fit != 0 {
					t.Errorf("room %d input %d: fit %d although the input alone costs %d", room, input, fit, inputOnly)
				}
				continue
			}
			cost, _ := prices.EstimateMax("claude-opus-5-5", input, fit, marks)
			if cost > room {
				t.Errorf("room %d input %d: fit %d costs %d", room, input, fit, cost)
			}
			if fit < 24_000 {
				if over, _ := prices.EstimateMax("claude-opus-5-5", input, fit+1, marks); over <= room {
					t.Errorf("room %d input %d: fit %d is not the largest (fit+1 costs %d)", room, input, fit, over)
				}
			}
		}
	}
	// Not even the input fits.
	if fit, _ := prices.FitOutputTokens("claude-opus-5-5", 100_000, 24_000, marks, 1_000); fit != 0 {
		t.Errorf("fit = %d with no room for the input", fit)
	}
	if _, err := prices.FitOutputTokens("claude-nope", 1, 1, marks, 1); err == nil {
		t.Error("an unpriced model fits")
	}
}

// A $0.96 first edit on Opus: a full-size call (24,000 x $20/MTok = $0.48 plus the input) is reserved at first, then cut to what is left,
// and the sum of what the calls cost never passes the budget. The run is never refused for a call that fits.
func TestCallsShrinkToTheRunBudgetAndNeverPassIt(t *testing.T) {
	rg := newRig(t, nil)
	rg.countAt(2500)
	// Each call really costs about $0.11: 2,000 input tokens, 5,000 output tokens on Opus.
	rg.api.messages = func(_ int, call recordedCall) fakeReply { return replyWithin(call, 2000, 5000) }
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	budget := newSpyBudget(960_000)
	req := opusRequest(24_000)
	req.Run = RunInfo{Budget: budget}

	var shortened, full int
	for i := 0; ; i++ {
		resp, err := c.Messages(context.Background(), req)
		if err != nil {
			var ce *CapError
			if !errors.As(err, &ce) || ce.Kind != CapRun {
				t.Fatalf("call %d: %v", i, err)
			}
			// Refused only when not even the smallest answer fits: the room is under the input plus the minimum output.
			if room := budget.Remaining(); room > 60_000 {
				t.Fatalf("call %d refused with %d micros left", i, room)
			}
			break
		}
		sent := sentMaxTokens(t, rg, i)
		if resp.Shortened != (sent < 24_000) {
			t.Fatalf("call %d: Shortened = %v with max_tokens %d", i, resp.Shortened, sent)
		}
		if resp.Shortened {
			shortened++
			if sent < DefaultMinOutputTokens || sent%16 != 0 {
				t.Fatalf("call %d: max_tokens %d is below the minimum or not a multiple of 16", i, sent)
			}
		} else {
			full++
		}
		if i > 20 {
			t.Fatal("the budget never ran out")
		}
	}
	if full == 0 || shortened == 0 {
		t.Fatalf("%d full-size and %d shortened calls: want both", full, shortened)
	}
	// The cap held: the run's settled spend and the ledger are both within the budget.
	var ledger int64
	for _, r := range rg.ledger.snapshot() {
		ledger += r.ListCostMicros
	}
	if budget.Spent() > 960_000 || ledger > 960_000 || ledger != budget.Spent() {
		t.Fatalf("spent %d, ledger %d, budget 960,000", budget.Spent(), ledger)
	}
	for _, r := range budget.reserved {
		if r > 960_000 {
			t.Errorf("a reservation of %d is above the budget", r)
		}
	}
}

// A $0.32 revision: the first call is cut to fit (a full 24,000-token call is $0.48), and a plan, an edit and a check all fit.
func TestARevisionBudgetStillMakesCalls(t *testing.T) {
	rg := newRig(t, nil)
	rg.countAt(2500)
	rg.api.messages = func(_ int, call recordedCall) fakeReply { return replyWithin(call, 2000, 2500) } // $0.058 each
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	budget := newSpyBudget(320_000)
	req := opusRequest(12_000)
	req.Run = RunInfo{Budget: budget}
	for i := range 3 {
		if _, err := c.Messages(context.Background(), req); err != nil {
			t.Fatalf("call %d of a $0.32 revision: %v", i+1, err)
		}
	}
	if sent := sentMaxTokens(t, rg, 0); sent != 12_000 {
		t.Errorf("the first call was cut to %d although 12,000 fit", sent)
	}
	if budget.Spent() > 320_000 {
		t.Fatalf("spent %d", budget.Spent())
	}
}

// Below the minimum the call is refused with the run-budget error, and nothing is sent or left reserved.
func TestCallBelowTheMinimumIsRefusedAsTheRunBudget(t *testing.T) {
	rg := newRig(t, nil)
	rg.countAt(2500)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := opusRequest(24_000)
	// Room for the input (2,500 tokens at $4/MTok) and 1,500 output tokens at $20/MTok: under the default minimum of 2,048.
	const limit = 10_000 + 1_500*20 // 2,500 counted input tokens at $4/MTok, 1,500 of output
	budget := newSpyBudget(limit)
	req.Run = RunInfo{Budget: budget}
	_, err := c.Messages(context.Background(), req)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapRun || !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("err = %#v, want the run-budget error", err)
	}
	if len(rg.api.messageCalls()) != 0 || budget.Remaining() != limit {
		t.Errorf("a refused call was sent (%d) or left %d reserved", len(rg.api.messageCalls()), limit-budget.Remaining())
	}
}

// The minimum is configurable per request (the routing entry's min_output_tokens), and the default holds when it is not set.
func TestMinOutputTokensIsConfigurable(t *testing.T) {
	rg := newRig(t, nil)
	rg.countAt(2500)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	room := int64(10_000 + 1_000*20) // about 1,000 output tokens of room after the input: under the default minimum, over a minimum of 512
	req := opusRequest(24_000)

	req.Run = RunInfo{Budget: newSpyBudget(room)}
	if _, err := c.Messages(context.Background(), req); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("default minimum: %v", err)
	}
	req.MinOutputTokens = 512
	req.Run = RunInfo{Budget: newSpyBudget(room)}
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatalf("minimum 512: %v", err)
	}
	if sent := sentMaxTokens(t, rg, 0); sent < 512 || sent > 1_100 {
		t.Errorf("max_tokens sent = %d, want about 1,000", sent)
	}
}

// Manual extended thinking counts toward max_tokens and must stay under it: a shortened call cuts the thinking budget with it, the caller's
// setting is not touched, and a call whose room is too small for the least thinking the API accepts is refused.
func TestThinkingBudgetFollowsAShortenedCall(t *testing.T) {
	rg := newRig(t, nil)
	rg.countAt(2500)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	const inputOnly = 10_000 // 2,500 tokens of input at $4/MTok
	th := &Thinking{Mode: ThinkingEnabled, BudgetTokens: 16_000}
	req := opusRequest(24_000)
	req.Thinking = th
	req.Run = RunInfo{Budget: newSpyBudget(inputOnly + 6_000*20)} // about 6,000 output tokens of room
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	body := rg.api.messageCalls()[0].json(t)
	sentMax, _ := body["max_tokens"].(float64)
	sent := int(sentMax)
	think, _ := body["thinking"].(map[string]any)
	budget, _ := think["budget_tokens"].(float64)
	if sent >= 24_000 || int(budget) < 1024 || int(budget) >= sent {
		t.Fatalf("max_tokens %d, thinking budget %v: the thinking must stay inside the answer", sent, think)
	}
	if th.BudgetTokens != 16_000 {
		t.Errorf("the caller's thinking setting was changed to %d", th.BudgetTokens)
	}
	// Adaptive thinking has no budget to fit.
	req.Thinking = &Thinking{Mode: ThinkingAdaptive}
	req.Run = RunInfo{Budget: newSpyBudget(inputOnly + 6_000*20)}
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Too little room for the least manual thinking: refused as the run budget.
	req.Thinking = th
	req.MinOutputTokens = 300
	req.Run = RunInfo{Budget: newSpyBudget(inputOnly + 700*20)}
	if _, err := c.Messages(context.Background(), req); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("700 tokens of room for manual thinking: %v", err)
	}
}

// A request that fits is sent as it is.
func TestCallThatFitsIsNotShortened(t *testing.T) {
	rg := newRig(t, nil)
	rg.countAt(2500)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := opusRequest(24_000)
	req.Run = RunInfo{Budget: newSpyBudget(5_000_000)}
	resp, err := c.Messages(context.Background(), req)
	if err != nil || resp.Shortened || sentMaxTokens(t, rg, 0) != 24_000 {
		t.Fatalf("shortened=%v err=%v sent=%d", resp.Shortened, err, sentMaxTokens(t, rg, 0))
	}
	// A budget without a way to say what is left is never shortened: the call is refused, as before.
	req.Run = RunInfo{Budget: opaqueBudget{NewRunBudget(100_000)}}
	if _, err := c.Messages(context.Background(), req); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("opaque budget: %v", err)
	}
}

// opaqueBudget hides Remaining.
type opaqueBudget struct{ b *RunBudget }

func (o opaqueBudget) Reserve(m int64) (func(int64), error) { return o.b.Reserve(m) }
