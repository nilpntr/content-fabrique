package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v3/jwt"
	"github.com/google/uuid"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const (
	testOrigin = "http://localhost:3000"
	testKID    = "ins_test_kid"
	testIssuer = "https://clerk.test.example"
)

// jwksBackend is a Clerk Backend that serves a local key set for GET /jwks, the way Clerk's Frontend API does. It is
// the injected "fake JWKS" of the tests: the middleware under test makes the same JWKS call it makes in production.
type jwksBackend struct {
	keys  []*clerk.JSONWebKey
	calls int
}

func (b *jwksBackend) Call(_ context.Context, req *clerk.APIRequest, out clerk.ResponseReader) error {
	b.calls++
	if req.Path != "/jwks" {
		return errors.New("unexpected path " + req.Path)
	}
	set, ok := out.(*clerk.JSONWebKeySet)
	if !ok {
		return errors.New("unexpected resource")
	}
	set.Keys = b.keys
	return nil
}

type fixture struct {
	key     *rsa.PrivateKey
	backend *jwksBackend
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	// A key id per test: the SDK keeps verified keys in a process-wide cache keyed by kid.
	kid := testKID + "_" + uuid.NewString()
	return &fixture{key: key, backend: &jwksBackend{keys: []*clerk.JSONWebKey{{Key: &key.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"}}}}
}

func (f *fixture) kid() string { return f.backend.keys[0].KeyID }

// claims builds the payload of a Clerk session token (version 2: the organization is under "o"). Pass overrides to
// replace a claim, with nil to drop it.
func (f *fixture) claims(overrides map[string]any) map[string]any {
	now := time.Now()
	c := map[string]any{
		"iss": testIssuer,
		"sub": "user_2abc",
		"sid": "sess_2abc",
		"azp": testOrigin,
		"iat": now.Add(-5 * time.Second).Unix(),
		"nbf": now.Add(-5 * time.Second).Unix(),
		"exp": now.Add(55 * time.Second).Unix(),
		"v":   2,
		"o":   map[string]any{"id": "org_2abc", "rol": "admin", "slg": "fit-studio"},
	}
	for k, v := range overrides {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

func (f *fixture) sign(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *fixture) token(t *testing.T, overrides map[string]any) string {
	return f.sign(t, f.key, f.kid(), f.claims(overrides))
}

type fakeResolver struct {
	got   []Identity
	ret   Principal
	err   error
	calls int
}

func (r *fakeResolver) Resolve(_ context.Context, id Identity) (Principal, error) {
	r.calls++
	r.got = append(r.got, id)
	return r.ret, r.err
}

func testPrincipal() Principal {
	ws := ids.WorkspaceID{UUID: uuid.Must(uuid.NewV7())}
	us := ids.UserID{UUID: uuid.Must(uuid.NewV7())}
	return Principal{UserID: us, Email: "ada@example.com", WorkspaceID: ws, WorkspaceName: "Fit Studio", Role: store.RoleOwner}
}

func (f *fixture) handler(t *testing.T, res Resolver) http.Handler {
	t.Helper()
	mw, err := NewMiddleware(MiddlewareConfig{AppOrigin: testOrigin, JWKSClient: &jwks.Client{Backend: f.backend}}, res)
	if err != nil {
		t.Fatal(err)
	}
	return mw(MeHandler())
}

func get(h http.Handler, authz string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type errBody struct {
	Error struct{ Code, Message string } `json:"error"`
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var b errBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body %q is not the error shape: %v", rec.Body.String(), err)
	}
	if rec.Code != status || b.Error.Code != code {
		t.Fatalf("got %d %q (%s), want %d %q", rec.Code, b.Error.Code, b.Error.Message, status, code)
	}
	if b.Error.Message == "" {
		t.Error("error message is empty")
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestMiddlewareAcceptsAValidToken(t *testing.T) {
	f := newFixture(t)
	res := &fakeResolver{ret: testPrincipal()}
	rec := get(f.handler(t, res), "Bearer "+f.token(t, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if res.calls != 1 {
		t.Fatalf("resolver calls = %d", res.calls)
	}
	id := res.got[0]
	if id.ClerkUserID != "user_2abc" || id.ClerkOrgID != "org_2abc" || id.OrgRole != "org:admin" || id.IssuedAtMillis == 0 || id.IssuedAtMillis%1000 != 0 {
		t.Errorf("identity = %+v", id)
	}

	// The body is the /v1/me shape: TypeIDs, the email, the name and the role.
	var me struct {
		User      struct{ ID, Email string }
		Workspace struct{ ID, Name, Role string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	p := res.ret
	if me.User.ID != p.UserID.String() || !strings.HasPrefix(me.User.ID, "usr_") || me.User.Email != "ada@example.com" ||
		me.Workspace.ID != p.WorkspaceID.String() || !strings.HasPrefix(me.Workspace.ID, "wks_") || me.Workspace.Name != "Fit Studio" || me.Workspace.Role != "owner" {
		t.Errorf("me = %+v", me)
	}
}

func TestMiddlewareAcceptsAVersion1Token(t *testing.T) {
	f := newFixture(t)
	res := &fakeResolver{ret: testPrincipal()}
	tok := f.token(t, map[string]any{"v": nil, "o": nil, "org_id": "org_v1", "org_role": "org:member"})
	if rec := get(f.handler(t, res), "Bearer "+tok); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if id := res.got[0]; id.ClerkOrgID != "org_v1" || id.OrgRole != "org:member" {
		t.Errorf("identity = %+v", id)
	}
}

func TestMiddlewareRefusesBadTokens(t *testing.T) {
	f := newFixture(t)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	hs256 := func() string { // alg confusion: an HS256 token "signed" with the public key as the secret
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("not-the-private-key")}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", f.kid()))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jwt.Signed(signer).Claims(f.claims(nil)).CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}()
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	payload, _ := json.Marshal(f.claims(nil))
	algNone := b64(`{"alg":"none","typ":"JWT","kid":"`+f.kid()+`"}`) + "." + b64(string(payload)) + "."

	tests := []struct {
		name  string
		token string
	}{
		{"expired", f.token(t, map[string]any{"exp": now.Add(-time.Minute).Unix(), "iat": now.Add(-2 * time.Minute).Unix(), "nbf": now.Add(-2 * time.Minute).Unix()})},
		{"expired just past the leeway", f.token(t, map[string]any{"exp": now.Add(-DefaultLeeway - 2*time.Second).Unix()})},
		{"not valid yet", f.token(t, map[string]any{"nbf": now.Add(time.Minute).Unix()})},
		{"issued in the future", f.token(t, map[string]any{"iat": now.Add(time.Hour).Unix()})},
		{"no expiry", f.token(t, map[string]any{"exp": nil})},
		{"azp of another origin", f.token(t, map[string]any{"azp": "http://evil.example"})},
		{"azp of another port", f.token(t, map[string]any{"azp": "http://localhost:3001"})},
		{"azp with a trailing slash", f.token(t, map[string]any{"azp": testOrigin + "/"})},
		{"no azp", f.token(t, map[string]any{"azp": nil})},
		{"empty azp", f.token(t, map[string]any{"azp": ""})},
		{"signed by another key", f.sign(t, otherKey, f.kid(), f.claims(nil))},
		{"unknown key id", f.sign(t, f.key, "ins_unknown", f.claims(nil))},
		{"issuer that is not Clerk", f.token(t, map[string]any{"iss": "https://evil.example"})},
		{"HS256 instead of RS256", hs256},
		{"alg none", algNone},
		{"tampered payload", func() string {
			parts := strings.Split(f.token(t, nil), ".")
			other, _ := json.Marshal(f.claims(map[string]any{"sub": "user_victim"}))
			return parts[0] + "." + base64.RawURLEncoding.EncodeToString(other) + "." + parts[2]
		}()},
		{"garbage", "not.a.jwt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := &fakeResolver{ret: testPrincipal()}
			rec := get(f.handler(t, res), "Bearer "+tc.token)
			wantError(t, rec, http.StatusUnauthorized, "invalid_token")
			if res.calls != 0 {
				t.Error("the resolver ran for a token that failed verification")
			}
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("no WWW-Authenticate header")
			}
		})
	}
}

func TestMiddlewareAllowsSmallClockSkew(t *testing.T) {
	f := newFixture(t)
	res := &fakeResolver{ret: testPrincipal()}
	// Expired two seconds ago: inside the five-second leeway.
	tok := f.token(t, map[string]any{"exp": time.Now().Add(-2 * time.Second).Unix()})
	if rec := get(f.handler(t, res), "Bearer "+tok); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMiddlewareRefusesMissingCredentials(t *testing.T) {
	f := newFixture(t)
	tok := f.token(t, nil)
	for name, authz := range map[string]string{
		"no header":          "",
		"empty bearer":       "Bearer ",
		"basic scheme":       "Basic " + tok,
		"token with no word": tok, // the SDK's default extractor would take this
		"two words":          "Bearer a b",
	} {
		t.Run(name, func(t *testing.T) {
			res := &fakeResolver{ret: testPrincipal()}
			rec := get(f.handler(t, res), authz)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			var b errBody
			_ = json.Unmarshal(rec.Body.Bytes(), &b)
			if b.Error.Code != "unauthenticated" && b.Error.Code != "invalid_token" {
				t.Errorf("code = %q", b.Error.Code)
			}
			if res.calls != 0 {
				t.Error("the resolver ran")
			}
		})
	}
	// No header at all is the plain "unauthenticated".
	wantError(t, get(f.handler(t, &fakeResolver{}), ""), http.StatusUnauthorized, "unauthenticated")
}

func TestMiddlewareAcceptsLowercaseBearerScheme(t *testing.T) {
	f := newFixture(t)
	res := &fakeResolver{ret: testPrincipal()}
	if rec := get(f.handler(t, res), "bearer "+f.token(t, nil)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMiddlewareRequiresAnActiveOrganization(t *testing.T) {
	f := newFixture(t)
	res := &fakeResolver{ret: testPrincipal()}
	// A signed-in user whose session has no active organization (a pending "choose an organization" task).
	tok := f.token(t, map[string]any{"o": nil})
	rec := get(f.handler(t, res), "Bearer "+tok)
	wantError(t, rec, http.StatusForbidden, "no_active_workspace")
	if res.calls != 0 {
		t.Error("the resolver ran without an organization")
	}
	// Same for an organization claim with an empty id.
	wantError(t, get(f.handler(t, res), "Bearer "+f.token(t, map[string]any{"o": map[string]any{"id": "", "rol": "admin"}})), http.StatusForbidden, "no_active_workspace")
	wantError(t, get(f.handler(t, res), "Bearer "+f.token(t, map[string]any{"v": nil, "o": nil, "org_id": ""})), http.StatusForbidden, "no_active_workspace")
}

func TestMiddlewareWorkspaceComesFromTheTokenOnly(t *testing.T) {
	f := newFixture(t)
	res := &fakeResolver{ret: testPrincipal()}
	rec := get(f.handler(t, res), "Bearer "+f.token(t, nil), func(r *http.Request) {
		r.Header.Set("X-Workspace-Id", "wks_attacker")
		r.Header.Set("X-Clerk-Org-Id", "org_attacker")
		q := r.URL.Query()
		q.Set("workspace_id", "wks_attacker")
		q.Set("org_id", "org_attacker")
		r.URL.RawQuery = q.Encode()
	})
	if rec.Code != http.StatusOK || len(res.got) != 1 || res.got[0].ClerkOrgID != "org_2abc" {
		t.Fatalf("status = %d, identities = %+v", rec.Code, res.got)
	}
}

func TestMiddlewareMapsResolverErrors(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{ErrUserDeleted, http.StatusForbidden, "user_deleted"},
		{ErrWorkspaceDeleted, http.StatusForbidden, "workspace_deleted"},
		{ErrDirectoryUnavailable, http.StatusServiceUnavailable, "identity_unavailable"},
		{errors.New("pq: password authentication failed for user cf_app"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range tests {
		rec := get(f.handler(t, &fakeResolver{err: tc.err}), "Bearer "+f.token(t, nil))
		wantError(t, rec, tc.status, tc.code)
		if strings.Contains(rec.Body.String(), "password") {
			t.Errorf("the response leaks the internal error: %s", rec.Body)
		}
	}
}

func TestMeHandlerWithoutMiddlewareIsNotSignedIn(t *testing.T) {
	rec := httptest.NewRecorder()
	MeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	wantError(t, rec, http.StatusUnauthorized, "unauthenticated")
}

func TestNotConfiguredMiddlewareFailsClosed(t *testing.T) {
	called := false
	h := NotConfigured()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	rec := get(h, "Bearer anything")
	wantError(t, rec, http.StatusServiceUnavailable, "auth_not_configured")
	if called {
		t.Error("the protected handler ran")
	}
}

func TestNewMiddlewareValidatesItsConfig(t *testing.T) {
	if _, err := NewMiddleware(MiddlewareConfig{SecretKey: "sk_test_x"}, &fakeResolver{}); err == nil {
		t.Error("no app origin: want an error, since every azp would be accepted or refused arbitrarily")
	}
	if _, err := NewMiddleware(MiddlewareConfig{AppOrigin: testOrigin}, &fakeResolver{}); err == nil {
		t.Error("no key source: want an error")
	}
}

func TestMapRole(t *testing.T) {
	for in, want := range map[string]store.Role{
		"org:admin": store.RoleOwner, "admin": store.RoleOwner,
		"org:member": store.RoleEditor, "member": store.RoleEditor,
		"org:billing": store.RoleViewer, "": store.RoleViewer, "org:ADMIN": store.RoleViewer, "owner": store.RoleViewer,
	} {
		if got := MapRole(in); got != want {
			t.Errorf("MapRole(%q) = %q, want %q", in, got, want)
		}
	}
}
