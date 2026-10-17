package auth

import (
	"net/http"

	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// MeResponse is the body of GET /v1/me. It is written by hand until P0-08 generates the API from the OpenAPI contract.
type MeResponse struct {
	User      MeUser      `json:"user"`
	Workspace MeWorkspace `json:"workspace"`
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
// resolved from the verified token, and reads nothing from the request.
func MeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			// Wired without the middleware: a programming error, and it must not answer as if signed in.
			httpapi.WriteError(w, http.StatusUnauthorized, CodeUnauthenticated, "sign in to use the API")
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, MeResponse{
			User:      MeUser{ID: p.UserID, Email: p.Email},
			Workspace: MeWorkspace{ID: p.WorkspaceID, Name: p.WorkspaceName, Role: p.Role},
		})
	})
}
