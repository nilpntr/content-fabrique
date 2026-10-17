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

// keyLimiter limits one API key: a semaphore on requests in flight, and a shared cool-down. When any caller is told
// to back off, every caller of the key waits out the same pause instead of piling more requests onto a throttled key.
type keyLimiter struct {
	sem chan struct{}

	mu       sync.Mutex
	coolDown time.Time
}

func newKeyLimiter(concurrency int) *keyLimiter {
	if concurrency < 1 {
		concurrency = DefaultConcurrency
	}
	return &keyLimiter{sem: make(chan struct{}, concurrency)}
}

func (l *keyLimiter) coolUntil() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.coolDown
}

// extendCoolDown moves the pause out to at least t.
func (l *keyLimiter) extendCoolDown(t time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t.After(l.coolDown) {
		l.coolDown = t
	}
}

// acquire waits for a free slot and for the cool-down to pass. A caller that has to wait is queued, not failed; only
// the context ends the wait.
func (l *keyLimiter) acquire(ctx context.Context, p retryPolicy) error {
	for {
		select {
		case l.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		if wait := l.coolUntil().Sub(p.Now()); wait > 0 {
			<-l.sem // do not hold a slot while paused: an unthrottled caller (count_tokens) may use it
			if err := p.Sleep(ctx, wait); err != nil {
				return err
			}
			continue
		}
		return nil
	}
}

func (l *keyLimiter) release() { <-l.sem }

// limiterRegistry hands out one keyLimiter per API key, so every client built for the same key shares its limit.
// Keys are identified by a hash and never stored.
type limiterRegistry struct {
	mu sync.Mutex
	m  map[string]*keyLimiter
}

func (r *limiterRegistry) forKey(key string, concurrency int) *keyLimiter {
	sum := sha256.Sum256([]byte(key))
	id := hex.EncodeToString(sum[:])
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*keyLimiter{}
	}
	l, ok := r.m[id]
	if !ok {
		l = newKeyLimiter(concurrency)
		r.m[id] = l
	}
	return l
}

// limited is the Client that applies a keyLimiter and the retry policy to the client inside it. This is the only
// place requests are retried.
type limited struct {
	inner Client
	lim   *keyLimiter
	pol   retryPolicy
	log   *slog.Logger
}

func newLimited(inner Client, lim *keyLimiter, pol retryPolicy, log *slog.Logger) *limited {
	return &limited{inner: inner, lim: lim, pol: pol.withDefaults(), log: log}
}

// Messages implements Client.
func (c *limited) Messages(ctx context.Context, req Request) (Response, error) {
	var resp Response
	err := c.do(ctx, "messages", func() (err error) { resp, err = c.inner.Messages(ctx, req); return })
	return resp, err
}

// CountTokens implements Client.
func (c *limited) CountTokens(ctx context.Context, req Request) (int64, error) {
	var n int64
	err := c.do(ctx, "count_tokens", func() (err error) { n, err = c.inner.CountTokens(ctx, req); return })
	return n, err
}

func (c *limited) do(ctx context.Context, op string, call func() error) error {
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		if err := c.lim.acquire(ctx, c.pol); err != nil {
			return err
		}
		err := call()
		c.lim.release()

		var apiErr *APIError
		if err == nil || !errors.As(err, &apiErr) || !apiErr.Retryable() {
			return err
		}
		delay := c.delay(attempt, apiErr.RetryAfter)
		if attempt >= c.pol.MaxAttempts || waited+delay > c.pol.MaxTotalWait {
			return fmt.Errorf("%w: giving up on %s after %d attempts and %s of waiting: %w", ErrRateLimited, op, attempt, waited.Round(time.Millisecond), err)
		}
		c.lim.extendCoolDown(c.pol.Now().Add(delay))
		c.log.Warn("llm: throttled, backing off", "op", op, "status", apiErr.Status, "attempt", attempt, "wait", delay.Round(time.Millisecond), "request_id", apiErr.RequestID)
		if err := c.pol.Sleep(ctx, delay); err != nil {
			return err
		}
		waited += delay
	}
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
