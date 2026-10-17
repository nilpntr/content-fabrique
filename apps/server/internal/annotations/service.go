// Package annotations is the comments of P1-08: ranges of time on a clip, a render or a version of a timeline, each with a kind
// (comment, include, exclude, prefer, instruction), an optional pin on the frame and a status the editor agent moves.
//
// Tenancy is the point of this package. The workspace of every call comes from the verified caller and nowhere else. The target of
// a new comment is looked up in that workspace, per type (a clip that is not deleted, a render or a version of a project that is not
// deleted), and the row carries project and version from the target, never from the request; a request that names a project names
// it only to be checked against the target's. Every query filters by workspace, so an id of another workspace is "not found", the
// same answer as an id that never existed.
package annotations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/textclean"
)

// Limits of the API.
const (
	MaxBodyChars    = store.MaxAnnotationBodyChars
	DefaultPageSize = 200
	MaxPageSize     = 500
	// MaxMs is the largest time a comment may name: 24 hours. Nothing in the product is longer, and it keeps nonsense out.
	MaxMs int64 = 24 * 60 * 60 * 1000
)

// Annotation is a comment as the service hands it out.
type Annotation = store.Annotation

// Error is a failure with the answer to give for it. The message never contains request data.
type Error struct {
	Status  int
	Code    api.ErrorCode
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

func invalid(msg string) *Error {
	return &Error{Status: 400, Code: api.ErrorCodeInvalidRequest, Message: msg}
}

func forbidden(msg string) *Error {
	return &Error{Status: 403, Code: api.ErrorCodeInsufficientRole, Message: msg}
}

var (
	errNotFound       = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such comment"}
	errTargetNotFound = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such clip, render or version in this workspace"}
	errProjectMissing = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such project"}
	errParentMissing  = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such comment to reply to"}
)

// Actor is who acts: the verified workspace, user and role of the request.
type Actor struct {
	Workspace ids.WorkspaceID
	User      ids.UserID
	Role      store.Role
}

func (a Actor) canWrite() bool { return a.Role == store.RoleOwner || a.Role == store.RoleEditor }

// authorID is how the user is recorded as the author of a comment.
func (a Actor) authorID() string { return a.User.String() }

// Service is the annotations API without HTTP.
type Service struct {
	store  *store.Store
	logger *slog.Logger
}

// NewService returns a Service. The store is required.
func NewService(st *store.Store, logger *slog.Logger) (*Service, error) {
	if st == nil {
		return nil, errors.New("annotations: a store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, logger: logger}, nil
}

// ---- Validation ------------------------------------------------------------------------------------------------

// Pin is a spot on the frame in the JSON the table stores: a point, or a box when W and H are set.
type Pin struct {
	X float64  `json:"x"`
	Y float64  `json:"y"`
	W *float64 `json:"w,omitempty"`
	H *float64 `json:"h,omitempty"`
}

// pinEpsilon is the slack on the right and bottom edges of a box, so a box that ends exactly on the frame's edge survives the
// rounding of a client's arithmetic.
const pinEpsilon = 1e-9

// Check says whether the pin is on the frame: x and y in [0, 1]; w and h together, above 0 and inside the frame.
func (p Pin) Check() error {
	inUnit := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }
	if !inUnit(p.X) || !inUnit(p.Y) {
		return errors.New("the pin must be on the frame: x and y between 0 and 1")
	}
	if (p.W == nil) != (p.H == nil) {
		return errors.New("a box pin needs both w and h")
	}
	if p.W != nil {
		w, h := *p.W, *p.H
		if !inUnit(w) || !inUnit(h) || w <= 0 || h <= 0 {
			return errors.New("the size of a box pin must be above 0 and at most 1")
		}
		if p.X+w > 1+pinEpsilon || p.Y+h > 1+pinEpsilon {
			return errors.New("the box pin must stay inside the frame")
		}
	}
	return nil
}

func (p Pin) json() json.RawMessage {
	raw, _ := json.Marshal(p) // floats and pointers to floats: cannot fail once Check passed
	return raw
}

// ParsePin reads a stored pin; nil for none.
func ParsePin(raw []byte) (*Pin, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var p Pin
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("annotations: stored pin: %w", err)
	}
	return &p, nil
}

func validKind(k string) bool {
	switch k {
	case store.AnnotationComment, store.AnnotationInclude, store.AnnotationExclude, store.AnnotationPrefer, store.AnnotationInstruction:
		return true
	}
	return false
}

// needsBody says whether a kind is a note and cannot be a bare marker.
func needsBody(kind string) bool {
	return kind == store.AnnotationComment || kind == store.AnnotationInstruction
}

// cleanBody makes the text safe to store, show and hand to a model (textclean.User, the policy of a title a person types: NFC, no
// control or format characters, white space turned into single spaces, so a comment is one line), and checks its length and, for a
// note, that there is some. Text that is not UTF-8 is refused.
func cleanBody(raw, kind string) (string, *Error) {
	if !utf8.ValidString(raw) {
		return "", invalid("body is not valid text")
	}
	// textclean cuts at its own bound (4096); a body that long is refused here, not shortened.
	body, _ := textclean.User(raw, 4096)
	switch {
	case utf8.RuneCountInString(body) > MaxBodyChars:
		return "", invalid(fmt.Sprintf("body is over %d characters", MaxBodyChars))
	case body == "" && needsBody(kind):
		return "", invalid("body is required for a comment or an instruction")
	}
	return body, nil
}

func checkRange(start, end int64) *Error {
	switch {
	case start < 0 || end < 0:
		return invalid("start_ms and end_ms cannot be negative")
	case end < start:
		return invalid("end_ms cannot be before start_ms")
	case end > MaxMs:
		return invalid("end_ms is past 24 hours")
	}
	return nil
}

// TargetUUID reads the TypeID of a target of the given type.
func TargetUUID(targetType, id string) (uuid.UUID, error) {
	switch targetType {
	case store.AnnotationOnAsset:
		v, err := ids.ParseAssetID(id)
		return v.UUID, err
	case store.AnnotationOnRender:
		v, err := ids.ParseRenderID(id)
		return v.UUID, err
	case store.AnnotationOnVersion:
		v, err := ids.ParseTimelineID(id)
		return v.UUID, err
	}
	return uuid.Nil, ids.ErrInvalid
}

// ValidTargetType says whether s names a kind of target.
func ValidTargetType(s string) bool {
	return s == store.AnnotationOnAsset || s == store.AnnotationOnRender || s == store.AnnotationOnVersion
}

// ---- Create ----------------------------------------------------------------------------------------------------

// CreateInput is a new comment as the request states it. The empty target is allowed in a reply only.
type CreateInput struct {
	TargetType string
	TargetID   string
	// Project, when set, must be the target's own.
	Project *ids.ProjectID
	StartMs *int64
	EndMs   *int64
	Pin     *Pin
	Kind    string
	Body    string
	Parent  *ids.AnnotationID
}

// Create adds a comment (or a reply) for the actor.
func (s *Service) Create(ctx context.Context, who Actor, in CreateInput) (Annotation, *Error) {
	if !who.canWrite() {
		return Annotation{}, forbidden("your role in this workspace cannot change comments")
	}
	kind := in.Kind
	if kind == "" {
		kind = store.AnnotationComment
	}
	if !validKind(kind) {
		return Annotation{}, invalid("kind is not one of comment, include, exclude, prefer, instruction")
	}
	body, e := cleanBody(in.Body, kind)
	if e != nil {
		return Annotation{}, e
	}
	var pin json.RawMessage
	if in.Pin != nil {
		if err := in.Pin.Check(); err != nil {
			return Annotation{}, invalid(err.Error())
		}
		pin = in.Pin.json()
	}

	if in.Parent != nil {
		return s.createReply(ctx, who, in, kind, body)
	}

	// A comment on a target.
	if !ValidTargetType(in.TargetType) {
		return Annotation{}, invalid("target_type must be asset, render or timeline_version")
	}
	targetID, err := TargetUUID(in.TargetType, in.TargetID)
	if err != nil {
		return Annotation{}, invalid("target_id is not a valid id for this target_type")
	}
	if in.StartMs == nil {
		return Annotation{}, invalid("start_ms is required")
	}
	start := *in.StartMs
	end := start
	if in.EndMs != nil {
		end = *in.EndMs
	}
	if e := checkRange(start, end); e != nil {
		return Annotation{}, e
	}
	if in.Project != nil && in.TargetType == store.AnnotationOnAsset {
		return Annotation{}, invalid("a comment on a clip has no project_id")
	}

	var created Annotation
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		target, err := tx.FindAnnotationTarget(ctx, who.Workspace, in.TargetType, targetID)
		if err != nil {
			return err
		}
		if in.Project != nil && *in.Project != target.Project {
			return errProjectMissing // the same answer as a target that is not there: no hint about other projects
		}
		if target.DurationMs > 0 && end > target.DurationMs {
			return invalid("the range is past the end of the clip or render")
		}
		created, err = tx.CreateAnnotation(ctx, who.Workspace, store.NewAnnotation{
			Target: target, StartMs: start, EndMs: end, Pin: pin, Kind: kind, Body: body,
			AuthorKind: store.AuthorUser, AuthorID: who.authorID(),
		})
		return err
	})
	if err != nil {
		return Annotation{}, s.failure(ctx, "create comment", err, errTargetNotFound)
	}
	return created, nil
}

func (s *Service) createReply(ctx context.Context, who Actor, in CreateInput, kind, body string) (Annotation, *Error) {
	if kind != store.AnnotationComment || in.Pin != nil {
		return Annotation{}, invalid("a reply is a plain comment: no kind other than comment, and no pin")
	}
	if body == "" {
		return Annotation{}, invalid("body is required for a reply")
	}
	var created Annotation
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		parent, err := tx.GetAnnotationForUpdate(ctx, who.Workspace, *in.Parent)
		if err != nil {
			return err
		}
		if !parent.ParentID.IsZero() {
			return invalid("a reply cannot be answered: reply to the comment itself")
		}
		// What the request says about the target and the range, if anything, must be the comment's own.
		if in.TargetType != "" && in.TargetType != parent.TargetType {
			return invalid("a reply is on the target of the comment it answers")
		}
		if in.TargetID != "" {
			if id, err := TargetUUID(parent.TargetType, in.TargetID); err != nil || id != parent.TargetID {
				return invalid("a reply is on the target of the comment it answers")
			}
		}
		if (in.StartMs != nil && *in.StartMs != parent.StartMs) || (in.EndMs != nil && *in.EndMs != parent.EndMs) {
			return invalid("a reply has the range of the comment it answers")
		}
		target := store.AnnotationTarget{Type: parent.TargetType, ID: parent.TargetID, Project: parent.ProjectID}
		if parent.TimelineVersion != nil {
			target.Version = int(*parent.TimelineVersion)
		}
		created, err = tx.CreateAnnotation(ctx, who.Workspace, store.NewAnnotation{
			Target: target, StartMs: parent.StartMs, EndMs: parent.EndMs, Kind: kind, Body: body,
			Parent: parent.ID, AuthorKind: store.AuthorUser, AuthorID: who.authorID(),
		})
		return err
	})
	if err != nil {
		return Annotation{}, s.failure(ctx, "create reply", err, errParentMissing)
	}
	return created, nil
}

// ---- List ------------------------------------------------------------------------------------------------------

// ListQuery says which comments to list: those on one target, or those of one project.
type ListQuery struct {
	TargetType string
	TargetID   string
	Project    *ids.ProjectID
	Limit      int // 0: the default; negative: out of range
	Cursor     string
}

// List returns a page of comments, oldest first, and the cursor of the next page ("" on the last).
func (s *Service) List(ctx context.Context, who Actor, q ListQuery) ([]Annotation, string, *Error) {
	limit := q.Limit
	switch {
	case limit == 0:
		limit = DefaultPageSize
	case limit < 0 || limit > MaxPageSize:
		return nil, "", invalid(fmt.Sprintf("limit must be between 1 and %d", MaxPageSize))
	}
	var after ids.AnnotationID
	if q.Cursor != "" {
		var err error
		if after, err = ids.ParseAnnotationID(q.Cursor); err != nil {
			return nil, "", invalid("cursor is not valid")
		}
	}
	hasTarget := q.TargetType != "" || q.TargetID != ""
	switch {
	case hasTarget && q.Project != nil:
		return nil, "", invalid("name a target or a project, not both")
	case !hasTarget && q.Project == nil:
		return nil, "", invalid("name a target (target_type and target_id) or a project (project_id)")
	}

	f := store.AnnotationFilter{After: after}
	if q.Project != nil {
		if _, err := s.store.GetProject(ctx, who.Workspace, *q.Project); err != nil {
			return nil, "", s.failure(ctx, "list comments: project", err, errProjectMissing)
		}
		f.Project = *q.Project
	} else {
		if !ValidTargetType(q.TargetType) || q.TargetID == "" {
			return nil, "", invalid("target_type (asset, render or timeline_version) and target_id go together")
		}
		id, err := TargetUUID(q.TargetType, q.TargetID)
		if err != nil {
			return nil, "", invalid("target_id is not a valid id for this target_type")
		}
		// An id of another workspace is a 404 here, not an empty list: the same answer as for an id that is not there.
		if _, err := s.store.FindAnnotationTarget(ctx, who.Workspace, q.TargetType, id); err != nil {
			return nil, "", s.failure(ctx, "list comments: target", err, errTargetNotFound)
		}
		f.TargetType, f.TargetID = q.TargetType, id
	}
	rows, err := s.store.ListAnnotations(ctx, who.Workspace, f, limit+1)
	if err != nil {
		return nil, "", s.failure(ctx, "list comments", err, errTargetNotFound)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID.String()
	}
	return rows, next, nil
}

// ---- Update ----------------------------------------------------------------------------------------------------

// UpdateInput is an edit: the fields that are set change.
type UpdateInput struct {
	Body      *string
	Kind      *string
	Status    *string
	StartMs   *int64
	EndMs     *int64
	Pin       *Pin
	RemovePin bool
}

func (u UpdateInput) changesContent() bool {
	return u.Body != nil || u.Kind != nil || u.StartMs != nil || u.EndMs != nil || u.Pin != nil || u.RemovePin
}

// isAuthor says whether the actor wrote the comment.
func (a Actor) isAuthor(c Annotation) bool {
	return c.AuthorKind == store.AuthorUser && c.AuthorID == a.authorID()
}

// Update edits a comment. The author may change anything; another owner or editor only the status.
func (s *Service) Update(ctx context.Context, who Actor, id ids.AnnotationID, in UpdateInput) (Annotation, *Error) {
	if !who.canWrite() {
		return Annotation{}, forbidden("your role in this workspace cannot change comments")
	}
	if !in.changesContent() && in.Status == nil {
		return Annotation{}, invalid("the request changes nothing: send at least one field")
	}
	if in.Pin != nil && in.RemovePin {
		return Annotation{}, invalid("pin and remove_pin cannot go together")
	}
	if in.Status != nil && *in.Status != store.AnnotationOpen && *in.Status != store.AnnotationResolved {
		return Annotation{}, invalid("status must be open or resolved")
	}
	if in.Kind != nil && !validKind(*in.Kind) {
		return Annotation{}, invalid("kind is not one of comment, include, exclude, prefer, instruction")
	}
	if in.Pin != nil {
		if err := in.Pin.Check(); err != nil {
			return Annotation{}, invalid(err.Error())
		}
	}

	var updated Annotation
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetAnnotationForUpdate(ctx, who.Workspace, id)
		if err != nil {
			return err
		}
		isReply := !cur.ParentID.IsZero()
		if in.changesContent() && !who.isAuthor(cur) {
			return forbidden("only the author can edit a comment; you can resolve or reopen it")
		}
		if in.Status != nil && cur.AuthorKind == store.AuthorAgent {
			return forbidden("the editor's replies have no status of their own")
		}
		if isReply && in.Status != nil {
			return invalid("a reply has no status of its own: resolve the comment it answers")
		}
		if isReply && (in.Kind != nil || in.StartMs != nil || in.EndMs != nil || in.Pin != nil) {
			return invalid("a reply has the kind and range of the comment it answers, and no pin")
		}

		next := store.AnnotationEdit{StartMs: cur.StartMs, EndMs: cur.EndMs, Pin: cur.Pin, Kind: cur.Kind, Body: cur.Body, Status: cur.Status}
		if in.Kind != nil {
			next.Kind = *in.Kind
		}
		if in.StartMs != nil || in.EndMs != nil {
			if in.StartMs != nil {
				next.StartMs = *in.StartMs
			}
			if in.EndMs != nil {
				next.EndMs = *in.EndMs
			} else if in.StartMs != nil && cur.EndMs == cur.StartMs {
				next.EndMs = next.StartMs // a moment stays a moment when only its start moves
			}
			if e := checkRange(next.StartMs, next.EndMs); e != nil {
				return e
			}
			target, err := tx.FindAnnotationTarget(ctx, who.Workspace, cur.TargetType, cur.TargetID)
			if err != nil {
				return err
			}
			if target.DurationMs > 0 && next.EndMs > target.DurationMs {
				return invalid("the range is past the end of the clip or render")
			}
		}
		switch {
		case in.Pin != nil:
			next.Pin = in.Pin.json()
		case in.RemovePin:
			next.Pin = nil
		}
		if in.Body != nil || in.Kind != nil {
			body := cur.Body
			if in.Body != nil {
				body = *in.Body
			}
			cleaned, e := cleanBody(body, next.Kind)
			if e != nil {
				return e
			}
			next.Body = cleaned
		}
		if next.Kind != store.AnnotationComment && isReply {
			return invalid("a reply is a plain comment")
		}

		switch {
		case in.Status != nil:
			next.Status = *in.Status
		case cur.Status == store.AnnotationApplied && in.changesContent():
			// What the editor applied is not what is written now.
			next.Status = store.AnnotationOpen
		}
		if next.Status == store.AnnotationApplied {
			next.AppliedInRun = uuid.UUID(cur.AppliedInRunID.Bytes)
			if cur.AddressedInVersion != nil {
				v := int(*cur.AddressedInVersion)
				next.AddressedInVersion = &v
			}
		}
		updated, err = tx.UpdateAnnotation(ctx, who.Workspace, id, next)
		return err
	})
	if err != nil {
		return Annotation{}, s.failure(ctx, "update comment", err, errNotFound)
	}
	return updated, nil
}

// ---- Delete ----------------------------------------------------------------------------------------------------

// Delete removes a comment and its replies. Only the author may.
func (s *Service) Delete(ctx context.Context, who Actor, id ids.AnnotationID) *Error {
	if !who.canWrite() {
		return forbidden("your role in this workspace cannot change comments")
	}
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		cur, err := tx.GetAnnotationForUpdate(ctx, who.Workspace, id)
		if err != nil {
			return err
		}
		if !who.isAuthor(cur) {
			return forbidden("only the author can delete a comment")
		}
		return tx.SoftDeleteAnnotation(ctx, who.Workspace, id)
	})
	if err != nil {
		return s.failure(ctx, "delete comment", err, errNotFound)
	}
	return nil
}

// failure turns what a transaction returned into the answer: an *Error as it is, "not found" as notFound, the rest as a 500 that
// says nothing about the cause.
func (s *Service) failure(ctx context.Context, what string, err error, notFound *Error) *Error {
	var e *Error
	switch {
	case errors.As(err, &e):
		return e
	case errors.Is(err, store.ErrNotFound):
		return notFound
	case errors.Is(err, store.ErrInvalid):
		// A value the checks of the table refused after the service's own checks passed: log it, the caller's input is fine to
		// call invalid but the message must not quote it.
		s.logger.WarnContext(ctx, "annotations: "+what+": the database refused the value", "error", err)
		return invalid("the comment could not be stored as written")
	}
	s.logger.ErrorContext(ctx, "annotations: "+what+" failed", "error", err)
	return &Error{Status: 500, Code: api.ErrorCodeInternalError, Message: "something went wrong"}
}
