package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// fakeEnts is an EntitlementReader that answers from a field. The zero value is a workspace with no plan.
type fakeEnts struct {
	e     store.Entitlements
	err   error
	calls int
	asked []ids.WorkspaceID
}

func (f *fakeEnts) GetEntitlements(_ context.Context, ws ids.WorkspaceID) (store.Entitlements, error) {
	f.calls++
	f.asked = append(f.asked, ws)
	if f.err != nil {
		return store.Entitlements{}, f.err
	}
	e := f.e
	e.WorkspaceID = ws
	if e.Plan == "" {
		e.Plan, e.Status = plans.None, store.StatusNone
	}
	return e, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// served runs the guard in front of a handler that records whether it was reached.
func served(t *testing.T, ents EntitlementReader, withPrincipal bool) (rec *httptest.ResponseRecorder, reached bool, p Principal) {
	t.Helper()
	p = testPrincipal()
	h := RequireActivePlan(ents, quiet())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/library/summary", nil)
	if withPrincipal {
		req = req.WithContext(ContextWithPrincipal(req.Context(), p))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, reached, p
}

func TestRequireActivePlan(t *testing.T) {
	future := time.Now().Add(time.Hour)

	t.Run("no plan is 402 with a stable code", func(t *testing.T) {
		rec, reached, _ := served(t, &fakeEnts{}, true)
		wantError(t, rec, http.StatusPaymentRequired, "plan_required")
		if reached {
			t.Error("the paid handler ran for a workspace without a plan")
		}
	})
	t.Run("an expired or inactive plan is 402", func(t *testing.T) {
		// The store decides "active" with the database clock; the guard believes it and nothing else.
		past := time.Now().Add(-time.Hour)
		for name, e := range map[string]store.Entitlements{
			"expired":  {Plan: plans.Creator, Status: store.StatusActive, CurrentPeriodEnd: &past, Active: false},
			"past due": {Plan: plans.Creator, Status: store.StatusPastDue, Active: false},
			"canceled": {Plan: plans.None, Status: store.StatusCanceled, Active: false},
		} {
			rec, reached, _ := served(t, &fakeEnts{e: e}, true)
			wantError(t, rec, http.StatusPaymentRequired, "plan_required")
			if reached {
				t.Errorf("%s: the paid handler ran", name)
			}
		}
	})
	t.Run("an active plan passes, and the lookup is for the principal's workspace", func(t *testing.T) {
		ents := &fakeEnts{e: store.Entitlements{Plan: plans.Creator, Status: store.StatusActive, CurrentPeriodEnd: &future, Active: true}}
		rec, reached, p := served(t, ents, true)
		if rec.Code != http.StatusOK || !reached {
			t.Fatalf("status = %d, reached = %v", rec.Code, reached)
		}
		if len(ents.asked) != 1 || ents.asked[0] != p.WorkspaceID {
			t.Errorf("asked about %v, want only %v", ents.asked, p.WorkspaceID)
		}
	})
	t.Run("a failing lookup is 500, never a pass", func(t *testing.T) {
		rec, reached, _ := served(t, &fakeEnts{err: errors.New("connection refused: password=hunter2")}, true)
		wantError(t, rec, http.StatusInternalServerError, "internal_error")
		if reached {
			t.Error("the paid handler ran although the plan could not be read")
		}
		if strings.Contains(rec.Body.String(), "hunter2") {
			t.Errorf("the response leaks the error: %s", rec.Body)
		}
	})
	t.Run("without a principal it is 401 and does not even look", func(t *testing.T) {
		ents := &fakeEnts{e: store.Entitlements{Plan: plans.Pro, Status: store.StatusActive, Active: true}}
		rec, reached, _ := served(t, ents, false)
		wantError(t, rec, http.StatusUnauthorized, "unauthenticated")
		if reached || ents.calls != 0 {
			t.Errorf("reached = %v, lookups = %d", reached, ents.calls)
		}
	})
}

func TestMeCarriesTheEntitlements(t *testing.T) {
	f := newFixture(t)
	end := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	for name, tc := range map[string]struct {
		ents *fakeEnts
		want string
	}{
		"no plan":     {&fakeEnts{}, `"entitlements":{"plan":"none","status":"none","active":false,"current_period_end":null}`},
		"with a plan": {&fakeEnts{e: store.Entitlements{Plan: plans.Creator, Status: store.StatusActive, Active: true, CurrentPeriodEnd: &end}}, `"entitlements":{"plan":"creator","status":"active","active":true,"current_period_end":"2026-12-31T23:59:59Z"}`},
	} {
		mw, err := NewMiddleware(MiddlewareConfig{AppOrigin: testOrigin, JWKSClient: &jwks.Client{Backend: f.backend}}, &fakeResolver{ret: testPrincipal()})
		if err != nil {
			t.Fatal(err)
		}
		rec := get(mw(MeHandler(tc.ents, quiet())), "Bearer "+f.token(t, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s, want it to contain %s", name, rec.Code, rec.Body, tc.want)
		}
	}

	mw, err := NewMiddleware(MiddlewareConfig{AppOrigin: testOrigin, JWKSClient: &jwks.Client{Backend: f.backend}}, &fakeResolver{ret: testPrincipal()})
	if err != nil {
		t.Fatal(err)
	}
	wantError(t, get(mw(MeHandler(&fakeEnts{err: errors.New("boom")}, quiet())), "Bearer "+f.token(t, nil)), http.StatusInternalServerError, "internal_error")
}

// The whole API path with the real Syncer and Postgres and locally signed tokens: the paywall, from a missing plan to a
// grant, a revoke and an expiry, for two workspaces.
func TestMountedPaywall(t *testing.T) {
	ctx := context.Background()
	st, dir, f := newStore(t), newFakeDir(), newFixture(t)
	dir.users["user_ada"] = ClerkUser{ID: "user_ada", Email: "ada@example.com", UpdatedAt: 1}
	dir.users["user_bob"] = ClerkUser{ID: "user_bob", Email: "bob@example.com", UpdatedAt: 1}
	dir.orgs["org_ada"] = ClerkOrg{ID: "org_ada", Name: "Ada Studio", UpdatedAt: 1}
	dir.orgs["org_bob"] = ClerkOrg{ID: "org_bob", Name: "Bob Studio", UpdatedAt: 1}

	mux := http.NewServeMux()
	if _, err := Mount(mux, Deps{
		Config:     config.Config{AppOrigin: testOrigin, ClerkSecretKey: "sk_test_unused", ClerkWebhookSecret: testWebhookSecret},
		Store:      st,
		Directory:  dir,
		JWKSClient: &jwks.Client{Backend: f.backend},
	}); err != nil {
		t.Fatal(err)
	}
	// A token for a user acting in an organization.
	token := func(user, org string) string {
		return f.token(t, map[string]any{"sub": user, "o": map[string]any{"id": org, "rol": "admin", "slg": "x"}})
	}
	call := func(path, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	ada, bob := token("user_ada", "org_ada"), token("user_bob", "org_bob")

	// No plan anywhere: the paid endpoint is 402, the allowlisted ones answer and say why.
	wantError(t, call("/v1/library/summary", ada), http.StatusPaymentRequired, "plan_required")
	wantError(t, call("/v1/library/summary", bob), http.StatusPaymentRequired, "plan_required")
	rec := call("/v1/me", ada)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"active":false`) || !strings.Contains(rec.Body.String(), `"plan":"none"`) {
		t.Fatalf("/v1/me without a plan: %d %s", rec.Code, rec.Body)
	}
	rec = call("/v1/workspace/plan", ada)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"active":false`) || !strings.Contains(rec.Body.String(), `"limits":{"exports":0,"storage_gb":0,"ai_billing":""}`) {
		t.Fatalf("/v1/workspace/plan without a plan: %d %s", rec.Code, rec.Body)
	}
	// Authentication comes first: no token is 401, not 402.
	wantError(t, call("/v1/library/summary", ""), http.StatusUnauthorized, "unauthenticated")

	wsA, err := st.GetWorkspaceByClerkOrgID(ctx, "org_ada")
	if err != nil {
		t.Fatal(err)
	}

	// Granting Ada's workspace unlocks it at once (no reload of the server, no cache), and only it.
	if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: wsA.ID, Plan: plans.Creator}); err != nil {
		t.Fatal(err)
	}
	rec = call("/v1/library/summary", ada)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"assets":0,"projects":0}` {
		t.Fatalf("/v1/library/summary with a plan: %d %s", rec.Code, rec.Body)
	}
	rec = call("/v1/me", ada)
	if !strings.Contains(rec.Body.String(), `"entitlements":{"plan":"creator","status":"active","active":true,"current_period_end":null}`) {
		t.Fatalf("/v1/me with a plan: %s", rec.Body)
	}
	rec = call("/v1/workspace/plan", ada)
	if !strings.Contains(rec.Body.String(), `"limits":{"exports":15,"storage_gb":50,"ai_billing":"managed"}`) || !strings.Contains(rec.Body.String(), `"source":"manual"`) {
		t.Fatalf("/v1/workspace/plan with a plan: %s", rec.Body)
	}
	wantError(t, call("/v1/library/summary", bob), http.StatusPaymentRequired, "plan_required")

	// A user who is in both workspaces gets whatever the active workspace of the token has.
	if rec := call("/v1/library/summary", token("user_ada", "org_bob")); rec.Code == http.StatusOK {
		t.Fatalf("Ada acting in Bob's workspace got in: %d %s", rec.Code, rec.Body)
	}

	// Revoked: 402 again, with the same token.
	if _, err := st.RevokePlan(ctx, wsA.ID, "", "test"); err != nil {
		t.Fatal(err)
	}
	wantError(t, call("/v1/library/summary", ada), http.StatusPaymentRequired, "plan_required")
	// Expired: 402 as well.
	past := time.Now().Add(-time.Minute)
	if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: wsA.ID, Plan: plans.Pro, PeriodEnd: &past}); err != nil {
		t.Fatal(err)
	}
	wantError(t, call("/v1/library/summary", ada), http.StatusPaymentRequired, "plan_required")
	rec = call("/v1/me", ada)
	if !strings.Contains(rec.Body.String(), `"active":false`) || !strings.Contains(rec.Body.String(), `"plan":"pro"`) {
		t.Fatalf("/v1/me with an expired plan: %s", rec.Body)
	}

	// The body of a 402 says nothing about the plan or the workspace.
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(call("/v1/library/summary", ada).Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body.Error.Message, "Ada") || strings.Contains(body.Error.Message, "pro") {
		t.Errorf("the 402 message leaks: %q", body.Error.Message)
	}
}

// The webhook is not behind the session or the paywall: a workspace without a plan still gets its Clerk events.
func TestWebhookIsNotPaywalled(t *testing.T) {
	st := newStore(t)
	mux := http.NewServeMux()
	if _, err := Mount(mux, deps(st, testWebhookSecret)); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/webhooks/clerk", strings.NewReader("{}")))
	// Refused for its missing signature (400), not for a plan (402) or a session (401).
	if rec.Code == http.StatusPaymentRequired || rec.Code == http.StatusUnauthorized {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

// Without CLERK_SECRET_KEY the paid route fails closed too (503), never open.
func TestPaidRouteWithoutClerkFailsClosed(t *testing.T) {
	st := newStore(t)
	mux := http.NewServeMux()
	if _, err := Mount(mux, Deps{Config: config.Config{AppOrigin: testOrigin}, Store: st}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/library/summary", "/v1/workspace/plan", "/v1/me"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer anything")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		wantError(t, rec, http.StatusServiceUnavailable, "auth_not_configured")
	}
}
