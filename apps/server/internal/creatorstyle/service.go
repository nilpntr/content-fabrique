// Package creatorstyle is the creator's style in the app (P6-05b, docs/design/style-profile-v1.md section 11): the API that shows the
// style (the AI summary, the measures in plain words, the creator's settings, the effective settings and the reference videos), lets
// the creator change their settings, remove a reference, learn the style again or forget it; and the background work that measures
// the reference videos and makes the profile and its summary from them.
//
// Tenancy: every method takes the WorkspaceID of the verified token (the Handler reads it from the caller, never from the request) and
// only reads or changes rows of that workspace; an asset of another workspace is exactly as unknown as one that was never issued.
package creatorstyle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// PosterURLExpiry is how long the poster URLs of the references work.
const PosterURLExpiry = 30 * time.Minute

// Error is a failure with the answer to give for it. The message never contains request data.
type Error struct {
	Status  int
	Code    api.ErrorCode
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

var (
	errNotFound  = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such style reference"}
	errLearning  = &Error{Status: 409, Code: api.ErrorCodeStyleLearningInProgress, Message: "your videos are being learnt; learn again when that is done"}
	errQueue     = &Error{Status: 503, Code: api.ErrorCodeQueueNotMigrated, Message: "the job queue is not ready; try again later"}
	errInternal  = &Error{Status: 500, Code: api.ErrorCodeInternalError, Message: "something went wrong"}
	errReadSetup = errors.New("creatorstyle: a store is required")
)

func invalid(msg string) *Error {
	return &Error{Status: 400, Code: api.ErrorCodeInvalidRequest, Message: msg}
}

// Presigner makes short-lived GET URLs for objects. *storage.Client is the real one.
type Presigner interface {
	PresignGet(ctx context.Context, key string, expires time.Duration) (string, error)
}

// Service is the style API without HTTP.
type Service struct {
	store *store.Store
	// objects presigns the posters; nil (no object storage) leaves the poster URLs out.
	objects Presigner
	jobs    jobs.TxInserter
	logger  *slog.Logger
	// Plans is the price list whose plans cap the "learn again" passes per day; nil uses credits.DefaultStyleRelearnsPerDay.
	Plans *credits.PriceList
	// Now replaces time.Now (tests).
	Now func() time.Time
}

// NewService returns a Service. objects may be nil.
func NewService(st *store.Store, objects Presigner, enq jobs.TxInserter, logger *slog.Logger) (*Service, error) {
	if st == nil || enq == nil {
		return nil, errReadSetup
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, objects: objects, jobs: enq, logger: logger}, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) internal(ctx context.Context, what string, err error) *Error {
	if errors.Is(err, store.ErrQueueNotMigrated) {
		return errQueue
	}
	s.logger.ErrorContext(ctx, "creatorstyle: "+what+" failed", "error", err)
	return errInternal
}

// Actor is who acts: the verified workspace and user of the request.
type Actor struct {
	Workspace ids.WorkspaceID
	User      ids.UserID
}

// ---- the view ------------------------------------------------------------------------------------------------------------------

// The overall states of a style.
const (
	StatusEmpty    = "empty"
	StatusLearning = "learning"
	StatusReady    = "ready"
)

// The states of a reference as the app shows it.
const (
	RefUploading  = "uploading"
	RefProcessing = "processing"
	RefAnalysing  = "analysing"
	RefReady      = "ready"
	RefFailed     = "failed"
)

// Reference is a reference video as the app shows it.
type Reference struct {
	// Asset is nil for a file measured by cfctl.
	Asset         *ids.AssetID
	Name          string
	Status        string
	FailureReason string
	Progress      int
	DurationMs    *int64
	PosterURL     string
	CreatedAt     time.Time
}

// View is the style as the API answers it.
type View struct {
	Status      string
	Done, Total int
	Style       *styleprofile.Style
	References  []Reference
	LearnedAt   *time.Time
	// PostersExpire is when the poster URLs stop working (zero without one).
	PostersExpire time.Time
}

// Get returns the workspace's style.
func (s *Service) Get(ctx context.Context, who Actor) (View, *Error) {
	return s.view(ctx, who.Workspace)
}

func (s *Service) view(ctx context.Context, ws ids.WorkspaceID) (View, *Error) {
	style, err := styleprofile.LoadStyle(ctx, s.store, ws)
	if err != nil {
		return View{}, s.internal(ctx, "load the style", err)
	}
	assets, err := s.store.ListStyleReferenceAssets(ctx, ws)
	if err != nil {
		return View{}, s.internal(ctx, "list the reference assets", err)
	}
	rows, err := s.store.ListStyleReferences(ctx, ws)
	if err != nil {
		return View{}, s.internal(ctx, "list the references", err)
	}
	var profileRefs []uuid.UUID
	var learnedAt *time.Time
	if row, err := s.store.GetCreatorStyle(ctx, ws); err == nil {
		profileRefs = row.ReferenceIds
		t := row.UpdatedAt
		learnedAt = &t
	} else if !errors.Is(err, store.ErrNotFound) {
		return View{}, s.internal(ctx, "read the profile", err)
	}
	refs, ready := s.references(ctx, ws, assets, rows)
	v := View{Style: style, References: refs, LearnedAt: learnedAt}
	if style.Profile == nil {
		v.LearnedAt = nil
	}
	busy := false
	for _, r := range v.References {
		switch r.Status {
		case RefUploading, RefProcessing, RefAnalysing:
			busy = true
			v.Total++
		case RefReady:
			v.Done++
			v.Total++
		}
		if r.PosterURL != "" && v.PostersExpire.IsZero() {
			v.PostersExpire = s.now().Add(PosterURLExpiry)
		}
	}
	// The profile is up to date when it was made from exactly the references that are measured now. A profile stored by an older cfctl
	// has no reference ids, and stands as long as there is no measured reference to make a new one from.
	stale := len(ready) > 0 && !slices.Equal(sortedIDs(profileRefs), ProfileSet(ready))
	if len(ready) == 0 && len(profileRefs) > 0 && style.Profile != nil {
		stale = true // every reference it was made from is gone: the profile job removes it
	}
	if learnedAt != nil && len(ready) > 0 {
		var measured []store.StyleReference
		for _, r := range rows {
			if slices.Contains(ready, r.ID) {
				measured = append(measured, r)
			}
		}
		stale = stale || measuredSince(measured, *learnedAt) // learnt again: the profile job makes it from the new measures
	}
	switch {
	case busy || stale:
		v.Status = StatusLearning
	case style.Profile != nil:
		v.Status = StatusReady
	default:
		v.Status = StatusEmpty
	}
	return v, nil
}

// ProfileSet is the set of measured references a profile is made from, sorted: the newest styleprofile.MaxReferences (UUIDv7 ids sort
// by time).
func ProfileSet(ready []uuid.UUID) []uuid.UUID {
	out := sortedIDs(ready)
	if len(out) > styleprofile.MaxReferences {
		out = out[len(out)-styleprofile.MaxReferences:]
	}
	return out
}

func sortedIDs(in []uuid.UUID) []uuid.UUID {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

// references merges the reference assets with their rows, oldest first, and returns the ids of the rows that are measured and count
// (their asset is live, or they are files).
func (s *Service) references(ctx context.Context, ws ids.WorkspaceID, assets []store.Asset, rows []store.StyleReference) ([]Reference, []uuid.UUID) {
	byAsset := map[ids.AssetID]store.StyleReference{}
	var out []Reference
	var ready []uuid.UUID
	for _, r := range rows {
		if id, ok := store.StyleReferenceAsset(r); ok {
			byAsset[id] = r
			continue
		}
		// A file measured by cfctl.
		if r.Status == store.StyleRefReady {
			ready = append(ready, r.ID)
		}
		out = append(out, Reference{Name: r.Name, Status: refStatus(r), FailureReason: deref(r.FailureReason), CreatedAt: r.CreatedAt})
	}
	posters := s.posters(ctx, ws, assets)
	for _, a := range assets {
		id := a.ID
		ref := Reference{Asset: &id, Name: a.Filename, CreatedAt: a.CreatedAt, DurationMs: a.DurationMs, PosterURL: posters[a.ID]}
		row, hasRow := byAsset[a.ID]
		switch {
		case a.Status == store.AssetUploading:
			ref.Status = RefUploading
		case a.Status == store.AssetUploaded || a.Status == store.AssetProcessing:
			ref.Status, ref.Progress = RefProcessing, int(a.IngestProgress)
		case a.Status == store.AssetFailed:
			ref.Status, ref.FailureReason = RefFailed, deref(a.FailureReason)
		case !hasRow:
			ref.Status = RefAnalysing // ready and not queued yet: the ingest's hook queues it in the same transaction, so this is brief
		default:
			ref.Status, ref.FailureReason = refStatus(row), deref(row.FailureReason)
			if row.Status == store.StyleRefReady {
				ready = append(ready, row.ID)
			}
		}
		if ref.Status == RefProcessing && ref.Progress == 0 {
			ref.Progress = 1
		}
		out = append(out, ref)
	}
	slices.SortStableFunc(out, func(a, b Reference) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, ready
}

func refStatus(r store.StyleReference) string {
	switch r.Status {
	case store.StyleRefReady:
		return RefReady
	case store.StyleRefFailed:
		return RefFailed
	}
	return RefAnalysing
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// posters presigns the poster of each ready reference; none without object storage, and a failure leaves that poster out.
func (s *Service) posters(ctx context.Context, ws ids.WorkspaceID, assets []store.Asset) map[ids.AssetID]string {
	out := map[ids.AssetID]string{}
	if s.objects == nil || len(assets) == 0 {
		return out
	}
	idList := make([]ids.AssetID, 0, len(assets))
	for _, a := range assets {
		if a.Status == store.AssetReady {
			idList = append(idList, a.ID)
		}
	}
	derivs, err := s.store.ListAssetDerivatives(ctx, ws, idList)
	if err != nil {
		s.logger.WarnContext(ctx, "creatorstyle: the posters could not be listed", "error", err)
		return out
	}
	for _, d := range derivs {
		if d.Kind != store.DerivativePoster || d.WorkspaceID != ws {
			continue
		}
		url, err := s.objects.PresignGet(ctx, d.R2Key, PosterURLExpiry)
		if err != nil {
			s.logger.WarnContext(ctx, "creatorstyle: a poster could not be presigned", "error", err)
			continue
		}
		out[d.AssetID] = url
	}
	return out
}

// ---- changes -------------------------------------------------------------------------------------------------------------------

// PutSettings replaces the creator's settings and returns the style.
func (s *Service) PutSettings(ctx context.Context, who Actor, in styleprofile.Settings) (View, *Error) {
	clean, err := in.Normalize()
	if err != nil {
		return View{}, invalid(strings.TrimPrefix(err.Error(), "styleprofile: "))
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return View{}, s.internal(ctx, "encode the settings", err)
	}
	if _, err := s.store.SaveCreatorStyleSettings(ctx, who.Workspace, raw, who.User); err != nil {
		return View{}, s.internal(ctx, "save the settings", err)
	}
	return s.view(ctx, who.Workspace)
}

// PatchSettings changes only the settings that are given and clears the ones named (P6-05d): the creator's other settings stay. The
// read and the write are one transaction under the workspace's style lock, so two changes at once both land.
func (s *Service) PatchSettings(ctx context.Context, who Actor, change styleprofile.Settings, unset []string) (View, *Error) {
	var bad error
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockWorkspaceStyle(ctx, who.Workspace); err != nil {
			return err
		}
		var cur styleprofile.Settings
		row, err := tx.GetCreatorStyleSettings(ctx, who.Workspace)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return err
		default:
			if cur, err = styleprofile.ParseSettings(row.Settings); err != nil {
				s.logger.WarnContext(ctx, "creatorstyle: the stored settings cannot be read; they are replaced", "error", err)
				cur = styleprofile.Settings{}
			}
		}
		next, err := cur.Merge(change, unset)
		if err != nil {
			bad = err
			return err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		_, err = tx.SaveCreatorStyleSettings(ctx, who.Workspace, raw, who.User)
		return err
	})
	switch {
	case bad != nil:
		return View{}, invalid(strings.TrimPrefix(bad.Error(), "styleprofile: "))
	case err != nil:
		return View{}, s.internal(ctx, "change the settings", err)
	}
	return s.view(ctx, who.Workspace)
}

// Remove deletes a reference video: the asset (its objects are removed by a job), its measures, and the profile is made again from the
// others. An asset that is not a style reference of the workspace is not found.
func (s *Service) Remove(ctx context.Context, who Actor, id ids.AssetID) *Error {
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		a, u, err := tx.GetAssetUpload(ctx, who.Workspace, id)
		if err != nil {
			return err
		}
		if a.Purpose != store.PurposeStyleReference {
			return store.ErrNotFound
		}
		if u.CompletedAt == nil && u.AbortedAt == nil {
			// Still uploading: the upload is aborted (its parts expire in object storage).
			if _, err := tx.AbortUpload(ctx, who.Workspace, id, store.FailureUploadAborted, true); err != nil && !errors.Is(err, store.ErrNotActive) {
				return err
			}
		}
		// Hidden whatever state it was in (the abort hides an asset that was still uploading; this one is not an error then).
		if _, err := tx.SoftDeleteAsset(ctx, who.Workspace, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, err := s.jobs.InsertInStore(ctx, tx, jobs.AssetObjectDeleteArgs{WorkspaceID: who.Workspace, AssetID: id}, nil); err != nil {
			return err
		}
		if err := tx.DeleteStyleReferenceByAsset(ctx, who.Workspace, id); err != nil {
			return err
		}
		return jobs.EnqueueStyleProfile(ctx, s.jobs, tx, who.Workspace, s.now())
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return errNotFound
	}
	return s.internal(ctx, "remove a reference", err)
}

// relearnCap is the workspace's "learn again" passes per UTC day: its plan's, else the default.
func (s *Service) relearnCap(ctx context.Context, tx *store.Store, ws ids.WorkspaceID) (int, error) {
	if s.Plans == nil {
		return credits.DefaultStyleRelearnsPerDay, nil
	}
	ent, err := tx.GetEntitlements(ctx, ws)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	if pl, ok := s.Plans.Plan(ent.Plan); ok && err == nil {
		return pl.RelearnsPerDay(), nil
	}
	return credits.DefaultStyleRelearnsPerDay, nil
}

// Relearn measures every reference again and makes the profile and its summary again, in the background. Under the workspace's style
// lock it refuses while a reference is being measured (a pass, or a new upload: the profile job makes the profile once they are all
// measured, so a second pass now would only measure them twice) and past the plan's passes for the UTC day; each pass is recorded.
func (s *Service) Relearn(ctx context.Context, who Actor) (View, *Error) {
	now := s.now()
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.LockWorkspaceStyle(ctx, who.Workspace); err != nil {
			return err
		}
		if n, err := tx.CountStyleReferencesInFlight(ctx, who.Workspace, now); err != nil {
			return err
		} else if n > 0 {
			return errLearning
		}
		limit, err := s.relearnCap(ctx, tx, who.Workspace)
		if err != nil {
			return err
		}
		utc := now.UTC()
		day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
		if n, err := tx.CountStyleLearnPasses(ctx, who.Workspace, day); err != nil {
			return err
		} else if n >= limit {
			return &Error{Status: 429, Code: api.ErrorCodeTooManyRelearns, Message: fmt.Sprintf("you can learn your style again %d times a day; try again tomorrow", limit)}
		}
		if err := tx.InsertStyleLearnPass(ctx, who.Workspace, who.User); err != nil {
			return err
		}
		assets, err := tx.ListStyleReferenceAssets(ctx, who.Workspace)
		if err != nil {
			return err
		}
		live := map[ids.AssetID]bool{}
		for _, a := range assets {
			if a.Status == store.AssetReady {
				live[a.ID] = true
			}
		}
		rows, err := tx.RequeueStyleReferences(ctx, who.Workspace)
		if err != nil {
			return err
		}
		queued := map[ids.AssetID]bool{}
		for _, r := range rows {
			if id, ok := store.StyleReferenceAsset(r); ok && live[id] {
				queued[id] = true
				if _, err := s.jobs.InsertInStore(ctx, tx, jobs.StyleReferenceArgs{WorkspaceID: who.Workspace, AssetID: id}, nil); err != nil {
					return err
				}
			}
		}
		// A ready reference whose row was never made (its ingest finished on a worker without the hook) is queued too.
		for id := range live {
			if queued[id] {
				continue
			}
			a, err := tx.GetAsset(ctx, who.Workspace, id)
			if err != nil {
				return err
			}
			if _, err := tx.QueueStyleReference(ctx, who.Workspace, id, a.Filename); err != nil {
				return err
			}
			if _, err := s.jobs.InsertInStore(ctx, tx, jobs.StyleReferenceArgs{WorkspaceID: who.Workspace, AssetID: id}, nil); err != nil {
				return err
			}
		}
		// The profile job follows every reference's; with no asset reference (only files, or none) it is the only job.
		return jobs.EnqueueStyleProfile(ctx, s.jobs, tx, who.Workspace, now)
	})
	if ae := (*Error)(nil); errors.As(err, &ae) {
		return View{}, ae
	}
	if err != nil {
		return View{}, s.internal(ctx, "learn again", err)
	}
	return s.view(ctx, who.Workspace)
}

// Reset forgets the style: the reference videos (their objects are removed by jobs), what was measured, the summary and the settings.
func (s *Service) Reset(ctx context.Context, who Actor) *Error {
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		assets, err := tx.ListStyleReferenceAssets(ctx, who.Workspace)
		if err != nil {
			return err
		}
		for _, a := range assets {
			_, u, err := tx.GetAssetUpload(ctx, who.Workspace, a.ID)
			if err != nil {
				return err
			}
			if u.CompletedAt == nil && u.AbortedAt == nil {
				if _, err := tx.AbortUpload(ctx, who.Workspace, a.ID, store.FailureUploadAborted, true); err != nil && !errors.Is(err, store.ErrNotActive) {
					return err
				}
			}
		}
		gone, err := tx.SoftDeleteStyleReferenceAssets(ctx, who.Workspace)
		if err != nil {
			return err
		}
		seen := map[ids.AssetID]bool{}
		for _, a := range append(assets, gone...) {
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			if _, err := s.jobs.InsertInStore(ctx, tx, jobs.AssetObjectDeleteArgs{WorkspaceID: who.Workspace, AssetID: a.ID}, nil); err != nil {
				return err
			}
		}
		if err := tx.DeleteStyleReferences(ctx, who.Workspace); err != nil {
			return err
		}
		if err := tx.DeleteCreatorStyle(ctx, who.Workspace); err != nil {
			return err
		}
		return tx.DeleteCreatorStyleSettings(ctx, who.Workspace)
	})
	if err != nil {
		return s.internal(ctx, "reset the style", err)
	}
	return nil
}
