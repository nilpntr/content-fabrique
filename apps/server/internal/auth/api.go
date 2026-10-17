package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/assets"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
)

// apiServer implements api.StrictServerInterface, the generated interface of the OpenAPI contract
// (packages/schemas/openapi.yaml). The handlers live next to what they report: GetMe in me.go, GetWorkspacePlan and
// GetLibrarySummary in entitlements.go, and the assets operations come from the embedded *assets.Handler (package assets,
// which reads the workspace from the Principal through callerOf). The Clerk webhook is not here: it reads the raw body and the Svix headers
// itself (webhook.go), so the spec documents it and the generator leaves it out.
type apiServer struct {
	*assets.Handler
	ents   EntitlementReader
	logger *slog.Logger
}

var _ api.StrictServerInterface = (*apiServer)(nil)

func newAPIServer(ents EntitlementReader, logger *slog.Logger) *apiServer {
	if logger == nil {
		logger = slog.Default()
	}
	// Without a configured object store the assets operations answer 503 storage_not_configured (Mount replaces this
	// handler when it is given a service).
	return &apiServer{Handler: assets.NewHandler(nil, callerOf, logger), ents: ents, logger: logger}
}

// callerOf is how the assets package learns who is calling: the Principal the authentication middleware resolved from the
// verified token. Nothing else names the workspace.
func callerOf(ctx context.Context) (assets.Caller, bool) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return assets.Caller{}, false
	}
	return assets.Caller{Workspace: p.WorkspaceID, User: p.UserID, Role: p.Role}, true
}

// maxBodyBytes caps the body of every API request. The largest legitimate one is the completion of a 10,000 part upload
// (about 0.7 MB); a bigger body is refused (the generated binder answers 400 invalid_request) before it is buffered.
const maxBodyBytes = 2 << 20

// routeTable collects what the generated code registers, so Mount decides which middleware each route sits behind. It
// is the api.ServeMux the generated HandlerWithOptions registers on; nothing is ever served from the table itself.
type routeTable map[string]http.Handler

// HandleFunc implements api.ServeMux. Every answer of the API is uncacheable, which the generated writer does not say.
func (t routeTable) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	t[pattern] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		h(w, r)
	})
}

// ServeHTTP implements api.ServeMux and is never called.
func (routeTable) ServeHTTP(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }

// errNoResponse is what a handler that returned neither a response nor an error becomes. The generated strict handler
// would answer such a call with an empty 200, which for an API that must never answer as if something succeeded is the
// wrong failure mode.
var errNoResponse = errors.New("the handler returned neither a response nor an error")

// requireResponse is the strict middleware that turns "no response and no error" into an error, which the generated error
// handler logs and answers 500 internal_error.
func requireResponse(next api.StrictHandlerFunc, operationID string) api.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		response, err := next(ctx, w, r, request)
		if err == nil && response == nil {
			return nil, fmt.Errorf("%s: %w", operationID, errNoResponse)
		}
		return response, err
	}
}

// apiRoutes returns the handlers the generated code builds for srv (logging to logger), by their ServeMux pattern ("GET /v1/me"). Errors the
// generated code meets (a request it cannot bind, a response it cannot write, a handler that failed) use the standard
// error body.
func apiRoutes(srv api.StrictServerInterface, logger *slog.Logger) routeTable {
	badRequest := func(w http.ResponseWriter, r *http.Request, err error) {
		logger.DebugContext(r.Context(), "request could not be bound", "error", err)
		httpapi.WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "the request is not valid")
	}
	internal := func(w http.ResponseWriter, r *http.Request, err error) {
		logger.ErrorContext(r.Context(), "handling the request failed", "error", err)
		httpapi.WriteError(w, http.StatusInternalServerError, CodeInternal, "something went wrong")
	}
	strict := api.NewStrictHandlerWithOptions(srv, []api.StrictMiddlewareFunc{requireResponse}, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  badRequest,
		ResponseErrorHandlerFunc: internal,
	})
	table := routeTable{}
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseRouter: table, ErrorHandlerFunc: badRequest})
	return table
}

// openExtension is the operation extension of the spec that opens an operation to a workspace without a plan.
const openExtension = "x-open-without-plan"

// openOperations is the allowlist: the operations of the OpenAPI contract that are behind the authentication middleware
// alone, so a workspace without a plan can call them, by ServeMux pattern. It is read from the spec embedded in the
// generated code (the operations marked `x-open-without-plan: true`), so the contract and the server cannot disagree. Every
// other operation is paid-only.
var openOperations = sync.OnceValues(func() (map[string]bool, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("auth: reading the embedded OpenAPI spec: %w", err)
	}
	open := map[string]bool{}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if isOpen, _ := op.Extensions[openExtension].(bool); isOpen {
				open[method+" "+path] = true
			}
		}
	}
	return open, nil
})
