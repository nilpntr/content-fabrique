package auth

import (
	"context"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// GetMe is GET /v1/me. It must sit behind the authentication middleware: it reports the Principal the middleware
// resolved from the verified token and the plan of its workspace, and reads nothing from the request. It is open to a
// workspace without a plan, because it is how the web app finds that out.
func (s *apiServer) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		// Wired without the middleware: a programming error, and it must not answer as if signed in.
		return api.GetMe401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(errorBody(CodeUnauthenticated, "sign in to use the API"))}, nil
	}
	// Who the caller is does not depend on the plan, so a plan that cannot be read is not a failure of /v1/me: it is
	// reported as unknown (never active), and the web app says it could not check, rather than showing the paywall.
	e, err := readEntitlements(ctx, s.ents, p)
	if err != nil {
		s.logger.ErrorContext(ctx, "reading the workspace plan failed", "error", err)
		e = store.Unknown(p.WorkspaceID)
	}
	return api.GetMe200JSONResponse{
		User:         api.MeUser{Id: p.UserID.String(), Email: p.Email},
		Workspace:    api.MeWorkspace{Id: p.WorkspaceID.String(), Name: p.WorkspaceName, Role: api.Role(p.Role)},
		Entitlements: entitlementsOf(e),
	}, nil
}
