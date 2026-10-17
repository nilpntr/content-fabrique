package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/api"
)

// The CORS policy of the API (D-23). The web app and the API are different origins, and the session token travels in
// an Authorization header, so a browser sends a preflight before every API call and only the web app may read the answer.
//
// The allowlist is exactly one origin, the one APP_BASE_URL names. There is no wildcard, and the request's Origin is
// never copied into the answer: the header carries the configured origin, and only when the request's Origin equals it.
// There are no credentials (cookies) in play, so Access-Control-Allow-Credentials is never sent.
//
// CORS is the browser's rule, not a firewall: the server still answers a request from another origin, it just does not
// let that origin's script read the answer. The authentication is what keeps data safe, and the azp check of
// internal/auth keeps a token issued to another origin from working here.
const (
	corsAllowMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	corsAllowHeaders = "Authorization, Content-Type"
	// How long a browser may reuse a preflight answer (browsers cap it themselves, Chrome at two hours).
	corsMaxAge = "600"
)

// CORS returns middleware that admits allowedOrigin (scheme://host[:port], no trailing slash) and no one else.
//
//   - Every response carries Vary: Origin, so a shared cache never serves one origin's answer to another.
//   - A request from the allowed origin gets Access-Control-Allow-Origin on its answer.
//   - A preflight (OPTIONS with Access-Control-Request-Method) from the allowed origin is answered 204 with the methods
//     and headers, and does not reach the handler. From any other origin, or with no Origin, it is answered 403 with no
//     CORS headers, which a browser reads as a refusal.
//   - A preflight that asks (Access-Control-Request-Headers) for a header other than Authorization and Content-Type is
//     refused the same way; the refused header's name is logged at debug level.
//   - A request from any other origin is passed on without CORS headers: the browser then hides the answer from the
//     page.
func CORS(allowedOrigin string, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			allowed := allowedOrigin != "" && origin == allowedOrigin
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""

			if preflight {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				if allowed {
					// A preflight that asks for a header we do not allow is refused as a whole, like one from another site.
					if bad := disallowedRequestHeader(r.Header.Get("Access-Control-Request-Headers")); bad != "" {
						logger.DebugContext(r.Context(), "cors preflight refused: header not allowed", "header", bad)
						allowed = false
					}
				}
				if !allowed || !corsMethodAllowed(r.Header.Get("Access-Control-Request-Method")) {
					WriteError(w, http.StatusForbidden, api.ErrorCodeOriginNotAllowed, "this origin may not call the API")
					return
				}
				h.Set("Access-Control-Allow-Origin", allowedOrigin)
				h.Set("Access-Control-Allow-Methods", corsAllowMethods)
				h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
				h.Set("Access-Control-Max-Age", corsMaxAge)
				w.WriteHeader(http.StatusNoContent)
				return
			}

			if allowed {
				h.Set("Access-Control-Allow-Origin", allowedOrigin)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func corsMethodAllowed(method string) bool {
	for _, m := range strings.Split(corsAllowMethods, ", ") {
		if m == method {
			return true
		}
	}
	return false
}

// disallowedRequestHeader returns the first header named in an Access-Control-Request-Headers value that is not in the
// allowlist ("" when all are). Header names are case-insensitive.
func disallowedRequestHeader(requested string) string {
	for _, name := range strings.Split(requested, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		listed := false
		for _, ok := range strings.Split(corsAllowHeaders, ", ") {
			if strings.EqualFold(name, ok) {
				listed = true
				break
			}
		}
		if !listed {
			return name
		}
	}
	return ""
}
