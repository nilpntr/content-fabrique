package llm

import (
	"encoding/json"
	"hash/fnv"
	"sync"
	"time"

	"github.com/google/uuid"
)

// What the client remembers about the prompt cache of a run, so that the worst case of its next call is not priced as if nothing were cached.
//
// The worst case of a call (PriceTable.EstimateMax) prices every input token as a cache write: the dearest way a token can be billed. In an
// agent run that is far from the truth. Each call extends the previous one, which wrote the cache up to its last breakpoint; the next call
// reads that prefix at a tenth of the price and writes only what is new. A run with 70,000 tokens of conversation was reserved at $0.35 of
// input although the call cost about $0.03, so with $0.39 of a $0.96 budget left the next call could not be reserved (the output room that
// was left was under the minimum), and the run ended "out of budget" with money in hand.
//
// So the reservation prices the prefix that the previous call of the same run cached at the cache-read rate, and only the rest at the
// dearest. That is a true upper bound whenever the cache entry is still alive, and the entry is alive when it was used less than the cache's
// lifetime ago and nothing before the new part of the request changed. The memo only claims the prefix then; in any other case the full
// price stands (see claim). What it cannot know is an entry that the API dropped early; that call would be billed the prefix as a cache
// write, up to the full-price worst case, which the budget would then show as an overrun (metered.Messages logs it and the next call sees
// the budget used). The spend gate's holds (the workspace's daily cap and the platform breaker) always use the full-price worst case.

const (
	// cacheMemoFresh is how long after a call started its cache entry is trusted: the cache lives five minutes from its last use, and the
	// use happens at the start of the call, so a minute of margin covers the call itself and our own delays.
	cacheMemoFresh = 4 * time.Minute
	// maxAppendedBlocks is how many content blocks a request may add to the previous one for its prefix to be found in the cache: the API
	// looks back at most 20 blocks from a breakpoint for an earlier entry.
	maxAppendedBlocks = 15
	// cacheMemoRuns bounds the memory of the memo (one entry per run in progress).
	cacheMemoRuns = 256
)

// cachedCall is what is remembered of the newest call of a run that reported a cached prefix.
type cachedCall struct {
	model string
	// startedAt is when the call began: the cache entry was last used then.
	startedAt time.Time
	// tokens is the prefix the call left in the cache: what it read plus what it wrote.
	tokens int64
	// config hashes what must not change for the cache to stay valid: the system, the tools, the tool choice, the thinking and the effort.
	config uint64
	// messages hashes each message, without its cache breakpoints (a breakpoint moves from call to call).
	messages []uint64
}

type cacheMemo struct {
	mu   sync.Mutex
	runs map[uuid.UUID]cachedCall
}

// remember records the call that just finished. A call that touched no cache forgets the run: there is nothing to claim.
func (c *cacheMemo) remember(req Request, started time.Time, u Usage) {
	if req.Run.ID == uuid.Nil {
		return
	}
	tokens := u.CacheReadTokens + u.CacheWriteTokens
	c.mu.Lock()
	defer c.mu.Unlock()
	if tokens <= 0 {
		delete(c.runs, req.Run.ID)
		return
	}
	cfg, msgs, ok := fingerprint(req)
	if !ok {
		delete(c.runs, req.Run.ID)
		return
	}
	if c.runs == nil {
		c.runs = map[uuid.UUID]cachedCall{}
	}
	if len(c.runs) >= cacheMemoRuns {
		var oldest uuid.UUID
		var at time.Time
		for id, v := range c.runs {
			if at.IsZero() || v.startedAt.Before(at) {
				oldest, at = id, v.startedAt
			}
		}
		delete(c.runs, oldest)
	}
	c.runs[req.Run.ID] = cachedCall{model: req.Model, startedAt: started, tokens: tokens, config: cfg, messages: msgs}
}

// claim returns how many tokens at the front of the request the previous call of the run left in the cache and the request still starts
// with: 0 unless every condition holds. The call is of the same run and model; it began less than cacheMemoFresh ago; the system, the
// tools, the tool choice, the thinking and the effort are the same; every earlier message is unchanged and in place; and the request adds
// at most maxAppendedBlocks blocks to them.
func (c *cacheMemo) claim(req Request, now time.Time) int64 {
	if req.Run.ID == uuid.Nil {
		return 0
	}
	c.mu.Lock()
	prev, ok := c.runs[req.Run.ID]
	c.mu.Unlock()
	if !ok || prev.model != req.Model || now.Sub(prev.startedAt) >= cacheMemoFresh || now.Before(prev.startedAt) {
		return 0
	}
	cfg, msgs, ok := fingerprint(req)
	if !ok || cfg != prev.config || len(msgs) < len(prev.messages) {
		return 0
	}
	for i, h := range prev.messages {
		if msgs[i] != h {
			return 0
		}
	}
	appended := 0
	for _, m := range req.Messages[len(prev.messages):] {
		appended += max(len(m.Content), 1)
	}
	if appended > maxAppendedBlocks {
		return 0
	}
	return prev.tokens
}

// fingerprint hashes the parts of a request that the cache depends on. ok is false when something cannot be encoded (nothing is claimed).
func fingerprint(req Request) (config uint64, messages []uint64, ok bool) {
	h := fnv.New64a()
	write := func(v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0})
		return true
	}
	tools := make([]Tool, len(req.Tools))
	for i, t := range req.Tools {
		t.Cache = nil
		tools[i] = t
	}
	if !write(stripBlocks(req.System)) || !write(tools) || !write(req.ToolChoice) || !write(req.Thinking) || !write(req.Effort) {
		return 0, nil, false
	}
	config = h.Sum64()
	for _, m := range req.Messages {
		m.Content = stripBlocks(m.Content)
		mh := fnv.New64a()
		b, err := json.Marshal(m)
		if err != nil {
			return 0, nil, false
		}
		_, _ = mh.Write(b)
		messages = append(messages, mh.Sum64())
	}
	return config, messages, true
}

// stripBlocks copies blocks without their cache breakpoints, all the way down.
func stripBlocks(in []Block) []Block {
	if in == nil {
		return nil
	}
	out := make([]Block, len(in))
	for i, b := range in {
		b.Cache = nil
		b.Content = stripBlocks(b.Content)
		out[i] = b
	}
	return out
}
