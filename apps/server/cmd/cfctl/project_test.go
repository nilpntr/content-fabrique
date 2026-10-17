package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

type projectRig struct {
	t       *testing.T
	st      *store.Store
	dsn     string
	ws      store.Workspace
	owner   store.User
	viewer  store.User
	envFile string
	n       int
}

func newProjectRig(t *testing.T) *projectRig {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "project-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	ws, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_project", Name: "Fit Studio"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := st.UpsertUserByClerkID(ctx, "user_owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := st.UpsertUserByClerkID(ctx, "user_viewer", "viewer@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMember(ctx, ws.ID, viewer.ID, store.RoleViewer); err != nil { // joined first, but is no owner
		t.Fatal(err)
	}
	if _, err := st.AddMember(ctx, ws.ID, owner.ID, store.RoleOwner); err != nil {
		t.Fatal(err)
	}
	r := &projectRig{t: t, st: st, dsn: dsn, ws: ws, owner: owner, viewer: viewer}
	r.envFile = pingEnv(t, "CF_ENV=local\nDATABASE_URL="+dsn+"\n")
	return r
}

// asset makes an asset of the workspace in the given state: "ready" (probed), or the state it is left in.
func (r *projectRig) asset(ws store.Workspace, kind, state, title string) store.Asset {
	r.t.Helper()
	ctx := context.Background()
	id, err := r.st.NewAssetID(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	r.n++
	ct := map[string]string{store.AssetKindVideo: "video/mp4", store.AssetKindAudio: "audio/mpeg", store.AssetKindImage: "image/png"}[kind]
	a, _, err := r.st.CreateAssetWithUpload(ctx, ws.ID, store.NewAsset{
		ID: id, Kind: kind, Filename: fmt.Sprintf("%s-%d", title, r.n), ContentType: ct, SizeBytes: 1 << 20,
		Title: title, R2Key: "ws/" + ws.ID.String() + "/assets/" + id.String() + "/original", CreatedBy: r.owner.ID,
		R2UploadID: "r2-" + id.String(), PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
	}, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	if state == "uploading" {
		return a
	}
	if _, err := r.st.CompleteUpload(ctx, ws.ID, id); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.st.ClaimAssetForIngest(ctx, ws.ID, id); err != nil {
		r.t.Fatal(err)
	}
	if a, err = r.st.FinishAssetIngest(ctx, ws.ID, id, store.IngestResult{DurationMs: 4000, Width: 1080, Height: 1920, FPS: 30, HasAudio: true}); err != nil {
		r.t.Fatal(err)
	}
	if state == "deleted" {
		if a, err = r.st.SoftDeleteAsset(ctx, ws.ID, id); err != nil {
			r.t.Fatal(err)
		}
	}
	return a
}

func (r *projectRig) run(args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	return r.runWith(map[string]string{}, args...)
}

func (r *projectRig) runWith(proc map[string]string, args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	deps := agentDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "project-test", MaxConns: 2})
	}}
	code = cmdProject(append(args, "--env-file", r.envFile), &out, &errb, fakeProcessEnv(proc), deps)
	return code, out.String(), errb.String()
}

func (r *projectRig) projectIDIn(out string) ids.ProjectID {
	r.t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "project  "); ok {
			id, err := ids.ParseProjectID(strings.TrimSpace(rest))
			if err != nil {
				r.t.Fatal(err)
			}
			return id
		}
	}
	r.t.Fatalf("no project id in the output:\n%s", out)
	return ids.ProjectID{}
}

func TestProjectCreateRefusedOutsideDevelopment(t *testing.T) {
	// Arguments are fine and the database is never reached: the guard comes first.
	for _, tc := range []struct {
		name, env, want string
	}{
		{"staging", "CF_ENV=staging\n", "refusing to run with CF_ENV=staging"},
		{"production", "CF_ENV=production\n", "refusing to run with CF_ENV=production"},
		{"unset", "", "CF_ENV is not set"},
		{"unknown", "CF_ENV=prod-ish\n", "is not local, ci or bench"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := pingEnv(t, tc.env+"DATABASE_URL=postgres://u:p@localhost:1/db\n")
			prj := ids.Encode(ids.PrefixProject, mustUUID(t))
			for _, sub := range [][]string{
				{"create", "--workspace", "org_x", "--format", "9:16", "--all-ready", "--yes", "--env-file", env},
				{"show", prj, "--workspace", "org_x", "--env-file", env},
			} {
				var out, errb bytes.Buffer
				code := cmdProject(sub, &out, &errb, fakeProcessEnv(nil), agentDeps{})
				if code != 2 || !strings.Contains(errb.String(), tc.want) {
					t.Errorf("%s: exit %d, stderr %q, want 2 and %q", sub[0], code, errb.String(), tc.want)
				}
			}
		})
	}
}

func mustUUID(t *testing.T) uuid.UUID {
	t.Helper()
	u, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestProjectCreateNeedsYesOutsidePlainLocal(t *testing.T) {
	env := pingEnv(t, "CF_ENV=bench\nDATABASE_URL=postgres://u:p@localhost:1/db\n")
	var out, errb bytes.Buffer
	code := cmdProject([]string{"create", "--workspace", "org_x", "--format", "9:16", "--all-ready", "--env-file", env}, &out, &errb, fakeProcessEnv(nil), agentDeps{})
	if code != 2 || !strings.Contains(errb.String(), "without --yes") || !strings.Contains(errb.String(), "CF_ENV=bench is not local or ci") {
		t.Errorf("exit %d, stderr %q", code, errb.String())
	}
}

func TestProjectCreateArguments(t *testing.T) {
	ast := ids.Encode(ids.PrefixAsset, mustUUID(t))
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no workspace", []string{"create", "--format", "9:16", "--all-ready"}, "--workspace is required"},
		{"bad workspace", []string{"create", "--workspace", "nope", "--format", "9:16", "--all-ready"}, "--workspace must be"},
		{"no format", []string{"create", "--workspace", "org_x", "--all-ready"}, "--format must be one of"},
		{"bad format", []string{"create", "--workspace", "org_x", "--format", "3:2", "--all-ready"}, "--format must be one of"},
		{"no footage", []string{"create", "--workspace", "org_x", "--format", "1:1"}, "--assets"},
		{"both footage flags", []string{"create", "--workspace", "org_x", "--format", "1:1", "--all-ready", "--assets", ast}, "not both"},
		{"bad asset id", []string{"create", "--workspace", "org_x", "--format", "1:1", "--assets", ast + ",wrong"}, `"wrong"`},
		{"both briefs", []string{"create", "--workspace", "org_x", "--format", "1:1", "--all-ready", "--brief", "a", "--brief-file", "b"}, "--brief or --brief-file"},
		{"missing brief file", []string{"create", "--workspace", "org_x", "--format", "1:1", "--all-ready", "--brief-file", "/no/such/brief.txt"}, "no such file"},
		{"stray argument", []string{"create", "--workspace", "org_x", "--format", "1:1", "--all-ready", "extra"}, `unexpected argument "extra"`},
		{"show without id", []string{"show", "--workspace", "org_x"}, "exactly one project id"},
		{"show with a bad id", []string{"show", "ast_x", "--workspace", "org_x"}, "not a project id"},
		{"unknown", []string{"delete"}, `unknown command "delete"`},
	} {
		var out, errb bytes.Buffer
		code := cmdProject(tc.args, &out, &errb, fakeProcessEnv(nil), agentDeps{})
		if code != 2 || !strings.Contains(errb.String(), tc.want) {
			t.Errorf("%s: exit %d, stderr %q, want 2 and %q", tc.name, code, errb.String(), tc.want)
		}
	}
}

func TestProjectCreateAllReady(t *testing.T) {
	r := newProjectRig(t)
	other, err := r.st.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: "org_other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	first := r.asset(r.ws, store.AssetKindVideo, "ready", "Squat")
	r.asset(r.ws, store.AssetKindVideo, "uploading", "Still uploading")
	r.asset(r.ws, store.AssetKindAudio, "ready", "Music")
	r.asset(r.ws, store.AssetKindVideo, "deleted", "Deleted")
	r.asset(other, store.AssetKindVideo, "ready", "Someone else's")
	second := r.asset(r.ws, store.AssetKindVideo, "ready", "Deadlift")
	third := r.asset(r.ws, store.AssetKindVideo, "ready", "Bench")

	code, out, errOut := r.run("create", "--workspace", r.ws.ID.String(), "--format", "9:16", "--title", "Leg day", "--brief", "Make it punchy", "--all-ready", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	pid := r.projectIDIn(out)
	d, err := r.st.GetProject(context.Background(), r.ws.ID, pid)
	if err != nil {
		t.Fatal(err)
	}
	if d.CreatedBy != r.owner.ID || d.Title != "Leg day" || d.Format != "9:16" || d.Brief != "Make it punchy" || d.Status != store.ProjectDraft {
		t.Errorf("project: %+v (owner %s, viewer %s)", d, r.owner.ID, r.viewer.ID)
	}
	rows, err := r.st.ListProjectAssets(context.Background(), r.ws.ID, pid)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range rows {
		got = append(got, a.AssetID.String())
	}
	want := []string{first.ID.String(), second.ID.String(), third.ID.String()}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("assets %v, want %v (ready videos of this workspace, oldest first)", got, want)
	}

	for _, line := range []string{
		`workspace   "Fit Studio" (` + r.ws.ID.String() + `)`,
		"created by  owner@example.com (owner)",
		"project  " + pid.String(),
		"title    Leg day",
		"format   9:16",
		"status   draft",
		"brief    Make it punchy",
		"assets   3",
		"    " + first.ID.String() + "  Squat  (4.0s)",
		"    " + third.ID.String() + "  Bench  (4.0s)",
		"cfctl agent run --agent editor --workspace " + r.ws.ID.String() + " --project " + pid.String() + " --profile dev-cheap --budget-usd 0.25 --events --yes",
		"cfctl agent run --agent editor --workspace " + r.ws.ID.String() + " --project " + pid.String() + " --profile v1-start --budget-usd 5 --events --yes",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	for _, not := range []string{"Still uploading", "Music", "Deleted", "Someone else's"} {
		if strings.Contains(out, not) {
			t.Errorf("output lists %q:\n%s", not, out)
		}
	}
}

func TestProjectCreateAllReadyCapsAtFifty(t *testing.T) {
	r := newProjectRig(t)
	var firstIDs []string
	for i := range 52 {
		a := r.asset(r.ws, store.AssetKindVideo, "ready", fmt.Sprintf("Clip %02d", i))
		if i < 50 {
			firstIDs = append(firstIDs, a.ID.String())
		}
	}
	code, out, errOut := r.run("create", "--workspace", r.ws.ID.String(), "--format", "16:9", "--all-ready", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "52 ready video assets; using the oldest 50") || !strings.Contains(out, "assets   50") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", out, errOut)
	}
	rows, err := r.st.ListProjectAssets(context.Background(), r.ws.ID, r.projectIDIn(out))
	if err != nil || len(rows) != 50 {
		t.Fatalf("assets: %d, %v", len(rows), err)
	}
	for i, a := range rows {
		if a.AssetID.String() != firstIDs[i] {
			t.Fatalf("asset %d is %s, want %s", i, a.AssetID, firstIDs[i])
		}
	}
	if !strings.Contains(out, "Untitled project") {
		t.Errorf("no default title:\n%s", out)
	}
}

func TestProjectCreateAssetsOrgAndBriefFile(t *testing.T) {
	r := newProjectRig(t)
	a := r.asset(r.ws, store.AssetKindVideo, "ready", "One")
	b := r.asset(r.ws, store.AssetKindAudio, "ready", "Two")
	pending := r.asset(r.ws, store.AssetKindVideo, "uploading", "Three")
	brief := filepath.Join(t.TempDir(), "brief.txt")
	if err := os.WriteFile(brief, []byte("line one\nline two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Explicit order, the Clerk organization id, the brief from a file.
	code, out, errOut := r.run("create", "--workspace", "org_project", "--format", "4:5", "--assets", b.ID.String()+", "+a.ID.String(), "--brief-file", brief, "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	pid := r.projectIDIn(out)
	p, err := r.st.GetProject(context.Background(), r.ws.ID, pid)
	if err != nil || p.Brief != "line one\nline two" || p.Format != "4:5" {
		t.Fatalf("project %+v, %v", p, err)
	}
	rows, _ := r.st.ListProjectAssets(context.Background(), r.ws.ID, pid)
	if len(rows) != 2 || rows[0].AssetID != b.ID || rows[1].AssetID != a.ID {
		t.Errorf("assets %+v, want Two then One", rows)
	}

	// The service's own refusals come through as they are.
	code, _, errOut = r.run("create", "--workspace", r.ws.ID.String(), "--format", "9:16", "--assets", pending.ID.String(), "--yes")
	if code != 1 || !strings.Contains(errOut, "assets_not_usable") || !strings.Contains(errOut, pending.ID.String()) {
		t.Errorf("not ready: exit %d, %q", code, errOut)
	}
}

func TestProjectCreateWithoutOwnerOrAssets(t *testing.T) {
	r := newProjectRig(t)
	ctx := context.Background()
	ownerless, err := r.st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_ownerless", Name: "No Owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AddMember(ctx, ownerless.ID, r.viewer.ID, store.RoleEditor); err != nil {
		t.Fatal(err)
	}
	r.asset(ownerless, store.AssetKindVideo, "ready", "Clip")
	code, out, errOut := r.run("create", "--workspace", ownerless.ID.String(), "--format", "9:16", "--all-ready", "--yes")
	if code != 2 || !strings.Contains(errOut, "no owner") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if rows, err := r.st.ListProjects(ctx, ownerless.ID, nil, 10); err != nil || len(rows) != 0 {
		t.Errorf("projects %+v, %v", rows, err)
	}

	code, _, errOut = r.run("create", "--workspace", r.ws.ID.String(), "--format", "9:16", "--all-ready", "--yes")
	if code != 1 || !strings.Contains(errOut, "no ready video assets") {
		t.Errorf("empty library: exit %d, %q", code, errOut)
	}
	code, _, errOut = r.run("create", "--workspace", ids.Encode(ids.PrefixWorkspace, mustUUID(t)), "--format", "9:16", "--all-ready", "--yes")
	if code != 1 || !strings.Contains(errOut, "no such workspace") {
		t.Errorf("unknown workspace: exit %d, %q", code, errOut)
	}
}

func TestProjectShow(t *testing.T) {
	r := newProjectRig(t)
	a := r.asset(r.ws, store.AssetKindVideo, "ready", "Squat")
	code, out, errOut := r.run("create", "--workspace", r.ws.ID.String(), "--format", "1:1", "--assets", a.ID.String(), "--yes")
	if code != 0 {
		t.Fatalf("create: exit %d\n%s%s", code, out, errOut)
	}
	pid := r.projectIDIn(out)

	// No versions yet; no renders table in this schema, so no render section.
	code, out, errOut = r.run("show", pid.String(), "--workspace", r.ws.ID.String())
	if code != 0 || !strings.Contains(out, "project  "+pid.String()) || !strings.Contains(out, "versions (0):") || !strings.Contains(out, a.ID.String()+"  Squat") {
		t.Fatalf("show: exit %d\n%s%s", code, out, errOut)
	}

	for n := 1; n <= 2; n++ {
		if _, err := r.st.AddTimelineVersion(context.Background(), r.ws.ID, pid, store.NewTimelineVersion{
			Version: n, Parent: n - 1, Spec: json.RawMessage(`{}`), Summary: []string{fmt.Sprintf("edit number %d", n), "second line"},
			AuthorKind: store.AuthorUser, AuthorID: "usr_x",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Flags may come before the id, too.
	code, out, errOut = r.run("show", "--workspace", "org_project", pid.String())
	if code != 0 {
		t.Fatalf("show: exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"current  version 2", "versions (2):", "v2   parent 1", "v1   parent -", "- edit number 2", "- edit number 1", "- second line"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "v2 ") > strings.Index(out, "v1 ") {
		t.Errorf("versions are not newest first:\n%s", out)
	}

	// Another workspace's project is as unknown as one that never existed.
	code, _, errOut = r.run("show", ids.Encode(ids.PrefixProject, mustUUID(t)), "--workspace", r.ws.ID.String())
	if code != 1 || !strings.Contains(errOut, "not_found") {
		t.Errorf("unknown project: exit %d, %q", code, errOut)
	}
}

func TestProjectShowLatestRender(t *testing.T) {
	r := newProjectRig(t)
	a := r.asset(r.ws, store.AssetKindVideo, "ready", "Squat")
	_, out, _ := r.run("create", "--workspace", r.ws.ID.String(), "--format", "1:1", "--assets", a.ID.String(), "--yes")
	pid := r.projectIDIn(out)
	ctx := context.Background()
	// A stand-in for the renders table of a later migration: the command reads it without knowing its columns.
	for _, q := range []string{
		`CREATE TABLE renders (id text, workspace_id uuid, project_id uuid, status text, timeline_version int, created_at timestamptz)`,
		`GRANT SELECT ON renders TO PUBLIC`,
	} {
		if _, err := r.st.Pool().Exec(ctx, q); err != nil {
			t.Skipf("cannot create a stand-in renders table as this role: %v", err)
		}
	}
	code, out, errOut := r.run("show", pid.String(), "--workspace", r.ws.ID.String())
	if code != 0 || !strings.Contains(out, "latest render: none") {
		t.Fatalf("no render: exit %d\n%s%s", code, out, errOut)
	}
	if _, err := r.st.Pool().Exec(ctx, `INSERT INTO renders VALUES ('ren_old', $1, $2, 'failed', 1, now() - interval '1 hour'), ('ren_new', $1, $2, 'succeeded', 2, now())`,
		r.ws.ID.UUID, pid.UUID); err != nil {
		t.Skipf("cannot fill the stand-in renders table: %v", err)
	}
	code, out, errOut = r.run("show", pid.String(), "--workspace", r.ws.ID.String())
	if code != 0 || !strings.Contains(out, "latest render:") || !strings.Contains(out, "ren_new") || !strings.Contains(out, "succeeded") || strings.Contains(out, "ren_old") {
		t.Errorf("latest render: exit %d\n%s%s", code, out, errOut)
	}
}
