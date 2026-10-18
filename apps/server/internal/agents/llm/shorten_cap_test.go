package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A long-input max-effort call: 200,000 counted input tokens ($0.80 at $4/MTok) and 64,000 asked for ($1.28 at $20/MTok) is $2.08 in the
// worst case, over the per-request cap of $2 (LLM_MAX_REQUEST_MICROS), while the run's budget ($3.00) has room. Fitting max_tokens to the
// budget alone left the call at 64,000 and the cap then refused it (request_cap), failing a run a smaller answer could have served. It is
// now fitted to the lesser of the two: the call is sent shortened, its worst case is within the cap, and it is not refused.
func TestLongInputCallIsFittedToThePerRequestCap(t *testing.T) {
	rg := newRig(t, nil)
	rg.countAt(200_000)
	rg.api.messages = func(_ int, call recordedCall) fakeReply { return replyWithin(call, 200_000, 3_000) }
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	budget := newSpyBudget(3_000_000)
	req := opusRequest(64_000)
	req.Messages = []Message{{Role: RoleUser, Content: []Block{TextBlock(strings.Repeat("lat pulldown ", 40_000))}}} // 520 KB: the local bound is high, the count says 200,000
	req.Run = RunInfo{Budget: budget}

	resp, err := c.Messages(context.Background(), req)
	if err != nil {
		var ce *CapError
		if errors.As(err, &ce) && ce.Kind == CapRequest {
			t.Fatalf("a $3.00 run was refused by the per-request cap: %v", err)
		}
		t.Fatal(err)
	}
	sent := sentMaxTokens(t, rg, 0)
	if !resp.Shortened || sent >= 64_000 || sent < DefaultMinOutputTokens || sent%16 != 0 {
		t.Fatalf("max_tokens sent = %d, shortened = %v; want a cut to the cap, a multiple of 16", sent, resp.Shortened)
	}
	// The cut is the largest that fits $2: 200,000 x $4/MTok = $0.80 of input leaves $1.20 for output, 60,000 tokens.
	if sent < 59_000 || sent > 60_000 {
		t.Errorf("max_tokens sent = %d, want about 60,000 (the $2.00 cap less $0.80 of input, at $20/MTok)", sent)
	}
	for _, r := range budget.reserved {
		if r > 2_000_000 {
			t.Errorf("a reservation of %d is over the per-request cap of 2,000,000", r)
		}
	}

	// With the cap itself the binding limit and nothing left for a useful answer, the refusal is still the request cap, not a silent cut:
	// 480,000 input tokens cost $1.92 alone, which leaves $0.08 (4,000 tokens) of the cap; under the default minimum of 2,048 that still
	// fits, so push the input until it does not.
	rg2 := newRig(t, nil)
	rg2.countAt(499_000) // $1.996 of input: 200 tokens of output room
	c2, _ := rg2.client(t, newWorkspaceID(), "editor")
	req2 := opusRequest(64_000)
	req2.Messages = req.Messages
	req2.Run = RunInfo{Budget: newSpyBudget(3_000_000)}
	_, err = c2.Messages(context.Background(), req2)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapRequest {
		t.Fatalf("an input that leaves less than the minimum under the cap: err = %v, want request_cap", err)
	}
	if len(rg2.api.messageCalls()) != 0 {
		t.Error("a refused call was sent")
	}
}
