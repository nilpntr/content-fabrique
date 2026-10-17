package annotations

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// tenant is a workspace with a member of each role and some content.
type tenant struct {
	ws                     store.Workspace
	owner, editor, viewer  Actor
	editor2                Actor
	project                store.Project
	asset                  store.Asset
	version1, version2     store.TimelineVersion
	render                 store.Render // ready, of version 2
	renderV1               store.Render // queued, of version 1
	projectID              ids.ProjectID
	deletedProject         store.Project
	deletedProjectsVersion store.TimelineVersion
}

type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	st   *store.Store
	svc  *Service
	a, b *tenant
}

func newEnv(t *testing.T) *env {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st := store.New(pool)
	svc, err := NewService(st, quiet)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool, st: st, svc: svc}
	e.a, e.b = e.tenant("a"), e.tenant("b")
	return e
}

func (e *env) tenant(name string) *tenant {
	e.t.Helper()
	ctx := context.Background()
	w, err := e.st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_ann_" + name, Name: name})
	if err != nil {
		e.t.Fatal(err)
	}
	user := func(role string) ids.UserID {
		u, err := e.st.UpsertUserByClerkID(ctx, "user_ann_"+name+"_"+role, role+"-"+name+"@example.com")
		if err != nil {
			e.t.Fatal(err)
		}
		return u.ID
	}
	tn := &tenant{ws: w}
	for role, dst := range map[store.Role]*Actor{store.RoleOwner: &tn.owner, store.RoleEditor: &tn.editor, store.RoleViewer: &tn.viewer} {
		id := user(string(role))
		if _, err := e.st.AddMember(ctx, w.ID, id, role); err != nil {
			e.t.Fatal(err)
		}
		*dst = Actor{Workspace: w.ID, User: id, Role: role}
	}
	id2 := user("editor2")
	if _, err := e.st.AddMember(ctx, w.ID, id2, store.RoleEditor); err != nil {
		e.t.Fatal(err)
	}
	tn.editor2 = Actor{Workspace: w.ID, User: id2, Role: store.RoleEditor}

	assetID, err := e.st.NewAssetID(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	tn.asset, _, err = e.st.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
		ID: assetID, Kind: store.AssetKindVideo, Filename: "a.mp4", ContentType: "video/mp4", SizeBytes: 1 << 20, Title: "a",
		R2Key: "ws/" + w.ID.String() + "/assets/" + assetID.String() + "/original", CreatedBy: tn.owner.User,
		R2UploadID: "r2-" + assetID.String(), PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
	}, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	// A clip of 60 s.
	if _, err := e.pool.Exec(ctx, `UPDATE assets SET duration_ms = 60000 WHERE id = $1`, tn.asset.ID.UUID); err != nil {
		e.t.Fatal(err)
	}

	mkProject := func(title string) (store.Project, store.TimelineVersion, store.TimelineVersion) {
		var p store.Project
		err := e.st.InTx(ctx, func(tx *store.Store) error {
			var err error
			if p, err = tx.CreateProject(ctx, w.ID, store.NewProject{Title: title, Format: store.Format9x16, CreatedBy: tn.owner.User}); err != nil {
				return err
			}
			return tx.AddProjectAssets(ctx, w.ID, p.ID, []ids.AssetID{tn.asset.ID})
		})
		if err != nil {
			e.t.Fatal(err)
		}
		var vs [2]store.TimelineVersion
		for v := 1; v <= 2; v++ {
			if vs[v-1], err = e.st.AddTimelineVersion(ctx, w.ID, p.ID, store.NewTimelineVersion{
				Version: v, Parent: v - 1, Spec: json.RawMessage(`{"schema":"x"}`), AuthorKind: store.AuthorUser, AuthorID: "usr_x",
			}); err != nil {
				e.t.Fatal(err)
			}
		}
		return p, vs[0], vs[1]
	}
	tn.project, tn.version1, tn.version2 = mkProject("Back day")
	tn.projectID = tn.project.ID
	tn.deletedProject, tn.deletedProjectsVersion, _ = mkProject("Old")

	mkRender := func(p store.Project, version int, ready bool) store.Render {
		r, err := e.st.CreateRender(ctx, w.ID, store.NewRender{Project: p.ID, Version: version, Profile: store.ProfilePreview, RequestedByKind: store.RenderRequestedByUser, RequestedByID: tn.owner.User.String()})
		if err != nil {
			e.t.Fatal(err)
		}
		if !ready {
			return r
		}
		if _, err := e.st.ClaimRender(ctx, w.ID, r.ID); err != nil {
			e.t.Fatal(err)
		}
		prefix := "ws/" + w.ID.String() + "/projects/" + p.ID.String() + "/renders/" + r.ID.String() + "/"
		r, err = e.st.FinishRender(ctx, w.ID, r.ID, store.RenderResult{
			MP4Key: prefix + "render.mp4", ContactSheetKey: prefix + "contact_sheet.jpg", TimeMapKey: prefix + "time_map.json",
			DurationMs: 30000, Width: 540, Height: 960, SizeBytes: 1 << 20, ContactSheetBytes: 1 << 10, TimeMapBytes: 1 << 10,
			ContactSheet: json.RawMessage(`{"columns":4,"tile_width":216,"tile_height":384,"tiles":[]}`), SoundCues: json.RawMessage(`[]`),
		})
		if err != nil {
			e.t.Fatal(err)
		}
		return r
	}
	tn.render = mkRender(tn.project, 2, true)
	tn.renderV1 = mkRender(tn.project, 1, false)

	// A project that is deleted afterwards: its comments are gone with it.
	if err := e.st.SoftDeleteProject(ctx, w.ID, tn.deletedProject.ID); err != nil {
		e.t.Fatal(err)
	}
	return tn
}

// The ids of the targets in the TypeID form of the API.
func (tn *tenant) assetID() string   { return tn.asset.ID.String() }
func (tn *tenant) renderID() string  { return tn.render.ID.String() }
func (tn *tenant) versionID() string { return tn.version2.ID.String() }

func i64(v int64) *int64   { return &v }
func str(v string) *string { return &v }

// onRender is a comment on the ready render of a, by who.
func (e *env) comment(who Actor, tn *tenant, in CreateInput) Annotation {
	e.t.Helper()
	if in.TargetType == "" && in.Parent == nil {
		in.TargetType, in.TargetID = store.AnnotationOnRender, tn.renderID()
	}
	if in.StartMs == nil && in.Parent == nil {
		in.StartMs = i64(4000)
	}
	if in.Body == "" {
		in.Body = "make it tighter"
	}
	a, err := e.svc.Create(context.Background(), who, in)
	if err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	return a
}

var _ = uuid.Nil
