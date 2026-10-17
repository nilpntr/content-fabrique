package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A throttled key queues the call instead of failing it: 429s with retry-after are waited out, then it succeeds.
func TestThrottledKeyQueuesInsteadOfFailing(t *testing.T) {
	rg := newRig(t, nil)
	rg.api.messages = func(n int, _ recordedCall) fakeReply {
		switch n {
		case 1:
			return errReply(429, "rate_limit_error", "slow down", map[string]string{"retry-after": "7"})
		case 2:
			return errReply(529, "overloaded_error", "Overloaded", nil)
		}
		return okReply("finally", defaultUsage)
	}
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	resp, err := c.Messages(context.Background(), simpleRequest())
	if err != nil {
		t.Fatalf("a throttled call failed instead of queueing: %v", err)
	}
	if resp.Text() != "finally" {
		t.Errorf("text = %q", resp.Text())
	}
	if got := len(rg.api.messageCalls()); got != 3 {
		t.Errorf("the API saw %d message calls, want 3", got)
	}
	// Waits: the first honours retry-after (7s beats the 0.75s jittered backoff); the second is backoff for attempt 2:
	// base 2s, equal jitter with Rand 0.5 -> 1s + 0.5s = 1.5s.
	sleeps := *rg.sleeps
	if len(sleeps) != 2 || sleeps[0] != 7*time.Second || sleeps[1] != 1500*time.Millisecond {
		t.Errorf("sleeps = %v", sleeps)
	}
	// One call, one ledger row, even though three requests were sent.
	if rows := rg.ledger.snapshot(); len(rows) != 1 {
		t.Errorf("ledger rows = %d, want 1", len(rows))
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	l := newLimited(nil, newKeyLimiter(1), retryPolicy{Rand: func() float64 { return 0 }}, nil)
	// Rand 0: half of the exponential step. Steps: 1,2,4,8,16,30(cap),30.
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second}
	for i, w := range want {
		if got := l.delay(i+1, 0); got != w {
			t.Errorf("delay(%d) = %v, want %v", i+1, got, w)
		}
	}
	// retry-after is a floor, never a ceiling.
	if got := l.delay(1, 20*time.Second); got != 20*time.Second {
		t.Errorf("delay with retry-after 20s = %v", got)
	}
	if got := l.delay(7, time.Second); got != 15*time.Second {
		t.Errorf("a short retry-after shortened the backoff: %v", got)
	}
}

func TestGivesUpAfterTheWaitBudget(t *testing.T) {
	rg := newRig(t, nil)
	rg.api.messages = func(int, recordedCall) fakeReply {
		return errReply(429, "rate_limit_error", "slow down", map[string]string{"retry-after": "120"})
	}
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	_, err := c.Messages(context.Background(), simpleRequest())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 429 {
		t.Errorf("the 429 is not reachable from the error: %v", err)
	}
	// 120s waits against a 5 minute budget: two waits fit (240s), the third (360s) does not.
	if got := len(rg.api.messageCalls()); got != 3 {
		t.Errorf("message calls = %d, want 3", got)
	}
	if len(rg.ledger.snapshot()) != 0 {
		t.Error("a call that never succeeded wrote a ledger row")
	}
	mustNotContain(t, "logs", rg.logs.String(), testKey)
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	rg := newRig(t, nil, func(o *FactoryOptions) { o.retry.MaxAttempts = 2 })
	rg.api.messages = func(int, recordedCall) fakeReply { return errReply(529, "overloaded_error", "Overloaded", nil) }
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
	if got := len(rg.api.messageCalls()); got != 2 {
		t.Errorf("message calls = %d, want 2", got)
	}
}

func TestOtherErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 500} {
		rg := newRig(t, nil)
		rg.api.messages = func(int, recordedCall) fakeReply { return errReply(status, "invalid_request_error", "nope", nil) }
		c, _ := rg.client(t, newWorkspaceID(), "editor")
		_, err := c.Messages(context.Background(), simpleRequest())
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != status || errors.Is(err, ErrRateLimited) {
			t.Errorf("%d: err = %v", status, err)
		}
		if got := len(rg.api.messageCalls()); got != 1 {
			t.Errorf("%d: message calls = %d, want 1 (retries live only on 429 and 529)", status, got)
		}
	}
}

func TestCancelledWhileWaiting(t *testing.T) {
	rg := newRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	rg.api.messages = func(int, recordedCall) fakeReply {
		cancel() // the caller gives up while the key is throttled
		return errReply(429, "rate_limit_error", "slow down", map[string]string{"retry-after": "5"})
	}
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	_, err := c.Messages(ctx, simpleRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := len(rg.api.messageCalls()); got != 1 {
		t.Errorf("message calls = %d, want 1", got)
	}
}

// The real sleep honours the context.
func TestSleepCtx(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("sleepCtx = %v after %v", err, time.Since(start))
	}
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}

// Queued callers wait for a slot: at most Concurrency requests are in flight per key, and all of them complete.
func TestConcurrencyLimitQueuesCallers(t *testing.T) {
	const limit, callers = 2, 9
	rg := newRig(t, map[string]string{"LLM_KEY_CONCURRENCY": "2"})
	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	rg.api.messages = func(int, recordedCall) fakeReply {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return okReply("ok", defaultUsage)
	}
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := rg.client(t, newWorkspaceID(), "editor")
			_, err := c.Messages(context.Background(), simpleRequest())
			errs <- err
		}()
	}
	// Let the first two in, give the rest time to queue, then drain.
	deadline := time.Now().Add(5 * time.Second)
	for inFlight.Load() < limit && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := inFlight.Load(); got != limit {
		t.Errorf("in flight = %d, want exactly %d while others queue", got, limit)
	}
	go func() {
		for range callers {
			release <- struct{}{}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a queued call failed: %v", err)
		}
	}
	if peak.Load() > limit {
		t.Errorf("peak in flight = %d, limit %d", peak.Load(), limit)
	}
}

// The limit belongs to the key: clients of two workspaces share it, and a different key has its own.
func TestLimiterIsPerKey(t *testing.T) {
	var reg limiterRegistry
	a1, a2, b := reg.forKey("key-a", 3), reg.forKey("key-a", 3), reg.forKey("key-b", 3)
	if a1 != a2 || a1 == b {
		t.Fatal("limiters are not shared per key")
	}
	if cap(a1.sem) != 3 {
		t.Errorf("capacity = %d", cap(a1.sem))
	}
}

// One caller told to back off pauses the others of the same key, instead of every caller discovering the 429.
func TestCoolDownIsSharedAcrossCallers(t *testing.T) {
	rg := newRig(t, nil)
	var first sync.Once
	rg.api.messages = func(int, recordedCall) fakeReply {
		var rep fakeReply
		first.Do(func() { rep = errReply(429, "rate_limit_error", "slow", map[string]string{"retry-after": "30"}) })
		if rep.Status != 0 {
			return rep
		}
		return okReply("ok", defaultUsage)
	}
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	// A second caller arriving "during" the pause: set the pause by hand to see acquire honour it.
	ltd, _ := asMetered(t, c).inner.(*limited)
	lim, pol := ltd.lim, ltd.pol
	lim.extendCoolDown(pol.Now().Add(10 * time.Second))
	before := len(*rg.sleeps)
	if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
		t.Fatal(err)
	}
	sleeps := *rg.sleeps
	if len(sleeps) != before+1 || sleeps[before] != 10*time.Second {
		t.Errorf("second caller did not wait out the shared cool-down: %v", sleeps)
	}
}
