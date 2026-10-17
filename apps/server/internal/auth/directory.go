package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/organization"
	"github.com/clerk/clerk-sdk-go/v2/user"
)

// ClerkUser is the part of a Clerk user we keep.
type ClerkUser struct {
	ID    string
	Email string // the primary email address; empty when the user has none
	// UpdatedAt is the user's updated_at in Unix milliseconds, the version for the order-safe sync.
	UpdatedAt int64
}

// ClerkOrg is the part of a Clerk organization we keep.
type ClerkOrg struct {
	ID        string
	Name      string
	UpdatedAt int64
}

// ErrDirectoryNotFound is returned by a Directory when Clerk has no such user or organization (it was deleted).
var ErrDirectoryNotFound = errors.New("auth: not found in Clerk")

// Directory reads users and organizations from Clerk's Backend API. It is an interface so tests need no Clerk. A failure
// other than "not found" must wrap ErrDirectoryUnavailable.
type Directory interface {
	User(ctx context.Context, clerkUserID string) (ClerkUser, error)
	Organization(ctx context.Context, clerkOrgID string) (ClerkOrg, error)
}

// ClerkDirectory is the Directory over Clerk's Backend API, authenticated with CLERK_SECRET_KEY.
type ClerkDirectory struct {
	users *user.Client
	orgs  *organization.Client
}

// NewClerkDirectory returns a Directory that calls Clerk's Backend API with secretKey. The key is held by the SDK's
// clients only: this package never logs or returns it.
func NewClerkDirectory(secretKey string) *ClerkDirectory {
	cfg := &clerk.ClientConfig{BackendConfig: clerk.BackendConfig{Key: &secretKey, HTTPClient: backendHTTPClient()}}
	return &ClerkDirectory{users: user.NewClient(cfg), orgs: organization.NewClient(cfg)}
}

// User implements Directory.
func (d *ClerkDirectory) User(ctx context.Context, clerkUserID string) (ClerkUser, error) {
	u, err := d.users.Get(ctx, clerkUserID)
	if err != nil {
		return ClerkUser{}, directoryError(err)
	}
	var addrs []emailAddress
	for _, a := range u.EmailAddresses {
		if a != nil {
			addrs = append(addrs, emailAddress{ID: a.ID, Address: a.EmailAddress})
		}
	}
	primary := ""
	if u.PrimaryEmailAddressID != nil {
		primary = *u.PrimaryEmailAddressID
	}
	return ClerkUser{ID: u.ID, Email: primaryEmail(primary, addrs), UpdatedAt: u.UpdatedAt}, nil
}

// Organization implements Directory.
func (d *ClerkDirectory) Organization(ctx context.Context, clerkOrgID string) (ClerkOrg, error) {
	o, err := d.orgs.Get(ctx, clerkOrgID)
	if err != nil {
		return ClerkOrg{}, directoryError(err)
	}
	return ClerkOrg{ID: o.ID, Name: o.Name, UpdatedAt: o.UpdatedAt}, nil
}

// directoryError maps an SDK error: a 404 is ErrDirectoryNotFound, anything else ErrDirectoryUnavailable. The detail of
// the SDK error is dropped on purpose: it can echo what was requested.
func directoryError(err error) error {
	var apiErr *clerk.APIErrorResponse
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode == http.StatusNotFound {
		return ErrDirectoryNotFound
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrDirectoryUnavailable, err)
	}
	if errors.As(err, &apiErr) {
		return fmt.Errorf("%w: status %d", ErrDirectoryUnavailable, apiErr.HTTPStatusCode)
	}
	return fmt.Errorf("%w: request failed", ErrDirectoryUnavailable)
}

// emailAddress is an address of a Clerk user, from the Backend API or from a webhook payload.
type emailAddress struct {
	ID      string
	Address string
}

// primaryEmail returns the address whose id is the user's primary one, else the first address, else "".
func primaryEmail(primaryID string, addrs []emailAddress) string {
	for _, a := range addrs {
		if a.ID == primaryID && primaryID != "" {
			return a.Address
		}
	}
	if len(addrs) > 0 {
		return addrs[0].Address
	}
	return ""
}
