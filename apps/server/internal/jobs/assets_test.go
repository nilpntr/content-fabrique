package jobs_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// assetsEnv is env with object storage (a MinIO container) and an asset helper.
type assetsEnv struct {
	*env
	cfg  storage.Config
	objs *storage.Client
	user ids.UserID
}

func newAssetsEnv(t *testing.T) *assetsEnv {
	t.Helper()
	e := newEnv(t)
	cfg := storagetest.NewBucket(t)
	objs, err := storage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.api.UpsertUserByClerkID(context.Background(), "user_assets_jobs", "jobs@example.com")
	if err != nil {
		t.Fatal(err)
	}
	return &assetsEnv{env: e, cfg: cfg, objs: objs, user: u.ID}
}

// newAsset makes an asset with an upload that is really started in object storage.
func (e *assetsEnv) newAsset(t *testing.T, w store.Workspace, name string, expires time.Time) (store.Asset, store.Upload) {
	t.Helper()
	ctx := context.Background()
	id, err := e.api.NewAssetID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := storage.OriginalKey(w.ID, id)
	r2Upload, err := e.objs.CreateMultipart(ctx, key, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	a, u, err := e.api.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
		ID: id, Kind: store.AssetKindVideo, Filename: name + ".mp4", ContentType: "video/mp4", SizeBytes: 100, Title: name, R2Key: key,
		CreatedBy: e.user, R2UploadID: r2Upload, PartSize: 16 << 20, PartCount: 1, ExpiresAt: expires,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a, u
}

func (e *assetsEnv) put(t *testing.T, key string) {
	t.Helper()
	_, err := storagetest.S3(e.cfg).PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(e.cfg.Bucket), Key: aws.String(key), Body: strings.NewReader("data"),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *assetsEnv) exists(t *testing.T, key string) bool {
	t.Helper()
	_, err := e.objs.Head(context.Background(), key)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func (e *env) jobState(t *testing.T, kind string, args string) (state, output string) {
	t.Helper()
	err := e.super.QueryRow(context.Background(),
		`SELECT state::text, COALESCE((metadata->'output')::text, '') FROM river_job WHERE kind = $1 AND args->>'asset_id' = $2`, kind, args).Scan(&state, &output)
	if err != nil {
		t.Fatalf("job %s of %s: %v", kind, args, err)
	}
	return state, output
}

func TestAssetIngestJobIsPendingUntilP104(t *testing.T) {
	e := newAssetsEnv(t)
	ctx := context.Background()
	w := e.newWorkspace(t, "org_ingest")
	a, _ := e.newAsset(t, w, "clip", time.Now().Add(time.Hour))
	e.startWorker(t, jobs.WorkerOptions{})

	err := e.api.InTx(ctx, func(tx *store.Store) error {
		_, err := e.enq.InsertInStore(ctx, tx, jobs.AssetIngestArgs{WorkspaceID: w.ID, AssetID: a.ID}, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the ingest job to complete", func() bool {
		state, _ := e.jobState(t, "asset_ingest", a.ID.String())
		return state == "completed"
	})
	if _, out := e.jobState(t, "asset_ingest", a.ID.String()); !strings.Contains(out, "pending") {
		t.Errorf("recorded output = %s, want it to say pending", out)
	}
}

// The deletion job removes everything under the deleted asset's prefix and nothing else: not another asset's objects, not
// another workspace's, and nothing of a live asset.
func TestAssetObjectDeleteJob(t *testing.T) {
	e := newAssetsEnv(t)
	ctx := context.Background()
	wa, wb := e.newWorkspace(t, "org_del_a"), e.newWorkspace(t, "org_del_b")
	e.startWorker(t, jobs.WorkerOptions{Objects: e.objs})

	doomed, _ := e.newAsset(t, wa, "doomed", time.Now().Add(time.Hour))
	sibling, _ := e.newAsset(t, wa, "sibling", time.Now().Add(time.Hour))
	foreign, _ := e.newAsset(t, wb, "foreign", time.Now().Add(time.Hour))
	live, _ := e.newAsset(t, wa, "live", time.Now().Add(time.Hour))
	doomedKey := storage.OriginalKey(wa.ID, doomed.ID)
	doomedProxy := storage.AssetPrefix(wa.ID, doomed.ID) + "proxy_720.mp4"
	for _, k := range []string{doomedKey, doomedProxy, storage.OriginalKey(wa.ID, sibling.ID), storage.OriginalKey(wb.ID, foreign.ID), storage.OriginalKey(wa.ID, live.ID)} {
		e.put(t, k)
	}
	if _, err := e.api.SoftDeleteAsset(ctx, wa.ID, doomed.ID); err != nil {
		t.Fatal(err)
	}

	enqueue := func(ws ids.WorkspaceID, id ids.AssetID) {
		t.Helper()
		err := e.api.InTx(ctx, func(tx *store.Store) error {
			_, err := e.enq.InsertInStore(ctx, tx, jobs.AssetObjectDeleteArgs{WorkspaceID: ws, AssetID: id}, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Jobs that should do nothing: a live asset, and a deleted asset named under the wrong workspace.
	enqueue(wa.ID, live.ID)
	enqueue(wb.ID, doomed.ID)
	enqueue(wa.ID, doomed.ID)

	eventually(t, "the deletion job of the deleted asset to complete", func() bool {
		var n int
		err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'asset_object_delete' AND args->>'asset_id' = $1 AND state = 'completed'`, doomed.ID.String()).Scan(&n)
		return err == nil && n >= 1
	})
	if e.exists(t, doomedKey) || e.exists(t, doomedProxy) {
		t.Error("the deleted asset's objects are still there")
	}
	for name, key := range map[string]string{
		"a sibling asset":   storage.OriginalKey(wa.ID, sibling.ID),
		"another workspace": storage.OriginalKey(wb.ID, foreign.ID),
		"a live asset":      storage.OriginalKey(wa.ID, live.ID),
	} {
		if !e.exists(t, key) {
			t.Errorf("the object of %s was deleted", name)
		}
	}
	// The job for the live asset was cancelled, not run.
	eventually(t, "the job for the live asset to be cancelled", func() bool {
		state, _ := e.jobState(t, "asset_object_delete", live.ID.String())
		return state == "cancelled"
	})
}

// The clean-up aborts uploads past their expiry in storage and in the database, marks the assets failed, and leaves
// uploads that are still running, completed, or already aborted alone.
func TestUploadsExpireJob(t *testing.T) {
	e := newAssetsEnv(t)
	ctx := context.Background()
	wa, wb := e.newWorkspace(t, "org_exp_a"), e.newWorkspace(t, "org_exp_b")
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	staleA, upA := e.newAsset(t, wa, "stale-a", past)
	staleB, upB := e.newAsset(t, wb, "stale-b", past)
	running, upRunning := e.newAsset(t, wa, "running", future)
	completed, _ := e.newAsset(t, wa, "completed", past)
	if _, err := e.api.CompleteUpload(ctx, wa.ID, completed.ID); err != nil {
		t.Fatal(err)
	}
	// Storage already lost this one (its own lifecycle rule): aborting it must not be an error.
	gone, _ := e.newAsset(t, wb, "gone", past)
	if err := e.objs.Abort(ctx, gone.R2Key, mustUploadID(t, e, gone.ID)); err != nil {
		t.Fatal(err)
	}

	e.startWorker(t, jobs.WorkerOptions{Objects: e.objs})
	client, err := jobs.NewInsertClient(e.api.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, jobs.UploadsExpireArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the expire job to complete", func() bool {
		var n int
		err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'uploads_expire' AND state = 'completed'`).Scan(&n)
		return err == nil && n == 1
	})

	for _, c := range []struct {
		ws  ids.WorkspaceID
		a   store.Asset
		up  store.Upload
		raw string
	}{{wa.ID, staleA, upA, "a"}, {wb.ID, staleB, upB, "b"}} {
		got, up, err := e.api.GetAssetUpload(ctx, c.ws, c.a.ID)
		if err != nil || got.Status != store.AssetFailed || got.FailureReason == nil || *got.FailureReason != store.FailureUploadExpired || got.DeletedAt != nil || up.AbortedAt == nil {
			t.Errorf("stale %s: %+v %+v %v", c.raw, got, up, err)
		}
		if _, err := e.objs.ListParts(ctx, c.a.R2Key, c.up.R2UploadID); !errors.Is(err, storage.ErrNoSuchUpload) {
			t.Errorf("stale %s: the multipart upload still exists in storage (%v)", c.raw, err)
		}
	}
	if got, up, _ := e.api.GetAssetUpload(ctx, wa.ID, running.ID); got.Status != store.AssetUploading || up.AbortedAt != nil {
		t.Errorf("a running upload was touched: %+v %+v", got, up)
	}
	if _, err := e.objs.ListParts(ctx, running.R2Key, upRunning.R2UploadID); err != nil {
		t.Errorf("a running upload was aborted in storage: %v", err)
	}
	if got, up, _ := e.api.GetAssetUpload(ctx, wa.ID, completed.ID); got.Status != store.AssetUploaded || up.AbortedAt != nil || up.CompletedAt == nil {
		t.Errorf("a completed upload was touched: %+v %+v", got, up)
	}
	if got, _, _ := e.api.GetAssetUpload(ctx, wb.ID, gone.ID); got.Status != store.AssetFailed {
		t.Errorf("an upload storage had lost was not cleaned up: %+v", got)
	}
	var out string
	if err := e.super.QueryRow(ctx, `SELECT (metadata->'output')::text FROM river_job WHERE kind = 'uploads_expire'`).Scan(&out); err != nil || !strings.Contains(out, `"aborted": 3`) {
		t.Errorf("output = %s, %v, want 3 aborted", out, err)
	}
}

func mustUploadID(t *testing.T, e *assetsEnv, id ids.AssetID) string {
	t.Helper()
	var r2 string
	if err := e.super.QueryRow(context.Background(), `SELECT r2_upload_id FROM uploads WHERE asset_id = $1`, id).Scan(&r2); err != nil {
		t.Fatal(err)
	}
	return r2
}

// Without object storage the jobs that need it snooze: they neither pretend to succeed nor burn attempts (a deletion job
// that ran out of attempts for a missing setting would leave the objects behind for good).
func TestAssetJobsWithoutObjectStorageSnooze(t *testing.T) {
	e := newAssetsEnv(t)
	ctx := context.Background()
	w := e.newWorkspace(t, "org_nostorage")
	a, _ := e.newAsset(t, w, "x", time.Now().Add(-time.Hour))
	e.startWorker(t, jobs.WorkerOptions{}) // no Objects

	client, err := jobs.NewInsertClient(e.api.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, jobs.UploadsExpireArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	// A deleted asset's object deletion is waiting too.
	if _, err := e.api.SoftDeleteAsset(ctx, w.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, jobs.AssetObjectDeleteArgs{WorkspaceID: w.ID, AssetID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"uploads_expire", "asset_object_delete"} {
		eventually(t, "the "+kind+" job to be snoozed", func() bool {
			var n int
			err := e.super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = $1 AND state = 'scheduled' AND scheduled_at > now() + interval '4 minutes' AND COALESCE(cardinality(errors), 0) = 0 AND attempt = 0`, kind).Scan(&n)
			return err == nil && n == 1
		})
	}
	if got, _, _ := e.api.GetAssetUploadAnyState(ctx, w.ID, a.ID); got.Status != store.AssetUploading {
		t.Errorf("the asset changed although storage was unavailable: %+v", got)
	}
}

// An upload whose multipart upload was completed in storage while the database never heard of it (the request died between
// the two) leaves an object behind. The clean-up marks the upload expired and the object is deleted.
func TestUploadsExpireJobDeletesAnOrphanedObject(t *testing.T) {
	e := newAssetsEnv(t)
	ctx := context.Background()
	w := e.newWorkspace(t, "org_orphan")
	a, up := e.newAsset(t, w, "orphan", time.Now().Add(-time.Hour))
	// One part, completed in storage only.
	url, err := e.objs.PresignPart(ctx, a.R2Key, up.R2UploadID, 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader("data"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT part: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	parts, err := e.objs.ListParts(ctx, a.R2Key, up.R2UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.objs.Complete(ctx, a.R2Key, up.R2UploadID, parts); err != nil {
		t.Fatal(err)
	}
	if !e.exists(t, a.R2Key) {
		t.Fatal("the object is not there")
	}
	e.startWorker(t, jobs.WorkerOptions{Objects: e.objs})
	client, err := jobs.NewInsertClient(e.api.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, jobs.UploadsExpireArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the orphaned object to be deleted", func() bool { return !e.exists(t, a.R2Key) })
	got, upload, err := e.api.GetAssetUpload(ctx, w.ID, a.ID)
	if err != nil || got.Status != store.AssetFailed || upload.AbortedAt == nil {
		t.Errorf("asset after the clean-up: %+v %+v %v", got, upload, err)
	}
	var out string
	if err := e.super.QueryRow(ctx, `SELECT (metadata->'output')::text FROM river_job WHERE kind = 'uploads_expire'`).Scan(&out); err != nil || !strings.Contains(out, `"orphans": 1`) {
		t.Errorf("output = %s, %v, want 1 orphan", out, err)
	}
}
