package auth

import (
	"log/slog"
	"net/http"

	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// MeResponse is the body of GET /v1/me. It is written by hand until P0-08 generates the API from the OpenAPI contract.
type MeResponse struct {
	User         MeUser       `json:"user"`
	Workspace    MeWorkspace  `json:"workspace"`
	Entitlements Entitlements `json:"entitlements"` // the workspace's plan; the web app shows the paywall when !active
}

// MeUser is the signed-in user.
type MeUser struct {
	ID    ids.UserID `json:"id"` // usr_...
	Email string     `json:"email"`
}

// MeWorkspace is the active workspace (the Clerk organization of the session) and the caller's role in it.
type MeWorkspace struct {
	ID   ids.WorkspaceID `json:"id"` // wks_...
	Name string          `json:"name"`
	Role store.Role      `json:"role"` // owner, editor or viewer
}

// MeHandler is GET /v1/me. It must sit behind the authentication middleware: it reports the Principal the middleware
// resolved from the verified token and the plan of its workspace, and reads nothing from the request. It is open to a
// workspace without a plan, because it is how the web app finds that out.
func MeHandler(ents EntitlementReader, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			// Wired without the middleware: a programming error, and it must not answer as if signed in.
			httpapi.WriteError(w, http.StatusUnauthorized, CodeUnauthenticated, "sign in to use the API")
			return
		}
		e, err := ents.GetEntitlements(r.Context(), p.WorkspaceID)
		if err != nil {
			logger.ErrorContext(r.Context(), "reading the workspace plan failed", "error", err)
			httpapi.WriteError(w, http.StatusInternalServerError, CodeInternal, "something went wrong")
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, MeResponse{
			User:         MeUser{ID: p.UserID, Email: p.Email},
			Workspace:    MeWorkspace{ID: p.WorkspaceID, Name: p.WorkspaceName, Role: p.Role},
			Entitlements: entitlementsOf(e),
		})
	})
}
