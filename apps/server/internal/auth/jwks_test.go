package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwks"
)

// countingBackend is the Clerk API under the key set cache: it counts the fetches.
type countingBackend struct {
	calls atomic.Int32
	keys  []*clerk.JSONWebKey
	fail  atomic.Bool
	delay time.Duration
}

func (b *countingBackend) Call(_ context.Context, _ *clerk.APIRequest, out clerk.ResponseReader) error {
	b.calls.Add(1)
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	if b.fail.Load() {
		return errors.New("clerk is down")
	}
	set, ok := out.(*clerk.JSONWebKeySet)
	if !ok {
		return errors.New("unexpected resource")
	}
	set.Keys = b.keys
	return nil
}

func TestUnknownKeyIDsDoNotAmplifyIntoClerkCalls(t *testing.T) {
	f := newFixture(t)
	counting := &countingBackend{keys: f.backend.keys}
	mw, err := NewMiddleware(MiddlewareConfig{AppOrigin: testOrigin, JWKSClient: &jwks.Client{Backend: counting}}, &fakeResolver{ret: testPrincipal()})
	if err != nil {
		t.Fatal(err)
	}
	h := mw(MeHandler())

	// The first request fetches the key set once.
	if rec := get(h, "Bearer "+f.token(t, nil)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := counting.calls.Load(); got != 1 {
		t.Fatalf("calls after the first request = %d", got)
	}

	// 50 tokens with key ids nobody has: all refused, and none of them costs a call.
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		tok := f.sign(t, other, "ins_random_"+string(rune('a'+i%26))+string(rune('A'+i/26)), f.claims(nil))
		wantError(t, get(h, "Bearer "+tok), http.StatusUnauthorized, "invalid_token")
	}
	if got := counting.calls.Load(); got != 1 {
		t.Fatalf("unknown key ids caused %d Clerk calls, want 1 in total", got)
	}
}

func TestConcurrentUnknownKeyIDsShareOneFetch(t *testing.T) {
	f := newFixture(t)
	counting := &countingBackend{keys: f.backend.keys, delay: 100 * time.Millisecond}
	mw, err := NewMiddleware(MiddlewareConfig{AppOrigin: testOrigin, JWKSClient: &jwks.Client{Backend: counting}}, &fakeResolver{ret: testPrincipal()})
	if err != nil {
		t.Fatal(err)
	}
	h := mw(MeHandler())
	other, _ := rsa.GenerateKey(rand.Reader, 2048)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok := f.sign(t, other, "ins_concurrent_"+string(rune('a'+i)), f.claims(nil))
			if rec := get(h, "Bearer "+tok); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	if got := counting.calls.Load(); got != 1 {
		t.Fatalf("20 concurrent requests made %d fetches, want 1", got)
	}
}

func TestKeySetIsRefetchedOncePerWindowAndPicksUpANewKey(t *testing.T) {
	f := newFixture(t)
	counting := &countingBackend{keys: f.backend.keys}
	b := newKeySetBackend(counting)
	now := time.Now()
	b.now = func() time.Time { return now }
	call := func() ([]*clerk.JSONWebKey, error) {
		set := &clerk.JSONWebKeySet{}
		err := b.Call(context.Background(), clerk.NewAPIRequest(http.MethodGet, "/jwks"), set)
		return set.Keys, err
	}

	if keys, err := call(); err != nil || len(keys) != 1 {
		t.Fatalf("first: %v, %v", keys, err)
	}
	// Clerk rotates a key in. Inside the window it is not asked.
	rotated, _ := rsa.GenerateKey(rand.Reader, 2048)
	counting.keys = append(append([]*clerk.JSONWebKey{}, f.backend.keys...), &clerk.JSONWebKey{Key: &rotated.PublicKey, KeyID: "ins_new", Algorithm: "RS256"})
	now = now.Add(JWKSMinRefresh - time.Second)
	if keys, _ := call(); len(keys) != 1 || counting.calls.Load() != 1 {
		t.Fatalf("inside the window: %d keys, %d calls", len(keys), counting.calls.Load())
	}
	// After it, the next unknown key id refetches, once, and finds the new key.
	now = now.Add(2 * time.Second)
	if keys, err := call(); err != nil || len(keys) != 2 || counting.calls.Load() != 2 {
		t.Fatalf("after the window: %d keys, %d calls, %v", len(keys), counting.calls.Load(), err)
	}
	if _, err := call(); err != nil || counting.calls.Load() != 2 {
		t.Fatalf("again: %d calls, %v", counting.calls.Load(), err)
	}
}

// Failing to fetch the key set is Clerk's problem: 503 identity_unavailable, not 401 invalid_token, and a failing Clerk is
// not hammered either.
func TestKeySetUnavailableIs503(t *testing.T) {
	f := newFixture(t)
	counting := &countingBackend{keys: f.backend.keys}
	counting.fail.Store(true)
	res := &fakeResolver{ret: testPrincipal()}
	mw, err := NewMiddleware(MiddlewareConfig{AppOrigin: testOrigin, JWKSClient: &jwks.Client{Backend: counting}}, res)
	if err != nil {
		t.Fatal(err)
	}
	h := mw(MeHandler())
	tok := f.token(t, nil)

	for range 5 {
		wantError(t, get(h, "Bearer "+tok), http.StatusServiceUnavailable, "identity_unavailable")
	}
	if got := counting.calls.Load(); got != 1 {
		t.Fatalf("a failing Clerk was called %d times, want 1 inside the backoff", got)
	}
	if res.calls != 0 {
		t.Error("the resolver ran")
	}
	// A token with no key id at all is a bad token, not an outage.
	wantError(t, get(h, "Bearer not.a.jwt"), http.StatusUnauthorized, "invalid_token")
}

func TestKeySetRecoversAfterAnOutage(t *testing.T) {
	f := newFixture(t)
	counting := &countingBackend{keys: f.backend.keys}
	counting.fail.Store(true)
	b := newKeySetBackend(counting)
	now := time.Now()
	b.now = func() time.Time { return now }
	call := func() error {
		return b.Call(context.Background(), clerk.NewAPIRequest(http.MethodGet, "/jwks"), &clerk.JSONWebKeySet{})
	}
	if call() == nil {
		t.Fatal("want the outage")
	}
	counting.fail.Store(false)
	now = now.Add(jwksFailureBackoff + time.Second)
	if err := call(); err != nil {
		t.Fatalf("after the backoff: %v", err)
	}
}
