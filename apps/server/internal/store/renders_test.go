package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// rendersSuite is part of repositorySuite, which TestRepository runs as the superuser and TestRoleSplit runs again as the app
// role (DML rights only; no DELETE on renders). It covers the state machine of a render, the dedupe index, the tenancy rule
// (workspace B can neither read nor change workspace A's renders) and what the composite foreign keys refuse.
func rendersSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	newWorkspace := func(name string) (store.Workspace, ids.UserID) {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_renders_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		u, err := st.UpsertUserByClerkID(ctx, "user_renders_"+name+"_"+suffix, name+"-renders@example.com")
		if err != nil {
			t.Fatal(err)
		}
		return w, u.ID
	}
	// A project with versions 1 and 2 (the content of a spec does not matter to the table).
	newProject := func(w store.Workspace, user ids.UserID) store.Project {
		t.Helper()
		id, err := st.NewAssetID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a, _, err := st.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
			ID: id, Kind: store.AssetKindVideo, Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1 << 20, Title: "a",
			R2Key: "ws/" + w.ID.String() + "/assets/" + id.String() + "/original", CreatedBy: user,
			R2UploadID: "r2-" + id.String(), PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		var p store.Project
		err = st.InTx(ctx, func(tx *store.Store) error {
			var err error
			if p, err = tx.CreateProject(ctx, w.ID, store.NewProject{Title: "Back day", Format: store.Format9x16, CreatedBy: user}); err != nil {
				return err
			}
			return tx.AddProjectAssets(ctx, w.ID, p.ID, []ids.AssetID{a.ID})
		})
		if err != nil {
			t.Fatal(err)
		}
		for v := 1; v <= 2; v++ {
			if _, err := st.AddTimelineVersion(ctx, w.ID, p.ID, store.NewTimelineVersion{
				Version: v, Parent: v - 1, Spec: json.RawMessage(`{"schema":"x"}`), AuthorKind: store.AuthorUser, AuthorID: "usr_x",
			}); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	newRender := func(w store.Workspace, p store.Project, version int, profile string) store.Render {
		t.Helper()
		r, err := st.CreateRender(ctx, w.ID, store.NewRender{Project: p.ID, Version: version, Profile: profile, RequestedByKind: store.RenderRequestedByAgent, RequestedByID: "run_x"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	result := func(w store.Workspace, p store.Project, r store.Render) store.RenderResult {
		prefix := "ws/" + w.ID.String() + "/projects/" + p.ID.String() + "/renders/" + r.ID.String() + "/"
		return store.RenderResult{
			MP4Key: prefix + "render.mp4", ContactSheetKey: prefix + "contact_sheet.jpg", TimeMapKey: prefix + "time_map.json",
			DurationMs: 30500, Width: 540, Height: 960, SizeBytes: 4 << 20, ContactSheetBytes: 90 << 10, TimeMapBytes: 2 << 10,
			ContactSheet: json.RawMessage(`{"columns":4,"tile_width":216,"tile_height":384,"tiles":[]}`), SoundCues: json.RawMessage(`[]`), CPUMs: 16000, WallMs: 4000,
		}
	}

	wa, ua := newWorkspace("a")
	wb, ub := newWorkspace("b")
	pa, pb := newProject(wa, ua), newProject(wb, ub)

	t.Run("a render goes queued, rendering, ready", func(t *testing.T) {
		r := newRender(wa, pa, 1, store.ProfilePreview)
		if !strings.HasPrefix(r.ID.String(), "ren_") || r.ID.Version() != 7 || r.WorkspaceID != wa.ID || r.Status != store.RenderQueued || r.Progress != 0 ||
			r.TimelineVersion != 1 || r.Profile != store.ProfilePreview || r.RequestedByKind != store.RenderRequestedByAgent || r.RequestedByID != "run_x" || string(r.SoundCues) != "[]" || r.Mp4Key != nil {
			t.Fatalf("new render: %+v", r)
		}
		if _, err := st.ClaimRender(ctx, wa.ID, r.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.SetRenderProgress(ctx, wa.ID, r.ID, 42, store.RenderStageRendering); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetRender(ctx, wa.ID, r.ID)
		if err != nil || got.Status != store.RenderRendering || got.Progress != 42 || got.Stage == nil || *got.Stage != store.RenderStageRendering || got.StartedAt == nil {
			t.Fatalf("running render: %+v, %v", got, err)
		}
		// 100 is for a ready row only: a running render shows at most 99, whatever is written.
		if err := st.SetRenderProgress(ctx, wa.ID, r.ID, 100, store.RenderStageUploading); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetRender(ctx, wa.ID, r.ID); got.Progress != store.MaxRunningProgress {
			t.Errorf("a running render shows progress %d after 100 was written, want %d", got.Progress, store.MaxRunningProgress)
		}
		// A retry claims it again and starts over.
		again, err := st.ClaimRender(ctx, wa.ID, r.ID)
		if err != nil || again.Progress != 0 || again.Status != store.RenderRendering {
			t.Fatalf("second claim: %+v, %v", again, err)
		}
		done, err := st.FinishRender(ctx, wa.ID, r.ID, result(wa, pa, r))
		if err != nil || done.Status != store.RenderReady || done.Progress != 100 || done.Stage != nil || done.FinishedAt == nil || done.Mp4Key == nil ||
			done.DurationMs == nil || *done.DurationMs != 30500 || *done.ContactSheetBytes != 90<<10 || !strings.Contains(string(done.ContactSheet), "tile_width") {
			t.Fatalf("ready render: %+v, %v", done, err)
		}
		// Finished: it cannot be claimed, failed or have progress written again.
		if _, err := st.ClaimRender(ctx, wa.ID, r.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("claim of a ready render: %v", err)
		}
		if _, err := st.FailRender(ctx, wa.ID, r.ID, "render_failed", -1, -1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("fail of a ready render: %v", err)
		}
		if err := st.SetRenderProgress(ctx, wa.ID, r.ID, 5, "x"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("progress of a ready render: %v", err)
		}
	})

	t.Run("failing", func(t *testing.T) {
		r := newRender(wa, pa, 2, store.ProfileFinal)
		if err := st.SetRenderProgress(ctx, wa.ID, r.ID, 5, "x"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("progress of a queued render: %v", err)
		}
		f, err := st.FailRender(ctx, wa.ID, r.ID, "invalid_timeline", 1500, 6000)
		if err != nil || f.Status != store.RenderFailed || f.FailureReason == nil || *f.FailureReason != "invalid_timeline" || f.FinishedAt == nil || *f.WallMs != 1500 || *f.CpuMs != 6000 {
			t.Fatalf("failed render: %+v, %v", f, err)
		}
		if _, err := st.FailRender(ctx, wa.ID, r.ID, "again", -1, -1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a second fail: %v", err)
		}
		if _, err := st.FailRender(ctx, wa.ID, r.ID, "", -1, -1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("an empty reason: %v", err)
		}
	})

	t.Run("one active render per version and profile", func(t *testing.T) {
		r := newRender(wa, pa, 1, store.ProfileFinal)
		if _, err := st.CreateRender(ctx, wa.ID, store.NewRender{Project: pa.ID, Version: 1, Profile: store.ProfileFinal, RequestedByKind: store.RenderRequestedByUser, RequestedByID: ua.String()}); !errors.Is(err, store.ErrConflict) {
			t.Errorf("a second active render of the same version and profile: %v", err)
		}
		got, err := st.GetActiveRender(ctx, wa.ID, pa.ID, 1, store.ProfileFinal)
		if err != nil || got.ID != r.ID {
			t.Errorf("GetActiveRender = %+v, %v", got, err)
		}
		// Once it failed, a new one may be made.
		if _, err := st.FailRender(ctx, wa.ID, r.ID, "render_failed", -1, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetActiveRender(ctx, wa.ID, pa.ID, 1, store.ProfileFinal); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetActiveRender after the failure: %v", err)
		}
		if _, err := st.CreateRender(ctx, wa.ID, store.NewRender{Project: pa.ID, Version: 1, Profile: store.ProfileFinal, RequestedByKind: store.RenderRequestedByUser, RequestedByID: ua.String()}); err != nil {
			t.Errorf("a render after a failed one: %v", err)
		}
		list, err := st.ListRenders(ctx, wa.ID, pa.ID, store.RenderFilter{Version: 1, Profile: store.ProfileFinal}, 10)
		if err != nil || len(list) != 2 || list[0].ID.String() < list[1].ID.String() {
			t.Errorf("ListRenders = %d renders (newest first?), %v", len(list), err)
		}
	})

	t.Run("the database rejects what the service should have", func(t *testing.T) {
		for name, in := range map[string]store.NewRender{
			"a profile that does not exist": {Project: pa.ID, Version: 1, Profile: "draft", RequestedByKind: "user", RequestedByID: "x"},
			"a requester that is not one":   {Project: pa.ID, Version: 1, Profile: store.ProfilePreview, RequestedByKind: "bot", RequestedByID: "x"},
			"no requester id":               {Project: pa.ID, Version: 1, Profile: store.ProfilePreview, RequestedByKind: "user"},
		} {
			if _, err := st.CreateRender(ctx, wa.ID, in); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		// A version that does not exist, and a project of another workspace.
		if _, err := st.CreateRender(ctx, wa.ID, store.NewRender{Project: pa.ID, Version: 3, Profile: store.ProfilePreview, RequestedByKind: "user", RequestedByID: "x"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a version that does not exist: %v", err)
		}
		if _, err := st.CreateRender(ctx, wa.ID, store.NewRender{Project: pb.ID, Version: 1, Profile: store.ProfilePreview, RequestedByKind: "user", RequestedByID: "x"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another workspace's project: %v", err)
		}
		if _, err := st.CreateRender(ctx, wa.ID, store.NewRender{Project: pa.ID, Version: 0, Profile: store.ProfilePreview, RequestedByKind: "user", RequestedByID: "x"}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("version 0: %v", err)
		}
	})

	t.Run("tenancy", func(t *testing.T) {
		r := newRender(wa, pa, 2, store.ProfilePreview)
		if _, err := st.GetRender(ctx, wb.ID, r.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetRender in B: %v", err)
		}
		if _, err := st.ClaimRender(ctx, wb.ID, r.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ClaimRender in B: %v", err)
		}
		if err := st.SetRenderProgress(ctx, wb.ID, r.ID, 5, "x"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SetRenderProgress in B: %v", err)
		}
		if _, err := st.FinishRender(ctx, wb.ID, r.ID, result(wb, pa, r)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("FinishRender in B: %v", err)
		}
		if _, err := st.FailRender(ctx, wb.ID, r.ID, "render_failed", -1, -1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("FailRender in B: %v", err)
		}
		if _, err := st.GetActiveRender(ctx, wb.ID, pa.ID, 2, store.ProfilePreview); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetActiveRender in B: %v", err)
		}
		if rows, err := st.ListRenders(ctx, wb.ID, pa.ID, store.RenderFilter{}, 10); err != nil || len(rows) != 0 {
			t.Errorf("ListRenders of A's project in B = %d, %v", len(rows), err)
		}
		if rows, err := st.ListRenderInputs(ctx, wb.ID, pa.ID); err != nil || len(rows) != 0 {
			t.Errorf("ListRenderInputs of A's project in B = %d, %v", len(rows), err)
		}
		if n, err := st.CountRendersInFlight(ctx, wb.ID); err != nil || n != 0 {
			t.Errorf("B has %d renders in flight (%v)", n, err)
		}
		// Untouched by all of that.
		if got, err := st.GetRender(ctx, wa.ID, r.ID); err != nil || got.Status != store.RenderQueued {
			t.Errorf("A's render: %+v, %v", got, err)
		}
	})

	t.Run("render inputs are the project's ready footage", func(t *testing.T) {
		rows, err := st.ListRenderInputs(ctx, wa.ID, pa.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 { // the asset of this suite is still uploading
			t.Errorf("an asset that is not ready is a render input: %+v", rows)
		}
	})

	t.Run("a deleted project", func(t *testing.T) {
		w, u := newWorkspace("deleted")
		p := newProject(w, u)
		r := newRender(w, p, 1, store.ProfilePreview)
		running := newRender(w, p, 2, store.ProfilePreview)
		if _, err := st.ClaimRender(ctx, w.ID, running.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.SoftDeleteProject(ctx, w.ID, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetRender(ctx, w.ID, r.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetRender of a deleted project's render: %v", err)
		}
		if _, err := st.ClaimRender(ctx, w.ID, r.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ClaimRender: %v", err)
		}
		if err := st.SetRenderProgress(ctx, w.ID, running.ID, 50, "rendering"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SetRenderProgress: %v", err)
		}
		if _, err := st.FinishRender(ctx, w.ID, running.ID, result(w, p, running)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("FinishRender: %v", err)
		}
		// It can still be failed (the job does that when it finds the project gone), and then its objects are to be purged.
		if _, err := st.FailRender(ctx, w.ID, running.ID, "project_deleted", -1, -1); err != nil {
			t.Errorf("FailRender: %v", err)
		}
		if _, err := st.FailRender(ctx, w.ID, r.ID, "project_deleted", -1, -1); err != nil {
			t.Errorf("FailRender: %v", err)
		}
		rows, err := st.ListRendersToPurge(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var found int
		for _, x := range rows {
			if x.WorkspaceID == w.ID {
				found++
			}
		}
		if found != 2 {
			t.Errorf("%d of this workspace's renders are to be purged, want 2", found)
		}
		if err := st.MarkRenderObjectsPurged(ctx, w.ID, r.ID); err != nil {
			t.Fatal(err)
		}
		rows, _ = st.ListRendersToPurge(ctx, 1000)
		for _, x := range rows {
			if x.ID == r.ID {
				t.Error("a purged render is listed again")
			}
		}
	})

	t.Run("the app role cannot delete a render", func(t *testing.T) {
		r := newRender(wa, pa, 2, store.ProfileFinal)
		var super bool
		if err := st.Pool().QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
			t.Fatal(err)
		}
		if super {
			return
		}
		_, err := st.Pool().Exec(ctx, `DELETE FROM renders WHERE id = $1`, r.ID.UUID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != permissionDenied {
			t.Errorf("DELETE of a render as the app role: err = %v, want permission denied", err)
		}
	})
}
