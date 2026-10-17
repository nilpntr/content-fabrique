package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
)

// Error codes of the authentication middleware. They are part of the API: the web client branches on them.
const (
	CodeUnauthenticated      = "unauthenticated"        // 401: no token
	CodeInvalidToken         = "invalid_token"          // 401: the token failed verification (signature, expiry, issuer, azp)
	CodeNoActiveWorkspace    = "no_active_workspace"    // 403: signed in, but no organization is active
	CodeUserDeleted          = "user_deleted"           // 403
	CodeWorkspaceDeleted     = "workspace_deleted"      // 403
	CodeIdentityProvider     = "identity_unavailable"   // 503: Clerk's Backend API did not answer
	CodeNotConfigured        = "auth_not_configured"    // 503: no CLERK_SECRET_KEY
	CodeInternal             = "internal_error"         // 500
	CodeWebhookNotConfigured = "webhook_not_configured" // 503: no CLERK_WEBHOOK_SECRET
)

// DefaultLeeway is how long after its expiry a token is still accepted: just enough for clock skew between Clerk and us.
// Clerk's session tokens live 60 seconds and the web client fetches a fresh one per call, so this stays small.
const DefaultLeeway = 5 * time.Second

// MiddlewareConfig configures token verification.
type MiddlewareConfig struct {
	// AppOrigin is the one origin a session token may have been issued to: its azp claim must equal it exactly
	// (APP_BASE_URL reduced to scheme://host[:port]). Required.
	AppOrigin string
	// SecretKey is CLERK_SECRET_KEY. The default JWKS client uses it to fetch the instance's public signing keys.
	SecretKey string
	// JWKSClient replaces the default JWKS client. Tests inject one that serves a local key set.
	JWKSClient *jwks.Client
	// Leeway is the clock-skew tolerance. Zero means DefaultLeeway.
	Leeway time.Duration
	// Logger receives the failures that are not the caller's fault. Nil means slog.Default().
	Logger *slog.Logger
}

// NewMiddleware returns the authentication middleware: it requires a valid Clerk session token, turns it into a
// Principal with resolver and puts the Principal on the request context. Everything under /v1 except the webhooks sits
// behind it.
//
// It is built on clerkhttp.WithHeaderAuthorization, the SDK's verifier of the Authorization: Bearer header, and not on
// clerkhttp.RequireHeaderAuthorization, because Require answers a missing or unusable token with an empty 403. Here the
// decision is ours: no token is 401, and the answers carry the standard JSON error body. The checks:
//
//   - signature against the instance's JWKS (the algorithm must match the key's), expiry with a small leeway, and an
//     issuer that is a Clerk Frontend API host;
//   - azp must equal AppOrigin exactly. The SDK's own AuthorizedPartyMatches accepts a token with no azp at all, so
//     it is not used: a missing or different azp is refused;
//   - a subject, an expiry, and an active organization (no organization is 403 no_active_workspace: membership is required).
func NewMiddleware(cfg MiddlewareConfig, resolver Resolver) (func(http.Handler) http.Handler, error) {
	if cfg.AppOrigin == "" {
		return nil, errors.New("auth: AppOrigin is required")
	}
	if cfg.JWKSClient == nil && cfg.SecretKey == "" {
		return nil, errors.New("auth: SecretKey or JWKSClient is required")
	}
	if cfg.Leeway <= 0 {
		cfg.Leeway = DefaultLeeway
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	jwksClient := cfg.JWKSClient
	if jwksClient == nil {
		key := cfg.SecretKey
		jwksClient = jwks.NewClient(&clerk.ClientConfig{BackendConfig: clerk.BackendConfig{Key: &key, HTTPClient: backendHTTPClient()}})
	}

	appOrigin := cfg.AppOrigin
	verify := clerkhttp.WithHeaderAuthorization(
		clerkhttp.JWKSClient(jwksClient),
		clerkhttp.Leeway(cfg.Leeway),
		clerkhttp.AuthorizedParty(func(azp string) bool { return azp == appOrigin }),
		clerkhttp.AuthorizationJWTExtractor(bearerToken),
		clerkhttp.AuthorizationFailureHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			unauthorized(w, CodeInvalidToken, "the session token is not valid; sign in again")
		})),
	)

	return func(next http.Handler) http.Handler {
		return verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only the verifier above puts claims on the context, and only after verification.
			claims, ok := clerk.SessionClaimsFromContext(r.Context())
			if !ok || claims == nil || claims.Subject == "" {
				if bearerToken(r) != "" { // a token was sent, and it could not even be decoded
					unauthorized(w, CodeInvalidToken, "the session token is not valid; sign in again")
				} else {
					unauthorized(w, CodeUnauthenticated, "sign in to use the API")
				}
				return
			}
			if claims.Expiry == nil {
				// Clerk always sets exp, and the library's validation skips the check when it is missing.
				unauthorized(w, CodeInvalidToken, "the session token is not valid; sign in again")
				return
			}
			if claims.ActiveOrganizationID == "" {
				httpapi.WriteError(w, http.StatusForbidden, CodeNoActiveWorkspace, "choose or create a workspace first")
				return
			}

			var issuedAt int64
			if claims.IssuedAt != nil {
				issuedAt = *claims.IssuedAt * 1000
			} else {
				issuedAt = time.Now().UnixMilli()
			}
			p, err := resolver.Resolve(r.Context(), Identity{
				ClerkUserID:    claims.Subject,
				ClerkOrgID:     claims.ActiveOrganizationID,
				OrgRole:        claims.ActiveOrganizationRole,
				IssuedAtMillis: issuedAt,
			})
			if err != nil {
				writeResolveError(w, r, cfg.Logger, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), p)))
		}))
	}, nil
}

func writeResolveError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, ErrUserDeleted):
		httpapi.WriteError(w, http.StatusForbidden, CodeUserDeleted, "this account was deleted")
	case errors.Is(err, ErrWorkspaceDeleted):
		httpapi.WriteError(w, http.StatusForbidden, CodeWorkspaceDeleted, "this workspace was deleted")
	case errors.Is(err, ErrDirectoryUnavailable):
		logger.WarnContext(r.Context(), "clerk backend api unavailable", "error", err)
		httpapi.WriteError(w, http.StatusServiceUnavailable, CodeIdentityProvider, "the sign-in service is unavailable; try again")
	default:
		logger.ErrorContext(r.Context(), "resolving the session failed", "error", err)
		httpapi.WriteError(w, http.StatusInternalServerError, CodeInternal, "something went wrong")
	}
}

func unauthorized(w http.ResponseWriter, code, message string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpapi.WriteError(w, http.StatusUnauthorized, code, message)
}

// bearerToken returns the token of an "Authorization: Bearer <token>" header, or "" for anything else. The SDK's
// default extractor would take a header with no scheme as a token too.
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// NotConfigured returns the middleware used when CLERK_SECRET_KEY is empty: every request is answered 503, so a
// missing key is loud and never a silently open API.
func NotConfigured() func(http.Handler) http.Handler {
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			httpapi.WriteError(w, http.StatusServiceUnavailable, CodeNotConfigured, "sign-in is not configured on this server")
		})
	}
}

// backendHTTPClient is the HTTP client of calls to Clerk's Backend API (JWKS and the just-in-time user lookups).
func backendHTTPClient() *http.Client { return &http.Client{Timeout: 10 * time.Second} }
