package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The reservation of a call prices the prefix that the previous call of the run cached at the cache-read rate (cachememo.go), so that a run
// with a long conversation and money left is not refused. These tests pin what that must never become: an under-estimate while the cache
// entry is alive, or a claim on a prefix that is not the request's.

// agentRequest is a request shaped like an editor turn: a cached system prompt, a conversation of turns that grows by one assistant and one
// user message at a time, a breakpoint on the newest message. turns is the number of user messages; edit changes the first.
func agentRequest(run uuid.UUID, turns int, edit string) Request {
	cached := func(b Block) Block { b.Cache = &CacheControl{}; return b }
	msgs := []Message{{Role: RoleUser, Content: []Block{TextBlock("the brief" + edit)}}}
	for i := 1; i < turns; i++ {
		msgs = append(msgs,
			Message{Role: RoleAssistant, Content: []Block{TextBlock("thinking about step " + string(rune('a'+i)))}},
			Message{Role: RoleUser, Content: []Block{TextBlock("tool result " + string(rune('a'+i)))}})
	}
	last := &msgs[len(msgs)-1]
	last.Content = []Block{cached(last.Content[0])}
	return Request{
		Model: "claude-opus-5-5", MaxTokens: 24_000,
		System:   []Block{cached(TextBlock("You are the editor."))},
		Messages: msgs,
		Run:      RunInfo{ID: run},
	}
}

// The shape of a live run that ended "out of budget" with $0.39 of $0.96 left (it was refused): about 80,000 tokens of input, 86 % of them
// cached, Opus 5.5 with 24,000 tokens asked for. The worst case with every input token priced as a cache write ($5/MTok) is $0.40 for the
// input alone, so no output room was left; with the prefix that the run's previous call cached priced as a read, the call is allowed, with
// max_tokens fitted to the money, and what it cost is within what was set aside.
func TestACachedPrefixLetsARunWithMoneyLeftKeepWorking(t *testing.T) {
	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	run := uuid.New()

	// Call 1: the run's earlier work wrote 60,000 tokens to the cache (on a generous budget; the memo follows the run, not the budget).
	rg.countAt(60_000)
	rg.api.messages = func(_ int, _ recordedCall) fakeReply {
		return okReply("ok", apiUsage{Input: 100, CacheWrite5m: 60_000, Output: 500})
	}
	first := agentRequest(run, 1, "")
	first.Run.Budget = newSpyBudget(5_000_000)
	if _, err := c.Messages(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	// Call 2: the conversation grew by a turn (two blocks), 80,000 tokens in all, and $0.39 is left.
	rg.countAt(80_000)
	rg.api.messages = func(_ int, call recordedCall) fakeReply {
		// 86 % of 80,000 read from the cache, the rest written; a thousand tokens of answer.
		return okReply("ok", apiUsage{Input: 200, CacheRead: 68_800, CacheWrite5m: 11_000, Output: min(1_000, maxTokensOfCall(call))})
	}
	budget := newSpyBudget(390_000)
	second := agentRequest(run, 2, "")
	second.Run.Budget = budget
	resp, err := c.Messages(context.Background(), second)
	if err != nil {
		t.Fatalf("a call with $0.39 left and a cached prefix was refused: %v", err)
	}
	sent := sentMaxTokens(t, rg, 1)
	// 60,000 tokens at the read rate ($12,000) and 20,000 at the write rate ($100,000) leave $278,000 of the $390,000 for output: 13,900 tokens.
	if !resp.Shortened || sent < 13_000 || sent > 14_000 || sent%16 != 0 || sent < DefaultMinOutputTokens {
		t.Fatalf("max_tokens sent = %d (shortened %v), want about 13,900 in whole sixteens", sent, resp.Shortened)
	}
	if len(budget.reserved) != 1 || len(budget.settled) != 1 {
		t.Fatalf("reserved %v, settled %v", budget.reserved, budget.settled)
	}
	if budget.settled[0] > budget.reserved[0] || budget.reserved[0] > 390_000 {
		t.Fatalf("the call cost %d against a reservation of %d (budget 390,000): the reservation must be an upper bound", budget.settled[0], budget.reserved[0])
	}

	// The same call without the memory (another run) is what used to happen: the worst case with all 80,000 tokens at $5/MTok is $0.40
	// for the input alone, over the $0.39 left, so the run was refused.
	other := agentRequest(uuid.New(), 2, "")
	other.Run.Budget = newSpyBudget(390_000)
	var ce *CapError
	if _, err := c.Messages(context.Background(), other); !errors.As(err, &ce) || ce.Kind != CapRun {
		t.Fatalf("a run with no cached prefix of its own: err = %v, want the run-budget error", err)
	}
}

// The memo claims a prefix only when it is the request's: the same run, a fresh entry, nothing before the new part changed, not too much
// added. Anything else is priced at the full worst case (here: refused, as in the test above).
func TestACachedPrefixIsClaimedOnlyWhenItIsTheRequests(t *testing.T) {
	for name, tc := range map[string]struct {
		advance time.Duration
		next    func(run uuid.UUID) Request
	}{
		"the entry has expired":     {advance: cacheMemoFresh, next: func(run uuid.UUID) Request { return agentRequest(run, 2, "") }},
		"the first message changed": {next: func(run uuid.UUID) Request { return agentRequest(run, 2, " (edited)") }},
		"the system changed": {next: func(run uuid.UUID) Request {
			r := agentRequest(run, 2, "")
			r.System = []Block{{Type: BlockText, Text: "You are somebody else.", Cache: &CacheControl{}}}
			return r
		}},
		"the effort changed": {next: func(run uuid.UUID) Request {
			r := agentRequest(run, 2, "")
			r.Effort = "max"
			return r
		}},
		"a message was removed": {next: func(run uuid.UUID) Request {
			r := agentRequest(run, 3, "")
			r.Messages = r.Messages[2:] // the conversation lost its start
			return r
		}},
		"too many blocks were added": {next: func(run uuid.UUID) Request { return agentRequest(run, 12, "") }},
	} {
		t.Run(name, func(t *testing.T) {
			rg := newRig(t, nil)
			c, _ := rg.client(t, newWorkspaceID(), "editor")
			run := uuid.New()
			rg.countAt(60_000)
			rg.api.messages = func(_ int, _ recordedCall) fakeReply {
				return okReply("ok", apiUsage{Input: 100, CacheWrite5m: 60_000, Output: 500})
			}
			first := agentRequest(run, 1, "")
			first.Run.Budget = newSpyBudget(5_000_000)
			if _, err := c.Messages(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			*rg.now = rg.now.Add(tc.advance)
			rg.countAt(80_000)
			next := tc.next(run)
			next.Run.Budget = newSpyBudget(390_000)
			var ce *CapError
			if _, err := c.Messages(context.Background(), next); !errors.As(err, &ce) || ce.Kind != CapRun {
				t.Fatalf("err = %v, want the run-budget error (no cached prefix may be claimed)", err)
			}
		})
	}
}

// While the cache entry is alive the estimate is an upper bound for every way the call can really be billed: whatever the call read from the
// cache beyond the claimed prefix, wrote or paid as plain input, and however many tokens it answered (up to max_tokens).
func TestCachedEstimateStaysAnUpperBound(t *testing.T) {
	prices, err := LoadPriceTable(pricingFS, PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(7, 11))
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
		for i := range 4000 {
			input := int64(rng.IntN(300_000) + 1)
			claimed := int64(rng.IntN(int(input) + 1))
			maxTokens := int64(rng.IntN(64_000) + 1)
			marks := CacheMarks{Any: true, OneHour: rng.IntN(4) == 0, ReadTokens: claimed}
			est, err := prices.EstimateMax(model, input, maxTokens, marks)
			if err != nil {
				t.Fatal(err)
			}
			// A real billing: at least the claimed prefix is read; the rest of the input is split between plain input, cache writes of
			// either lifetime (a 1-hour write only when the request asked for the 1-hour lifetime) and more reads.
			read := claimed + int64(rng.IntN(int(input-claimed)+1))
			rest := input - read
			plain := int64(rng.IntN(int(rest) + 1))
			write := rest - plain
			var w1h int64
			if marks.OneHour {
				w1h = int64(rng.IntN(int(write) + 1))
			}
			u := Usage{InputTokens: plain, CacheWriteTokens: write, CacheWrite1hTokens: w1h, CacheReadTokens: read, OutputTokens: int64(rng.IntN(int(maxTokens) + 1))}
			cost, err := prices.Cost(model, u)
			if err != nil {
				t.Fatal(err)
			}
			if cost > est {
				t.Fatalf("%s case %d: the call cost %d against an estimate of %d (input %d, claimed %d, usage %+v, marks %+v)", model, i, cost, est, input, claimed, u, marks)
			}
			// And never above the estimate that claims nothing.
			if full, _ := prices.EstimateMax(model, input, maxTokens, CacheMarks{Any: marks.Any, OneHour: marks.OneHour}); est > full {
				t.Fatalf("%s case %d: the cached estimate %d is above the full one %d", model, i, est, full)
			}
		}
	}
}

// What the memo cannot know: the API dropped the entry early and the call wrote the prefix again. That call is billed more than its
// reservation (never more than the full worst case); the client says so in the log, the run's budget shows it used, and the next call is
// refused by it. The platform's holds (the daily cap, the breaker) were of the full worst case all along.
func TestAnEntryThatWasDroppedEarlyIsReportedNotHidden(t *testing.T) {
	rg := newRig(t, nil)
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	run := uuid.New()
	rg.countAt(60_000)
	rg.api.messages = func(_ int, _ recordedCall) fakeReply {
		return okReply("ok", apiUsage{Input: 100, CacheWrite5m: 60_000, Output: 500})
	}
	first := agentRequest(run, 1, "")
	first.Run.Budget = newSpyBudget(5_000_000)
	if _, err := c.Messages(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	rg.countAt(80_000)
	rg.api.messages = func(_ int, _ recordedCall) fakeReply { // a miss: all of it written again
		return okReply("ok", apiUsage{Input: 200, CacheWrite5m: 79_800, Output: 1_000})
	}
	budget := newSpyBudget(390_000)
	second := agentRequest(run, 2, "")
	second.Run.Budget = budget
	if _, err := c.Messages(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if budget.settled[0] <= budget.reserved[0] {
		t.Fatalf("the test needs a miss that cost more than its reservation: %d against %d", budget.settled[0], budget.reserved[0])
	}
	if !strings.Contains(rg.logs.String(), "cache entry was gone") {
		t.Errorf("an overrun of the reservation was not logged")
	}
	if budget.Remaining() >= 0 {
		t.Errorf("the budget does not show the overrun: %d left", budget.Remaining())
	}
	third := agentRequest(run, 3, "")
	third.Run.Budget = budget
	var ce *CapError
	if _, err := c.Messages(context.Background(), third); !errors.As(err, &ce) || ce.Kind != CapRun {
		t.Fatalf("the call after an overrun: %v, want the run-budget error", err)
	}
}

// maxTokensOfCall is the max_tokens a recorded call was sent with.
func maxTokensOfCall(call recordedCall) int64 {
	var body struct {
		MaxTokens int64 `json:"max_tokens"`
	}
	_ = json.Unmarshal(call.Body, &body)
	return body.MaxTokens
}

// The memo is exact about what it claims: nothing for another model, another run or a request outside a run; the whole cached prefix for a
// request that extends it.
func TestCacheMemoClaims(t *testing.T) {
	var memo cacheMemo
	run := uuid.New()
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	memo.remember(agentRequest(run, 1, ""), t0, Usage{InputTokens: 100, CacheReadTokens: 40_000, CacheWriteTokens: 5_000})
	next := agentRequest(run, 2, "")
	if got := memo.claim(next, t0.Add(time.Minute)); got != 45_000 {
		t.Fatalf("claim = %d, want the 45,000 tokens the call left in the cache", got)
	}
	other := next
	other.Model = "claude-sonnet-5-5"
	if got := memo.claim(other, t0.Add(time.Minute)); got != 0 {
		t.Errorf("another model claims %d", got)
	}
	other = agentRequest(uuid.New(), 2, "")
	if got := memo.claim(other, t0.Add(time.Minute)); got != 0 {
		t.Errorf("another run claims %d", got)
	}
	outside := agentRequest(uuid.Nil, 2, "")
	if got := memo.claim(outside, t0.Add(time.Minute)); got != 0 {
		t.Errorf("a request outside a run claims %d", got)
	}
	if got := memo.claim(next, t0.Add(cacheMemoFresh)); got != 0 {
		t.Errorf("an expired entry claims %d", got)
	}
	if got := memo.claim(next, t0.Add(-time.Second)); got != 0 {
		t.Errorf("an entry from the future claims %d", got)
	}
	// A call that touched no cache leaves nothing to claim.
	memo.remember(agentRequest(run, 2, ""), t0.Add(time.Minute), Usage{InputTokens: 100})
	if got := memo.claim(agentRequest(run, 3, ""), t0.Add(2*time.Minute)); got != 0 {
		t.Errorf("a run whose last call cached nothing claims %d", got)
	}
}
