package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Rows as the repository returns them.
type (
	Asset         = db.Asset
	Upload        = db.Upload
	ExpiredUpload = db.ListExpiredUploadsRow
)

// The kinds and statuses the assets check constraints allow.
const (
	AssetKindVideo = "video"
	AssetKindAudio = "audio"
	AssetKindImage = "image"

	AssetUploading  = "uploading"
	AssetUploaded   = "uploaded"
	AssetProcessing = "processing"
	AssetReady      = "ready"
	AssetFailed     = "failed"
)

// The reasons an asset fails before it has been ingested (assets.failure_reason).
const (
	FailureUploadExpired = "upload_expired"
	FailureUploadAborted = "upload_aborted"
	FailureSizeMismatch  = "size_mismatch"
)

// ErrTooManyUploads is returned by CreateAssetWithUpload when the workspace already has the most uploads in progress.
var ErrTooManyUploads = errors.New("store: too many uploads in progress")

// ErrNotActive is returned when an upload cannot change state because it is not active any more: it was completed or
// aborted already (by a concurrent request, or the clean-up job). The asset exists.
var ErrNotActive = errors.New("store: upload is not active")

// NewAssetID returns an unused asset id, a UUIDv7 made here so the object key (which contains it) is known before
// anything is written. Other tables let the database make their ids (uuidv7()); this one cannot wait for the insert.
func (s *Store) NewAssetID(context.Context) (ids.AssetID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return ids.AssetID{}, fmt.Errorf("new asset id: %w", err)
	}
	return ids.AssetID{UUID: id}, nil
}

// NewAsset is the input of CreateAssetWithUpload. The caller has validated and normalised it (package assets); the
// database checks the rest.
type NewAsset struct {
	ID          ids.AssetID // from NewAssetID: R2Key contains it
	Kind        string
	Filename    string
	ContentType string
	SizeBytes   int64
	SHA256      string // lowercase hex; empty when the client gave none
	Title       string
	R2Key       string
	CreatedBy   ids.UserID

	R2UploadID string
	PartSize   int64
	PartCount  int
	ExpiresAt  time.Time
}

// CreateAssetWithUpload inserts the asset (status uploading) and its upload row in one transaction. With maxActive above
// zero it first takes the workspace's upload lock, counts the uploads in progress and returns ErrTooManyUploads when
// there are maxActive already: creators in one workspace queue on the lock, so the cap holds however many race.
func (s *Store) CreateAssetWithUpload(ctx context.Context, ws ids.WorkspaceID, in NewAsset, maxActive int) (Asset, Upload, error) {
	var (
		a Asset
		u Upload
	)
	err := s.inTx(ctx, func(tx *Store) error {
		if maxActive > 0 {
			if err := tx.q.LockWorkspaceUploads(ctx, ws.UUID.String()); err != nil {
				return mapErr(err)
			}
			n, err := tx.q.CountActiveUploads(ctx, ws)
			if err != nil {
				return mapErr(err)
			}
			if int(n) >= maxActive {
				return ErrTooManyUploads
			}
		}
		var sha *string
		if in.SHA256 != "" {
			sha = &in.SHA256
		}
		var err error
		a, err = tx.q.CreateAsset(ctx, db.CreateAssetParams{
			ID: in.ID, WorkspaceID: ws, Kind: in.Kind, Filename: in.Filename, ContentType: in.ContentType,
			SizeBytes: in.SizeBytes, Sha256: sha, Title: in.Title, R2Key: in.R2Key, CreatedBy: in.CreatedBy,
		})
		if err != nil {
			return mapErr(err)
		}
		u, err = tx.q.CreateUpload(ctx, db.CreateUploadParams{
			WorkspaceID: ws, AssetID: a.ID, R2UploadID: in.R2UploadID, PartSize: in.PartSize,
			PartCount: int32(in.PartCount), ExpiresAt: in.ExpiresAt, //nolint:gosec // 1..10000, checked by the caller and the table
		})
		return mapErr(err)
	})
	if err != nil {
		return Asset{}, Upload{}, err
	}
	return a, u, nil
}

// GetAsset returns the (not deleted) asset with this id in this workspace. An asset of another workspace is ErrNotFound.
func (s *Store) GetAsset(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Asset, error) {
	a, err := s.q.GetAsset(ctx, db.GetAssetParams{WorkspaceID: ws, ID: id})
	return a, mapErr(err)
}

// GetAssetUploadAnyState is GetAssetUpload without the filter on deleted assets, for a request that lost a race with an
// abort or a delete and must tell that from "no such asset". Use it for nothing else: a deleted asset is gone to the API.
func (s *Store) GetAssetUploadAnyState(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Asset, Upload, error) {
	a, err := s.q.GetAssetAnyState(ctx, db.GetAssetAnyStateParams{WorkspaceID: ws, ID: id})
	if err != nil {
		return Asset{}, Upload{}, mapErr(err)
	}
	u, err := s.q.GetUploadByAsset(ctx, db.GetUploadByAssetParams{WorkspaceID: ws, AssetID: id})
	if err != nil {
		return Asset{}, Upload{}, mapErr(err)
	}
	return a, u, nil
}

// GetAssetUpload returns the asset and its upload row. ErrNotFound when the asset is not in this workspace or is deleted.
func (s *Store) GetAssetUpload(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Asset, Upload, error) {
	a, err := s.GetAsset(ctx, ws, id)
	if err != nil {
		return Asset{}, Upload{}, err
	}
	u, err := s.q.GetUploadByAsset(ctx, db.GetUploadByAssetParams{WorkspaceID: ws, AssetID: id})
	if err != nil {
		return Asset{}, Upload{}, mapErr(err)
	}
	return a, u, nil
}

// ListAssets returns up to limit assets of the workspace, newest first, those older than before when it is not nil.
func (s *Store) ListAssets(ctx context.Context, ws ids.WorkspaceID, before *ids.AssetID, limit int) ([]Asset, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	var cursor pgtype.UUID
	if before != nil {
		cursor = pgtype.UUID{Bytes: before.UUID, Valid: true}
	}
	rows, err := s.q.ListAssets(ctx, db.ListAssetsParams{WorkspaceID: ws, Before: cursor, RowLimit: int32(min(limit, 1000))}) //nolint:gosec // capped
	return rows, mapErr(err)
}

// CountActiveUploads counts the uploads in progress of the workspace (not completed, aborted or expired).
func (s *Store) CountActiveUploads(ctx context.Context, ws ids.WorkspaceID) (int, error) {
	n, err := s.q.CountActiveUploads(ctx, ws)
	return int(n), mapErr(err)
}

// CompleteUpload marks the upload completed and the asset uploaded, in one transaction (run it inside InTx to enqueue the
// ingest job in the same one). ErrNotActive when the upload was completed or aborted already, or the asset is not
// uploading any more; nothing changes then.
func (s *Store) CompleteUpload(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Asset, error) {
	var a Asset
	err := s.inTx(ctx, func(tx *Store) error {
		if _, err := tx.q.MarkUploadCompleted(ctx, db.MarkUploadCompletedParams{WorkspaceID: ws, AssetID: id}); err != nil {
			return notActive(err)
		}
		var err error
		a, err = tx.q.MarkAssetUploaded(ctx, db.MarkAssetUploadedParams{WorkspaceID: ws, ID: id})
		return notActive(err)
	})
	return a, err
}

// AbortUpload marks the upload aborted and the asset failed with the reason (one of the Failure constants), in one
// transaction (run it inside InTx to enqueue a job in the same one). With hide the asset is also soft-deleted, so it
// disappears from the library; the deletion job can then remove whatever object it has. ErrNotActive when the upload
// was completed or aborted already; nothing changes then.
func (s *Store) AbortUpload(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string, hide bool) (Asset, error) {
	var a Asset
	err := s.inTx(ctx, func(tx *Store) error {
		if _, err := tx.q.MarkUploadAborted(ctx, db.MarkUploadAbortedParams{WorkspaceID: ws, AssetID: id}); err != nil {
			return notActive(err)
		}
		var err error
		a, err = tx.q.MarkAssetFailed(ctx, db.MarkAssetFailedParams{WorkspaceID: ws, ID: id, FailureReason: &reason, Hide: hide})
		return notActive(err)
	})
	return a, err
}

// SoftDeleteAsset sets deleted_at on the asset. ErrNotFound when it is not in this workspace or is deleted already.
func (s *Store) SoftDeleteAsset(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Asset, error) {
	a, err := s.q.SoftDeleteAsset(ctx, db.SoftDeleteAssetParams{WorkspaceID: ws, ID: id})
	return a, mapErr(err)
}

// GetAssetForObjectDeletion returns the asset only if it is deleted or failed: the object deletion job goes through it, so
// it can never be made to remove the objects of a live asset (an upload that failed has no object anyone may use).
// ErrNotFound otherwise.
func (s *Store) GetAssetForObjectDeletion(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Asset, error) {
	a, err := s.q.GetAssetForObjectDeletion(ctx, db.GetAssetForObjectDeletionParams{WorkspaceID: ws, ID: id})
	return a, mapErr(err)
}

// ListExpiredUploads is for the clean-up job and is the one asset query that spans workspaces: the active uploads whose
// expires_at is at or before now, in upload id order, after the given id (the zero ID starts at the beginning). The job
// then acts on each row with that row's own workspace.
func (s *Store) ListExpiredUploads(ctx context.Context, now time.Time, after ids.UploadID, limit int) ([]ExpiredUpload, error) {
	rows, err := s.q.ListExpiredUploads(ctx, db.ListExpiredUploadsParams{Now: now, After: after, RowLimit: int32(min(max(limit, 1), 1000))}) //nolint:gosec // capped
	return rows, mapErr(err)
}

// notActive maps "no row updated" to ErrNotActive.
func notActive(err error) error {
	if err == nil {
		return nil
	}
	if mapped := mapErr(err); errors.Is(mapped, ErrNotFound) {
		return ErrNotActive
	}
	return mapErr(err)
}
