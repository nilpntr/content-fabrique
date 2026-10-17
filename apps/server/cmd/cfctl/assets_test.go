package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// importRig is a migrated database with a workspace and its owner, a bucket in MinIO, and the env file that points
// cfctl at both.
type importRig struct {
	t       *testing.T
	st      *store.Store
	pool    *pgxpool.Pool
	cfg     storage.Config
	objs    *storage.Client
	ws      store.Workspace
	owner   store.User
	envFile string
	dir     string // where the files to import are
}

func newImportRig(t *testing.T) *importRig {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "import-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	cfg := storagetest.NewBucket(t)
	objs, err := storage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ws, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_import", Name: "Fit Studio"})
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
	r := &importRig{t: t, st: st, pool: st.Pool(), cfg: cfg, objs: objs, ws: ws, owner: owner, dir: t.TempDir()}
	r.envFile = filepath.Join(t.TempDir(), "import.env")
	r.writeEnv("local", dsn)
	return r
}

func (r *importRig) writeEnv(cfEnv, dsn string) {
	r.t.Helper()
	body := fmt.Sprintf("CF_ENV=%s\nDATABASE_URL=%s\nR2_ENDPOINT=%s\nR2_BUCKET=%s\nR2_ACCESS_KEY_ID=%s\nR2_SECRET_ACCESS_KEY=%s\n",
		cfEnv, dsn, r.cfg.Endpoint, r.cfg.Bucket, r.cfg.AccessKeyID, r.cfg.SecretAccessKey)
	if err := os.WriteFile(r.envFile, []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *importRig) run(args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	// --yes: the test container's host may be a Docker host; the guard has its own test below.
	full := append([]string{"import", "--env-file", r.envFile, "--yes"}, args...)
	code = cmdAssets(full, &out, &errb, fakeProcessEnv(map[string]string{}))
	return code, out.String(), errb.String()
}

// clip writes a small real video if ffmpeg is installed, else bytes with an .mp4 name (the import does not look inside).
func (r *importRig) clip(name string) string {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	if ff, err := exec.LookPath("ffmpeg"); err == nil {
		cmd := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=160x120:rate=15", "-pix_fmt", "yuv420p", "-y", path) //nolint:gosec // a test helper with fixed arguments
		if out, err := cmd.CombinedOutput(); err == nil {
			return path
		} else {
			r.t.Logf("ffmpeg failed (%v): %s; using plain bytes", err, out)
		}
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("clip"), 1000), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return path
}

// big writes a file that needs two parts (16 MiB and 5 bytes).
func (r *importRig) big(name string) string {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	data := make([]byte, 16<<20+5)
	for i := range data {
		data[i] = byte(i*7 + i>>12)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		r.t.Fatal(err)
	}
	return path
}

func sum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test file
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func (r *importRig) assetRows() map[string]struct {
	ID, Status, Title, Key string
	Size                   int64
	CreatedBy              string
} {
	r.t.Helper()
	rows, err := r.pool.Query(context.Background(), `SELECT id::text, status, title, r2_key, size_bytes, created_by::text FROM assets WHERE workspace_id = $1 ORDER BY id`, r.ws.ID.UUID)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]struct {
		ID, Status, Title, Key string
		Size                   int64
		CreatedBy              string
	}{}
	for rows.Next() {
		var a struct {
			ID, Status, Title, Key string
			Size                   int64
			CreatedBy              string
		}
		if err := rows.Scan(&a.ID, &a.Status, &a.Title, &a.Key, &a.Size, &a.CreatedBy); err != nil {
			r.t.Fatal(err)
		}
		out[a.Title] = a
	}
	return out
}

func (r *importRig) ingestJobs() int {
	r.t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'asset_ingest'`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func TestAssetsImport(t *testing.T) {
	r := newImportRig(t)
	small, big := r.clip("Back Day.mp4"), r.big("deadlift.mov")
	smallSum, bigSum := sum(t, small), sum(t, big)
	smallInfo, _ := os.Stat(small)

	code, out, errOut := r.run("--workspace", r.ws.ID.String(), "--title-from-filename", small, big)
	if code != 0 {
		t.Fatalf("import = %d\n%s%s", code, out, errOut)
	}
	if strings.Count(out, "ast_") != 2 || !strings.Contains(out, "uploaded") || !strings.Contains(out, "Back Day.mp4") || !strings.Contains(out, "deadlift.mov") ||
		!strings.Contains(out, "Fit Studio") {
		t.Fatalf("output:\n%s", out)
	}
	if strings.Contains(out+errOut, r.cfg.SecretAccessKey+"x") || strings.Contains(out+errOut, "X-Amz-Signature") {
		t.Errorf("the output holds a secret or a presigned URL:\n%s%s", out, errOut)
	}

	rows := r.assetRows()
	if len(rows) != 2 {
		t.Fatalf("assets: %+v", rows)
	}
	for title, size := range map[string]int64{"Back Day": smallInfo.Size(), "deadlift": 16<<20 + 5} {
		a, ok := rows[title]
		if !ok || a.Status != store.AssetUploaded || a.Size != size || a.CreatedBy != r.owner.ID.UUID.String() {
			t.Fatalf("asset %q: %+v (want %d bytes, uploaded, by the owner)", title, a, size)
		}
		if !strings.HasPrefix(a.Key, "ws/"+r.ws.ID.String()+"/assets/") {
			t.Errorf("key %q", a.Key)
		}
		info, err := r.objs.Head(context.Background(), a.Key)
		if err != nil || info.Size != size {
			t.Errorf("object of %q: %+v, %v", title, info, err)
		}
	}
	// The ingest is enqueued the way POST /complete does: one job per asset.
	if n := r.ingestJobs(); n != 2 {
		t.Errorf("asset_ingest jobs = %d, want 2", n)
	}
	var uploads int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM uploads WHERE completed_at IS NOT NULL`).Scan(&uploads); err != nil || uploads != 2 {
		t.Errorf("completed uploads = %d, %v", uploads, err)
	}
	// Read in place: the files are as they were, and nothing else appeared next to them.
	if sum(t, small) != smallSum || sum(t, big) != bigSum {
		t.Error("an imported file changed")
	}
	if entries, _ := os.ReadDir(r.dir); len(entries) != 2 {
		t.Errorf("the directory of the files has %d entries", len(entries))
	}

	t.Run("by org id and by user, flags after the files", func(t *testing.T) {
		code, out, errOut := r.run(small, "--workspace", "org_import", "--user", "OWNER@example.com")
		if code != 0 || strings.Count(out, "ast_") != 1 {
			t.Fatalf("import = %d\n%s%s", code, out, errOut)
		}
		if n := r.ingestJobs(); n != 3 {
			t.Errorf("asset_ingest jobs = %d, want 3", n)
		}
	})

	t.Run("a bad file stops the batch before anything is written", func(t *testing.T) {
		before := len(r.assetRows())
		notes := filepath.Join(r.dir, "notes.txt")
		if err := os.WriteFile(notes, []byte("hi"), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, args := range map[string][]string{
			"unknown type":  {"--workspace", r.ws.ID.String(), small, notes},
			"missing file":  {"--workspace", r.ws.ID.String(), small, filepath.Join(r.dir, "nope.mp4")},
			"no files":      {"--workspace", r.ws.ID.String()},
			"no workspace":  {small},
			"bad workspace": {"--workspace", "ast_01j3er3kwcgrasdgd4aznmxxe0", small},
		} {
			if code, out, errOut := r.run(args...); code != 2 {
				t.Errorf("%s: exit %d\n%s%s", name, code, out, errOut)
			}
		}
		if n := len(r.assetRows()); n != before {
			t.Errorf("assets after refused batches = %d, want %d", n, before)
		}
	})

	t.Run("workspace and member problems", func(t *testing.T) {
		if code, _, errOut := r.run("--workspace", "org_nope", small); code != 1 || !strings.Contains(errOut, "no such workspace") {
			t.Errorf("unknown workspace: %d %s", code, errOut)
		}
		if code, _, errOut := r.run("--workspace", r.ws.ID.String(), "--user", "stranger@example.com", small); code != 2 || !strings.Contains(errOut, "not a member") {
			t.Errorf("unknown member: %d %s", code, errOut)
		}
	})
}

// --wait ends when the worker has made the assets ready (here a goroutine plays the worker) or failed them, and gives up
// at --timeout.
func TestAssetsImportWait(t *testing.T) {
	r := newImportRig(t)
	prev := importPollInterval
	importPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { importPollInterval = prev })
	f := r.clip("one.mp4")

	t.Run("ready and failed", func(t *testing.T) {
		g := r.clip("two.mp4")
		var stop atomic.Bool
		done := make(chan struct{})
		go func() { // the worker
			defer close(done)
			ctx := context.Background()
			for !stop.Load() {
				time.Sleep(30 * time.Millisecond)
				list, err := r.st.ListAssets(ctx, r.ws.ID, nil, 50)
				if err != nil {
					return
				}
				for _, a := range list {
					if a.Status != store.AssetUploaded {
						continue
					}
					if _, err := r.st.ClaimAssetForIngest(ctx, r.ws.ID, a.ID); err != nil {
						continue
					}
					if a.Title == "two" {
						_, _ = r.pool.Exec(ctx, `UPDATE assets SET status = 'failed', failure_reason = 'corrupt_media', ingest_stage = NULL WHERE id = $1`, a.ID.UUID)
						continue
					}
					_, _ = r.st.FinishAssetIngest(ctx, r.ws.ID, a.ID, store.IngestResult{DurationMs: 1000, Width: 160, Height: 120, FPS: 15, VideoCodec: "h264"})
				}
			}
		}()
		code, out, errOut := r.run("--workspace", r.ws.ID.String(), "--wait", f, g)
		stop.Store(true)
		<-done
		if code != 1 { // one of the two failed
			t.Fatalf("exit %d\n%s%s", code, out, errOut)
		}
		if !strings.Contains(out, "ready  one.mp4  1.0s  160x120") || !strings.Contains(out, "failed  two.mp4  reason: corrupt_media") {
			t.Fatalf("output:\n%s", out)
		}
	})

	t.Run("gives up without a worker", func(t *testing.T) {
		h := r.clip("three.mp4")
		code, out, errOut := r.run("--workspace", r.ws.ID.String(), "--wait", "--timeout", "300ms", h)
		if code != 1 || !strings.Contains(out, "not finished") || !strings.Contains(out, "is the worker running") {
			t.Fatalf("exit %d\n%s%s", code, out, errOut)
		}
	})
}

// Development only: staging and production are refused outright (--yes does not help), and so is a CF_ENV that is not set
// or not a development one. Nothing is read from the database or storage, nor written.
func TestAssetsImportRefusals(t *testing.T) {
	r := newImportRig(t)
	f := r.clip("clip.mp4")
	for _, env := range []string{"staging", "production", "", "prod", "sandbox"} {
		t.Run("CF_ENV="+env, func(t *testing.T) {
			// An unreachable database: the refusal must come first.
			r.writeEnv(env, "postgres://nobody:nope@db.invalid:5432/x")
			code, out, errOut := r.run("--workspace", r.ws.ID.String(), f)
			if code != 2 || !strings.Contains(errOut, "development only") {
				t.Fatalf("exit %d\n%s%s", code, out, errOut)
			}
			if strings.Contains(out+errOut, "nope") {
				t.Errorf("the output holds the password:\n%s%s", out, errOut)
			}
		})
	}
	if n := len(r.assetRows()); n != 0 {
		t.Errorf("assets = %d, want 0", n)
	}
	// help works without any environment.
	var out, errb bytes.Buffer
	if code := cmdAssets([]string{"help"}, &out, &errb, fakeProcessEnv(nil)); code != 0 || !strings.Contains(out.String(), "Development only") {
		t.Errorf("help: %d %s%s", code, out.String(), errb.String())
	}
	if code := cmdAssets(nil, &out, &errb, fakeProcessEnv(nil)); code != 2 {
		t.Errorf("no arguments: %d", code)
	}
	if code := cmdAssets([]string{"remove"}, &out, &errb, fakeProcessEnv(nil)); code != 2 {
		t.Errorf("unknown command: %d", code)
	}
}
