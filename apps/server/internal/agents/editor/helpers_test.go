package editor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) {
	code := storetest.Run(m)
	storagetest.Stop()
	os.Exit(code)
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// ---- an in-memory object store -------------------------------------------------------------------------------------

type memObjects struct {
	mu   sync.Mutex
	m    map[string][]byte
	puts []string
}

func newMemObjects() *memObjects { return &memObjects{m: map[string][]byte{}} }

func (o *memObjects) set(key string, data []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[key] = data
}

func (o *memObjects) Get(_ context.Context, key string) (storage.Object, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	data, ok := o.m[key]
	if !ok {
		return storage.Object{}, fmt.Errorf("get: %w", storage.ErrNotFound)
	}
	return storage.Object{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data)), ContentType: "application/octet-stream"}, nil
}

func (o *memObjects) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	data, ok := o.m[key]
	if !ok {
		return storage.ObjectInfo{}, fmt.Errorf("head: %w", storage.ErrNotFound)
	}
	return storage.ObjectInfo{Size: int64(len(data))}, nil
}

func (o *memObjects) Put(_ context.Context, key string, body io.ReadSeeker, size int64, _ string) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("put: %d bytes, size said %d", len(data), size)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[key] = data
	o.puts = append(o.puts, key)
	return nil
}

func (o *memObjects) putCount() int { o.mu.Lock(); defer o.mu.Unlock(); return len(o.puts) }

// testJPEG is a deterministic picture whose colour depends on seed.
func testJPEG(seed string, w, h int) []byte {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(seed))
	s := hash.Sum32()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(s), G: uint8(s>>8) + uint8(x), B: uint8(s>>16) + uint8(y), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// ---- a fake frame source and a fake preview -------------------------------------------------------------------------

type fakeFrames struct {
	objs *memObjects
	mu   sync.Mutex
	// calls records (asset, times).
	calls []string
	err   error
	// durationMs is the proxy's duration for the clamp (0: none), and failAt the shown times that cannot be decoded.
	durationMs int64
	failAt     map[int64]bool
}

func (f *fakeFrames) Frames(_ context.Context, ws ids.WorkspaceID, asset ids.AssetID, atMs []int64) ([]Frame, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s %v", asset, atMs))
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([]Frame, len(atMs))
	made := 0
	for i, req := range atMs {
		at := ClampMs(req, f.durationMs)
		key, kerr := storage.FrameKey(ws, asset, at, FrameLongSide)
		if kerr != nil {
			return nil, kerr
		}
		out[i] = Frame{AtMs: at, RequestedMs: req}
		if f.failAt[at] {
			out[i].Err = fmt.Sprintf("the picture at %d ms could not be decoded", at)
			continue
		}
		f.objs.set(key, testJPEG(key, 216, 384))
		out[i].Ref = key
		made++
	}
	if made == 0 {
		return nil, fmt.Errorf("%w: nothing decoded", ErrNoFrames)
	}
	return out, nil
}

func (f *fakeFrames) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type fakePreview struct {
	ref   ContactSheetRef
	dur   DurationMs
	err   error
	block bool
	calls []string
	runs  []uuid.UUID
}

func (p *fakePreview) RenderAndWait(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int, run uuid.UUID) (ContactSheetRef, DurationMs, error) {
	p.runs = append(p.runs, run)
	p.calls = append(p.calls, fmt.Sprintf("%s %s v%d", ws, project, version))
	if p.block {
		<-ctx.Done()
		return "", 0, ctx.Err()
	}
	return p.ref, p.dur, p.err
}

// sheet makes the fake renderer answer with a contact sheet inside the workspace.
func (e *env) sheet(prj ids.ProjectID) {
	e.preview.err, e.preview.dur = nil, 16000
	e.preview.ref = ContactSheetRef(fmt.Sprintf("ws/%s/previews/%s/v1/contact.jpg", e.wsA.ID, prj))
}

// ---- the environment --------------------------------------------------------------------------------------------------

type env struct {
	t       *testing.T
	st      *store.Store
	pool    *pgxpool.Pool
	wsA     store.Workspace
	wsB     store.Workspace
	svc     *projects.Service
	objs    *memObjects
	frames  *fakeFrames
	preview *fakePreview
	deps    Deps
	tools   map[string]runtime.Tool
	reg     *runtime.Registry
	seq     int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "editor-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := &env{t: t, st: st, pool: st.Pool(), objs: newMemObjects(), preview: &fakePreview{}}
	e.wsA = e.workspace("A")
	e.wsB = e.workspace("B")
	if e.svc, err = projects.NewService(st, quiet); err != nil {
		t.Fatal(err)
	}
	e.frames = &fakeFrames{objs: e.objs}
	e.deps = Deps{Projects: e.svc, Store: st, Frames: e.frames, Preview: e.preview}
	e.rebuild()
	return e
}

func (e *env) rebuild() {
	e.t.Helper()
	tools, err := Tools(e.deps)
	if err != nil {
		e.t.Fatal(err)
	}
	if e.reg, err = runtime.NewRegistry(tools...); err != nil {
		e.t.Fatal(err)
	}
	e.tools = map[string]runtime.Tool{}
	for _, tl := range tools {
		e.tools[tl.Name()] = tl
	}
}

func (e *env) workspace(name string) store.Workspace {
	e.t.Helper()
	ws, err := e.st.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: "org_ed_" + uuid.NewString(), Name: "Editor " + name})
	if err != nil {
		e.t.Fatal(err)
	}
	return ws
}

// clip describes an asset to seed.
type clip struct {
	id      ids.AssetID
	title   string
	desc    string
	kind    string // default video
	durMs   int64  // default 10000
	w, h    int    // default 1080 x 1920
	silent  bool
	noProxy bool
	status  string // default ready
	deleted bool
}

func newAssetID() ids.AssetID { return ids.AssetID{UUID: uuid.Must(uuid.NewV7())} }

// asset seeds a ready asset (and its proxy derivative) in the workspace.
func (e *env) asset(ws store.Workspace, c clip) ids.AssetID {
	e.t.Helper()
	if c.id.IsZero() {
		c.id = newAssetID()
	}
	if c.kind == "" {
		c.kind = store.AssetKindVideo
	}
	if c.durMs == 0 {
		c.durMs = 10000
	}
	if c.w == 0 {
		c.w, c.h = 1080, 1920
	}
	var w, h any = c.w, c.h
	if c.kind != store.AssetKindVideo {
		w, h = nil, nil // audio has no picture
	}
	if c.status == "" {
		c.status = store.AssetReady
	}
	if c.title == "" {
		c.title = "clip " + c.id.String()[:12]
	}
	_, err := e.pool.Exec(context.Background(), `
		INSERT INTO assets (id, workspace_id, kind, status, filename, content_type, size_bytes, title, description, r2_key,
		                    duration_ms, width, height, rotation, has_audio, deleted_at)
		VALUES ($1, $2, $3, $4, 'f.mp4', 'video/mp4', 1000, $5, $6, $7, $8, $9, $10, 0, $11, CASE WHEN $12 THEN now() END)`,
		c.id, ws.ID, c.kind, c.status, c.title, c.desc, storage.OriginalKey(ws.ID, c.id), c.durMs, w, h, !c.silent, c.deleted)
	if err != nil {
		e.t.Fatal(err)
	}
	if !c.noProxy && c.kind == store.AssetKindVideo {
		key, kerr := storage.DerivativeKey(ws.ID, c.id, "proxy_720.mp4")
		if kerr != nil {
			e.t.Fatal(kerr)
		}
		if _, err := e.pool.Exec(context.Background(), `
			INSERT INTO asset_derivatives (workspace_id, asset_id, kind, r2_key, content_type, size_bytes)
			VALUES ($1, $2, 'proxy_720', $3, 'video/mp4', 1000)`, ws.ID, c.id, key); err != nil {
			e.t.Fatal(err)
		}
		e.objs.set(key, []byte("not really a video: the frame extractor of the tests is a fake"))
	}
	return c.id
}

// project seeds a project of the workspace with the assets, in order.
func (e *env) project(ws store.Workspace, format, brief string, assets ...ids.AssetID) ids.ProjectID {
	e.t.Helper()
	id := ids.ProjectID{UUID: uuid.Must(uuid.NewV7())}
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO projects (id, workspace_id, title, format, brief) VALUES ($1, $2, 'Test', $3, $4)`,
		id, ws.ID, format, brief); err != nil {
		e.t.Fatal(err)
	}
	for i, a := range assets {
		if _, err := e.pool.Exec(context.Background(), `INSERT INTO project_assets (workspace_id, project_id, asset_id, position) VALUES ($1, $2, $3, $4)`,
			ws.ID, id, a, i); err != nil {
			e.t.Fatal(err)
		}
	}
	return id
}

// runCtx is the context of one tool call of a run of the workspace on the project.
func (e *env) runCtx(ws store.Workspace, project ids.ProjectID, input string) runtime.RunContext {
	e.seq++
	run := uuid.Must(uuid.NewV7())
	toolUse := fmt.Sprintf("toolu_%03d", e.seq)
	rc := runtime.RunContext{
		WorkspaceID: ws.ID, RunID: run, ProjectID: project.UUID, Agent: Name, ToolUseID: toolUse,
		IdempotencyKey: run.String() + ":" + toolUse, Logger: quiet,
	}
	if input != "" {
		rc.Input = json.RawMessage(input)
	}
	return rc.WithArtifacts(func(context.Context, json.RawMessage) error { return nil })
}

// again is the same call of the same run, as the runtime makes it when a worker died before it stored the result.
func again(rc runtime.RunContext) runtime.RunContext {
	rc.Resumed = true
	return rc
}

// call validates the arguments against the tool's schema, as the loop does, then runs the tool.
func (e *env) call(rc runtime.RunContext, tool, args string) (runtime.Result, error) {
	e.t.Helper()
	if err := e.reg.Validate(tool, json.RawMessage(args)); err != nil {
		return runtime.Errorf("%v", err), nil
	}
	return e.tools[tool].Run(context.Background(), rc, json.RawMessage(args))
}

// ok runs a tool and fails the test unless it answered normally.
func (e *env) ok(rc runtime.RunContext, tool, args string) runtime.Result {
	e.t.Helper()
	res, err := e.call(rc, tool, args)
	if err != nil {
		e.t.Fatalf("%s: %v", tool, err)
	}
	if res.IsError {
		e.t.Fatalf("%s answered with an error: %s", tool, textOf(res))
	}
	return res
}

// toolErr runs a tool that must answer with a message for the model and returns it.
func (e *env) toolErr(rc runtime.RunContext, tool, args string) string {
	e.t.Helper()
	res, err := e.call(rc, tool, args)
	var te *runtime.ToolError
	switch {
	case errors.As(err, &te):
		return te.Msg
	case err != nil:
		e.t.Fatalf("%s: want a message for the model, got an internal error: %v", tool, err)
	case !res.IsError:
		e.t.Fatalf("%s: want an error, got: %s", tool, textOf(res))
	}
	return textOf(res)
}

func textOf(res runtime.Result) string {
	var parts []string
	for _, b := range res.Content {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func imageRefs(res runtime.Result) []string {
	var out []string
	for _, b := range res.Content {
		if b.Type == "image" {
			out = append(out, b.Image.Ref)
		}
	}
	return out
}

func addClipOp(asset ids.AssetID, in, out int) string {
	return fmt.Sprintf(`{"op":"add_clip","asset_id":%q,"source":{"in_ms":%d,"out_ms":%d}}`, asset.String(), in, out)
}

// opsArgs are the arguments of an edit_timeline call based on the project's current version, as a model that has just read it
// would send them.
func (e *env) opsArgs(prj ids.ProjectID, ops ...string) string {
	e.t.Helper()
	return e.opsArgsAt(e.currentVersion(prj), ops...)
}

func (e *env) opsArgsAt(base int, ops ...string) string {
	return fmt.Sprintf(`{"base_version":%d,"ops":[%s]}`, base, strings.Join(ops, ","))
}

func (e *env) currentVersion(prj ids.ProjectID) int {
	e.t.Helper()
	var v int
	err := e.pool.QueryRow(context.Background(), `SELECT coalesce(current_timeline_version, 0) FROM projects WHERE id = $1`, prj).Scan(&v)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		e.t.Fatal(err)
	}
	return v
}

// nextCall is another tool call of the same run: a new tool-use id and so a new idempotency key.
func (e *env) nextCall(rc runtime.RunContext) runtime.RunContext {
	e.seq++
	rc.ToolUseID = fmt.Sprintf("toolu_%03d", e.seq)
	rc.IdempotencyKey = rc.RunID.String() + ":" + rc.ToolUseID
	rc.Resumed = false
	return rc
}

// runUUIDOf is the UUID of a run's TypeID.
func (e *env) runUUIDOf(typeID string) string {
	e.t.Helper()
	u, err := ids.Decode(ids.PrefixRun, typeID)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.String()
}
