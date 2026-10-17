package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

// retryPolicy tunes the backoff. The zero value of each field takes the default; tests inject Now, Sleep and Rand.
type retryPolicy struct {
	Base         time.Duration
	Cap          time.Duration
	MaxTotalWait time.Duration
	MaxAttempts  int

	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	Rand  func() float64
}

func (p retryPolicy) withDefaults() retryPolicy {
	if p.Base <= 0 {
		p.Base = defaultBackoffBase
	}
	if p.Cap <= 0 {
		p.Cap = defaultBackoffCap
	}
	if p.MaxTotalWait <= 0 {
		p.MaxTotalWait = defaultMaxTotalWait
	}
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = defaultMaxAttempts
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.Sleep == nil {
		p.Sleep = sleepCtx
	}
	if p.Rand == nil {
		p.Rand = rand.Float64
	}
	return p
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// lane limits one kind of request on one API key: a semaphore on requests in flight, and a cool-down. When any caller
// of the lane is told to back off, every caller of it waits out the same pause instead of piling more requests onto a
// throttled key.
type lane struct {
	sem chan struct{}

	mu       sync.Mutex
	coolDown time.Time
}

func newLane(concurrency int) *lane {
	if concurrency < 1 {
		concurrency = DefaultConcurrency
	}
	return &lane{sem: make(chan struct{}, concurrency)}
}

func (l *lane) coolUntil() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.coolDown
}

// extendCoolDown moves the pause out to at least t.
func (l *lane) extendCoolDown(t time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t.After(l.coolDown) {
		l.coolDown = t
	}
}

// acquire waits for a free slot and for the cool-down to pass. A caller that has to wait is queued, not failed; only
// the context ends the wait.
func (l *lane) acquire(ctx context.Context, p retryPolicy) error {
	for {
		select {
		case l.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		if wait := l.coolUntil().Sub(p.Now()); wait > 0 {
			<-l.sem // do not hold a slot while paused
			if err := p.Sleep(ctx, wait); err != nil {
				return err
			}
			continue
		}
		return nil
	}
}

func (l *lane) release() { <-l.sem }

// keyLanes are the two lanes of one API key. count_tokens has its own slots and its own cool-down: a long generation
// holding every Messages slot (or a Messages 429) must not stall the free count that decides whether the next call may
// go out, and Anthropic rate-limits the two endpoints separately.
type keyLanes struct {
	messages *lane
	count    *lane
}

// limiterRegistry hands out the lanes of an API key, so every client built for the same key shares its limit. Keys are
// identified by a hash and never stored.
type limiterRegistry struct {
	mu sync.Mutex
	m  map[string]*keyLanes
}

func (r *limiterRegistry) forKey(key string, concurrency int) *keyLanes {
	sum := sha256.Sum256([]byte(key))
	id := hex.EncodeToString(sum[:])
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*keyLanes{}
	}
	l, ok := r.m[id]
	if !ok {
		l = &keyLanes{messages: newLane(concurrency), count: newLane(concurrency)}
		r.m[id] = l
	}
	return l
}

// limited is the Client that applies the key's lanes and the retry policy to the client inside it. This is the only
// place requests are retried.
//
// What is retried, with the same backoff, attempt and wait limits:
//   - an error answer that says "not now" (APIError.Retryable: 408, 429, 500, 502, 503, 504, 529, overloaded): the
//     server did not process the request, so a retry cannot bill twice. retry-after is honoured;
//   - a transport failure (reset, timeout, dial error) of a request that never reached the wire (Messages), or of any
//     count_tokens call, which is free and idempotent.
//
// A Messages request that was sent and then lost its connection is NOT retried: it may have run and been billed.
// metered records it at its worst-case estimate (ErrOutcomeUnknown). No other 4xx is ever retried.
type limited struct {
	inner Client
	lanes *keyLanes
	pol   retryPolicy
	log   *slog.Logger
}

func newLimited(inner Client, lanes *keyLanes, pol retryPolicy, log *slog.Logger) *limited {
	return &limited{inner: inner, lanes: lanes, pol: pol.withDefaults(), log: log}
}

// Messages implements Client.
func (c *limited) Messages(ctx context.Context, req Request) (Response, error) {
	var resp Response
	err := c.do(ctx, "messages", c.lanes.messages, func() (err error) { resp, err = c.inner.Messages(ctx, req); return })
	return resp, err
}

// CountTokens implements Client.
func (c *limited) CountTokens(ctx context.Context, req Request) (int64, error) {
	var n int64
	err := c.do(ctx, "count_tokens", c.lanes.count, func() (err error) { n, err = c.inner.CountTokens(ctx, req); return })
	return n, err
}

// retryable decides whether err is worth another attempt, and how long the server asked to wait.
func retryable(op string, err error) (ok bool, retryAfter time.Duration, throttled bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		throttled = apiErr.Status == 429 || apiErr.Status == 529 || apiErr.Type == "overloaded_error" || apiErr.Type == "rate_limit_error"
		return apiErr.Retryable(), apiErr.RetryAfter, throttled
	}
	var te *TransportError
	if errors.As(err, &te) {
		return op == "count_tokens" || !te.Sent, 0, false
	}
	return false, 0, false
}

func (c *limited) do(ctx context.Context, op string, l *lane, call func() error) error {
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		if err := l.acquire(ctx, c.pol); err != nil {
			return err
		}
		err := call()
		l.release()
		if err == nil || ctx.Err() != nil { // the caller giving up is never retried
			return err
		}
		ok, retryAfter, throttled := retryable(op, err)
		if !ok {
			return err
		}
		delay := c.delay(attempt, retryAfter)
		if attempt >= c.pol.MaxAttempts || waited+delay > c.pol.MaxTotalWait {
			sentinel := ErrRetriesExhausted
			if throttled {
				sentinel = ErrRateLimited
			}
			return fmt.Errorf("%w: giving up on %s after %d attempts and %s of waiting: %w", sentinel, op, attempt, waited.Round(time.Millisecond), err)
		}
		if throttled {
			l.extendCoolDown(c.pol.Now().Add(delay))
		}
		c.log.Warn("llm: backing off", "op", op, "attempt", attempt, "wait", delay.Round(time.Millisecond), "reason", describeRetry(err))
		if err := c.pol.Sleep(ctx, delay); err != nil {
			return err
		}
		waited += delay
	}
}

// describeRetry names why a call is being retried, without the error text (which can quote a URL).
func describeRetry(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("http %d %s request-id %s", apiErr.Status, apiErr.Type, apiErr.RequestID)
	}
	return "transport error before a response"
}

// delay is the wait before attempt+1: exponential with equal jitter (half fixed, half random), never shorter than the
// server's retry-after.
func (c *limited) delay(attempt int, retryAfter time.Duration) time.Duration {
	backoff := c.pol.Base
	for i := 1; i < attempt && backoff < c.pol.Cap; i++ {
		backoff *= 2
	}
	backoff = min(backoff, c.pol.Cap)
	d := backoff/2 + time.Duration(c.pol.Rand()*float64(backoff/2))
	return max(d, retryAfter)
}
