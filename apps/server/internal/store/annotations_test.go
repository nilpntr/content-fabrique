package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// annotationsSuite is part of repositorySuite, which TestRepository runs as the superuser and TestRoleSplit runs again as the app
// role (DML rights only; no DELETE on annotations). It covers the targets, what the constraints of the table refuse, the soft
// delete, and the tenancy rule: workspace B can neither read nor change nor point at workspace A's comments, clips, projects or
// versions.
func annotationsSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	type tenant struct {
		ws      store.Workspace
		user    ids.UserID
		asset   store.Asset
		project store.Project
		v1      store.TimelineVersion
		render  store.Render
	}
	newTenant := func(name string) tenant {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_ann_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		u, err := st.UpsertUserByClerkID(ctx, "user_ann_"+name+"_"+suffix, name+"-ann@example.com")
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
		err = st.InTx(ctx, func(tx *store.Store) error {
			var err error
			if p, err = tx.CreateProject(ctx, w.ID, store.NewProject{Title: "Back day", Format: store.Format9x16, CreatedBy: u.ID}); err != nil {
				return err
			}
			return tx.AddProjectAssets(ctx, w.ID, p.ID, []ids.AssetID{a.ID})
		})
		if err != nil {
			t.Fatal(err)
		}
		v1, err := st.AddTimelineVersion(ctx, w.ID, p.ID, store.NewTimelineVersion{Version: 1, Spec: json.RawMessage(`{"schema":"x"}`), AuthorKind: store.AuthorUser, AuthorID: "usr_x"})
		if err != nil {
			t.Fatal(err)
		}
		r, err := st.CreateRender(ctx, w.ID, store.NewRender{Project: p.ID, Version: 1, Profile: store.ProfilePreview, RequestedByKind: store.RenderRequestedByUser, RequestedByID: u.ID.String()})
		if err != nil {
			t.Fatal(err)
		}
		return tenant{ws: w, user: u.ID, asset: a, project: p, v1: v1, render: r}
	}
	a, b := newTenant("a"), newTenant("b")

	onRender := func(tn tenant) store.AnnotationTarget {
		t.Helper()
		got, err := st.FindAnnotationTarget(ctx, tn.ws.ID, store.AnnotationOnRender, tn.render.ID.UUID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	newComment := func(tn tenant, mut func(*store.NewAnnotation)) (store.Annotation, error) {
		in := store.NewAnnotation{Target: onRender(tn), StartMs: 1000, EndMs: 2000, Kind: store.AnnotationComment, Body: "tighter", AuthorKind: store.AuthorUser, AuthorID: tn.user.String()}
		if mut != nil {
			mut(&in)
		}
		return st.CreateAnnotation(ctx, tn.ws.ID, in)
	}

	t.Run("targets", func(t *testing.T) {
		got, err := st.FindAnnotationTarget(ctx, a.ws.ID, store.AnnotationOnRender, a.render.ID.UUID)
		if err != nil || got.Project != a.project.ID || got.Version != 1 || got.ID != a.render.ID.UUID {
			t.Errorf("render: %+v, %v", got, err)
		}
		got, err = st.FindAnnotationTarget(ctx, a.ws.ID, store.AnnotationOnVersion, a.v1.ID.UUID)
		if err != nil || got.Project != a.project.ID || got.Version != 1 {
			t.Errorf("version: %+v, %v", got, err)
		}
		got, err = st.FindAnnotationTarget(ctx, a.ws.ID, store.AnnotationOnAsset, a.asset.ID.UUID)
		if err != nil || !got.Project.IsZero() || got.Version != 0 {
			t.Errorf("asset: %+v, %v", got, err)
		}
		// B asking for A's targets, with the type right, and A's ids under the other types.
		for name, tc := range map[string]struct {
			typ string
			id  uuid.UUID
		}{
			"render in B": {store.AnnotationOnRender, a.render.ID.UUID}, "version in B": {store.AnnotationOnVersion, a.v1.ID.UUID},
			"asset in B": {store.AnnotationOnAsset, a.asset.ID.UUID}, "a render id as an asset": {store.AnnotationOnAsset, a.render.ID.UUID},
		} {
			if _, err := st.FindAnnotationTarget(ctx, b.ws.ID, tc.typ, tc.id); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := st.FindAnnotationTarget(ctx, a.ws.ID, "project", a.project.ID.UUID); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("an unknown type: %v", err)
		}
	})

	t.Run("create, read, list, edit, delete", func(t *testing.T) {
		c, err := newComment(a, func(in *store.NewAnnotation) { in.Pin = json.RawMessage(`{"x":0.5,"y":0.5}`) })
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(c.ID.String(), "ann_") || c.ID.UUID.Version() != 7 || c.WorkspaceID != a.ws.ID || c.ProjectID != a.project.ID || c.TimelineVersion == nil || *c.TimelineVersion != 1 ||
			c.Status != store.AnnotationOpen || c.Kind != store.AnnotationComment || !c.ParentID.IsZero() || c.Pin == nil {
			t.Fatalf("created: %+v", c)
		}
		reply, err := newComment(a, func(in *store.NewAnnotation) { in.Parent = c.ID; in.Body = "ok" })
		if err != nil || reply.ParentID != c.ID {
			t.Fatalf("reply: %+v, %v", reply, err)
		}
		clip, err := st.CreateAnnotation(ctx, a.ws.ID, store.NewAnnotation{
			Target: store.AnnotationTarget{Type: store.AnnotationOnAsset, ID: a.asset.ID.UUID}, StartMs: 0, EndMs: 5000, Kind: store.AnnotationExclude,
			AuthorKind: store.AuthorUser, AuthorID: a.user.String(),
		})
		if err != nil || !clip.ProjectID.IsZero() || clip.TimelineVersion != nil {
			t.Fatalf("clip: %+v, %v", clip, err)
		}

		rows, err := st.ListAnnotations(ctx, a.ws.ID, store.AnnotationFilter{Project: a.project.ID}, 10)
		if err != nil || len(rows) != 2 || rows[0].ID != c.ID || rows[1].ID != reply.ID {
			t.Errorf("by project: %+v, %v", rows, err)
		}
		rows, err = st.ListAnnotations(ctx, a.ws.ID, store.AnnotationFilter{TargetType: store.AnnotationOnAsset, TargetID: a.asset.ID.UUID}, 10)
		if err != nil || len(rows) != 1 || rows[0].ID != clip.ID {
			t.Errorf("by clip: %+v, %v", rows, err)
		}
		rows, _ = st.ListAnnotations(ctx, a.ws.ID, store.AnnotationFilter{Project: a.project.ID, After: c.ID}, 10)
		if len(rows) != 1 || rows[0].ID != reply.ID {
			t.Errorf("after the first: %+v", rows)
		}
		if _, err := st.ListAnnotations(ctx, a.ws.ID, store.AnnotationFilter{Project: a.project.ID}, 0); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("limit 0: %v", err)
		}

		upd, err := st.UpdateAnnotation(ctx, a.ws.ID, c.ID, store.AnnotationEdit{StartMs: 3000, EndMs: 4000, Kind: store.AnnotationInstruction, Body: "edited", Status: store.AnnotationResolved})
		if err != nil || upd.Pin != nil || upd.Body != "edited" || upd.Status != store.AnnotationResolved || !upd.UpdatedAt.After(c.UpdatedAt) {
			t.Errorf("update: %+v, %v", upd, err)
		}
		if got, _ := st.GetAnnotation(ctx, a.ws.ID, reply.ID); got.StartMs != 3000 || got.EndMs != 4000 {
			t.Errorf("the reply did not follow: %+v", got)
		}

		if err := st.SoftDeleteAnnotation(ctx, a.ws.ID, c.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetAnnotation(ctx, a.ws.ID, c.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("get after delete: %v", err)
		}
		if _, err := st.GetAnnotation(ctx, a.ws.ID, reply.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("the reply after its comment was deleted: %v", err)
		}
		if err := st.SoftDeleteAnnotation(ctx, a.ws.ID, c.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("second delete: %v", err)
		}
		if _, err := st.UpdateAnnotation(ctx, a.ws.ID, c.ID, store.AnnotationEdit{Kind: store.AnnotationComment, Body: "x", Status: store.AnnotationOpen}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("update after delete: %v", err)
		}
	})

	t.Run("tenancy", func(t *testing.T) {
		c, err := newComment(a, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetAnnotation(ctx, b.ws.ID, c.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("get in B: %v", err)
		}
		if _, err := st.GetAnnotationForUpdate(ctx, b.ws.ID, c.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("get for update in B: %v", err)
		}
		if _, err := st.UpdateAnnotation(ctx, b.ws.ID, c.ID, store.AnnotationEdit{Kind: store.AnnotationComment, Body: "hijack", Status: store.AnnotationOpen}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("update in B: %v", err)
		}
		if err := st.SoftDeleteAnnotation(ctx, b.ws.ID, c.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("delete in B: %v", err)
		}
		if rows, err := st.ListAnnotations(ctx, b.ws.ID, store.AnnotationFilter{Project: a.project.ID}, 10); err != nil || len(rows) != 0 {
			t.Errorf("A's project listed in B: %d, %v", len(rows), err)
		}
		if rows, err := st.ListAnnotations(ctx, b.ws.ID, store.AnnotationFilter{TargetType: store.AnnotationOnRender, TargetID: a.render.ID.UUID}, 10); err != nil || len(rows) != 0 {
			t.Errorf("A's render listed in B: %d, %v", len(rows), err)
		}
		if rows, err := st.ListOpenAnnotationsForProject(ctx, b.ws.ID, a.project.ID, 10); err != nil || len(rows) != 0 {
			t.Errorf("A's open comments in B: %d, %v", len(rows), err)
		}
		// A write in B that names A's project, version, comment or run is refused by the composite foreign keys, whatever the service
		// checked: the row cannot even be written.
		_, err = st.CreateAnnotation(ctx, b.ws.ID, store.NewAnnotation{
			Target: store.AnnotationTarget{Type: store.AnnotationOnRender, ID: a.render.ID.UUID, Project: a.project.ID, Version: 1}, StartMs: 1, EndMs: 1,
			Kind: store.AnnotationComment, Body: "x", AuthorKind: store.AuthorUser, AuthorID: "usr_x",
		})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a comment in B on A's project: %v", err)
		}
		_, err = st.CreateAnnotation(ctx, b.ws.ID, store.NewAnnotation{
			Target: onRender(b), StartMs: 1, EndMs: 1, Kind: store.AnnotationComment, Body: "x", Parent: c.ID, AuthorKind: store.AuthorUser, AuthorID: "usr_x",
		})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a reply in B to A's comment: %v", err)
		}
		// B's version number on A's project: no such version there.
		_, err = st.CreateAnnotation(ctx, a.ws.ID, store.NewAnnotation{
			Target: store.AnnotationTarget{Type: store.AnnotationOnVersion, ID: uuid.Must(uuid.NewV7()), Project: a.project.ID, Version: 7}, StartMs: 1, EndMs: 1,
			Kind: store.AnnotationComment, Body: "x", AuthorKind: store.AuthorUser, AuthorID: "usr_x",
		})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a version that does not exist: %v", err)
		}
		if got, err := st.GetAnnotation(ctx, a.ws.ID, c.ID); err != nil || got.Body != "tighter" || got.Status != store.AnnotationOpen {
			t.Errorf("A's comment after all that: %+v, %v", got, err)
		}
	})

	t.Run("the database rejects what the service should have", func(t *testing.T) {
		parent, err := newComment(a, nil)
		if err != nil {
			t.Fatal(err)
		}
		clipTarget := store.AnnotationTarget{Type: store.AnnotationOnAsset, ID: a.asset.ID.UUID}
		for name, mut := range map[string]func(*store.NewAnnotation){
			"an end before the start":        func(in *store.NewAnnotation) { in.StartMs, in.EndMs = 5, 4 },
			"a negative start":               func(in *store.NewAnnotation) { in.StartMs, in.EndMs = -1, 4 },
			"an unknown kind":                func(in *store.NewAnnotation) { in.Kind = "love" },
			"a body over 2000 characters":    func(in *store.NewAnnotation) { in.Body = strings.Repeat("a", 2001) },
			"an author kind that is not one": func(in *store.NewAnnotation) { in.AuthorKind = "bot" },
			"no author id":                   func(in *store.NewAnnotation) { in.AuthorID = "" },
			"a pin that is not an object":    func(in *store.NewAnnotation) { in.Pin = json.RawMessage(`[1,2]`) },
			"a clip with a project": func(in *store.NewAnnotation) {
				in.Target = store.AnnotationTarget{Type: store.AnnotationOnAsset, ID: a.asset.ID.UUID, Project: a.project.ID, Version: 1}
			},
			"a render without a version":           func(in *store.NewAnnotation) { in.Target.Version = 0 },
			"a render without a project":           func(in *store.NewAnnotation) { in.Target.Project = ids.ProjectID{} },
			"a reply that is an instruction":       func(in *store.NewAnnotation) { in.Parent, in.Kind = parent.ID, store.AnnotationInstruction },
			"a reply with a pin":                   func(in *store.NewAnnotation) { in.Parent, in.Pin = parent.ID, json.RawMessage(`{"x":0,"y":0}`) },
			"a target type that does not exist":    func(in *store.NewAnnotation) { in.Target.Type = "project" },
			"a NUL byte in the body":               func(in *store.NewAnnotation) { in.Body = "a\x00b" },
			"a clip with a version but no project": func(in *store.NewAnnotation) { in.Target = clipTarget; in.Target.Version = 2 },
		} {
			if _, err := newComment(a, mut); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		// "applied" names a run, and only an applied comment does.
		if _, err := st.UpdateAnnotation(ctx, a.ws.ID, parent.ID, store.AnnotationEdit{StartMs: 1, EndMs: 2, Kind: store.AnnotationComment, Body: "x", Status: store.AnnotationApplied}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("applied without a run: %v", err)
		}
		v := 3
		if _, err := st.UpdateAnnotation(ctx, a.ws.ID, parent.ID, store.AnnotationEdit{StartMs: 1, EndMs: 2, Kind: store.AnnotationComment, Body: "x", Status: store.AnnotationOpen, AddressedInVersion: &v}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("an open comment with an addressed version: %v", err)
		}
		if _, err := st.UpdateAnnotation(ctx, a.ws.ID, parent.ID, store.AnnotationEdit{StartMs: 1, EndMs: 2, Kind: store.AnnotationComment, Body: "x", Status: "done"}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("an unknown status: %v", err)
		}
		// Applied by a run of another workspace: refused by the composite foreign key.
		run, err := st.CreateAgentRun(ctx, b.ws.ID, store.NewAgentRun{
			Agent: "editor", Stage: "editor", RoutingProfile: "dev-cheap", Model: "m", Effort: "low", MaxTokens: 10,
			BudgetMicros: 1, BudgetTokens: 1, BudgetToolCalls: 1, BudgetWallClockMs: 1, BilledTo: "test",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpdateAnnotation(ctx, a.ws.ID, parent.ID, store.AnnotationEdit{StartMs: 1, EndMs: 2, Kind: store.AnnotationComment, Body: "x", Status: store.AnnotationApplied, AppliedInRun: run.ID}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("applied by another workspace's run: %v", err)
		}
	})

	t.Run("the app role cannot delete an annotation", func(t *testing.T) {
		c, err := newComment(a, nil)
		if err != nil {
			t.Fatal(err)
		}
		var super bool
		if err := st.Pool().QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
			t.Fatal(err)
		}
		if super {
			return
		}
		_, err = st.Pool().Exec(ctx, `DELETE FROM annotations WHERE id = $1`, c.ID.UUID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != permissionDenied {
			t.Errorf("DELETE of an annotation as the app role: err = %v, want permission denied", err)
		}
	})
}
