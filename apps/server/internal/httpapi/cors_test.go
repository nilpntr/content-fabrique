package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testAppOrigin = "http://localhost:3000"

func corsHandler(t *testing.T) (http.Handler, *int) {
	t.Helper()
	calls := new(int)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		WriteJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
	})
	return CORS(testAppOrigin, nil)(next), calls
}

func do(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// corsHeaders are the response headers that would let a browser share a response with a page.
var corsHeaders = []string{
	"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers",
	"Access-Control-Allow-Credentials", "Access-Control-Max-Age", "Access-Control-Expose-Headers",
}

func assertNoCORS(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, name := range corsHeaders {
		if v := rec.Header().Get(name); v != "" {
			t.Errorf("%s = %q, want none", name, v)
		}
	}
}

func TestCORSAllowedOrigin(t *testing.T) {
	h, calls := corsHandler(t)

	rec := do(h, http.MethodGet, "/v1/me", map[string]string{"Origin": testAppOrigin})
	if rec.Code != http.StatusOK || *calls != 1 {
		t.Fatalf("status = %d, calls = %d", rec.Code, *calls)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != testAppOrigin {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Allow-Credentials = %q: the API uses bearer tokens, never cookies", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
		t.Errorf("Vary = %q, want Origin", rec.Header().Get("Vary"))
	}
}

func TestCORSPreflightAllowed(t *testing.T) {
	h, calls := corsHandler(t)
	rec := do(h, http.MethodOptions, "/v1/me", map[string]string{
		"Origin":                         testAppOrigin,
		"Access-Control-Request-Method":  "GET",
		"Access-Control-Request-Headers": "authorization",
	})
	if rec.Code != http.StatusNoContent || *calls != 0 {
		t.Fatalf("status = %d, handler calls = %d, want 204 and the handler not reached", rec.Code, *calls)
	}
	want := map[string]string{
		"Access-Control-Allow-Origin":  testAppOrigin,
		"Access-Control-Allow-Methods": "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		"Access-Control-Allow-Headers": "Authorization, Content-Type",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("credentials must not be allowed")
	}
	if v := rec.Header().Values("Vary"); !strings.Contains(strings.Join(v, ","), "Origin") {
		t.Errorf("Vary = %v", v)
	}
}

func TestCORSRefusesEveryOtherOrigin(t *testing.T) {
	others := []string{
		"http://evil.example",
		"https://localhost:3000",          // other scheme
		"http://localhost:3001",           // other port
		"http://localhost",                // no port
		"http://localhost:3000.evil.test", // prefix match
		"http://LOCALHOST:3000",           // not what a browser sends; no case folding
		"http://localhost:3000/",          // trailing slash
		"null",                            // sandboxed iframes, file:// pages
		"*",
		"",
	}
	for _, origin := range others {
		t.Run("simple "+origin, func(t *testing.T) {
			h, calls := corsHandler(t)
			headers := map[string]string{}
			if origin != "" {
				headers["Origin"] = origin
			}
			rec := do(h, http.MethodGet, "/v1/me", headers)
			if *calls != 1 { // CORS does not block the request, the browser hides the answer
				t.Errorf("handler calls = %d", *calls)
			}
			assertNoCORS(t, rec)
			if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
				t.Errorf("Vary = %q: a cache must still key on Origin", rec.Header().Get("Vary"))
			}
		})
		t.Run("preflight "+origin, func(t *testing.T) {
			h, calls := corsHandler(t)
			headers := map[string]string{"Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization"}
			if origin != "" {
				headers["Origin"] = origin
			}
			rec := do(h, http.MethodOptions, "/v1/me", headers)
			if rec.Code != http.StatusForbidden || *calls != 0 {
				t.Errorf("status = %d, handler calls = %d, want 403 and the handler not reached", rec.Code, *calls)
			}
			assertNoCORS(t, rec)
		})
	}
}

func TestCORSPreflightOfUnknownMethodIsRefused(t *testing.T) {
	h, _ := corsHandler(t)
	rec := do(h, http.MethodOptions, "/v1/me", map[string]string{"Origin": testAppOrigin, "Access-Control-Request-Method": "TRACE"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d", rec.Code)
	}
	assertNoCORS(t, rec)
}

func TestCORSPreflightRefusesUnlistedRequestHeaders(t *testing.T) {
	for _, requested := range []string{"x-custom", "authorization, x-custom", "content-type,idempotency-key", "Authorization,Content-Type,Cookie"} {
		var logs bytes.Buffer
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
		h := CORS(testAppOrigin, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))(next)
		rec := do(h, http.MethodOptions, "/v1/me", map[string]string{
			"Origin": testAppOrigin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": requested,
		})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%q: status = %d, want 403", requested, rec.Code)
		}
		assertNoCORS(t, rec)
		if !strings.Contains(logs.String(), "header not allowed") {
			t.Errorf("%q: the refusal was not logged at debug: %q", requested, logs.String())
		}
	}
	// Listed headers, in any case and spacing, pass.
	h, _ := corsHandler(t)
	for _, requested := range []string{"authorization", "Authorization, Content-Type", "CONTENT-TYPE,authorization", ""} {
		rec := do(h, http.MethodOptions, "/v1/me", map[string]string{
			"Origin": testAppOrigin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": requested,
		})
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != testAppOrigin {
			t.Errorf("%q: status = %d", requested, rec.Code)
		}
	}
}

func TestCORSWithoutConfiguredOriginAllowsNobody(t *testing.T) {
	h := CORS("", nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	assertNoCORS(t, do(h, http.MethodGet, "/", map[string]string{"Origin": ""}))
	rec := do(h, http.MethodOptions, "/", map[string]string{"Access-Control-Request-Method": "GET"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d", rec.Code)
	}
	assertNoCORS(t, rec)
}

func TestCORSErrorsAreReadableByTheAllowedOrigin(t *testing.T) {
	// A 401 or 403 from behind the middleware must carry the header too, or the web app could not read the code.
	h := CORS(testAppOrigin, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusUnauthorized, "unauthenticated", "sign in")
	}))
	rec := do(h, http.MethodGet, "/v1/me", map[string]string{"Origin": testAppOrigin})
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Access-Control-Allow-Origin") != testAppOrigin {
		t.Errorf("status = %d, Allow-Origin = %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}
