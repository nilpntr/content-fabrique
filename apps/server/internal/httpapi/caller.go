package httpapi

import (
	"context"
	"net/http"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Caller is the authenticated caller of a request: the workspace and user the verified token resolved to, and the role.
// Nothing else names the workspace of a call.
type Caller struct {
	Workspace ids.WorkspaceID
	User      ids.UserID
	Role      store.Role
}

// CanWrite says whether the role may change things: owners and editors do, a viewer only looks.
func (c Caller) CanWrite() bool { return c.Role == store.RoleOwner || c.Role == store.RoleEditor }

// CallerFunc reads the Caller from the request context (internal/auth supplies it from the Principal). ok is false when the
// request did not pass the authentication middleware.
type CallerFunc func(ctx context.Context) (Caller, bool)

// Refusal is the answer to give a call that may not go on.
type Refusal struct {
	Status  int
	Code    api.ErrorCode
	Message string
}

// Authorize is the one check every handler of the API starts with: 401 without an authenticated caller, and for a call that
// changes something (write) 403 insufficient_role for a viewer. what names the things the role cannot change ("assets",
// "projects"), for the message.
func (f CallerFunc) Authorize(ctx context.Context, write bool, what string) (Caller, *Refusal) {
	c, ok := f(ctx)
	if !ok {
		return Caller{}, &Refusal{Status: http.StatusUnauthorized, Code: api.ErrorCodeUnauthenticated, Message: "sign in to use the API"}
	}
	if write && !c.CanWrite() {
		return Caller{}, &Refusal{Status: http.StatusForbidden, Code: api.ErrorCodeInsufficientRole, Message: "your role in this workspace cannot change " + what}
	}
	return c, nil
}
