package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The error codes are the generated constants, so a code the spec does not list cannot compile. This checks the values
// too: the ones the server answers with are in the ErrorCode enum.
func TestEveryErrorCodeIsInTheSpec(t *testing.T) {
	codes := []api.ErrorCode{
		CodeUnauthenticated, CodeInvalidToken, CodeNoActiveWorkspace, CodeUserDeleted, CodeWorkspaceDeleted, CodeNotMember,
		CodeIdentityProvider, CodeNotConfigured, CodeQueueNotMigrated, CodeInternal, CodeWebhookNotConfigured, CodeInvalidRequest,
		CodeInvalidSignature, CodeInvalidEvent, CodePayloadTooLarge, CodePlanRequired,
		api.ErrorCodeOriginNotAllowed, // internal/httpapi/cors.go
	}
	for _, c := range codes {
		if !c.Valid() {
			t.Errorf("error code %q is not in the ErrorCode enum of packages/schemas/openapi.yaml", c)
		}
	}
}

// The handlers convert the store's and the plan catalogue's values to the generated enums with plain casts. A value the spec
// does not list would go out on the wire unchecked, so every constant must be a member of its enum (and the other way for
// the enums the server owns).
func TestEnumCastsAreValid(t *testing.T) {
	for _, p := range append([]plans.Plan{plans.None}, plans.Granted...) {
		if !api.PlanName(p).Valid() {
			t.Errorf("plan %q is not in the PlanName enum", p)
		}
		if !p.Valid() {
			t.Errorf("plan %q is not a plans.Plan", p)
		}
	}
	for _, s := range []store.PlanStatus{store.StatusActive, store.StatusPastDue, store.StatusCanceled, store.StatusNone, store.StatusUnknown} {
		if !api.PlanStatus(s).Valid() {
			t.Errorf("plan status %q is not in the PlanStatus enum", s)
		}
	}
	for _, s := range []store.PlanSource{store.SourceManual, store.SourceMerchant, store.SourceNone} {
		if !api.PlanSource(s).Valid() {
			t.Errorf("plan source %q is not in the PlanSource enum", s)
		}
	}
	for _, r := range []store.Role{store.RoleOwner, store.RoleEditor, store.RoleViewer} {
		if !api.Role(r).Valid() {
			t.Errorf("role %q is not in the Role enum", r)
		}
	}
	// "" is what a workspace without an active plan gets (the zero plans.Limits).
	for _, b := range []plans.AIBilling{plans.BilledByUs, plans.BilledByCreator, plans.LimitsOf(plans.None).AIBilling} {
		if !api.AiBilling(b).Valid() {
			t.Errorf("AI billing %q is not in the AiBilling enum", b)
		}
	}
	// The enums of the spec that the server owns list nothing the server does not know.
	for _, v := range []api.PlanName{api.PlanNameNone, api.PlanNameTrial, api.PlanNameStudio, api.PlanNameCreator, api.PlanNamePro} {
		if !plans.Plan(v).Valid() {
			t.Errorf("the spec's plan %q is not a plan of internal/plans", v)
		}
	}
	for _, v := range []api.Role{api.RoleOwner, api.RoleEditor, api.RoleViewer} {
		switch store.Role(v) {
		case store.RoleOwner, store.RoleEditor, store.RoleViewer:
		default:
			t.Errorf("the spec's role %q is not a store.Role", v)
		}
	}
}

// The operations the spec opens to a workspace without a plan are the ones Mount puts behind authentication alone; every
// other /v1 operation of the spec is paid-only and documents its 402 plan_required answer, so a client and a reader of the
// contract see the paywall. The webhook is not an operation of the generated spec.
func TestSpecOperationsAreOpenOrDocumentThePaywall(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	open, err := openOperations()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"GET /v1/me", "GET /v1/workspace/plan"}; !slices.Equal(sortedKeys(open), want) {
		t.Errorf("operations open without a plan = %v, want %v (opening another one is a decision: change the test with it)", sortedKeys(open), want)
	}

	routes := apiRoutes(newAPIServer(&fakeEnts{}, quiet()), quiet())
	seen := map[string]bool{}
	for path, item := range spec.Paths.Map() {
		if !strings.HasPrefix(path, "/v1/") {
			t.Errorf("%s is not under /v1/, so V1.Handle would refuse it", path)
		}
		for method, op := range item.Operations() {
			pattern := method + " " + path
			seen[pattern] = true
			if routes[pattern] == nil {
				t.Errorf("%s is in the spec but the generated code did not register it", pattern)
			}
			r := op.Responses.Value("402")
			switch {
			case open[pattern] && r != nil:
				t.Errorf("%s is open without a plan but documents a 402", pattern)
			case !open[pattern] && (r == nil || r.Ref != "#/components/responses/PlanRequired"):
				t.Errorf("%s is paid-only but does not document the 402 PlanRequired response", pattern)
			}
		}
	}
	for pattern := range routes {
		if !seen[pattern] {
			t.Errorf("%s is registered but not in the spec", pattern)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
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

// noResponseServer is a server whose GetMe forgets to answer.
type noResponseServer struct{ api.StrictServerInterface }

func (noResponseServer) GetMe(context.Context, api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	return nil, nil
}

// The generated strict handler answers an empty 200 when a handler returns neither a response nor an error. That must be
// a logged 500, never a success.
func TestAHandlerWithoutAResponseIsA500(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	rec := httptest.NewRecorder()
	apiRoutes(noResponseServer{}, logger)["GET /v1/me"].ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	wantError(t, rec, http.StatusInternalServerError, "internal_error")
	if !strings.Contains(logs.String(), "neither a response nor an error") || !strings.Contains(logs.String(), "GetMe") {
		t.Errorf("the failure was not logged with its operation: %s", logs.String())
	}
}
