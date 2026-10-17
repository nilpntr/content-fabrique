// Package auth is the Clerk side of the API (D-12): it verifies session tokens, resolves the workspace of a request,
// keeps users, organizations and memberships in Postgres in step with Clerk (Svix-signed webhooks, and a just-in-time
// sync for local development where webhooks cannot reach), and serves GET /v1/me.
//
// Where trust comes from. The only input that decides who a request is, and which workspace it acts in, is the session
// token, after its signature, expiry, issuer and authorized party (azp) were checked. The workspace id is looked up from
// the token's organization, never from a path, query, header or body. A request with no active organization is refused.
// Postgres is a mirror of Clerk and is never consulted to widen access: a deleted user or workspace is refused even
// when its token is still valid.
package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Identity is what a verified session token says about who is calling. Nothing in it comes from request input.
type Identity struct {
	// ClerkUserID is the token's subject ("user_...").
	ClerkUserID string
	// ClerkOrgID is the active organization ("org_..."), the workspace of the request. Never empty here.
	ClerkOrgID string
	// OrgRole is the user's role in that organization as Clerk spells it ("org:admin", "org:member").
	OrgRole string
	// IssuedAtMillis is when Clerk issued the token. It is the version of the facts the token states: a sync from it
	// does not overwrite anything Clerk reported later.
	IssuedAtMillis int64
}

// Principal is the caller of an authenticated request: our own ids for the user and workspace, and the role. It is
// put on the request context by the middleware; read it with PrincipalFromContext.
type Principal struct {
	UserID        ids.UserID
	Email         string
	WorkspaceID   ids.WorkspaceID
	WorkspaceName string
	Role          store.Role
}

type principalKey struct{}

// ContextWithPrincipal returns a context that carries p.
func ContextWithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFromContext returns the caller of an authenticated request. ok is false outside the middleware.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Resolver turns a verified identity into a Principal. *Syncer is the real one.
type Resolver interface {
	Resolve(ctx context.Context, id Identity) (Principal, error)
}

// Errors of Resolve, which the middleware turns into stable error codes.
var (
	// ErrUserDeleted: the user was deleted in Clerk.
	ErrUserDeleted = errors.New("auth: user deleted")
	// ErrWorkspaceDeleted: the organization was deleted in Clerk.
	ErrWorkspaceDeleted = errors.New("auth: workspace deleted")
	// ErrDirectoryUnavailable: Clerk's Backend API could not be reached or answered with an error.
	ErrDirectoryUnavailable = errors.New("auth: clerk backend api unavailable")
)

// MapRole maps a Clerk organization role to ours: org:admin is an owner, org:member an editor, and any other role (a
// custom one, an empty one) a viewer, the least privilege. The roles are Clerk's to define; ours only limit what the
// API lets a member do.
func MapRole(clerkRole string) store.Role {
	switch strings.TrimPrefix(clerkRole, "org:") {
	case "admin":
		return store.RoleOwner
	case "member":
		return store.RoleEditor
	default:
		return store.RoleViewer
	}
}
