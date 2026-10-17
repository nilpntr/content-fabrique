package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// assetsSuite is part of repositorySuite, which TestRepository runs as the superuser and TestRoleSplit runs again as the
// app role (DML rights only). It covers the tenancy rule of assets and uploads (workspace B can neither read nor change
// workspace A's rows: ErrNotFound / ErrNotActive, never the data) and the state transitions.
func assetsSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	newWorkspace := func(name string) (store.Workspace, ids.UserID) {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_assets_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		u, err := st.UpsertUserByClerkID(ctx, "user_assets_"+name+"_"+suffix, name+"@example.com")
		if err != nil {
			t.Fatal(err)
		}
		return w, u.ID
	}
	// newAsset starts an upload the way the service does: an id first, then both rows.
	newAsset := func(w store.Workspace, user ids.UserID, name string, expires time.Time) (store.Asset, store.Upload) {
		t.Helper()
		id, err := st.NewAssetID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a, u, err := st.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
			ID: id, Kind: store.AssetKindVideo, Filename: name + ".mp4", ContentType: "video/mp4", SizeBytes: 40 << 20,
			Title: name, R2Key: "ws/" + w.ID.String() + "/assets/" + id.String() + "/original", CreatedBy: user,
			R2UploadID: "r2-" + id.String(), PartSize: 16 << 20, PartCount: 3, ExpiresAt: expires,
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return a, u
	}
	future := time.Now().Add(24 * time.Hour)

	wa, ua := newWorkspace("a")
	wb, ub := newWorkspace("b")

	t.Run("create and read", func(t *testing.T) {
		a, u := newAsset(wa, ua, "clip", future)
		if !strings.HasPrefix(a.ID.String(), "ast_") || !strings.HasPrefix(u.ID.String(), "upl_") || a.ID.Version() != 7 {
			t.Fatalf("ids = %v %v", a.ID, u.ID)
		}
		if a.WorkspaceID != wa.ID || u.WorkspaceID != wa.ID || u.AssetID != a.ID {
			t.Fatalf("rows are not tied to the workspace: %+v %+v", a, u)
		}
		if a.Status != store.AssetUploading || a.Title != "clip" || a.Sha256 != nil || a.FailureReason != nil || a.DeletedAt != nil || len(a.Tags) != 0 {
			t.Fatalf("defaults: %+v", a)
		}
		if u.CompletedAt != nil || u.AbortedAt != nil || u.PartCount != 3 {
			t.Fatalf("upload: %+v", u)
		}
		got, gu, err := st.GetAssetUpload(ctx, wa.ID, a.ID)
		if err != nil || got.ID != a.ID || gu.ID != u.ID || gu.R2UploadID != u.R2UploadID {
			t.Fatalf("GetAssetUpload = %+v %+v %v", got, gu, err)
		}
	})

	t.Run("the database rejects what the service should have", func(t *testing.T) {
		id, _ := st.NewAssetID(ctx)
		base := store.NewAsset{ID: id, Kind: store.AssetKindVideo, Filename: "x.mp4", ContentType: "video/mp4", SizeBytes: 1, Title: "x",
			R2Key: "k/" + id.String(), CreatedBy: ua, R2UploadID: "r", PartSize: 1, PartCount: 1, ExpiresAt: future}
		for name, mutate := range map[string]func(*store.NewAsset){
			"kind":         func(n *store.NewAsset) { n.Kind = "document" },
			"empty name":   func(n *store.NewAsset) { n.Filename = "" },
			"size":         func(n *store.NewAsset) { n.SizeBytes = 0 },
			"sha256":       func(n *store.NewAsset) { n.SHA256 = "NOT-A-HASH" },
			"part count 0": func(n *store.NewAsset) { n.PartCount = 0 },
			"part count >": func(n *store.NewAsset) { n.PartCount = 10001 },
		} {
			in := base
			mutate(&in)
			if _, _, err := st.CreateAssetWithUpload(ctx, wa.ID, in, 0); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		// Nothing was left half-written by the failed attempts.
		if _, err := st.GetAsset(ctx, wa.ID, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a rejected asset exists: %v", err)
		}
	})

	t.Run("workspace B cannot reach workspace A's asset", func(t *testing.T) {
		a, u := newAsset(wa, ua, "secret", future)
		if _, err := st.GetAsset(ctx, wb.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetAsset: %v, want ErrNotFound", err)
		}
		if _, _, err := st.GetAssetUpload(ctx, wb.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetAssetUpload: %v, want ErrNotFound", err)
		}
		if _, err := st.CompleteUpload(ctx, wb.ID, a.ID); !errors.Is(err, store.ErrNotActive) {
			t.Errorf("CompleteUpload: %v, want ErrNotActive", err)
		}
		if _, err := st.AbortUpload(ctx, wb.ID, a.ID, store.FailureUploadAborted, true); !errors.Is(err, store.ErrNotActive) {
			t.Errorf("AbortUpload: %v, want ErrNotActive", err)
		}
		if _, err := st.SoftDeleteAsset(ctx, wb.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SoftDeleteAsset: %v, want ErrNotFound", err)
		}
		// B's listing does not show it, and B's count does not include it.
		rows, err := st.ListAssets(ctx, wb.ID, nil, 100)
		if err != nil || len(rows) != 0 {
			t.Errorf("ListAssets(B) = %d rows, %v", len(rows), err)
		}
		// A's asset and upload are untouched by all of the above.
		got, gu, err := st.GetAssetUpload(ctx, wa.ID, a.ID)
		if err != nil || got.Status != store.AssetUploading || got.DeletedAt != nil || gu.CompletedAt != nil || gu.AbortedAt != nil || gu.ID != u.ID {
			t.Errorf("A's rows changed: %+v %+v %v", got, gu, err)
		}
		// B cannot attach an upload to A's asset: the composite foreign key refuses it.
		if _, _, err := st.CreateAssetWithUpload(ctx, wb.ID, store.NewAsset{
			ID: a.ID, Kind: store.AssetKindVideo, Filename: "x.mp4", ContentType: "video/mp4", SizeBytes: 1, Title: "x",
			R2Key: "other", CreatedBy: ub, R2UploadID: "r", PartSize: 1, PartCount: 1, ExpiresAt: future,
		}, 0); err == nil {
			t.Error("B created an asset with A's id")
		}
	})

	t.Run("the deletion job reads only deleted or failed assets of its own workspace", func(t *testing.T) {
		a, _ := newAsset(wa, ua, "to-delete", future)
		if _, err := st.GetAssetForObjectDeletion(ctx, wa.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a live asset was returned to the deletion job: %v", err)
		}
		if _, err := st.SoftDeleteAsset(ctx, wa.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetAssetForObjectDeletion(ctx, wb.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("workspace B read A's deleted asset: %v", err)
		}
		got, err := st.GetAssetForObjectDeletion(ctx, wa.ID, a.ID)
		if err != nil || got.R2Key == "" {
			t.Errorf("GetAssetForObjectDeletion = %+v, %v", got, err)
		}
		if _, err := st.GetAsset(ctx, wa.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetAsset of a deleted asset: %v, want ErrNotFound", err)
		}
		if _, err := st.SoftDeleteAsset(ctx, wa.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a second delete: %v, want ErrNotFound", err)
		}
	})

	t.Run("complete moves uploading to uploaded exactly once", func(t *testing.T) {
		w, u := newWorkspace("complete")
		a, _ := newAsset(w, u, "done", future)
		done, err := st.CompleteUpload(ctx, w.ID, a.ID)
		if err != nil || done.Status != store.AssetUploaded {
			t.Fatalf("CompleteUpload = %+v, %v", done, err)
		}
		_, up, err := st.GetAssetUpload(ctx, w.ID, a.ID)
		if err != nil || up.CompletedAt == nil || up.AbortedAt != nil {
			t.Fatalf("upload after complete: %+v %v", up, err)
		}
		if _, err := st.CompleteUpload(ctx, w.ID, a.ID); !errors.Is(err, store.ErrNotActive) {
			t.Errorf("second CompleteUpload: %v, want ErrNotActive", err)
		}
		if _, err := st.AbortUpload(ctx, w.ID, a.ID, store.FailureUploadAborted, true); !errors.Is(err, store.ErrNotActive) {
			t.Errorf("AbortUpload after complete: %v, want ErrNotActive", err)
		}
		if got, _ := st.GetAsset(ctx, w.ID, a.ID); got.Status != store.AssetUploaded || got.DeletedAt != nil {
			t.Errorf("a refused abort changed the asset: %+v", got)
		}
	})

	t.Run("abort fails the asset, hides it when asked, and wins only once", func(t *testing.T) {
		w, u := newWorkspace("abort")
		hidden, _ := newAsset(w, u, "hidden", future)
		failed, err := st.AbortUpload(ctx, w.ID, hidden.ID, store.FailureUploadAborted, true)
		if err != nil || failed.Status != store.AssetFailed || failed.FailureReason == nil || *failed.FailureReason != store.FailureUploadAborted || failed.DeletedAt == nil {
			t.Fatalf("AbortUpload(hide) = %+v, %v", failed, err)
		}
		if _, err := st.GetAsset(ctx, w.ID, hidden.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a hidden asset is readable: %v", err)
		}
		if _, err := st.AbortUpload(ctx, w.ID, hidden.ID, store.FailureUploadAborted, true); !errors.Is(err, store.ErrNotActive) {
			t.Errorf("second AbortUpload: %v, want ErrNotActive", err)
		}
		if _, err := st.CompleteUpload(ctx, w.ID, hidden.ID); !errors.Is(err, store.ErrNotActive) {
			t.Errorf("CompleteUpload after abort: %v, want ErrNotActive", err)
		}

		visible, _ := newAsset(w, u, "visible", future)
		failed, err = st.AbortUpload(ctx, w.ID, visible.ID, store.FailureUploadExpired, false)
		if err != nil || failed.Status != store.AssetFailed || failed.DeletedAt != nil {
			t.Fatalf("AbortUpload(show) = %+v, %v", failed, err)
		}
		got, up, err := st.GetAssetUpload(ctx, w.ID, visible.ID)
		if err != nil || got.Status != store.AssetFailed || up.AbortedAt == nil {
			t.Errorf("a failed asset stays readable: %+v %+v %v", got, up, err)
		}
	})

	t.Run("a failed enqueue rolls the completion back", func(t *testing.T) {
		w, u := newWorkspace("enqueue")
		a, _ := newAsset(w, u, "e", future)
		boom := errors.New("enqueue failed")
		err := st.InTx(ctx, func(tx *store.Store) error {
			if _, err := tx.CompleteUpload(ctx, w.ID, a.ID); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		got, up, _ := st.GetAssetUpload(ctx, w.ID, a.ID)
		if got.Status != store.AssetUploading || up.CompletedAt != nil {
			t.Errorf("the completion survived a failed transaction: %+v %+v", got, up)
		}
	})

	t.Run("listing is newest first, paged by cursor, per workspace", func(t *testing.T) {
		w, u := newWorkspace("list")
		var made []ids.AssetID
		for i := range 5 {
			a, _ := newAsset(w, u, "item"+string(rune('a'+i)), future)
			made = append(made, a.ID)
		}
		other, ou := newWorkspace("list-other")
		newAsset(other, ou, "not-mine", future)
		hide, _ := newAsset(w, u, "deleted", future)
		if _, err := st.SoftDeleteAsset(ctx, w.ID, hide.ID); err != nil {
			t.Fatal(err)
		}

		var got []ids.AssetID
		var cursor *ids.AssetID
		for {
			rows, err := st.ListAssets(ctx, w.ID, cursor, 2)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				got = append(got, r.ID)
			}
			if len(rows) < 2 {
				break
			}
			cursor = &rows[len(rows)-1].ID
		}
		want := make([]ids.AssetID, len(made))
		for i, id := range made {
			want[len(made)-1-i] = id
		}
		if len(got) != len(want) {
			t.Fatalf("listed %d assets, want %d: %v", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order differs at %d: %v, want %v", i, got, want)
			}
		}
		if _, err := st.ListAssets(ctx, w.ID, nil, 0); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("limit 0: %v, want ErrInvalid", err)
		}
	})

	t.Run("active uploads are counted per workspace and not past expiry", func(t *testing.T) {
		w, u := newWorkspace("count")
		other, ou := newWorkspace("count-other")
		a, _ := newAsset(w, u, "one", future)
		newAsset(w, u, "two", future)
		newAsset(w, u, "expired", time.Now().Add(-time.Minute))
		newAsset(other, ou, "theirs", future)
		if n, err := st.CountActiveUploads(ctx, w.ID); err != nil || n != 2 {
			t.Errorf("CountActiveUploads = %d, %v, want 2", n, err)
		}
		if _, err := st.CompleteUpload(ctx, w.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		if n, _ := st.CountActiveUploads(ctx, w.ID); n != 1 {
			t.Errorf("after a completion CountActiveUploads = %d, want 1", n)
		}
	})

	t.Run("expired uploads are found across workspaces, once each", func(t *testing.T) {
		wx, ux := newWorkspace("expire-x")
		wy, uy := newWorkspace("expire-y")
		past := time.Now().Add(-time.Hour)
		ax, _ := newAsset(wx, ux, "x", past)
		ay, _ := newAsset(wy, uy, "y", past)
		live, _ := newAsset(wx, ux, "live", future)
		done, _ := newAsset(wy, uy, "done", past)
		if _, err := st.CompleteUpload(ctx, wy.ID, done.ID); err != nil {
			t.Fatal(err)
		}

		found := map[ids.AssetID]store.ExpiredUpload{}
		var after ids.UploadID
		for {
			rows, err := st.ListExpiredUploads(ctx, time.Now(), after, 1) // a page of one, to exercise the cursor
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 0 {
				break
			}
			for _, r := range rows {
				found[r.AssetID] = r
				after = r.UploadID
			}
		}
		for _, want := range []store.Asset{ax, ay} {
			r, ok := found[want.ID]
			if !ok || r.WorkspaceID != want.WorkspaceID || r.R2Key != want.R2Key || r.R2UploadID == "" {
				t.Errorf("expired upload of %v missing or wrong: %+v", want.ID, r)
			}
		}
		if _, ok := found[live.ID]; ok {
			t.Error("an upload that has not expired was listed")
		}
		if _, ok := found[done.ID]; ok {
			t.Error("a completed upload was listed")
		}
		// Acting on a row uses the row's own workspace, and only once.
		if _, err := st.AbortUpload(ctx, wx.ID, ax.ID, store.FailureUploadExpired, false); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AbortUpload(ctx, wx.ID, ax.ID, store.FailureUploadExpired, false); !errors.Is(err, store.ErrNotActive) {
			t.Errorf("second expiry: %v, want ErrNotActive", err)
		}
		rows, _ := st.ListExpiredUploads(ctx, time.Now(), ids.UploadID{}, 1000)
		for _, r := range rows {
			if r.AssetID == ax.ID {
				t.Error("an aborted upload is still listed as expired")
			}
		}
	})

	t.Run("a workspace's data goes with the workspace", func(t *testing.T) {
		// The foreign keys cascade; a made-up workspace id is refused.
		id, _ := st.NewAssetID(ctx)
		_, _, err := st.CreateAssetWithUpload(ctx, ids.WorkspaceID{UUID: uuid.New()}, store.NewAsset{
			ID: id, Kind: store.AssetKindImage, Filename: "x.png", ContentType: "image/png", SizeBytes: 1, Title: "x",
			R2Key: "nope/" + id.String(), CreatedBy: ua, R2UploadID: "r", PartSize: 1, PartCount: 1, ExpiresAt: future,
		}, 0)
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("asset in a workspace that does not exist: %v, want ErrNotFound", err)
		}
	})

	t.Run("a failed upload's objects may be deleted, a live one's may not", func(t *testing.T) {
		w, u := newWorkspace("objdel")
		a, _ := newAsset(w, u, "live", future)
		if _, err := st.GetAssetForObjectDeletion(ctx, w.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an uploading asset: %v, want ErrNotFound", err)
		}
		if _, err := st.AbortUpload(ctx, w.ID, a.ID, store.FailureUploadExpired, false); err != nil {
			t.Fatal(err)
		}
		if got, err := st.GetAssetForObjectDeletion(ctx, w.ID, a.ID); err != nil || got.Status != store.AssetFailed {
			t.Errorf("a failed asset: %+v, %v", got, err)
		}
		done, _ := newAsset(w, u, "done", future)
		if _, err := st.CompleteUpload(ctx, w.ID, done.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetAssetForObjectDeletion(ctx, w.ID, done.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an uploaded asset: %v, want ErrNotFound", err)
		}
	})

	t.Run("hidden assets are found only by the any-state lookup", func(t *testing.T) {
		w, u := newWorkspace("anystate")
		other, _ := newWorkspace("anystate-other")
		a, _ := newAsset(w, u, "hidden", future)
		if _, err := st.AbortUpload(ctx, w.ID, a.ID, store.FailureUploadAborted, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.GetAssetUpload(ctx, w.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetAssetUpload of a hidden asset: %v", err)
		}
		got, up, err := st.GetAssetUploadAnyState(ctx, w.ID, a.ID)
		if err != nil || got.DeletedAt == nil || up.AbortedAt == nil {
			t.Errorf("GetAssetUploadAnyState = %+v %+v %v", got, up, err)
		}
		if _, _, err := st.GetAssetUploadAnyState(ctx, other.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another workspace read a hidden asset: %v", err)
		}
	})

	t.Run("the cap on uploads in progress holds under concurrent creates", func(t *testing.T) {
		const limit = 25
		w, u := newWorkspace("cap")
		for i := range limit - 1 {
			newAsset(w, u, "pre"+string(rune('a'+i)), future)
		}
		create := func() error {
			id, err := st.NewAssetID(ctx)
			if err != nil {
				return err
			}
			_, _, err = st.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
				ID: id, Kind: store.AssetKindVideo, Filename: "c.mp4", ContentType: "video/mp4", SizeBytes: 1, Title: "c",
				R2Key: "cap/" + id.String(), CreatedBy: u, R2UploadID: "r", PartSize: 1, PartCount: 1, ExpiresAt: future,
			}, limit)
			return err
		}
		const racers = 10
		errs := make(chan error, racers)
		start := make(chan struct{})
		for range racers {
			go func() { <-start; errs <- create() }()
		}
		close(start)
		ok, refused := 0, 0
		for range racers {
			switch err := <-errs; {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrTooManyUploads):
				refused++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}
		if ok != 1 || refused != racers-1 {
			t.Errorf("%d created and %d refused with 24 in progress and a cap of 25, want 1 and %d", ok, refused, racers-1)
		}
		if n, _ := st.CountActiveUploads(ctx, w.ID); n != limit {
			t.Errorf("uploads in progress = %d, want %d", n, limit)
		}
		// Another workspace is not held up by this one's cap.
		other, ou := newWorkspace("cap-other")
		newAsset(other, ou, "free", future)
	})
	t.Run("ingest: claim, progress, finish, fail", func(t *testing.T) {
		a, _ := newAsset(wa, ua, "ingest", future)
		derivative := func(kind, name string) store.NewDerivative {
			return store.NewDerivative{Kind: kind, Key: "ws/" + wa.ID.String() + "/assets/" + a.ID.String() + "/" + name + "-" + suffix, ContentType: "image/jpeg", SizeBytes: 10, Meta: []byte(`{"width":720}`)}
		}
		// Not uploaded yet: nothing to claim.
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("claim of an asset that is still uploading = %v", err)
		}
		if err := st.InTx(ctx, func(tx *store.Store) error { _, err := tx.CompleteUpload(ctx, wa.ID, a.ID); return err }); err != nil {
			t.Fatal(err)
		}
		// Workspace B cannot claim, move, finish or fail A's asset.
		if _, err := st.ClaimAssetForIngest(ctx, wb.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B claimed A's asset: %v", err)
		}
		got, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID)
		if err != nil || got.Status != store.AssetProcessing || got.IngestProgress != 0 || got.IngestStage == nil || *got.IngestStage != store.StageStarting {
			t.Fatalf("claim = %+v %v", got, err)
		}
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID); err != nil {
			t.Fatalf("a retry must be able to claim again: %v", err)
		}
		if err := st.SetIngestProgress(ctx, wa.ID, a.ID, 55, store.StageProxy); err != nil {
			t.Fatal(err)
		}
		if err := st.SetIngestProgress(ctx, wb.ID, a.ID, 99, store.StageProxy); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B set A's progress: %v", err)
		}
		if _, err := st.FailAssetIngest(ctx, wb.ID, a.ID, store.FailureCorruptMedia); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B failed A's asset: %v", err)
		}
		res := store.IngestResult{
			DurationMs: 87135, Width: 2160, Height: 3840, FPS: 30, Rotation: 90, VideoCodec: "h264", AudioCodec: "aac", HasAudio: true, HDR: true,
			Probe:       []byte(`{"container":"mov"}`),
			Derivatives: []store.NewDerivative{derivative(store.DerivativePoster, "poster"), derivative(store.DerivativeAudio16k, "audio"), derivative(store.DerivativeProxy720, "proxy")},
		}
		if _, err := st.FinishAssetIngest(ctx, wb.ID, a.ID, res); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B finished A's asset: %v", err)
		}
		if rows, _ := st.ListAssetDerivatives(ctx, wa.ID, []ids.AssetID{a.ID}); len(rows) != 0 {
			t.Fatalf("a refused finish left derivatives: %v", rows)
		}
		done, err := st.FinishAssetIngest(ctx, wa.ID, a.ID, res)
		if err != nil || done.Status != store.AssetReady || done.IngestProgress != 100 || done.IngestStage != nil || done.FailureReason != nil ||
			*done.DurationMs != 87135 || *done.Width != 2160 || *done.Height != 3840 || *done.Rotation != 90 || !*done.HasAudio || !*done.Hdr || *done.VideoCodec != "h264" || string(done.Probe) != `{"container": "mov"}` {
			t.Fatalf("finish = %+v %v", done, err)
		}
		rows, err := st.ListAssetDerivatives(ctx, wa.ID, []ids.AssetID{a.ID})
		if err != nil || len(rows) != 3 || rows[0].Kind != store.DerivativeAudio16k || rows[2].Kind != store.DerivativeProxy720 || rows[1].WorkspaceID != wa.ID {
			t.Fatalf("derivatives = %+v %v", rows, err)
		}
		if rows, _ := st.ListAssetDerivatives(ctx, wb.ID, []ids.AssetID{a.ID}); len(rows) != 0 {
			t.Fatalf("workspace B lists A's derivatives: %v", rows)
		}

		// A re-run replaces the rows of its kinds and drops the kinds it no longer makes (the clip lost its audio).
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID); err != nil {
			t.Fatalf("a ready asset can be ingested again: %v", err)
		}
		res.Derivatives = []store.NewDerivative{derivative(store.DerivativePoster, "poster2"), derivative(store.DerivativeProxy720, "proxy")}
		res.HasAudio, res.AudioCodec = false, ""
		if _, err := st.FinishAssetIngest(ctx, wa.ID, a.ID, res); err != nil {
			t.Fatal(err)
		}
		rows, _ = st.ListAssetDerivatives(ctx, wa.ID, []ids.AssetID{a.ID})
		if len(rows) != 2 || rows[0].Kind != store.DerivativePoster || !strings.Contains(rows[0].R2Key, "poster2") {
			t.Fatalf("after the re-run: %+v", rows)
		}

		// A finish that fails half way (here: an unknown kind) writes nothing.
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		bad := res
		bad.Derivatives = []store.NewDerivative{{Kind: "nonsense", Key: "k-" + suffix, ContentType: "x", SizeBytes: 1}}
		if _, err := st.FinishAssetIngest(ctx, wa.ID, a.ID, bad); err == nil {
			t.Fatal("an unknown derivative kind was accepted")
		}
		if got, _ := st.GetAsset(ctx, wa.ID, a.ID); got.Status != store.AssetProcessing {
			t.Fatalf("a failed finish must roll back; status = %s", got.Status)
		}

		// Failing: only from uploaded or processing, with the reason kept.
		if rows, _ := st.ListAssetDerivatives(ctx, wa.ID, []ids.AssetID{a.ID}); len(rows) != 2 {
			t.Fatalf("derivatives before the failure: %v", rows)
		}
		failed, err := st.FailAssetIngest(ctx, wa.ID, a.ID, store.FailureCorruptMedia)
		if err != nil || failed.Status != store.AssetFailed || failed.FailureReason == nil || *failed.FailureReason != store.FailureCorruptMedia || failed.DeletedAt != nil {
			t.Fatalf("fail = %+v %v", failed, err)
		}
		if rows, _ := st.ListAssetDerivatives(ctx, wa.ID, []ids.AssetID{a.ID}); len(rows) != 0 {
			t.Fatalf("a failed asset keeps no derivatives, found %v", rows)
		}
		if _, err := st.FailAssetIngest(ctx, wa.ID, a.ID, store.FailureProcessing); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("failing a failed asset = %v", err)
		}
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("claiming a failed asset = %v", err)
		}
	})

	t.Run("ingest: a deleted asset is not claimed, finished or failed", func(t *testing.T) {
		a, _ := newAsset(wa, ua, "ingest-deleted", future)
		if err := st.InTx(ctx, func(tx *store.Store) error { _, err := tx.CompleteUpload(ctx, wa.ID, a.ID); return err }); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SoftDeleteAsset(ctx, wa.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.SetIngestProgress(ctx, wa.ID, a.ID, 70, store.StageProxy); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("progress of a deleted asset = %v", err)
		}
		if _, err := st.FinishAssetIngest(ctx, wa.ID, a.ID, store.IngestResult{DurationMs: 1, Width: 2, Height: 2}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("finish of a deleted asset = %v", err)
		}
		if _, err := st.FailAssetIngest(ctx, wa.ID, a.ID, store.FailureProcessing); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("fail of a deleted asset = %v", err)
		}
		if _, err := st.ClaimAssetForIngest(ctx, wa.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("claim of a deleted asset = %v", err)
		}
	})
}
