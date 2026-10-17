package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// The Clerk sync: the functions the webhook handler and the just-in-time sync of internal/auth use to mirror Clerk's
// users, organizations and memberships into Postgres.
//
// Every write is order-safe. It carries the time of the Clerk object it comes from (Unix milliseconds, the object's
// updated_at) and applies only when that is newer than what the row holds, so a replayed or reordered webhook, or a
// sync from a token that is a little stale, cannot move a row back. The second return value of the Sync functions
// says whether the write applied; the row returned is the current one either way. A deleted user or workspace is never
// changed again, and a deleted workspace or user has no memberships. The role of a member comes from Clerk and nowhere
// else: do not call AddMember from a request path.

// SyncUserInput is one version of a Clerk user.
type SyncUserInput struct {
	ClerkUserID    string
	Email          string // may be empty: a user without an email address is still a user
	ClerkUpdatedAt int64
}

// SyncUser creates the user or updates its email, when this version is newer. The user keeps its first id.
func (s *Store) SyncUser(ctx context.Context, in SyncUserInput) (User, bool, error) {
	if strings.TrimSpace(in.ClerkUserID) == "" {
		return User{}, false, fmt.Errorf("%w: clerk user id is required", ErrInvalid)
	}
	u, err := s.q.SyncUser(ctx, db.SyncUserParams{ClerkUserID: in.ClerkUserID, Email: in.Email, ClerkUpdatedAt: in.ClerkUpdatedAt})
	if errors.Is(err, pgx.ErrNoRows) { // stale, a replay, or deleted
		u, err = s.q.GetUserByClerkID(ctx, in.ClerkUserID)
		return u, false, mapErr(err)
	}
	return u, true, mapErr(err)
}

// GetUserByClerkID returns the user, deleted or not (check DeletedAt), or ErrNotFound.
func (s *Store) GetUserByClerkID(ctx context.Context, clerkUserID string) (User, error) {
	u, err := s.q.GetUserByClerkID(ctx, clerkUserID)
	return u, mapErr(err)
}

// DeleteUser marks the user deleted, removes its email and its memberships. A user that is not synced yet gets a
// tombstone, so a user.created that arrives later cannot bring it back. Deleting twice is fine.
func (s *Store) DeleteUser(ctx context.Context, clerkUserID string) (User, error) {
	if strings.TrimSpace(clerkUserID) == "" {
		return User{}, fmt.Errorf("%w: clerk user id is required", ErrInvalid)
	}
	var u User
	err := s.inTx(ctx, func(tx *Store) error {
		var err error
		if u, err = tx.q.DeleteUserByClerkID(ctx, clerkUserID); err != nil {
			return err
		}
		return tx.q.DeleteMembershipsOfUser(ctx, u.ID)
	})
	return u, mapErr(err)
}

// SyncWorkspaceInput is one version of a Clerk organization.
type SyncWorkspaceInput struct {
	ClerkOrgID     string
	Name           string
	ClerkUpdatedAt int64
}

// SyncWorkspace creates the workspace of a Clerk organization or renames it, when this version is newer. A new
// workspace gets the defaults (region, billing mode) of the table.
func (s *Store) SyncWorkspace(ctx context.Context, in SyncWorkspaceInput) (Workspace, bool, error) {
	if strings.TrimSpace(in.ClerkOrgID) == "" {
		return Workspace{}, false, fmt.Errorf("%w: clerk org id is required", ErrInvalid)
	}
	var (
		w       Workspace
		applied bool
	)
	// One transaction: the insert and the created hook (a River job) commit or roll back together.
	err := s.inTx(ctx, func(tx *Store) error {
		var err error
		w, err = tx.q.InsertWorkspaceIfNew(ctx, db.InsertWorkspaceIfNewParams{ClerkOrgID: in.ClerkOrgID, Name: in.Name, ClerkUpdatedAt: in.ClerkUpdatedAt})
		if err == nil {
			applied = true
			if hook := tx.hooks.workspaceCreated; hook != nil {
				return hook(ctx, tx, w)
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// It exists: rename it when this version is newer.
		w, err = tx.q.SyncWorkspace(ctx, db.SyncWorkspaceParams{ClerkOrgID: in.ClerkOrgID, Name: in.Name, ClerkUpdatedAt: in.ClerkUpdatedAt})
		if errors.Is(err, pgx.ErrNoRows) { // stale, a replay, or deleted
			w, err = tx.q.GetWorkspaceByClerkOrgID(ctx, in.ClerkOrgID)
			return err
		}
		applied = err == nil
		return err
	})
	if err != nil {
		return Workspace{}, false, mapErr(err)
	}
	return w, applied, nil
}

// DeleteWorkspace marks the workspace deleted and removes its memberships. The workspace's data stays in place. A
// workspace that is not synced yet gets a tombstone. Deleting twice is fine.
func (s *Store) DeleteWorkspace(ctx context.Context, clerkOrgID string) (Workspace, error) {
	if strings.TrimSpace(clerkOrgID) == "" {
		return Workspace{}, fmt.Errorf("%w: clerk org id is required", ErrInvalid)
	}
	var w Workspace
	err := s.inTx(ctx, func(tx *Store) error {
		var err error
		if w, err = tx.q.DeleteWorkspaceByClerkOrgID(ctx, clerkOrgID); err != nil {
			return err
		}
		return tx.q.DeleteMembershipsOfWorkspace(ctx, w.ID)
	})
	return w, mapErr(err)
}

// ErrMembershipRemoved is returned by SyncMember when a membership delete from Clerk at or after the version being written
// was already applied: the membership is gone, and the write that would bring it back is the stale one.
var ErrMembershipRemoved = errors.New("store: membership was removed in Clerk")

// SyncMember makes the user a member of the workspace with the role, or changes the role, when this version is newer.
// It refuses with ErrMembershipRemoved when the membership was deleted in Clerk at or after this version: a webhook
// that arrives late and a token issued before the removal cannot bring it back. The check and the write run under one
// advisory lock per membership (also taken by the delete webhook, see LockMembership), so they cannot interleave.
func (s *Store) SyncMember(ctx context.Context, ws ids.WorkspaceID, user ids.UserID, role Role, clerkUpdatedAt int64) (Member, bool, error) {
	if !role.valid() {
		return Member{}, false, fmt.Errorf("%w: role %q", ErrInvalid, role)
	}
	var (
		m       Member
		applied bool
	)
	err := s.inTx(ctx, func(tx *Store) error {
		subject, err := tx.q.GetMembershipSubject(ctx, db.GetMembershipSubjectParams{WorkspaceID: ws, UserID: user})
		if err != nil {
			return err
		}
		if err := tx.q.LockMembership(ctx, subject); err != nil {
			return err
		}
		removed, err := tx.q.MembershipDeletedSince(ctx, db.MembershipDeletedSinceParams{Subject: subject, ClerkEventAt: clerkUpdatedAt})
		if err != nil {
			return err
		}
		if removed {
			return ErrMembershipRemoved
		}
		m, err = tx.q.SyncWorkspaceMember(ctx, db.SyncWorkspaceMemberParams{WorkspaceID: ws, UserID: user, Role: string(role), ClerkUpdatedAt: clerkUpdatedAt})
		if errors.Is(err, pgx.ErrNoRows) { // not newer than the row
			m, err = tx.q.GetWorkspaceMember(ctx, db.GetWorkspaceMemberParams{WorkspaceID: ws, UserID: user})
			return err
		}
		applied = err == nil
		return err
	})
	if errors.Is(err, ErrMembershipRemoved) {
		return Member{}, false, err
	}
	return m, applied, mapErr(err)
}

// LockMembership takes the transaction-scoped advisory lock of one membership ("<clerk org id>/<clerk user id>"). The
// delete webhook calls it before it records the delete, and holds it until it commits.
func (s *Store) LockMembership(ctx context.Context, subject string) error {
	return mapErr(s.q.LockMembership(ctx, subject))
}

// DeleteMember removes the membership. Removing one that does not exist is fine.
func (s *Store) DeleteMember(ctx context.Context, ws ids.WorkspaceID, user ids.UserID) error {
	return mapErr(s.q.DeleteWorkspaceMember(ctx, db.DeleteWorkspaceMemberParams{WorkspaceID: ws, UserID: user}))
}

func (r Role) valid() bool { return r == RoleOwner || r == RoleEditor || r == RoleViewer }

// Session is what an authenticated request needs from Postgres: the user and the workspace of a verified token and the
// user's role in that workspace.
type Session struct {
	UserID           ids.UserID
	Email            string
	UserDeleted      bool
	WorkspaceID      ids.WorkspaceID
	WorkspaceName    string
	WorkspaceDeleted bool
	// Role is nil when the user is not (yet) recorded as a member of the workspace.
	Role *Role
	// MemberClerkUpdatedAt is the version of the membership row (0 when there is none).
	MemberClerkUpdatedAt int64
}

// ResolveSession looks up the user and the workspace of a verified token (by their Clerk ids) and the membership between
// them in one query. It returns ErrNotFound when the user or the workspace is not in Postgres yet.
func (s *Store) ResolveSession(ctx context.Context, clerkUserID, clerkOrgID string) (Session, error) {
	r, err := s.q.ResolveSession(ctx, db.ResolveSessionParams{ClerkUserID: clerkUserID, ClerkOrgID: clerkOrgID})
	if err != nil {
		return Session{}, mapErr(err)
	}
	out := Session{
		UserID: r.UserID, Email: r.Email, UserDeleted: r.UserDeletedAt != nil,
		WorkspaceID: r.WorkspaceID, WorkspaceName: r.WorkspaceName, WorkspaceDeleted: r.WorkspaceDeletedAt != nil,
	}
	if r.MemberRole != nil {
		role := Role(*r.MemberRole)
		out.Role = &role
		if r.MemberClerkUpdatedAt != nil {
			out.MemberClerkUpdatedAt = *r.MemberClerkUpdatedAt
		}
	}
	return out, nil
}

// WebhookEvent identifies one delivery and what it is about.
type WebhookEvent struct {
	// SvixID is the delivery id (the svix-id header), the same for every retry of one message.
	SvixID string
	// Type is the Clerk event type, such as "user.created".
	Type string
	// Subject is "<clerk org id>/<clerk user id>" for membership events and empty for the rest.
	Subject string
	// ClerkEventAt is the event's time in Unix milliseconds.
	ClerkEventAt int64
	// Failed marks a signed delivery that could not be used. It is recorded so Svix gets a 2xx and does not retry it.
	Failed bool
	// Detail is a short reason for a failed delivery. It must never contain a value from the payload.
	Detail string
}

// RecordWebhookEvent notes that this delivery is being applied. It returns false when the delivery was recorded before:
// a replay, to be acknowledged and not applied again. Call it inside the transaction that applies the event, so the
// record and the change commit or roll back together; a second delivery of the same id waits for the first to finish.
func (s *Store) RecordWebhookEvent(ctx context.Context, e WebhookEvent) (bool, error) {
	if e.SvixID == "" || e.Type == "" {
		return false, fmt.Errorf("%w: svix id and event type are required", ErrInvalid)
	}
	status := "applied"
	if e.Failed {
		status = "failed"
	}
	n, err := s.q.RecordWebhookEvent(ctx, db.RecordWebhookEventParams{SvixID: e.SvixID, EventType: e.Type, Subject: e.Subject, ClerkEventAt: e.ClerkEventAt, Status: status, Detail: e.Detail})
	return n == 1, mapErr(err)
}

// MembershipDeletedSince reports whether a (successfully applied) membership delete for this "<clerk org id>/<clerk user id>" pair was applied
// with an event time at or after atMillis. A membership create or update that is not newer than that delete is stale.
func (s *Store) MembershipDeletedSince(ctx context.Context, subject string, atMillis int64) (bool, error) {
	deleted, err := s.q.MembershipDeletedSince(ctx, db.MembershipDeletedSinceParams{Subject: subject, ClerkEventAt: atMillis})
	return deleted, mapErr(err)
}

// inTx runs fn in a transaction. Inside a transaction already (a Store from InTx), it simply runs fn.
func (s *Store) inTx(ctx context.Context, fn func(tx *Store) error) error {
	if s.pool == nil {
		return fn(s)
	}
	return s.InTx(ctx, fn)
}

// PruneWebhookEvents deletes delivery records received before otherBefore, except the applied membership deletes (the
// tombstones MembershipDeletedSince reads), which are deleted when received before tombstoneBefore. It returns how
// many rows went. The webhook_events_prune job calls it.
func (s *Store) PruneWebhookEvents(ctx context.Context, otherBefore, tombstoneBefore time.Time) (int64, error) {
	n, err := s.q.PruneWebhookEvents(ctx, db.PruneWebhookEventsParams{OtherBefore: otherBefore, TombstoneBefore: tombstoneBefore})
	return n, mapErr(err)
}
