package clipfeedback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/textclean"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Limits of the API.
const (
	DefaultPageSize = 200
	MaxPageSize     = 500
	// MaxNoteChars is the longest note (clip_feedback_note_check).
	MaxNoteChars = store.MaxClipFeedbackNoteChars
)

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

func notFound(msg string) *Error {
	return &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: msg}
}

var (
	errProjectNotFound  = notFound("no such project")
	errVersionNotFound  = notFound("no such timeline version")
	errShotNotFound     = notFound("that version has no shot with this number")
	errClipGone         = notFound("the shot's clip is no longer in the project")
	errFeedbackNotFound = notFound("no such clip feedback")
)

// Store is what the service reads and writes. *store.Store is the real one.
type Store interface {
	GetProject(ctx context.Context, ws ids.WorkspaceID, id ids.ProjectID) (store.Project, error)
	GetTimelineVersion(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (store.TimelineVersion, error)
	ListProjectCatalogAssets(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID) ([]store.ProjectCatalogRow, error)
	SaveClipFeedback(ctx context.Context, ws ids.WorkspaceID, in store.NewClipFeedback) (store.ClipFeedback, error)
	ListClipFeedback(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID, limit int) ([]store.ClipFeedback, error)
	ListAllClipFeedback(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]store.ClipFeedback, error)
	DeleteClipFeedback(ctx context.Context, ws ids.WorkspaceID, id ids.ClipFeedbackID) error
}

// Service is the clip feedback without HTTP.
type Service struct {
	store  Store
	logger *slog.Logger
}

// NewService returns a Service over the store.
func NewService(st Store, logger *slog.Logger) (*Service, error) {
	if st == nil {
		return nil, errors.New("clipfeedback: a store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, logger: logger}, nil
}

// ListedShot is a shot of a shot list: the shot, its clip's title ("" with HasTitle false when the clip is no longer in the project) and
// the workspace's feedback on the range it plays.
type ListedShot struct {
	timeline.Shot
	Title    string
	HasTitle bool
	Feedback []store.ClipFeedback
}

// ShotList is the shots of one version, as its proof numbers them.
type ShotList struct {
	Project ids.ProjectID
	Version int
	Shots   []ListedShot
}

// version reads a version of a project that is not deleted, and its spec.
func (s *Service) version(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (*timeline.Spec, []store.ProjectCatalogRow, *Error) {
	if version < 1 || version > 1<<30 {
		return nil, nil, invalid("version must be a timeline version number")
	}
	if _, err := s.store.GetProject(ctx, ws, project); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, errProjectNotFound
		}
		return nil, nil, s.internal(ctx, "get project", err)
	}
	v, err := s.store.GetTimelineVersion(ctx, ws, project, version)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, errVersionNotFound
	}
	if err != nil {
		return nil, nil, s.internal(ctx, "get timeline version", err)
	}
	spec, err := timeline.Parse(v.Spec)
	if err != nil {
		return nil, nil, s.internal(ctx, "parse stored spec", err)
	}
	cat, err := s.store.ListProjectCatalogAssets(ctx, ws, project)
	if err != nil {
		return nil, nil, s.internal(ctx, "list project assets", err)
	}
	return spec, cat, nil
}

// ShotList returns the shots of a version with the feedback on each one's range.
func (s *Service) ShotList(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (ShotList, *Error) {
	spec, cat, e := s.version(ctx, ws, project, version)
	if e != nil {
		return ShotList{}, e
	}
	titles := map[string]string{}
	for _, a := range cat {
		titles[a.AssetID.String()] = a.Title
	}
	shots := timeline.Shots(spec)
	var rows []store.ClipFeedback
	if assets := ShotAssets(shots); len(assets) > 0 {
		var err error
		if rows, err = s.store.ListAllClipFeedback(ctx, ws, assets); err != nil {
			return ShotList{}, s.internal(ctx, "list clip feedback", err)
		}
	}
	out := ShotList{Project: project, Version: version, Shots: make([]ListedShot, len(shots))}
	for i, sh := range shots {
		title, ok := titles[sh.AssetID]
		ls := ListedShot{Shot: sh, Title: title, HasTitle: ok, Feedback: []store.ClipFeedback{}}
		for _, r := range rows {
			if RuleOf(r).Touches(sh) {
				ls.Feedback = append(ls.Feedback, r)
			}
		}
		out.Shots[i] = ls
	}
	return out, nil
}

// Give is the input of Give.
type Give struct {
	Project    ids.ProjectID
	Version    int
	ShotNumber int
	Kind       string
	OffsetMs   int64
	WholeClip  bool
	Note       string
}

// Giver is who gives feedback: the workspace and the user (a cfctl operator is a user too).
type Giver struct {
	Workspace ids.WorkspaceID
	User      ids.UserID
}

// Give stores feedback on a shot of a version: against the shot's clip and the range it plays (Range), never against the number.
func (s *Service) Give(ctx context.Context, who Giver, in Give) (store.ClipFeedback, *Error) {
	if in.ShotNumber < 1 || in.ShotNumber > 1<<20 {
		return store.ClipFeedback{}, invalid("shot_number must be a shot's number, from 1")
	}
	if in.Kind != NeverUse && in.Kind != StartLater {
		return store.ClipFeedback{}, invalid("kind must be never_use or start_later")
	}
	note, e := cleanNote(in.Note)
	if e != nil {
		return store.ClipFeedback{}, e
	}
	spec, cat, e := s.version(ctx, who.Workspace, in.Project, in.Version)
	if e != nil {
		return store.ClipFeedback{}, e
	}
	shot, ok := timeline.ShotByNumber(spec, in.ShotNumber)
	if !ok {
		return store.ClipFeedback{}, errShotNotFound
	}
	var clipMs int64
	found := false
	for _, a := range cat {
		if a.AssetID.String() == shot.AssetID {
			found = true
			if a.DurationMs != nil {
				clipMs = *a.DurationMs
			}
		}
	}
	aid, err := ids.ParseAssetID(shot.AssetID)
	if err != nil || !found {
		return store.ClipFeedback{}, errClipGone
	}
	start, end, err := Range(shot, in.Kind, in.OffsetMs, in.WholeClip, clipMs)
	if err != nil {
		return store.ClipFeedback{}, invalid(trimPrefix(err))
	}
	row, err := s.store.SaveClipFeedback(ctx, who.Workspace, store.NewClipFeedback{
		Asset: aid, Kind: in.Kind, StartMs: start, EndMs: end, Project: in.Project, Version: in.Version, ShotNumber: in.ShotNumber,
		ItemID: shot.ItemID, Note: note, CreatedByKind: store.AuthorUser, CreatedByID: who.User.String(),
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.ClipFeedback{}, errClipGone
	case errors.Is(err, store.ErrInvalid):
		return store.ClipFeedback{}, invalid("this feedback cannot be stored for that shot")
	case err != nil:
		return store.ClipFeedback{}, s.internal(ctx, "save clip feedback", err)
	}
	return row, nil
}

// trimPrefix is the reason of a Range refusal without the package's own prefix.
func trimPrefix(err error) string {
	msg, prefix := err.Error(), ErrInvalid.Error()+": "
	if len(msg) > len(prefix) && msg[:len(prefix)] == prefix {
		return msg[len(prefix):]
	}
	return msg
}

// cleanNote makes the creator's note safe to store, show and hand to a model (textclean.User: one line, no control characters) and
// checks its length.
func cleanNote(raw string) (string, *Error) {
	if !utf8.ValidString(raw) {
		return "", invalid("note is not valid text")
	}
	note, _ := textclean.User(raw, 4096)
	if utf8.RuneCountInString(note) > MaxNoteChars {
		return "", invalid(fmt.Sprintf("note is over %d characters", MaxNoteChars))
	}
	return note, nil
}

// List returns the workspace's feedback, newest first: on one clip, or on all of them (asset nil).
func (s *Service) List(ctx context.Context, ws ids.WorkspaceID, asset *ids.AssetID, limit int) ([]store.ClipFeedback, *Error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, invalid(fmt.Sprintf("limit must be between 1 and %d", MaxPageSize))
	}
	var assets []ids.AssetID
	if asset != nil {
		assets = []ids.AssetID{*asset}
	}
	rows, err := s.store.ListClipFeedback(ctx, ws, assets, limit)
	if err != nil {
		return nil, s.internal(ctx, "list clip feedback", err)
	}
	return rows, nil
}

// Delete takes feedback back.
func (s *Service) Delete(ctx context.Context, ws ids.WorkspaceID, id ids.ClipFeedbackID) *Error {
	switch err := s.store.DeleteClipFeedback(ctx, ws, id); {
	case errors.Is(err, store.ErrNotFound):
		return errFeedbackNotFound
	case err != nil:
		return s.internal(ctx, "delete clip feedback", err)
	}
	return nil
}

// Reader is what LoadRules reads: every row on the clips, never a page of them (a rule left out would let an edit play a ruled-out
// shot). *store.Store is the real one.
type Reader interface {
	ListAllClipFeedback(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]store.ClipFeedback, error)
}

// LoadRules reads the rules on the given clips, by clip (none for no clips).
func LoadRules(ctx context.Context, rd Reader, ws ids.WorkspaceID, assets []ids.AssetID) (map[string][]Rule, error) {
	if len(assets) == 0 {
		return map[string][]Rule{}, nil
	}
	rows, err := rd.ListAllClipFeedback(ctx, ws, assets)
	if err != nil {
		return nil, fmt.Errorf("read the clip feedback: %w", err)
	}
	return ByAsset(rows), nil
}

// ShotAssets is the distinct clips the shots play, in shot order (an id that does not parse is left out: it is no clip of ours).
func ShotAssets(shots []timeline.Shot) []ids.AssetID {
	var out []ids.AssetID
	seen := map[string]bool{}
	for _, s := range shots {
		if aid, err := ids.ParseAssetID(s.AssetID); err == nil && !seen[s.AssetID] {
			seen[s.AssetID] = true
			out = append(out, aid)
		}
	}
	return out
}

// internal logs an unexpected failure and returns the 500 that says nothing about it.
func (s *Service) internal(ctx context.Context, what string, err error) *Error {
	s.logger.ErrorContext(ctx, "clipfeedback: "+what+" failed", "error", err)
	return &Error{Status: 500, Code: api.ErrorCodeInternalError, Message: "something went wrong"}
}
