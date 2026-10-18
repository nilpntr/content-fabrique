package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// creatorStyleSuite is part of repositorySuite (superuser, then the app role), so the creator style queries are proven to work with
// DML rights only.
func creatorStyleSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	newWorkspace := func(name string) store.Workspace {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_style_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	t.Run("none, save, replace, delete", func(t *testing.T) {
		w := newWorkspace("a")
		if _, err := st.GetCreatorStyle(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a workspace with no profile: %v", err)
		}
		row, err := st.SaveCreatorStyle(ctx, w.ID, store.NewCreatorStyle{Version: 1, Profile: json.RawMessage(`{"version":1,"references":2}`), Measures: json.RawMessage(`[{"name":"a"},{"name":"b"}]`), ReferencesCount: 2})
		if err != nil || row.ReferencesCount != 2 || row.Version != 1 {
			t.Fatalf("save: %+v %v", row, err)
		}
		got, err := st.GetCreatorStyle(ctx, w.ID)
		if err != nil || !strings.Contains(string(got.Profile), `"references": 2`) && !strings.Contains(string(got.Profile), `"references":2`) {
			t.Fatalf("get: %s %v", got.Profile, err)
		}
		// A second save replaces the first (one profile per workspace).
		if _, err = st.SaveCreatorStyle(ctx, w.ID, store.NewCreatorStyle{Version: 1, Profile: json.RawMessage(`{"version":1,"references":5}`), ReferencesCount: 5}); err != nil {
			t.Fatal(err)
		}
		got, err = st.GetCreatorStyle(ctx, w.ID)
		if err != nil || got.ReferencesCount != 5 || string(got.Measures) != "[]" {
			t.Fatalf("replace: %+v %v", got, err)
		}
		if err := st.DeleteCreatorStyle(ctx, w.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetCreatorStyle(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("after delete: %v", err)
		}
		// Deleting what is not there is fine.
		if err := st.DeleteCreatorStyle(ctx, w.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("one workspace never sees another's profile", func(t *testing.T) {
		a, b := newWorkspace("iso-a"), newWorkspace("iso-b")
		if _, err := st.SaveCreatorStyle(ctx, a.ID, store.NewCreatorStyle{Version: 1, Profile: json.RawMessage(`{"version":1}`), ReferencesCount: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetCreatorStyle(ctx, b.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace b sees workspace a's profile: %v", err)
		}
		if err := st.DeleteCreatorStyle(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetCreatorStyle(ctx, a.ID); err != nil {
			t.Fatalf("b's delete removed a's profile: %v", err)
		}
	})

	// A reference row belongs to its workspace: another workspace queueing the same asset id (the upsert's conflict on asset_id) changes
	// nothing and gets no row; the in-flight count and the passes are per workspace.
	t.Run("references and passes stay in their workspace", func(t *testing.T) {
		a, b := newWorkspace("ref-a"), newWorkspace("ref-b")
		u, err := st.UpsertUserByClerkID(ctx, "user_style_ref_"+suffix, "style-ref-"+suffix+"@example.com")
		if err != nil {
			t.Fatal(err)
		}
		id, err := st.NewAssetID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.CreateAssetWithUpload(ctx, a.ID, store.NewAsset{
			ID: id, Kind: store.AssetKindVideo, Filename: "ref.mp4", ContentType: "video/mp4", SizeBytes: 1 << 20, Title: "ref",
			R2Key: "ws/" + a.ID.String() + "/assets/" + id.String() + "/original", CreatedBy: u.ID, Purpose: store.PurposeStyleReference,
			R2UploadID: "r2-" + id.String(), PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
		}, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := st.QueueStyleReference(ctx, a.ID, id, "mine.mp4"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.FailStyleReference(ctx, a.ID, id, "analysis_failed", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.QueueStyleReference(ctx, b.ID, id, "theirs.mp4"); err == nil {
			t.Fatal("workspace b queued workspace a's reference")
		}
		if r, err := st.GetStyleReferenceByAsset(ctx, a.ID, id); err != nil || r.Name != "mine.mp4" || r.Status != store.StyleRefFailed {
			t.Fatalf("workspace a's row after b's upsert: %+v %v", r, err)
		}
		if _, err := st.GetStyleReferenceByAsset(ctx, b.ID, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace b sees the row: %v", err)
		}
		// The asset is not ready: its queued row is not in flight (nothing measures it until its ingest is done).
		if _, err := st.QueueStyleReference(ctx, a.ID, id, "mine.mp4"); err != nil {
			t.Fatal(err)
		}
		if n, err := st.CountStyleReferencesInFlight(ctx, a.ID, time.Now()); err != nil || n != 0 {
			t.Fatalf("in flight with an asset still uploading: %d %v", n, err)
		}
		for range 2 {
			if err := st.InsertStyleLearnPass(ctx, a.ID, u.ID); err != nil {
				t.Fatal(err)
			}
		}
		day := time.Now().Add(-time.Hour)
		if n, err := st.CountStyleLearnPasses(ctx, a.ID, day); err != nil || n != 2 {
			t.Errorf("a's passes: %d %v", n, err)
		}
		if n, err := st.CountStyleLearnPasses(ctx, b.ID, day); err != nil || n != 0 {
			t.Errorf("b's passes: %d %v", n, err)
		}
		if n, err := st.CountStyleLearnPasses(ctx, a.ID, time.Now().Add(time.Hour)); err != nil || n != 0 {
			t.Errorf("passes after now: %d %v", n, err)
		}
	})

	t.Run("what is not a profile is refused", func(t *testing.T) {
		w := newWorkspace("bad")
		for name, in := range map[string]store.NewCreatorStyle{
			"no version":      {Profile: json.RawMessage(`{}`)},
			"no profile":      {Version: 1},
			"not json":        {Version: 1, Profile: json.RawMessage(`{nope`)},
			"not an object":   {Version: 1, Profile: json.RawMessage(`[1]`)},
			"measures not []": {Version: 1, Profile: json.RawMessage(`{}`), Measures: json.RawMessage(`{"a":1}`)},
			"too big":         {Version: 1, Profile: json.RawMessage(`{"x":"` + strings.Repeat("a", 300_000) + `"}`)},
		} {
			if _, err := st.SaveCreatorStyle(ctx, w.ID, in); err == nil {
				t.Errorf("%s was stored", name)
			}
		}
		if _, err := st.GetCreatorStyle(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a refused save left a row: %v", err)
		}
	})
}
