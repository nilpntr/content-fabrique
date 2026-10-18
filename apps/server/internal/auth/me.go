package auth

import (
	"context"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// GetMe is GET /v1/me. It must sit behind the authentication middleware: it reports the Principal the middleware
// resolved from the verified token and the plan of its workspace, and reads nothing from the request. It is open to a
// workspace without a plan, because it is how the web app finds that out.
func (s *apiServer) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		// Wired without the middleware: a programming error, and it must not answer as if signed in.
		return api.GetMe401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(httpapi.ErrorBody(CodeUnauthenticated, "sign in to use the API"))}, nil
	}
	// Who the caller is does not depend on the plan, so a plan that cannot be read is not a failure of /v1/me: it is
	// reported as unknown (never active), and the web app says it could not check, rather than showing the paywall.
	e, err := readEntitlements(ctx, s.ents, p)
	if err != nil {
		s.logger.ErrorContext(ctx, "reading the workspace plan failed", "error", err)
		e = store.Unknown(p.WorkspaceID)
	}
	out := api.GetMe200JSONResponse{
		User:         api.MeUser{Id: p.UserID.String(), Email: p.Email},
		Workspace:    api.MeWorkspace{Id: p.WorkspaceID.String(), Name: p.WorkspaceName, Role: api.Role(p.Role)},
		Entitlements: entitlementsOf(e),
	}
	// The credits are the workspace's own (the Principal's workspace, nothing from the request). A balance that cannot be read is left
	// out, like an unreadable plan is reported as unknown: who the caller is does not depend on it.
	if s.credits != nil {
		if b, err := s.credits.CreditBalance(ctx, p.WorkspaceID, time.Now()); err != nil {
			s.logger.ErrorContext(ctx, "reading the workspace credits failed", "error", err)
		} else {
			c := creditsOf(b, s.list)
			out.Credits = &c
		}
	}
	return out, nil
}

// CreditReader reads the credit balance of a workspace. *store.Store is the real one.
type CreditReader interface {
	CreditBalance(ctx context.Context, ws ids.WorkspaceID, now time.Time) (store.CreditBalance, error)
}

// creditsOf is the balance as the API shows it. low is "at least 80 % of the allowance is used": what is available is at most a fifth of it.
func creditsOf(b store.CreditBalance, list *credits.PriceList) api.Credits {
	top := make([]api.CreditTopUp, 0, len(b.TopUps))
	for _, t := range b.TopUps {
		top = append(top, api.CreditTopUp{Credits: t.Credits, ExpiresAt: t.ExpiresAt})
	}
	return api.Credits{CreditsPerVideo: list.CreditsPerVideo(), Available: b.Available, Allowance: b.Allowance, PeriodEnd: b.PeriodEnd, Low: b.Allowance > 0 && b.Available*5 <= b.Allowance, TopUps: top}
}
