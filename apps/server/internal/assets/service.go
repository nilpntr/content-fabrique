// Package assets is the assets API of P1-01: starting a resumable multipart upload, presigning its parts, reporting what
// object storage already holds (to resume), completing or aborting it, listing and deleting assets.
//
// Tenancy: every method takes the WorkspaceID of the verified token (the Handler reads it from the Principal, never from
// the request) and only reads or changes rows of that workspace. An asset id of another workspace is exactly as unknown as
// one that was never issued: 404 not_found. Object keys are built from ids (storage.OriginalKey) and never taken from a
// client; R2's multipart upload id is no field of any answer (it is only inside the signed part URLs).
//
// The database and object storage cannot be changed in one transaction, so each flow orders its steps so that a failure in
// between leaves only things the clean-up knows about: an object storage call is made before the row that depends on its
// result, an object is deleted by a job enqueued in the transaction that hides the asset, and every row transition is
// guarded (`WHERE ... active`) so that two requests racing for the same upload cannot both win.
package assets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// UploadTTL is how long an upload can be completed after it started; the clean-up job aborts it after that.
const UploadTTL = 24 * time.Hour

// MaxActiveUploads is the most uploads in progress (not completed, aborted or expired) one workspace can have.
const MaxActiveUploads = 25

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

var (
	errNotFound       = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such asset"}
	errNotActive      = &Error{Status: 409, Code: api.ErrorCodeUploadNotActive, Message: "this upload is not in progress any more"}
	errExpired        = &Error{Status: 409, Code: api.ErrorCodeUploadExpired, Message: "this upload expired; start a new one"}
	errStorageDown    = &Error{Status: 503, Code: api.ErrorCodeStorageUnavailable, Message: "object storage did not answer; try again"}
	errQueueMigration = &Error{Status: 503, Code: api.ErrorCodeQueueNotMigrated, Message: "the job queue is not ready; try again later"}
)

// Objects is object storage. *storage.Client is the real one.
type Objects interface {
	CreateMultipart(ctx context.Context, key, contentType string) (string, error)
	PresignPart(ctx context.Context, key, uploadID string, partNumber int, size int64) (string, error)
	ListParts(ctx context.Context, key, uploadID string) ([]storage.Part, error)
	Complete(ctx context.Context, key, uploadID string, parts []storage.Part) error
	Abort(ctx context.Context, key, uploadID string) error
	Head(ctx context.Context, key string) (storage.ObjectInfo, error)
}

// Enqueuer inserts a job in the transaction behind tx. *jobs.Enqueuer is the real one.
type Enqueuer interface {
	InsertInStore(ctx context.Context, tx *store.Store, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Service is the assets API without HTTP.
type Service struct {
	store   *store.Store
	objects Objects
	jobs    Enqueuer
	logger  *slog.Logger
	// Now replaces time.Now (tests).
	Now func() time.Time
}

// NewService returns a Service. All three dependencies are required.
func NewService(st *store.Store, objects Objects, enq Enqueuer, logger *slog.Logger) (*Service, error) {
	if st == nil || objects == nil || enq == nil {
		return nil, errors.New("assets: a store, object storage and a job enqueuer are required")
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

// Actor is who acts: the verified workspace and user of the request.
type Actor struct {
	Workspace ids.WorkspaceID
	User      ids.UserID
}

// ---- Create ----------------------------------------------------------------------------------------------------

// Created is the result of Create.
type Created struct {
	Asset       store.Asset
	Upload      store.Upload
	Parts       []api.PartUrl
	URLsExpires time.Time
}

// Create starts an upload: it validates the request, starts the multipart upload in object storage, stores the asset and
// the upload and presigns the first parts.
func (s *Service) Create(ctx context.Context, who Actor, req api.CreateAssetRequest) (Created, *Error) {
	sp, e := validateCreate(req)
	if e != nil {
		return Created{}, e
	}
	active, err := s.store.CountActiveUploads(ctx, who.Workspace)
	if err != nil {
		return Created{}, s.internal(ctx, "count active uploads", err)
	}
	if active >= MaxActiveUploads {
		return Created{}, &Error{Status: 429, Code: api.ErrorCodeTooManyUploads, Message: "too many uploads are in progress; finish or abort one first"}
	}

	assetID, err := s.store.NewAssetID(ctx)
	if err != nil {
		return Created{}, s.internal(ctx, "new asset id", err)
	}
	key := storage.OriginalKey(who.Workspace, assetID)
	r2UploadID, err := s.objects.CreateMultipart(ctx, key, sp.ContentType)
	if err != nil {
		return Created{}, s.storageErr(ctx, "create multipart upload", err)
	}
	partSize, partCount := PartPlan(sp.Size)
	asset, upload, err := s.store.CreateAssetWithUpload(ctx, who.Workspace, store.NewAsset{
		ID: assetID, Kind: sp.Kind, Filename: sp.Filename, ContentType: sp.ContentType, SizeBytes: sp.Size,
		SHA256: sp.SHA256, Title: sp.Title, R2Key: key, CreatedBy: who.User,
		R2UploadID: r2UploadID, PartSize: partSize, PartCount: partCount, ExpiresAt: s.now().Add(UploadTTL),
	})
	if err != nil {
		// Nothing was stored: discard the multipart upload, best effort (the bucket's lifecycle rule is the backstop).
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if aerr := s.objects.Abort(cctx, key, r2UploadID); aerr != nil {
			s.logger.WarnContext(ctx, "could not abort the multipart upload of an asset that was not stored", "error", aerr)
		}
		return Created{}, s.internal(ctx, "store asset", err)
	}

	numbers := make([]int, min(FirstBatch, partCount))
	for i := range numbers {
		numbers[i] = i + 1
	}
	urls, expires, err := s.presign(ctx, asset, upload, numbers)
	if err != nil {
		return Created{}, s.internal(ctx, "presign first parts", err)
	}
	return Created{Asset: asset, Upload: upload, Parts: urls, URLsExpires: expires}, nil
}

// presign builds the part URLs of the upload of asset for the part numbers (already validated).
func (s *Service) presign(ctx context.Context, a store.Asset, u store.Upload, numbers []int) ([]api.PartUrl, time.Time, error) {
	expires := s.now().Add(storage.PartURLExpiry)
	out := make([]api.PartUrl, 0, len(numbers))
	for _, n := range numbers {
		size := PartSizeOf(a.SizeBytes, u.PartSize, int(u.PartCount), n)
		url, err := s.objects.PresignPart(ctx, a.R2Key, u.R2UploadID, n, size)
		if err != nil {
			return nil, time.Time{}, err
		}
		out = append(out, api.PartUrl{PartNumber: n, Url: url, SizeBytes: size})
	}
	return out, expires, nil
}

// ---- Presign more parts ----------------------------------------------------------------------------------------

// PresignParts returns URLs for the part numbers of an upload in progress.
func (s *Service) PresignParts(ctx context.Context, who Actor, id ids.AssetID, numbers []int) ([]api.PartUrl, time.Time, *Error) {
	if len(numbers) < 1 || len(numbers) > MaxPartBatch {
		return nil, time.Time{}, invalid("part_numbers must hold between 1 and 100 part numbers")
	}
	a, u, e := s.loadUpload(ctx, who, id)
	if e != nil {
		return nil, time.Time{}, e
	}
	if e := s.requireActive(u); e != nil {
		return nil, time.Time{}, e
	}
	seen := make(map[int]bool, len(numbers))
	for _, n := range numbers {
		if n < 1 || n > int(u.PartCount) {
			return nil, time.Time{}, invalid("a part number is outside 1 to part_count")
		}
		if seen[n] {
			return nil, time.Time{}, invalid("a part number is listed twice")
		}
		seen[n] = true
	}
	urls, expires, err := s.presign(ctx, a, u, numbers)
	if err != nil {
		return nil, time.Time{}, s.internal(ctx, "presign parts", err)
	}
	return urls, expires, nil
}

// ---- Upload state (resume) -------------------------------------------------------------------------------------

// UploadState is what a client needs to resume.
type UploadState struct {
	Upload store.Upload
	Status api.UploadStatus
	Parts  []storage.Part
}

// State reports the upload and, while it is in progress, the parts object storage holds.
func (s *Service) State(ctx context.Context, who Actor, id ids.AssetID) (UploadState, *Error) {
	a, u, e := s.loadUpload(ctx, who, id)
	if e != nil {
		return UploadState{}, e
	}
	st := UploadState{Upload: u, Status: s.StatusOf(u)}
	if st.Status != api.UploadStatusActive {
		return st, nil
	}
	parts, err := s.objects.ListParts(ctx, a.R2Key, u.R2UploadID)
	switch {
	case errors.Is(err, storage.ErrNoSuchUpload):
		// Object storage dropped the upload (its own lifecycle rule). Nothing can be resumed; the clean-up job marks the rows.
		return st, nil
	case err != nil:
		return UploadState{}, s.storageErr(ctx, "list parts", err)
	}
	st.Parts = parts
	return st, nil
}

// StatusOf is the state of an upload as the API reports it.
func (s *Service) StatusOf(u store.Upload) api.UploadStatus {
	switch {
	case u.CompletedAt != nil:
		return api.UploadStatusCompleted
	case u.AbortedAt != nil:
		return api.UploadStatusAborted
	case !s.now().Before(u.ExpiresAt):
		return api.UploadStatusExpired
	}
	return api.UploadStatusActive
}

// requireActive is the guard of the calls that need an upload in progress.
func (s *Service) requireActive(u store.Upload) *Error {
	switch s.StatusOf(u) {
	case api.UploadStatusActive:
		return nil
	case api.UploadStatusExpired:
		return errExpired
	}
	return errNotActive
}

func (s *Service) loadUpload(ctx context.Context, who Actor, id ids.AssetID) (store.Asset, store.Upload, *Error) {
	a, u, err := s.store.GetAssetUpload(ctx, who.Workspace, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Asset{}, store.Upload{}, errNotFound
	}
	if err != nil {
		return store.Asset{}, store.Upload{}, s.internal(ctx, "get asset upload", err)
	}
	return a, u, nil
}

// ---- Complete --------------------------------------------------------------------------------------------------

// Complete finishes the upload of the asset from the parts the client lists. It compares them with what object storage
// holds, completes the multipart upload, checks the size of the object and marks the asset uploaded, enqueuing its ingest
// in the same transaction. Completing twice is fine: the second call returns the asset.
func (s *Service) Complete(ctx context.Context, who Actor, id ids.AssetID, parts []api.CompletedPartRef) (store.Asset, *Error) {
	a, u, e := s.loadUpload(ctx, who, id)
	if e != nil {
		return store.Asset{}, e
	}
	if u.CompletedAt != nil {
		return a, nil // already done (a retry, or a second tab)
	}
	if e := s.requireActive(u); e != nil {
		return store.Asset{}, e
	}
	if e := checkClaim(parts, int(u.PartCount)); e != nil {
		return store.Asset{}, e
	}

	remote, err := s.objects.ListParts(ctx, a.R2Key, u.R2UploadID)
	switch {
	case errors.Is(err, storage.ErrNoSuchUpload):
		return s.finishCompleted(ctx, who, a) // completed by an earlier call whose answer was lost, or gone
	case err != nil:
		return store.Asset{}, s.storageErr(ctx, "list parts", err)
	}
	if e := compareParts(a, u, parts, remote); e != nil {
		return store.Asset{}, e
	}

	switch err := s.objects.Complete(ctx, a.R2Key, u.R2UploadID, remote); {
	case errors.Is(err, storage.ErrNoSuchUpload):
		return s.finishCompleted(ctx, who, a)
	case errors.Is(err, storage.ErrInvalidParts):
		s.logger.InfoContext(ctx, "object storage refused the parts", "asset_id", a.ID.String(), "error", err)
		return store.Asset{}, &Error{Status: 422, Code: api.ErrorCodePartsMismatch, Message: "object storage refused the parts; upload them again"}
	case err != nil:
		return store.Asset{}, s.storageErr(ctx, "complete multipart upload", err)
	}
	return s.finishCompleted(ctx, who, a)
}

// finishCompleted runs once the multipart upload is complete in object storage (or was, earlier): it checks the object,
// then marks the asset uploaded and enqueues its ingest.
func (s *Service) finishCompleted(ctx context.Context, who Actor, a store.Asset) (store.Asset, *Error) {
	info, err := s.objects.Head(ctx, a.R2Key)
	if errors.Is(err, storage.ErrNotFound) {
		// The upload is gone and left no object: aborted or expired meanwhile.
		return store.Asset{}, s.dropGoneUpload(ctx, who, a)
	}
	if err != nil {
		return store.Asset{}, s.storageErr(ctx, "head object", err)
	}
	if info.Size != a.SizeBytes {
		// The object is not what was announced: the upload failed. Hide the asset, keep the reason, delete the object.
		s.logger.WarnContext(ctx, "completed object has the wrong size", "asset_id", a.ID.String(), "want", a.SizeBytes, "got", info.Size)
		if _, e := s.abortRows(ctx, who.Workspace, a.ID, store.FailureSizeMismatch, true); e != nil && !errors.Is(e, store.ErrNotActive) {
			return store.Asset{}, s.internal(ctx, "fail asset", e)
		}
		return store.Asset{}, &Error{Status: 422, Code: api.ErrorCodeSizeMismatch, Message: "the uploaded file is not the size announced; start a new upload"}
	}

	var done store.Asset
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		if done, err = tx.CompleteUpload(ctx, who.Workspace, a.ID); err != nil {
			return err
		}
		_, err = s.jobs.InsertInStore(ctx, tx, jobs.AssetIngestArgs{WorkspaceID: who.Workspace, AssetID: a.ID}, nil)
		return err
	})
	switch {
	case err == nil:
		return done, nil
	case errors.Is(err, store.ErrNotActive):
		// Another request moved the upload while this one was in storage. If it completed, this is its asset too.
		cur, cu, e := s.loadUpload(ctx, who, a.ID)
		if e != nil {
			return store.Asset{}, e
		}
		if cu.CompletedAt != nil {
			return cur, nil
		}
		// Aborted: the object just completed is not wanted. Its deletion job is queued by the abort and again here.
		if derr := s.enqueueDelete(ctx, who.Workspace, a.ID); derr != nil {
			s.logger.ErrorContext(ctx, "could not enqueue the deletion of an object of an aborted upload", "asset_id", a.ID.String(), "error", derr)
		}
		return store.Asset{}, errNotActive
	case errors.Is(err, store.ErrQueueNotMigrated):
		return store.Asset{}, errQueueMigration
	}
	return store.Asset{}, s.internal(ctx, "complete upload", err)
}

// dropGoneUpload handles an upload that no longer exists in object storage and left no object: the rows follow.
func (s *Service) dropGoneUpload(ctx context.Context, who Actor, a store.Asset) *Error {
	if _, err := s.abortRows(ctx, who.Workspace, a.ID, store.FailureUploadExpired, false); err != nil && !errors.Is(err, store.ErrNotActive) {
		return s.internal(ctx, "fail asset", err)
	}
	return errNotActive
}

// abortRows marks the upload aborted and the asset failed, and enqueues the deletion of the asset's objects when the asset
// is hidden, all in one transaction.
func (s *Service) abortRows(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string, hide bool) (store.Asset, error) {
	var a store.Asset
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		var err error
		if a, err = tx.AbortUpload(ctx, ws, id, reason, hide); err != nil {
			return err
		}
		if hide {
			_, err = s.jobs.InsertInStore(ctx, tx, jobs.AssetObjectDeleteArgs{WorkspaceID: ws, AssetID: id}, nil)
		}
		return err
	})
	return a, err
}

func (s *Service) enqueueDelete(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) error {
	return s.store.InTx(ctx, func(tx *store.Store) error {
		_, err := s.jobs.InsertInStore(ctx, tx, jobs.AssetObjectDeleteArgs{WorkspaceID: ws, AssetID: id}, nil)
		return err
	})
}

// checkClaim checks the shape of the parts a client lists: every part number 1..count exactly once, each with an ETag.
func checkClaim(parts []api.CompletedPartRef, count int) *Error {
	if len(parts) == 0 {
		return invalid("parts must list every part with its ETag")
	}
	if len(parts) != count {
		return &Error{Status: 422, Code: api.ErrorCodePartsMismatch, Message: fmt.Sprintf("the upload has %d parts, %d were listed", count, len(parts))}
	}
	seen := make([]bool, count+1)
	for _, p := range parts {
		if p.PartNumber < 1 || p.PartNumber > count {
			return &Error{Status: 422, Code: api.ErrorCodePartsMismatch, Message: "a part number is outside 1 to part_count"}
		}
		if seen[p.PartNumber] {
			return &Error{Status: 422, Code: api.ErrorCodePartsMismatch, Message: "a part number is listed twice"}
		}
		seen[p.PartNumber] = true
		if storage.NormalizeETag(p.Etag) == "" {
			return invalid("every part needs its ETag")
		}
	}
	return nil
}

// compareParts checks that object storage holds every part, at the announced size, with the ETag the client has.
func compareParts(a store.Asset, u store.Upload, claimed []api.CompletedPartRef, remote []storage.Part) *Error {
	byNumber := make(map[int]storage.Part, len(remote))
	for _, p := range remote {
		byNumber[p.Number] = p
	}
	etags := make(map[int]string, len(claimed))
	for _, p := range claimed {
		etags[p.PartNumber] = storage.NormalizeETag(p.Etag)
	}
	var bad []int
	for n := 1; n <= int(u.PartCount); n++ {
		r, ok := byNumber[n]
		if !ok || r.Size != PartSizeOf(a.SizeBytes, u.PartSize, int(u.PartCount), n) || storage.NormalizeETag(r.ETag) != etags[n] {
			bad = append(bad, n)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	slices.Sort(bad)
	shown := bad[:min(len(bad), 10)]
	list := make([]string, len(shown))
	for i, n := range shown {
		list[i] = strconv.Itoa(n)
	}
	msg := "parts " + strings.Join(list, ", ")
	if len(bad) > len(shown) {
		msg += fmt.Sprintf(" and %d more", len(bad)-len(shown))
	}
	return &Error{Status: 422, Code: api.ErrorCodePartsMismatch, Message: msg + " are missing or differ from what storage holds; upload them again"}
}

// ---- Abort and delete ------------------------------------------------------------------------------------------

// Abort discards an upload in progress and removes the asset.
func (s *Service) Abort(ctx context.Context, who Actor, id ids.AssetID) *Error {
	a, u, e := s.loadUpload(ctx, who, id)
	if e != nil {
		return e
	}
	if u.CompletedAt != nil || u.AbortedAt != nil {
		return errNotActive
	}
	return s.abort(ctx, who, a, u)
}

// abort aborts the multipart upload in object storage first (a failure there leaves the rows as they are, so the client
// can retry), then hides the asset and queues the deletion of any object in one transaction.
func (s *Service) abort(ctx context.Context, who Actor, a store.Asset, u store.Upload) *Error {
	if err := s.objects.Abort(ctx, a.R2Key, u.R2UploadID); err != nil {
		return s.storageErr(ctx, "abort multipart upload", err)
	}
	_, err := s.abortRows(ctx, who.Workspace, a.ID, store.FailureUploadAborted, true)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotActive):
		// Completed or aborted by someone else meanwhile. Say which.
		if _, cu, e := s.loadUpload(ctx, who, a.ID); e == nil && cu.CompletedAt != nil {
			return errNotActive
		}
		return nil
	case errors.Is(err, store.ErrQueueNotMigrated):
		return errQueueMigration
	}
	return s.internal(ctx, "abort upload", err)
}

// Delete removes an asset: an upload in progress is aborted, anything else is soft-deleted and its objects are queued for
// deletion.
func (s *Service) Delete(ctx context.Context, who Actor, id ids.AssetID) *Error {
	a, u, e := s.loadUpload(ctx, who, id)
	if e != nil {
		return e
	}
	if u.CompletedAt == nil && u.AbortedAt == nil {
		return s.abort(ctx, who, a, u)
	}
	err := s.store.InTx(ctx, func(tx *store.Store) error {
		if _, err := tx.SoftDeleteAsset(ctx, who.Workspace, id); err != nil {
			return err
		}
		_, err := s.jobs.InsertInStore(ctx, tx, jobs.AssetObjectDeleteArgs{WorkspaceID: who.Workspace, AssetID: id}, nil)
		return err
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return errNotFound
	case errors.Is(err, store.ErrQueueNotMigrated):
		return errQueueMigration
	}
	return s.internal(ctx, "delete asset", err)
}

// ---- Read ------------------------------------------------------------------------------------------------------

// Get returns one asset of the workspace.
func (s *Service) Get(ctx context.Context, who Actor, id ids.AssetID) (store.Asset, *Error) {
	a, err := s.store.GetAsset(ctx, who.Workspace, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Asset{}, errNotFound
	}
	if err != nil {
		return store.Asset{}, s.internal(ctx, "get asset", err)
	}
	return a, nil
}

// DefaultPageSize and MaxPageSize bound a list page.
const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

// List returns a page of the workspace's assets, newest first, and the cursor of the next page ("" on the last one). The
// cursor is the id of the last asset of the page.
func (s *Service) List(ctx context.Context, who Actor, limit int, cursor string) ([]store.Asset, string, *Error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, "", invalid("limit must be between 1 and 100")
	}
	var before *ids.AssetID
	if cursor != "" {
		id, err := ids.ParseAssetID(cursor)
		if err != nil {
			return nil, "", invalid("cursor is not valid")
		}
		before = &id
	}
	rows, err := s.store.ListAssets(ctx, who.Workspace, before, limit+1)
	if err != nil {
		return nil, "", s.internal(ctx, "list assets", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID.String()
	}
	return rows, next, nil
}

// ---- Failures --------------------------------------------------------------------------------------------------

// internal logs an unexpected failure and returns the 500 that says nothing about it.
func (s *Service) internal(ctx context.Context, what string, err error) *Error {
	if errors.Is(err, store.ErrQueueNotMigrated) {
		return errQueueMigration
	}
	s.logger.ErrorContext(ctx, "assets: "+what+" failed", "error", err)
	return &Error{Status: 500, Code: api.ErrorCodeInternalError, Message: "something went wrong"}
}

// storageErr logs a failure of object storage and returns the 503 for it.
func (s *Service) storageErr(ctx context.Context, what string, err error) *Error {
	s.logger.ErrorContext(ctx, "assets: object storage: "+what+" failed", "error", err)
	return errStorageDown
}
