package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func TestPlainFirstPrefersAPreviewWithoutShotNumbers(t *testing.T) {
	ready := func(r store.Render) bool { return r.Status == store.RenderReady }
	proof := store.Render{Status: store.RenderReady, Proof: true}
	plain := store.Render{Status: store.RenderReady}
	failed := store.Render{Status: store.RenderFailed}
	if r := store.PlainFirst([]store.Render{proof, failed, plain}, ready); r == nil || r.Proof {
		t.Errorf("a plain preview exists: got %+v", r)
	}
	if r := store.PlainFirst([]store.Render{failed, proof}, ready); r == nil || !r.Proof {
		t.Errorf("only a proof: got %+v", r)
	}
	if r := store.PlainFirst([]store.Render{failed}, ready); r != nil {
		t.Errorf("nothing usable: got %+v", r)
	}
}

// proofSuite is part of repositorySuite, which TestRepository runs as the superuser and TestRoleSplit runs again as the app role (DML
// rights; the app may delete clip feedback). It covers proof renders (a preview only, deduped apart from the plain preview) and the clip
// feedback store: one row per thing said about one range, the tenancy rule, and what the composite foreign keys refuse.
func proofSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	type fixture struct {
		w     store.Workspace
		user  ids.UserID
		asset ids.AssetID
		p     store.Project
	}
	newFixture := func(name string) fixture {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_proof_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		u, err := st.UpsertUserByClerkID(ctx, "user_proof_"+name+"_"+suffix, name+"-proof@example.com")
		if err != nil {
			t.Fatal(err)
		}
		id, err := st.NewAssetID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a, _, err := st.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
			ID: id, Kind: store.AssetKindVideo, Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1 << 20, Title: "a",
			R2Key: "ws/" + w.ID.String() + "/assets/" + id.String() + "/original", CreatedBy: u.ID,
			R2UploadID: "r2-" + id.String(), PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		var p store.Project
		if err := st.InTx(ctx, func(tx *store.Store) error {
			var err error
			if p, err = tx.CreateProject(ctx, w.ID, store.NewProject{Title: "Leg day", Format: store.Format9x16, CreatedBy: u.ID}); err != nil {
				return err
			}
			return tx.AddProjectAssets(ctx, w.ID, p.ID, []ids.AssetID{a.ID})
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddTimelineVersion(ctx, w.ID, p.ID, store.NewTimelineVersion{
			Version: 1, Spec: json.RawMessage(`{"schema":"x"}`), AuthorKind: store.AuthorUser, AuthorID: "usr_x",
		}); err != nil {
			t.Fatal(err)
		}
		return fixture{w: w, user: u.ID, asset: a.ID, p: p}
	}
	a, b := newFixture("a"), newFixture("b")

	t.Run("proof renders", func(t *testing.T) {
		render := func(profile string, proof bool) (store.Render, error) {
			return st.CreateRender(ctx, a.w.ID, store.NewRender{Project: a.p.ID, Version: 1, Profile: profile, Proof: proof, RequestedByKind: store.RenderRequestedByUser, RequestedByID: a.user.String()})
		}
		plain, err := render(store.ProfilePreview, false)
		if err != nil || plain.Proof {
			t.Fatalf("plain preview: %+v, %v", plain, err)
		}
		proof, err := render(store.ProfilePreview, true)
		if err != nil || !proof.Proof {
			t.Fatalf("a proof next to the plain preview: %+v, %v", proof, err)
		}
		if _, err := render(store.ProfilePreview, true); !errors.Is(err, store.ErrConflict) {
			t.Errorf("a second active proof: %v, want ErrConflict", err)
		}
		if got, err := st.GetActiveRender(ctx, a.w.ID, a.p.ID, 1, store.ProfilePreview, true); err != nil || got.ID != proof.ID {
			t.Errorf("active proof = %v, %v", got.ID, err)
		}
		if got, err := st.GetActiveRender(ctx, a.w.ID, a.p.ID, 1, store.ProfilePreview, false); err != nil || got.ID != plain.ID {
			t.Errorf("active plain preview = %v, %v", got.ID, err)
		}
		if _, err := render(store.ProfileFinal, true); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("a final proof: %v, want ErrInvalid (renders_proof_check)", err)
		}
	})

	t.Run("clip feedback", func(t *testing.T) {
		in := store.NewClipFeedback{
			Asset: a.asset, Kind: store.ClipNeverUse, StartMs: 12_000, EndMs: 15_500, Project: a.p.ID, Version: 1, ShotNumber: 5,
			ItemID: "c5", CreatedByKind: store.AuthorUser, CreatedByID: a.user.String(),
		}
		first, err := st.SaveClipFeedback(ctx, a.w.ID, in)
		if err != nil || first.ID.IsZero() || first.Kind != store.ClipNeverUse || first.ShotNumber != 5 {
			t.Fatalf("save: %+v, %v", first, err)
		}
		// The same thing about the same range again is the same row, with the new note and who said it last.
		in.Note, in.ShotNumber, in.CreatedByID = "blurry", 6, "usr_other"
		again, err := st.SaveClipFeedback(ctx, a.w.ID, in)
		if err != nil || again.ID != first.ID || again.Note != "blurry" || again.ShotNumber != 6 || again.CreatedByID != "usr_other" {
			t.Fatalf("save again: %+v, %v", again, err)
		}
		later := in
		later.Kind, later.StartMs, later.EndMs = store.ClipStartLater, 20_000, 22_000
		second, err := st.SaveClipFeedback(ctx, a.w.ID, later)
		if err != nil || second.ID == first.ID {
			t.Fatalf("start later: %+v, %v", second, err)
		}
		rows, err := st.ListClipFeedback(ctx, a.w.ID, nil, 10)
		if err != nil || len(rows) != 2 || rows[0].ID != second.ID {
			t.Fatalf("list: %d rows, %v", len(rows), err)
		}
		if rows, err := st.ListClipFeedback(ctx, a.w.ID, []ids.AssetID{b.asset}, 10); err != nil || len(rows) != 0 {
			t.Errorf("list on another clip: %d rows, %v", len(rows), err)
		}
		if rows, err := st.ListClipFeedback(ctx, a.w.ID, []ids.AssetID{a.asset}, 1); err != nil || len(rows) != 1 {
			t.Errorf("list with a limit: %d rows, %v", len(rows), err)
		}
		if rows, err := st.ListAllClipFeedback(ctx, a.w.ID, []ids.AssetID{a.asset}); err != nil || len(rows) != 2 {
			t.Errorf("list all: %d rows, %v", len(rows), err)
		}

		// Tenancy: workspace B neither sees nor deletes A's rows, and cannot store feedback on A's clip or project.
		if rows, err := st.ListClipFeedback(ctx, b.w.ID, nil, 10); err != nil || len(rows) != 0 {
			t.Errorf("B lists %d of A's rows (%v)", len(rows), err)
		}
		if _, err := st.GetClipFeedback(ctx, b.w.ID, first.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("B reads A's row: %v", err)
		}
		if err := st.DeleteClipFeedback(ctx, b.w.ID, first.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("B deletes A's row: %v", err)
		}
		cross := in
		cross.CreatedByID = b.user.String()
		if _, err := st.SaveClipFeedback(ctx, b.w.ID, cross); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("B stores feedback on A's clip: %v, want ErrNotFound", err)
		}
		cross.Asset = b.asset
		if _, err := st.SaveClipFeedback(ctx, b.w.ID, cross); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("B stores feedback for A's project: %v, want ErrNotFound", err)
		}

		// What the checks refuse.
		bad := in
		bad.StartMs, bad.EndMs = 5000, 5000
		if _, err := st.SaveClipFeedback(ctx, a.w.ID, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("an empty range: %v", err)
		}
		bad = in
		bad.Kind = "maybe"
		if _, err := st.SaveClipFeedback(ctx, a.w.ID, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("an unknown kind: %v", err)
		}
		bad = in
		bad.ShotNumber = 0
		if _, err := st.SaveClipFeedback(ctx, a.w.ID, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("shot 0: %v", err)
		}

		// The creator takes it back.
		if err := st.DeleteClipFeedback(ctx, a.w.ID, first.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetClipFeedback(ctx, a.w.ID, first.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a deleted row is still there: %v", err)
		}
		if err := st.DeleteClipFeedback(ctx, a.w.ID, first.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("deleting twice: %v", err)
		}
	})
}
