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

// projectsSuite is part of repositorySuite, which TestRepository runs as the superuser and TestRoleSplit runs again as the
// app role (DML rights only; INSERT and SELECT on timeline_versions). It covers the tenancy rule of projects (workspace B
// can neither read nor change workspace A's rows), the composite foreign keys, and the immutability of versions.
func projectsSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	newWorkspace := func(name string) (store.Workspace, ids.UserID) {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_projects_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		u, err := st.UpsertUserByClerkID(ctx, "user_projects_"+name+"_"+suffix, name+"-projects@example.com")
		if err != nil {
			t.Fatal(err)
		}
		return w, u.ID
	}
	newAsset := func(w store.Workspace, user ids.UserID, name string) store.Asset {
		t.Helper()
		id, err := st.NewAssetID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a, _, err := st.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
			ID: id, Kind: store.AssetKindVideo, Filename: name + ".mp4", ContentType: "video/mp4", SizeBytes: 1 << 20,
			Title: name, R2Key: "ws/" + w.ID.String() + "/assets/" + id.String() + "/original", CreatedBy: user,
			R2UploadID: "r2-" + id.String(), PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	newProject := func(w store.Workspace, user ids.UserID, assets ...store.Asset) store.Project {
		t.Helper()
		var p store.Project
		err := st.InTx(ctx, func(tx *store.Store) error {
			var err error
			if p, err = tx.CreateProject(ctx, w.ID, store.NewProject{Title: "Back day", Format: store.Format9x16, Brief: "make it punchy", CreatedBy: user}); err != nil {
				return err
			}
			list := make([]ids.AssetID, len(assets))
			for i, a := range assets {
				list[i] = a.ID
			}
			return tx.AddProjectAssets(ctx, w.ID, p.ID, list)
		})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	version := func(n, parent int) store.NewTimelineVersion {
		return store.NewTimelineVersion{
			Version: n, Parent: parent, Spec: json.RawMessage(`{"schema":"x"}`), Ops: json.RawMessage(`[{"op":"set_look"}]`),
			Diff: json.RawMessage(`[]`), Summary: []string{"did a thing"}, AuthorKind: store.AuthorUser, AuthorID: "usr_x",
		}
	}

	wa, ua := newWorkspace("a")
	wb, ub := newWorkspace("b")
	a1, a2 := newAsset(wa, ua, "one"), newAsset(wa, ua, "two")
	b1 := newAsset(wb, ub, "bees")

	t.Run("create, read and order", func(t *testing.T) {
		p := newProject(wa, ua, a2, a1)
		if !strings.HasPrefix(p.ID.String(), "prj_") || p.ID.Version() != 7 || p.WorkspaceID != wa.ID || p.Status != store.ProjectDraft ||
			p.CurrentTimelineVersion != nil || p.DeletedAt != nil || p.Brief != "make it punchy" || p.CreatedBy != ua {
			t.Fatalf("project: %+v", p)
		}
		got, err := st.GetProject(ctx, wa.ID, p.ID)
		if err != nil || got.ID != p.ID {
			t.Fatalf("GetProject = %+v, %v", got, err)
		}
		rows, err := st.ListProjectAssets(ctx, wa.ID, p.ID)
		if err != nil || len(rows) != 2 || rows[0].AssetID != a2.ID || rows[0].Position != 0 || rows[1].AssetID != a1.ID || rows[1].Position != 1 {
			t.Fatalf("ListProjectAssets = %+v, %v", rows, err)
		}
		list, err := st.ListProjects(ctx, wa.ID, nil, 10)
		if err != nil || len(list) == 0 || list[0].ID != p.ID || list[0].AssetCount != 2 {
			t.Fatalf("ListProjects = %+v, %v", list, err)
		}
	})

	t.Run("the database rejects what the service should have", func(t *testing.T) {
		base := store.NewProject{Title: "ok", Format: store.Format1x1, CreatedBy: ua}
		for name, mutate := range map[string]func(*store.NewProject){
			"format":      func(n *store.NewProject) { n.Format = "2:1" },
			"empty title": func(n *store.NewProject) { n.Title = "" },
			"long title":  func(n *store.NewProject) { n.Title = strings.Repeat("t", 256) },
			"long brief":  func(n *store.NewProject) { n.Brief = strings.Repeat("b", 4001) },
		} {
			in := base
			mutate(&in)
			if _, err := st.CreateProject(ctx, wa.ID, in); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		if _, err := st.CreateProject(ctx, wa.ID, store.NewProject{Title: "4000", Format: store.Format1x1, Brief: strings.Repeat("é", 4000), CreatedBy: ua}); err != nil {
			t.Errorf("a brief of exactly 4000 characters: %v", err)
		}
		if err := st.AddProjectAssets(ctx, wa.ID, ids.ProjectID{}, nil); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("no assets: err = %v, want ErrInvalid", err)
		}
	})

	t.Run("tenancy", func(t *testing.T) {
		p := newProject(wa, ua, a1)
		if _, err := st.GetProject(ctx, wb.ID, p.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetProject in B: err = %v, want ErrNotFound", err)
		}
		if _, err := st.GetProjectForUpdate(ctx, wb.ID, p.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetProjectForUpdate in B: err = %v, want ErrNotFound", err)
		}
		if err := st.SoftDeleteProject(ctx, wb.ID, p.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SoftDeleteProject in B: err = %v, want ErrNotFound", err)
		}
		if rows, err := st.ListProjectAssets(ctx, wb.ID, p.ID); err != nil || len(rows) != 0 {
			t.Errorf("ListProjectAssets in B = %+v, %v", rows, err)
		}
		if rows, err := st.ListProjectCatalogAssets(ctx, wb.ID, p.ID); err != nil || len(rows) != 0 {
			t.Errorf("ListProjectCatalogAssets in B = %+v, %v", rows, err)
		}
		if _, err := st.AddTimelineVersion(ctx, wb.ID, p.ID, version(1, 0)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("AddTimelineVersion in B: err = %v, want ErrNotFound", err)
		}
		if _, err := st.GetTimelineVersion(ctx, wb.ID, p.ID, 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetTimelineVersion in B: err = %v, want ErrNotFound", err)
		}
		if rows, err := st.ListTimelineVersions(ctx, wb.ID, p.ID, 0, 10); err != nil || len(rows) != 0 {
			t.Errorf("ListTimelineVersions in B = %+v, %v", rows, err)
		}
		// Workspace B's list does not hold A's projects, and its assets are not A's to lock.
		if rows, err := st.ListProjects(ctx, wb.ID, nil, 10); err != nil {
			t.Errorf("ListProjects in B: %v", err)
		} else {
			for _, r := range rows {
				if r.WorkspaceID != wb.ID {
					t.Errorf("ListProjects in B returned a project of %v", r.WorkspaceID)
				}
			}
		}
		listed, err := st.ListAssetsForProject(ctx, wa.ID, []ids.AssetID{a1.ID, b1.ID})
		if err != nil || len(listed) != 1 || listed[0].ID != a1.ID {
			t.Errorf("ListAssetsForProject(A, [a1, b1]) = %+v, %v; want only a1", listed, err)
		}
		// The assets of this suite are still uploading, so none is ready to lock.
		err = st.InTx(ctx, func(tx *store.Store) error {
			locked, err := tx.LockReadyAssetsForProject(ctx, wa.ID, []ids.AssetID{a1.ID, b1.ID})
			if err == nil && len(locked) != 0 {
				t.Errorf("LockReadyAssetsForProject locked %+v, want nothing (not ready, or another workspace's)", locked)
			}
			return err
		})
		if err != nil {
			t.Error(err)
		}
		// The composite foreign key refuses another workspace's asset even when the service did not check.
		err = st.InTx(ctx, func(tx *store.Store) error {
			q, err := tx.CreateProject(ctx, wa.ID, store.NewProject{Title: "x", Format: store.Format1x1, CreatedBy: ua})
			if err != nil {
				return err
			}
			return tx.AddProjectAssets(ctx, wa.ID, q.ID, []ids.AssetID{a1.ID, b1.ID})
		})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a project of A with an asset of B: err = %v, want a foreign key failure (ErrNotFound)", err)
		}
		// Nor can a project row of A hold a version's workspace of B: a version points at (workspace, project).
		if _, err := st.Pool().Exec(ctx, `INSERT INTO timeline_versions (workspace_id, project_id, version, spec, author_kind, author_id)
			VALUES ($1, $2, 1, '{}', 'user', 'x')`, wb.ID.UUID, p.ID.UUID); err == nil {
			t.Error("a version of workspace B was inserted for A's project")
		}
	})

	t.Run("asset_count leaves out deleted assets; the app role cannot delete projects", func(t *testing.T) {
		c1, c2 := newAsset(wa, ua, "count-one"), newAsset(wa, ua, "count-two")
		p := newProject(wa, ua, c1, c2)
		count := func() int {
			t.Helper()
			rows, err := st.ListProjects(ctx, wa.ID, nil, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if r.ID == p.ID {
					return int(r.AssetCount)
				}
			}
			t.Fatal("project not listed")
			return 0
		}
		if n := count(); n != 2 {
			t.Fatalf("asset_count = %d, want 2", n)
		}
		if _, err := st.SoftDeleteAsset(ctx, wa.ID, c2.ID); err != nil {
			t.Fatal(err)
		}
		if n := count(); n != 1 {
			t.Fatalf("asset_count after deleting an asset = %d, want 1", n)
		}
		var super bool
		if err := st.Pool().QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
			t.Fatal(err)
		}
		if !super {
			_, err := st.Pool().Exec(ctx, `DELETE FROM projects WHERE id = $1`, p.ID.UUID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != permissionDenied {
				t.Errorf("DELETE of a project as the app role: err = %v, want permission denied", err)
			}
		}
	})

	t.Run("soft delete", func(t *testing.T) {
		p := newProject(wa, ua, a1)
		if err := st.SoftDeleteProject(ctx, wa.ID, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetProject(ctx, wa.ID, p.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetProject after delete: err = %v", err)
		}
		if err := st.SoftDeleteProject(ctx, wa.ID, p.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("second delete: err = %v", err)
		}
		if _, err := st.AddTimelineVersion(ctx, wa.ID, p.ID, version(1, 0)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("AddTimelineVersion on a deleted project: err = %v", err)
		}
	})

	t.Run("versions form an immutable chain", func(t *testing.T) {
		p := newProject(wa, ua, a1)
		v1, err := st.AddTimelineVersion(ctx, wa.ID, p.ID, version(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(v1.ID.String(), "tl_") || v1.Version != 1 || v1.ParentVersion != nil || v1.WorkspaceID != wa.ID || v1.AuthorKind != store.AuthorUser ||
			len(v1.Summary) != 1 || string(v1.Spec) != `{"schema": "x"}` {
			t.Fatalf("version 1: %+v (%s)", v1, v1.Spec)
		}
		cur, err := st.GetProject(ctx, wa.ID, p.ID)
		if err != nil || cur.CurrentTimelineVersion == nil || *cur.CurrentTimelineVersion != 1 {
			t.Fatalf("current version after the first: %+v, %v", cur.CurrentTimelineVersion, err)
		}
		v2, err := st.AddTimelineVersion(ctx, wa.ID, p.ID, version(2, 1))
		if err != nil || v2.ParentVersion == nil || *v2.ParentVersion != 1 {
			t.Fatalf("version 2: %+v, %v", v2, err)
		}

		// A version that does not follow the current one, or repeats a number, is a conflict and stores nothing.
		for name, in := range map[string]store.NewTimelineVersion{
			"a repeated number": version(2, 1), "a gap": version(4, 3), "a first version again": version(1, 0),
		} {
			if _, err := st.AddTimelineVersion(ctx, wa.ID, p.ID, in); !errors.Is(err, store.ErrVersionConflict) {
				t.Errorf("%s: err = %v, want ErrVersionConflict", name, err)
			}
		}
		if _, err := st.AddTimelineVersion(ctx, wa.ID, p.ID, version(3, 1)); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("a parent that is not the version before: err = %v, want ErrInvalid", err)
		}
		bad := version(3, 2)
		bad.AuthorKind = "robot"
		if _, err := st.AddTimelineVersion(ctx, wa.ID, p.ID, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("a bad author kind: err = %v, want ErrInvalid", err)
		}
		bad = version(3, 2)
		bad.Spec = json.RawMessage(`[1]`)
		if _, err := st.AddTimelineVersion(ctx, wa.ID, p.ID, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("a spec that is not an object: err = %v, want ErrInvalid", err)
		}
		big := version(3, 2)
		big.Spec = json.RawMessage(`{"pad":"` + strings.Repeat("x", 1<<20) + `"}`)
		if _, err := st.AddTimelineVersion(ctx, wa.ID, p.ID, big); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("a spec over 1 MiB: err = %v, want ErrInvalid", err)
		}
		rows, err := st.ListTimelineVersions(ctx, wa.ID, p.ID, 0, 10)
		if err != nil || len(rows) != 2 || rows[0].Version != 2 || rows[1].Version != 1 {
			t.Fatalf("ListTimelineVersions = %+v, %v", rows, err)
		}
		if rows, err = st.ListTimelineVersions(ctx, wa.ID, p.ID, 2, 10); err != nil || len(rows) != 1 || rows[0].Version != 1 {
			t.Fatalf("ListTimelineVersions before 2 = %+v, %v", rows, err)
		}

		// Immutable: the app role has no UPDATE or DELETE, and a trigger refuses UPDATE for every role.
		for _, stmt := range []string{
			`UPDATE timeline_versions SET summary = '{changed}' WHERE id = $1`,
			`UPDATE timeline_versions SET spec = '{}' WHERE id = $1`,
		} {
			_, err := st.Pool().Exec(ctx, stmt, v1.ID.UUID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || (pgErr.Code != permissionDenied && pgErr.Code != "23001") {
				t.Errorf("%s: err = %v, want permission denied or the immutability trigger", stmt, err)
			}
		}
		again, err := st.GetTimelineVersion(ctx, wa.ID, p.ID, 1)
		if err != nil || len(again.Summary) != 1 || again.Summary[0] != "did a thing" {
			t.Fatalf("version 1 after the attempts: %+v, %v", again, err)
		}
		// The app role may not delete one either. (A superuser can; this suite also runs as one, so that is not checked.)
		var super bool
		if err := st.Pool().QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
			t.Fatal(err)
		}
		if !super {
			_, err := st.Pool().Exec(ctx, `DELETE FROM timeline_versions WHERE id = $1`, v1.ID.UUID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != permissionDenied {
				t.Errorf("DELETE as the app role: err = %v, want permission denied", err)
			}
		}
		if _, err := st.GetTimelineVersion(ctx, wa.ID, p.ID, 3); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetTimelineVersion 3: err = %v, want ErrNotFound", err)
		}
	})

	// P4-02a: what the editor agent reads and how it finds the version a repeated tool call made.
	t.Run("the editor's queries are scoped", func(t *testing.T) {
		p := newProject(wa, ua, a1, a2)
		// Footage lists only ready assets; the suite's assets are still uploading, so there is none yet, in either workspace.
		for _, w := range []store.Workspace{wa, wb} {
			rows, err := st.ListProjectFootage(ctx, w.ID, p.ID)
			if err != nil || len(rows) != 0 {
				t.Fatalf("ListProjectFootage in %s = %+v, %v", w.Name, rows, err)
			}
		}
		agentV := version(1, 0)
		agentV.AuthorKind, agentV.AuthorID = store.AuthorAgent, "00000000-0000-7000-8000-000000000001:toolu_01"
		if _, err := st.AddTimelineVersion(ctx, wa.ID, p.ID, agentV); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetTimelineVersionByAuthor(ctx, wa.ID, p.ID, store.AuthorAgent, agentV.AuthorID)
		if err != nil || got.Version != 1 || got.AuthorID != agentV.AuthorID {
			t.Fatalf("GetTimelineVersionByAuthor = %+v, %v", got, err)
		}
		for name, c := range map[string]struct {
			ws           ids.WorkspaceID
			kind, author string
		}{
			"another workspace": {wb.ID, store.AuthorAgent, agentV.AuthorID},
			"another author":    {wa.ID, store.AuthorAgent, "00000000-0000-7000-8000-000000000001:toolu_02"},
			"another kind":      {wa.ID, store.AuthorUser, agentV.AuthorID},
		} {
			if _, err := st.GetTimelineVersionByAuthor(ctx, c.ws, p.ID, c.kind, c.author); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrNotFound", name, err)
			}
		}

		// The one unfinished editor run of a project.
		if _, err := st.ActiveAgentRunForProject(ctx, wa.ID, p.ID, "editor"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("before a run: err = %v", err)
		}
		run, err := st.CreateAgentRun(ctx, wa.ID, store.NewAgentRun{
			Agent: "editor", Stage: "editor", ProjectID: p.ID.UUID, Input: json.RawMessage(`{}`), RoutingProfile: "dev-cheap",
			Model: "claude-haiku-5-5", Effort: "medium", MaxTokens: 1000,
			BudgetMicros: 100_000, BudgetTokens: 10_000, BudgetToolCalls: 5, BudgetWallClockMs: 60_000, BilledTo: store.BilledToTest,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := st.ActiveAgentRunForProject(ctx, wa.ID, p.ID, "editor"); err != nil || got.ID != run.ID {
			t.Fatalf("active run = %+v, %v", got, err)
		}
		if _, err := st.ActiveAgentRunForProject(ctx, wb.ID, p.ID, "editor"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("workspace B sees A's run: err = %v", err)
		}
		if _, err := st.ActiveAgentRunForProject(ctx, wa.ID, p.ID, "echo"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another agent: err = %v", err)
		}
		if _, err := st.RequestAgentRunCancel(ctx, wa.ID, run.ID, json.RawMessage(`{}`), json.RawMessage(`{"state":"cancelled"}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ActiveAgentRunForProject(ctx, wa.ID, p.ID, "editor"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a cancelled run is still active: err = %v", err)
		}
	})
}
