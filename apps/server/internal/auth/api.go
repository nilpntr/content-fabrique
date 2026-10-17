package auth

import (
	"log/slog"
	"net/http"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
)

// apiServer implements api.StrictServerInterface, the generated interface of the OpenAPI contract
// (packages/schemas/openapi.yaml). The handlers live next to what they report: GetMe in me.go, GetWorkspacePlan and
// GetLibrarySummary in entitlements.go. The Clerk webhook is not here: it reads the raw body and the Svix headers
// itself (webhook.go), so the spec documents it and the generator leaves it out.
type apiServer struct {
	ents   EntitlementReader
	logger *slog.Logger
}

var _ api.StrictServerInterface = (*apiServer)(nil)

func newAPIServer(ents EntitlementReader, logger *slog.Logger) *apiServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &apiServer{ents: ents, logger: logger}
}

// errorBody is the standard error body, `{ "error": { "code", "message" } }`, as the generated type.
func errorBody(code, message string) api.Error {
	return api.Error{Error: api.ErrorDetail{Code: api.ErrorCode(code), Message: message}}
}

// routeTable collects what the generated code registers, so Mount decides which middleware each route sits behind. It
// is the api.ServeMux the generated HandlerWithOptions registers on; nothing is ever served from the table itself.
type routeTable map[string]http.Handler

// HandleFunc implements api.ServeMux. Every answer of the API is uncacheable, which the generated writer does not say.
func (t routeTable) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	t[pattern] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h(w, r)
	})
}

// ServeHTTP implements api.ServeMux and is never called.
func (routeTable) ServeHTTP(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }

// apiRoutes returns the handlers the generated code builds for srv, by their ServeMux pattern ("GET /v1/me"). Errors the
// generated code meets (a request it cannot bind, a response it cannot write) use the standard error body.
func apiRoutes(srv *apiServer) routeTable {
	logger := srv.logger
	badRequest := func(w http.ResponseWriter, r *http.Request, err error) {
		logger.DebugContext(r.Context(), "request could not be bound", "error", err)
		httpapi.WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "the request is not valid")
	}
	internal := func(w http.ResponseWriter, r *http.Request, err error) {
		logger.ErrorContext(r.Context(), "writing the response failed", "error", err)
		httpapi.WriteError(w, http.StatusInternalServerError, CodeInternal, "something went wrong")
	}
	strict := api.NewStrictHandlerWithOptions(srv, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  badRequest,
		ResponseErrorHandlerFunc: internal,
	})
	table := routeTable{}
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseRouter: table, ErrorHandlerFunc: badRequest})
	return table
}
