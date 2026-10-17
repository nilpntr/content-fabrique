package auth

import (
	"log/slog"
	"net/http"
)

// The generated routes one at a time, for tests that put a handler behind their own middleware.

func routeOf(pattern string, ents EntitlementReader, logger *slog.Logger) http.Handler {
	return apiRoutes(newAPIServer(ents, logger))[pattern]
}

// MeHandler is GET /v1/me.
func MeHandler(ents EntitlementReader, logger *slog.Logger) http.Handler {
	return routeOf("GET /v1/me", ents, logger)
}

// WorkspacePlanHandler is GET /v1/workspace/plan.
func WorkspacePlanHandler(ents EntitlementReader, logger *slog.Logger) http.Handler {
	return routeOf("GET /v1/workspace/plan", ents, logger)
}
