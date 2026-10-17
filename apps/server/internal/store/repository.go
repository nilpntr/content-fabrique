package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Rows as the repository returns them.
type (
	Workspace      = db.Workspace
	User           = db.User
	Member         = db.WorkspaceMember
	MemberDetail   = db.ListWorkspaceMembersRow
	CreatorProfile = db.CreatorProfile
)

var (
	// ErrNotFound is returned when no row matches. For tenant-owned data that includes a row of another workspace.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is returned when a unique constraint is violated.
	ErrConflict = errors.New("store: already exists")
	// ErrInvalid is returned for input the database would reject (an unknown role, an empty name).
	ErrInvalid = errors.New("store: invalid input")
)

// Role is a member's role in a workspace.
type Role string

// The roles the workspace_members check constraint allows.
const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// BillingMode says who pays for model access (architecture section 11a).
type BillingMode string

// The modes the workspaces check constraint allows.
const (
	BillingManaged BillingMode = "managed"
	BillingBYOK    BillingMode = "byok"
)

// Defaults for a new workspace. The EU region follows D-15.
const (
	DefaultRegion = "eu"
)

// NewWorkspace is the input of CreateWorkspace. Region and BillingMode fall back to the defaults.
type NewWorkspace struct {
	ClerkOrgID  string
	Name        string
	Region      string
	BillingMode BillingMode
}

// CreateWorkspace inserts a workspace; Postgres 18's uuidv7() makes its key. A second call for the same Clerk organization
// returns ErrConflict; the Clerk webhook handler decides whether that means "already synced".
func (s *Store) CreateWorkspace(ctx context.Context, in NewWorkspace) (Workspace, error) {
	if strings.TrimSpace(in.ClerkOrgID) == "" || strings.TrimSpace(in.Name) == "" {
		return Workspace{}, fmt.Errorf("%w: clerk org id and name are required", ErrInvalid)
	}
	if in.Region == "" {
		in.Region = DefaultRegion
	}
	if in.BillingMode == "" {
		in.BillingMode = BillingManaged
	}
	if in.BillingMode != BillingManaged && in.BillingMode != BillingBYOK {
		return Workspace{}, fmt.Errorf("%w: billing mode %q", ErrInvalid, in.BillingMode)
	}
	w, err := s.q.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		ClerkOrgID: in.ClerkOrgID, Name: in.Name, Region: in.Region, BillingMode: string(in.BillingMode),
	})
	return w, mapErr(err)
}

// GetWorkspace returns the workspace with this id.
func (s *Store) GetWorkspace(ctx context.Context, id ids.WorkspaceID) (Workspace, error) {
	w, err := s.q.GetWorkspace(ctx, id)
	return w, mapErr(err)
}

// GetWorkspaceByClerkOrgID resolves the active Clerk organization of a verified token to our workspace. It is the
// one lookup that starts from something other than a WorkspaceID; everything after it is scoped by the result.
func (s *Store) GetWorkspaceByClerkOrgID(ctx context.Context, clerkOrgID string) (Workspace, error) {
	w, err := s.q.GetWorkspaceByClerkOrgID(ctx, clerkOrgID)
	return w, mapErr(err)
}

// UpsertUserByClerkID creates the user or updates the email of the existing one. The user keeps its first id.
func (s *Store) UpsertUserByClerkID(ctx context.Context, clerkUserID, email string) (User, error) {
	if strings.TrimSpace(clerkUserID) == "" || strings.TrimSpace(email) == "" {
		return User{}, fmt.Errorf("%w: clerk user id and email are required", ErrInvalid)
	}
	u, err := s.q.UpsertUserByClerkID(ctx, db.UpsertUserByClerkIDParams{ClerkUserID: clerkUserID, Email: email})
	return u, mapErr(err)
}

// AddMember makes the user a member of the workspace with the role, or changes the role of an existing member, with no
// check of the order of events. That makes it a way to change a role, so no request handler calls it: the roles come
// from Clerk, through SyncMember (the webhook and just-in-time paths of internal/auth). It stays for seeding and tests.
func (s *Store) AddMember(ctx context.Context, ws ids.WorkspaceID, user ids.UserID, role Role) (Member, error) {
	switch role {
	case RoleOwner, RoleEditor, RoleViewer:
	default:
		return Member{}, fmt.Errorf("%w: role %q", ErrInvalid, role)
	}
	m, err := s.q.AddWorkspaceMember(ctx, db.AddWorkspaceMemberParams{WorkspaceID: ws, UserID: user, Role: string(role)})
	return m, mapErr(err)
}

// ListMembers returns the members of one workspace, oldest first.
func (s *Store) ListMembers(ctx context.Context, ws ids.WorkspaceID) ([]MemberDetail, error) {
	rows, err := s.q.ListWorkspaceMembers(ctx, ws)
	return rows, mapErr(err)
}

// ProfileInput is the editable part of a creator profile. Empty JSON fields are stored as {}, a nil
// AudienceRegions as an empty list, and an empty Language as "en".
type ProfileInput struct {
	Niche           json.RawMessage
	AudienceRegions []string
	Tone            string
	Language        string
	Brand           json.RawMessage
	Defaults        json.RawMessage
}

// GetCreatorProfile returns the profile of this workspace, or ErrNotFound when none was saved yet.
func (s *Store) GetCreatorProfile(ctx context.Context, ws ids.WorkspaceID) (CreatorProfile, error) {
	p, err := s.q.GetCreatorProfile(ctx, ws)
	return p, mapErr(err)
}

// UpsertCreatorProfile saves the whole profile of this workspace.
func (s *Store) UpsertCreatorProfile(ctx context.Context, ws ids.WorkspaceID, in ProfileInput) (CreatorProfile, error) {
	for name, raw := range map[string]json.RawMessage{"niche": in.Niche, "brand": in.Brand, "defaults": in.Defaults} {
		if len(raw) > 0 && !json.Valid(raw) {
			return CreatorProfile{}, fmt.Errorf("%w: %s is not valid JSON", ErrInvalid, name)
		}
	}
	p, err := s.q.UpsertCreatorProfile(ctx, db.UpsertCreatorProfileParams{
		WorkspaceID:     ws,
		Niche:           orEmptyObject(in.Niche),
		AudienceRegions: orEmptyList(in.AudienceRegions),
		Tone:            in.Tone,
		Language:        cmp.Or(in.Language, "en"),
		Brand:           orEmptyObject(in.Brand),
		Defaults:        orEmptyObject(in.Defaults),
	})
	return p, mapErr(err)
}

func orEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func orEmptyList(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// mapErr turns driver errors into the package errors. Anything else is returned as it came.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrConflict
		case "23503": // foreign_key_violation: the workspace or user does not exist
			return fmt.Errorf("%w: referenced row does not exist", ErrNotFound)
		case "23514", "23502": // check_violation, not_null_violation
			return fmt.Errorf("%w: %s", ErrInvalid, pgErr.ConstraintName)
		}
		// Class 22, data exceptions: input the column cannot hold, such as a NUL byte in text (22021) or \u0000 in
		// jsonb (22P05). The message is left out because it can quote the value.
		if strings.HasPrefix(pgErr.Code, "22") {
			return fmt.Errorf("%w: value rejected by the database (SQLSTATE %s)", ErrInvalid, pgErr.Code)
		}
	}
	return err
}
