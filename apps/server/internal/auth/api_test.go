package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
)

// Every error code the server can answer with is documented in the OpenAPI spec (api.ErrorCode is generated from its
// enum). A new code is added to the spec first, then to the list below.
func TestEveryErrorCodeIsInTheSpec(t *testing.T) {
	codes := []string{
		CodeUnauthenticated, CodeInvalidToken, CodeNoActiveWorkspace, CodeUserDeleted, CodeWorkspaceDeleted, CodeNotMember,
		CodeIdentityProvider, CodeNotConfigured, CodeQueueNotMigrated, CodeInternal, CodeWebhookNotConfigured, CodeInvalidRequest,
		CodeInvalidSignature, CodeInvalidEvent, CodePayloadTooLarge, CodePlanRequired,
		"origin_not_allowed", // internal/httpapi/cors.go
	}
	for _, c := range codes {
		if !api.ErrorCode(c).Valid() {
			t.Errorf("error code %q is not in the ErrorCode enum of packages/schemas/openapi.yaml", c)
		}
	}
}

// The allowlist can only name operations the spec has, and a route the spec has is registered by Mount.
func TestAllowlistNamesSpecOperations(t *testing.T) {
	routes := apiRoutes(newAPIServer(&fakeEnts{}, quiet()))
	for pattern := range openWithoutPlan {
		if routes[pattern] == nil {
			t.Errorf("%q is on the allowlist but is not an operation of the spec", pattern)
		}
	}
	if len(routes) == 0 {
		t.Fatal("the generated code registered no routes")
	}
}

// Answers of the generated routes are never cached, as before the API was generated.
func TestGeneratedRoutesAreNotCacheable(t *testing.T) {
	rec := httptest.NewRecorder()
	MeHandler(&fakeEnts{}, quiet()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}
