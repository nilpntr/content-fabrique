// Package ids converts between the plain UUIDv7 that Postgres stores and the TypeIDs the API, the timeline spec
// and the agents' tool calls use (decision D-24): a lowercase type prefix, an underscore, and the UUID in 26
// characters of Crockford base32, for example wks_01j9z3.... Postgres 18's uuidv7() generates the keys (column
// defaults), so this package never makes one; it only reads, checks and prints them.
//
// Inside Go a tenant-owned key is a typed ID such as WorkspaceID, so the compiler rejects a UserID where a
// WorkspaceID belongs. The types scan from and write to a Postgres uuid column directly, so sqlc uses them for
// the key columns (see sqlc.yaml), and they print and marshal as TypeIDs.
package ids

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.jetify.com/typeid"
)

// The prefixes of D-24. A new table adds its prefix here and to the decision's table.
const (
	PrefixWorkspace  = "wks"
	PrefixUser       = "usr"
	PrefixAsset      = "ast"
	PrefixMoment     = "mom"
	PrefixShot       = "shot"
	PrefixAnnotation = "ann"
	PrefixTrend      = "trd"
	PrefixConcept    = "con"
	PrefixIdea       = "ide"
	PrefixProject    = "prj"
	PrefixTimeline   = "tl"
	PrefixRender     = "ren"
	PrefixRun        = "run"
	PrefixExport     = "exp"
	PrefixLicence    = "lic"
	PrefixUpload     = "upl"
	PrefixEvent      = "evt"
)

// Prefixes lists every prefix above.
var Prefixes = []string{
	PrefixWorkspace, PrefixUser, PrefixAsset, PrefixMoment, PrefixShot, PrefixAnnotation, PrefixTrend, PrefixConcept,
	PrefixIdea, PrefixProject, PrefixTimeline, PrefixRender, PrefixRun, PrefixExport, PrefixLicence, PrefixUpload,
	PrefixEvent,
}

// ErrInvalid is wrapped by every parse failure, so callers can map it to a 400 or 404 with errors.Is.
var ErrInvalid = errors.New("invalid id")

// Encode returns the TypeID of id under prefix. It panics only on a prefix that is not a valid TypeID prefix,
// which is a programming error: use the constants above.
func Encode(prefix string, id uuid.UUID) string {
	tid, err := typeid.FromUUIDWithPrefix(prefix, id.String())
	if err != nil {
		panic(fmt.Sprintf("ids: %v", err))
	}
	return tid.String()
}

// Decode parses a TypeID that must carry wantPrefix and returns its UUID. The zero ID and anything that is not
// a UUIDv7 are rejected, because no row has such a key. Errors never echo the input.
func Decode(wantPrefix, s string) (uuid.UUID, error) {
	tid, err := typeid.FromString(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: not a TypeID", ErrInvalid)
	}
	if tid.Prefix() != wantPrefix {
		return uuid.Nil, fmt.Errorf("%w: want prefix %q", ErrInvalid, wantPrefix)
	}
	id, err := uuid.Parse(tid.UUID())
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: not a UUID", ErrInvalid)
	}
	if id == uuid.Nil || id.Version() != 7 {
		return uuid.Nil, fmt.Errorf("%w: not a UUIDv7", ErrInvalid)
	}
	return id, nil
}

// Prefixer names the TypeID prefix of one kind of object. Each kind is a tiny struct type, so ID[P] is a distinct
// Go type per kind.
type Prefixer interface{ Prefix() string }

// ID is a UUIDv7 key of the kind P. The zero value is the nil UUID, which no row has.
type ID[P Prefixer] struct{ uuid.UUID }

// Prefix returns the TypeID prefix of this kind.
func (ID[P]) Prefix() string {
	var p P
	return p.Prefix()
}

// FromUUID wraps a UUID, for example one read from a column that is not an ID type.
func FromUUID[P Prefixer](u uuid.UUID) ID[P] { return ID[P]{u} }

// Parse reads the TypeID form, checking the prefix.
func Parse[P Prefixer](s string) (ID[P], error) {
	var id ID[P]
	u, err := Decode(id.Prefix(), s)
	if err != nil {
		return ID[P]{}, err
	}
	return ID[P]{u}, nil
}

// IsZero reports whether the ID is unset.
func (id ID[P]) IsZero() bool { return id.UUID == uuid.Nil }

// String is the TypeID form. The UUID form is id.UUID.String().
func (id ID[P]) String() string { return Encode(id.Prefix(), id.UUID) }

// MarshalText makes JSON, query strings and log fields use the TypeID form.
func (id ID[P]) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText parses the TypeID form.
func (id *ID[P]) UnmarshalText(b []byte) error {
	parsed, err := Parse[P](string(b))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Value stores the plain UUID, as D-24 requires. It is spelled out so the TypeID form never reaches a column.
func (id ID[P]) Value() (driver.Value, error) { return id.UUID.String(), nil }

// Scan reads a uuid column.
func (id *ID[P]) Scan(src any) error { return id.UUID.Scan(src) }

// The kinds that have a Go type so far.
type (
	workspaceKind struct{}
	userKind      struct{}
)

func (workspaceKind) Prefix() string { return PrefixWorkspace }
func (userKind) Prefix() string      { return PrefixUser }

type (
	// WorkspaceID is the key of a workspace (wks_...). Every tenant-owned row carries one.
	WorkspaceID = ID[workspaceKind]
	// UserID is the key of a user (usr_...).
	UserID = ID[userKind]
)

// ParseWorkspaceID reads a wks_ TypeID.
func ParseWorkspaceID(s string) (WorkspaceID, error) { return Parse[workspaceKind](s) }

// ParseUserID reads a usr_ TypeID.
func ParseUserID(s string) (UserID, error) { return Parse[userKind](s) }
