package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/nilpntr/content-fabrique/internal/dbrole"
)

// ErrPrivilegedLogin means the DATABASE_URL login can do more than read and write rows: it is a superuser, can create
// roles or databases, owns the schema or the tables, or can create objects in schema public. The services refuse to
// serve with such a login on staging and production, where the whole point of the role split is that a compromised
// app cannot change the schema.
var ErrPrivilegedLogin = errors.New("store: the app login is privileged")

// VerifyAppLogin asks Postgres what the pool's login may do. It returns an error wrapping ErrPrivilegedLogin that
// names every privilege found, or another error when the question could not be asked (Postgres unreachable).
func (s *Store) VerifyAppLogin(ctx context.Context) error {
	if s.pool == nil {
		return errors.New("store: no pool inside a transaction")
	}
	rep, err := dbrole.Scan(s.pool.QueryRow(ctx, dbrole.Query))
	if err != nil {
		return fmt.Errorf("check the app login: %w", err)
	}
	if err := rep.Error(); err != nil {
		return fmt.Errorf("%w: %w", ErrPrivilegedLogin, err)
	}
	return nil
}
