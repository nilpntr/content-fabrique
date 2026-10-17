// Package dbrole asks Postgres what the connected login may do, to prove that the application's login is not a
// privileged one. Staging and production run the app as a role that can only read and write rows (docs/06 section
// 3.4); a DATABASE_URL that holds the schema owner, a superuser or a role that can create roles would make the whole
// role split decorative. cfctl doctor and the api and worker both use this.
package dbrole

import (
	"fmt"
	"strings"
)

// Query reports the privileges of current_user in one row. Run it on the connection DATABASE_URL opens.
//
// "Member" counts both the role itself and roles it belongs to, directly or not, so an app login that was made a
// member of the owner (or of pg_database_owner, which owns schema public on Postgres 15 and later when the login owns
// the database) is caught as well as the owner.
const Query = `SELECT
    r.rolsuper,
    r.rolcreaterole,
    r.rolcreatedb,
    pg_has_role(current_user, (SELECT nspowner FROM pg_namespace WHERE nspname = 'public'), 'MEMBER'),
    COALESCE((SELECT bool_or(pg_has_role(current_user, c.relowner, 'MEMBER'))
              FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
              WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')), false),
    has_schema_privilege(current_user, 'public', 'CREATE')
FROM pg_roles r
WHERE r.rolname = current_user`

// Row is what a connection returns for Query. Scan it in this order.
type Row interface {
	Scan(dest ...any) error
}

// Report is the answer of Query.
type Report struct {
	Superuser      bool
	CreateRole     bool
	CreateDB       bool
	OwnsPublic     bool // is, or is a member of, the owner of schema public
	OwnsTables     bool // is, or is a member of, the owner of a table in schema public
	CreateOnPublic bool // may CREATE objects in schema public
}

// Scan reads the single row of Query.
func Scan(row Row) (Report, error) {
	var r Report
	err := row.Scan(&r.Superuser, &r.CreateRole, &r.CreateDB, &r.OwnsPublic, &r.OwnsTables, &r.CreateOnPublic)
	return r, err
}

// Problems lists what the login may do that an app login must not, in plain words. Empty means least privilege.
func (r Report) Problems() []string {
	var out []string
	add := func(bad bool, what string) {
		if bad {
			out = append(out, what)
		}
	}
	add(r.Superuser, "is a superuser")
	add(r.CreateRole, "has CREATEROLE")
	add(r.CreateDB, "has CREATEDB")
	add(r.OwnsPublic, "owns schema public (or is a member of its owner)")
	add(r.OwnsTables, "owns tables in schema public (or is a member of their owner)")
	add(r.CreateOnPublic, "can CREATE in schema public")
	return out
}

// Error returns nil for an unprivileged login, else an error naming every privilege found.
func (r Report) Error() error {
	if p := r.Problems(); len(p) > 0 {
		return fmt.Errorf("the DATABASE_URL login %s", strings.Join(p, ", "))
	}
	return nil
}
