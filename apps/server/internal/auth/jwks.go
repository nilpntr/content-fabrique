package auth

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
)

// How the key lookup behaves towards Clerk. The SDK keeps a key it found for an hour, but asks Clerk's API for the whole
// key set again on every token whose key id it does not know, so a caller who sends tokens with random key ids would make
// us call Clerk once per request. keySetBackend sits under the SDK's JWKS client and limits that.
const (
	// JWKSMinRefresh is the least time between two fetches of the key set that succeeded. Inside it, an unknown key id is
	// answered from the key set we already hold (the token is refused, no call is made). A key Clerk rotates in is
	// picked up within this time.
	JWKSMinRefresh = 30 * time.Second
	// jwksFailureBackoff is how long a failed fetch is remembered, so a Clerk outage is not hammered either.
	jwksFailureBackoff = 5 * time.Second
	jwksFetchTimeout   = 10 * time.Second
)

// keySetBackend is a clerk.Backend for the JWKS client. It answers GET /jwks from a copy of the key set, fetched at
// most once per JWKSMinRefresh. Callers that arrive while a fetch is running wait for it and share its result (single
// flight), and the fetch is not cancelled when the first caller's request is.
type keySetBackend struct {
	next clerk.Backend
	now  func() time.Time
	slot chan struct{} // holds one token: whoever has it may read or write the fields below

	keys      []*clerk.JSONWebKey
	fetchedAt time.Time
	failedAt  time.Time
	failure   error
}

func newKeySetBackend(next clerk.Backend) *keySetBackend {
	b := &keySetBackend{next: next, now: time.Now, slot: make(chan struct{}, 1)}
	b.slot <- struct{}{}
	return b
}

// Call implements clerk.Backend.
func (b *keySetBackend) Call(ctx context.Context, req *clerk.APIRequest, out clerk.ResponseReader) error {
	set, ok := out.(*clerk.JSONWebKeySet)
	if !ok {
		return b.next.Call(ctx, req, out)
	}
	keys, err := b.keySet(ctx, req)
	if err != nil {
		if f := jwksFailureFrom(ctx); f != nil {
			f.failed.Store(true)
		}
		return err
	}
	set.Keys = keys
	return nil
}

func (b *keySetBackend) keySet(ctx context.Context, req *clerk.APIRequest) ([]*clerk.JSONWebKey, error) {
	select {
	case <-b.slot:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { b.slot <- struct{}{} }()

	now := b.now()
	if b.keys != nil && now.Sub(b.fetchedAt) < JWKSMinRefresh {
		return b.keys, nil
	}
	if b.failure != nil && now.Sub(b.failedAt) < jwksFailureBackoff {
		return nil, b.failure
	}

	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jwksFetchTimeout)
	defer cancel()
	set := &clerk.JSONWebKeySet{}
	if err := b.next.Call(fetchCtx, req, set); err != nil {
		b.failure, b.failedAt = err, now
		return nil, err
	}
	b.keys, b.fetchedAt, b.failure = set.Keys, now, nil
	if b.keys == nil {
		b.keys = []*clerk.JSONWebKey{}
	}
	return b.keys, nil
}

// jwksFailure tells the failure handler of the middleware that the key set could not be fetched, which is Clerk's
// problem (503), not the token's (401). The SDK's failure handler does not receive the error, so it travels in the context.
type jwksFailure struct{ failed atomic.Bool }

type jwksFailureKey struct{}

func withJWKSFailure(ctx context.Context) (context.Context, *jwksFailure) {
	f := &jwksFailure{}
	return context.WithValue(ctx, jwksFailureKey{}, f), f
}

func jwksFailureFrom(ctx context.Context) *jwksFailure {
	f, _ := ctx.Value(jwksFailureKey{}).(*jwksFailure)
	return f
}
