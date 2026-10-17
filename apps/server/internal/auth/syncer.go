package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// Syncer is the Resolver: it turns a verified identity into a Principal from Postgres, and when the user, the workspace
// or the membership is not there yet, or the role changed, brings Postgres up to date first (the just-in-time sync).
//
// Why: Clerk's webhooks cannot reach a laptop, so on localhost nothing would ever create the rows. The same path also
// covers the first request after a webhook was missed. Which user and organization to fetch comes from the verified
// token and nothing else, and what is fetched comes from Clerk's Backend API, so a caller cannot make us store anything
// of their choosing. The result is read from Postgres on every request and kept in no memory cache.
type Syncer struct {
	store *store.Store
	dir   Directory
	now   func() time.Time
}

// NewSyncer returns a Syncer over the store and Clerk's Backend API.
func NewSyncer(st *store.Store, dir Directory) *Syncer {
	return &Syncer{store: st, dir: dir, now: time.Now}
}

// Resolve implements Resolver.
func (s *Syncer) Resolve(ctx context.Context, id Identity) (Principal, error) {
	role := MapRole(id.OrgRole)
	issuedAt := id.IssuedAtMillis
	if issuedAt <= 0 {
		issuedAt = s.now().UnixMilli()
	}

	// The fast path, one query: everything is known and current.
	sess, err := s.store.ResolveSession(ctx, id.ClerkUserID, id.ClerkOrgID)
	switch {
	case err == nil:
		if sess.UserDeleted {
			return Principal{}, ErrUserDeleted
		}
		if sess.WorkspaceDeleted {
			return Principal{}, ErrWorkspaceDeleted
		}
		// The member row is current when it has the role of the token, or when it was written from something newer
		// than the token (a webhook that arrived after the token was issued): then the row wins.
		if sess.Role != nil && (*sess.Role == role || sess.MemberClerkUpdatedAt >= issuedAt) {
			return principalOf(sess), nil
		}
	case errors.Is(err, store.ErrNotFound):
		// The user or the workspace is not in Postgres yet.
	default:
		return Principal{}, fmt.Errorf("resolve session: %w", err)
	}

	u, err := s.ensureUser(ctx, id.ClerkUserID)
	if err != nil {
		return Principal{}, err
	}
	w, err := s.ensureWorkspace(ctx, id.ClerkOrgID)
	if err != nil {
		return Principal{}, err
	}
	if u.DeletedAt != nil {
		return Principal{}, ErrUserDeleted
	}
	if w.DeletedAt != nil {
		return Principal{}, ErrWorkspaceDeleted
	}
	if _, _, err := s.store.SyncMember(ctx, w.ID, u.ID, role, issuedAt); err != nil {
		return Principal{}, fmt.Errorf("sync member: %w", err)
	}

	// Postgres has the last word, so a newer webhook is not undone by an older token.
	sess, err = s.store.ResolveSession(ctx, id.ClerkUserID, id.ClerkOrgID)
	if err != nil {
		return Principal{}, fmt.Errorf("resolve session after sync: %w", err)
	}
	if sess.UserDeleted {
		return Principal{}, ErrUserDeleted
	}
	if sess.WorkspaceDeleted {
		return Principal{}, ErrWorkspaceDeleted
	}
	if sess.Role == nil {
		// The membership was removed between the write and the read, by a webhook for the same user.
		return Principal{}, fmt.Errorf("resolve session after sync: %w", store.ErrNotFound)
	}
	return principalOf(sess), nil
}

func principalOf(sess store.Session) Principal {
	return Principal{UserID: sess.UserID, Email: sess.Email, WorkspaceID: sess.WorkspaceID, WorkspaceName: sess.WorkspaceName, Role: *sess.Role}
}

// ensureUser returns the user's row, creating it from Clerk when it is missing.
func (s *Syncer) ensureUser(ctx context.Context, clerkUserID string) (store.User, error) {
	return ensureUser(ctx, s.store, s.dir, clerkUserID)
}

func (s *Syncer) ensureWorkspace(ctx context.Context, clerkOrgID string) (store.Workspace, error) {
	return ensureWorkspace(ctx, s.store, s.dir, clerkOrgID)
}

// ensureUser returns the row of the user, fetching the user from Clerk and storing it when there is none. A user Clerk
// does not know any more is a deleted user.
func ensureUser(ctx context.Context, st *store.Store, dir Directory, clerkUserID string) (store.User, error) {
	u, err := st.GetUserByClerkID(ctx, clerkUserID)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.User{}, fmt.Errorf("get user: %w", err)
	}
	if dir == nil {
		return store.User{}, fmt.Errorf("%w: no Clerk backend configured", ErrDirectoryUnavailable)
	}
	cu, err := dir.User(ctx, clerkUserID)
	if errors.Is(err, ErrDirectoryNotFound) {
		return store.User{}, ErrUserDeleted
	}
	if err != nil {
		return store.User{}, err
	}
	u, _, err = st.SyncUser(ctx, store.SyncUserInput{ClerkUserID: clerkUserID, Email: cu.Email, ClerkUpdatedAt: cu.UpdatedAt})
	if err != nil {
		return store.User{}, fmt.Errorf("sync user: %w", err)
	}
	return u, nil
}

func ensureWorkspace(ctx context.Context, st *store.Store, dir Directory, clerkOrgID string) (store.Workspace, error) {
	w, err := st.GetWorkspaceByClerkOrgID(ctx, clerkOrgID)
	if err == nil {
		return w, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Workspace{}, fmt.Errorf("get workspace: %w", err)
	}
	if dir == nil {
		return store.Workspace{}, fmt.Errorf("%w: no Clerk backend configured", ErrDirectoryUnavailable)
	}
	co, err := dir.Organization(ctx, clerkOrgID)
	if errors.Is(err, ErrDirectoryNotFound) {
		return store.Workspace{}, ErrWorkspaceDeleted
	}
	if err != nil {
		return store.Workspace{}, err
	}
	w, _, err = st.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: clerkOrgID, Name: co.Name, ClerkUpdatedAt: co.UpdatedAt})
	if err != nil {
		return store.Workspace{}, fmt.Errorf("sync workspace: %w", err)
	}
	return w, nil
}
