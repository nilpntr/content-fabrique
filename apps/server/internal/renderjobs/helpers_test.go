package renderjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/spend/spendtest"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The tests run the real service, pipeline and worker over a throwaway Postgres and MinIO (Docker, via testcontainers) and
// render with the ffmpeg on the machine through the local sandbox provider. They skip when ffmpeg is missing. With
// -docker-image they run ffmpeg in the media-tools image instead, through the docker provider (the test clips are still
// generated on the host):
//
//	go test ./internal/renderjobs -docker-image media-tools:dev
//
// -timing renders a 30 s edit with both profiles and logs how long the job takes (TestRenderTiming).
var (
	dockerImage = flag.String("docker-image", "", "run the render tools in this media-tools image instead of the local ffmpeg")
	timingFlag  = flag.Bool("timing", false, "render a 30 s edit with both profiles and log how long the job takes")
)

// errGone is what a job for a render that is not there ends with.
var errGone = jobs.ErrRenderGone

// callerOf is the httpapi.CallerFunc of a signed-in user with a role in workspace ws.
func callerOf(ws ids.WorkspaceID, user ids.UserID, role store.Role) httpapi.CallerFunc {
	return func(context.Context) (httpapi.Caller, bool) {
		return httpapi.Caller{Workspace: ws, User: user, Role: role}, true
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var (
	mediaOnce sync.Once
	mediaDir  string
	mediaErr  error
)

func TestMain(m *testing.M) {
	code := storetest.Run(m)
	storagetest.Stop()
	if mediaDir != "" {
		_ = os.RemoveAll(mediaDir)
	}
	os.Exit(code)
}

func needFFmpeg(t *testing.T) {
	t.Helper()
	// Also with -docker-image: the clips and the checks use the host's ffmpeg.
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("skipping: %s is not installed", tool)
		}
	}
}

// clipFiles are the two files of one source: the 1080x1920 original and its 720x1280 proxy, as the ingest makes them.
type clipFiles struct{ original, proxy []byte }

// Test clips are generated once per test binary: 10 s, 30 fps, 1080x1920 originals with a sine tone, and 720x1280 proxies
// (x264 veryfast, a keyframe every second, AAC, the index first). One is the test pattern, the other a moving gradient, so a
// tile of the contact sheet can be told from a tile of the other asset.
func testClips(t *testing.T) [2]clipFiles {
	t.Helper()
	needFFmpeg(t)
	mediaOnce.Do(func() {
		mediaDir, mediaErr = os.MkdirTemp("", "cf-renderjobs-media-")
		if mediaErr != nil {
			return
		}
		for i, src := range []string{"testsrc2=size=1080x1920:rate=30:duration=10", "gradients=size=1080x1920:rate=30:duration=10:speed=0.05"} {
			orig := filepath.Join(mediaDir, fmt.Sprintf("orig%d.mp4", i))
			proxy := filepath.Join(mediaDir, fmt.Sprintf("proxy%d.mp4", i))
			steps := [][]string{
				{"-f", "lavfi", "-i", src, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=10", 330+110*i),
					"-c:v", "libx264", "-preset", "ultrafast", "-crf", "24", "-pix_fmt", "yuv420p", "-g", "30", "-c:a", "aac", "-b:a", "128k", "-shortest", orig},
				{"-i", orig, "-vf", "scale=720:1280", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p", "-g", "30",
					"-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", proxy},
			}
			for _, args := range steps {
				if out, err := exec.Command("ffmpeg", append([]string{"-nostdin", "-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil { //nolint:gosec // test
					mediaErr = fmt.Errorf("ffmpeg %v: %w: %s", args, err, out)
					return
				}
			}
		}
	})
	if mediaErr != nil {
		t.Skipf("skipping: cannot generate the test clips: %v", mediaErr)
	}
	var out [2]clipFiles
	for i := range out {
		var err error
		if out[i].original, err = os.ReadFile(filepath.Join(mediaDir, fmt.Sprintf("orig%d.mp4", i))); err != nil {
			t.Fatal(err)
		}
		if out[i].proxy, err = os.ReadFile(filepath.Join(mediaDir, fmt.Sprintf("proxy%d.mp4", i))); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// env is a migrated database, a bucket, the service and a pipeline over them.
type env struct {
	dsn     string
	super   *pgxpool.Pool
	st      *store.Store
	cfg     storage.Config
	objs    *storage.Client
	enq     *jobs.Enqueuer
	svc     *Service
	pipe    *Pipeline
	tempDir string
	user    ids.UserID
	runner  media.Runner
	// mem is the in-memory spend backend the pipeline's meter writes to.
	mem *spendtest.Memory
}

// runnerOf is the sandbox the pipeline renders in: the local provider, or the docker provider and the image of -docker-image.
func runnerOf(root string) media.Runner {
	limits := sandbox.Limits{CPU: 4, MemoryMB: 4096, Timeout: 10 * time.Minute, PIDs: 1024}
	pol := sandbox.Policy{AllowedRoots: []string{root}}
	if *dockerImage != "" {
		return &media.SandboxRunner{Provider: sandbox.NewDocker(pol), Image: *dockerImage, Limits: limits}
	}
	return &media.SandboxRunner{Provider: sandbox.NewLocal(pol), Image: "unused", Limits: limits}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	needFFmpeg(t)
	ctx := context.Background()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	superPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(superPool.Close)
	st, err := store.Open(ctx, dsn, store.Options{AppName: "renderjobs-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	cfg := storagetest.NewBucket(t)
	objs, err := storage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	client, err := jobs.NewInsertClient(st.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	enq := jobs.NewEnqueuer(client, quiet)
	svc, err := NewService(st, objs, enq, quiet)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{mem: &spendtest.Memory{}, dsn: dsn, super: superPool, st: st, cfg: cfg, objs: objs, enq: enq, svc: svc, tempDir: filepath.Join(root, "render"), runner: runnerOf(root)}
	e.pipe = e.newPipeline(t, objs, e.runner)
	u, err := st.UpsertUserByClerkID(ctx, "user_renderjobs", "renderjobs@example.com")
	if err != nil {
		t.Fatal(err)
	}
	e.user = u.ID
	return e
}

func (e *env) newPipeline(t *testing.T, objs PipelineObjects, runner media.Runner) *Pipeline {
	t.Helper()
	meter := &spend.ComputeMeter{
		Gate: spend.NewGate(e.mem, spend.Options{Caps: store.SpendCaps{WorkspaceDailyMicros: 1 << 40, PlatformDailyMicros: 1 << 40}}),
		Cost: func(ms int64) (int64, error) { return (ms*20 + 999) / 1000, nil }, PriceVersion: "test-prices", BilledTo: store.BilledToTest,
	}
	p, err := New(Config{Store: e.st, Objects: objs, Runner: runner, TempDir: e.tempDir, Meter: meter, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *env) newWorkspace(t *testing.T, org string) store.Workspace {
	t.Helper()
	w, err := e.st.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: org, Name: org, Region: "eu", BillingMode: store.BillingManaged})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// readyAsset makes a ready video asset of the workspace whose original and 720p proxy are real objects.
func (e *env) readyAsset(t *testing.T, w store.Workspace, name string, c clipFiles) store.Asset {
	t.Helper()
	ctx := context.Background()
	id, err := e.st.NewAssetID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := storage.OriginalKey(w.ID, id)
	e.put(t, key, c.original)
	if _, _, err := e.st.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
		ID: id, Kind: store.AssetKindVideo, Filename: name + ".mp4", ContentType: "video/mp4", SizeBytes: int64(len(c.original)), Title: name, R2Key: key,
		CreatedBy: e.user, R2UploadID: "done", PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
	}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CompleteUpload(ctx, w.ID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.ClaimAssetForIngest(ctx, w.ID, id); err != nil {
		t.Fatal(err)
	}
	proxyKey, err := storage.DerivativeKey(w.ID, id, "proxy_720.mp4")
	if err != nil {
		t.Fatal(err)
	}
	e.put(t, proxyKey, c.proxy)
	a, err := e.st.FinishAssetIngest(ctx, w.ID, id, store.IngestResult{
		DurationMs: 10000, Width: 1080, Height: 1920, FPS: 30, VideoCodec: "h264", AudioCodec: "aac", HasAudio: true, Probe: json.RawMessage(`{}`),
		Derivatives: []store.NewDerivative{{Kind: store.DerivativeProxy720, Key: proxyKey, ContentType: "video/mp4", SizeBytes: int64(len(c.proxy))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (e *env) put(t *testing.T, key string, data []byte) {
	t.Helper()
	_, err := storagetest.S3(e.cfg).PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(e.cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(data)})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) get(t *testing.T, key string) []byte {
	t.Helper()
	obj, err := e.objs.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer func() { _ = obj.Body.Close() }()
	b, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *env) exists(t *testing.T, key string) bool {
	t.Helper()
	_, err := e.objs.Head(context.Background(), key)
	return err == nil
}

func (e *env) newProject(t *testing.T, w store.Workspace, assets ...store.Asset) store.Project {
	t.Helper()
	ctx := context.Background()
	var p store.Project
	err := e.st.InTx(ctx, func(tx *store.Store) error {
		var err error
		if p, err = tx.CreateProject(ctx, w.ID, store.NewProject{Title: "Back day", Format: store.Format9x16, CreatedBy: e.user}); err != nil {
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

// addVersion stores spec as the next version of the project and returns its number.
func (e *env) addVersion(t *testing.T, w store.Workspace, p store.Project, spec *timeline.Spec) int {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := e.st.GetProject(ctx, w.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 1
	if cur.CurrentTimelineVersion != nil {
		n = int(*cur.CurrentTimelineVersion) + 1
	}
	if _, err := e.st.AddTimelineVersion(ctx, w.ID, p.ID, store.NewTimelineVersion{
		Version: n, Parent: n - 1, Spec: raw, AuthorKind: store.AuthorUser, AuthorID: e.user.String(), Summary: []string{"test"},
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// caller is the Caller of the test user in workspace w.
func (e *env) caller(w store.Workspace) Caller { return UserCaller(w.ID, e.user) }

// request asks for a render and fails the test on a refusal.
func (e *env) request(t *testing.T, w store.Workspace, p store.Project, version int, profile string) (Render, bool) {
	t.Helper()
	r, created, err := e.svc.Request(context.Background(), e.caller(w), p.ID, version, profile)
	if err != nil {
		t.Fatalf("Request(%d, %s): %v", version, profile, err)
	}
	return r, created
}

// runRender runs the pipeline on the render and returns the render as the database has it afterwards.
func (e *env) runRender(t *testing.T, w store.Workspace, r Render) (Render, jobs.RenderOutcome) {
	t.Helper()
	out, err := e.pipe.Run(context.Background(), w.ID, r.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, gerr := e.svc.Get(context.Background(), w.ID, r.ID)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	return got, out
}

// ---- specs ----------------------------------------------------------------------------------------------------------

func ptr[T any](v T) *T { return &v }

// thirtySecondSpec is a 30.5 s edit of the two assets: ten 3.2 s clips with cuts, crossfades and whips, looks, all three
// reframe modes, source audio, a cue_only trend sound and 60 markers.
func thirtySecondSpec(a, b store.Asset) *timeline.Spec {
	looks := []string{"none", "punchy", "warm", "cool", "bw", "matte"}
	modes := []string{timeline.ReframeCover, timeline.ReframeContain, timeline.ReframeBlurFill}
	var items []timeline.VideoItem
	var prevEnd int64
	for i := range 10 {
		asset := a.ID
		if i%2 == 1 {
			asset = b.ID
		}
		in := int64(i/2) * 1500
		it := timeline.VideoItem{
			ID: fmt.Sprintf("c%d", i+1), AssetID: asset.String(), Source: timeline.Source{InMs: in, OutMs: in + 3200},
			Audio:   &timeline.VideoAudio{UseSource: ptr(true), GainDB: ptr(-3.0)},
			Look:    &timeline.Look{Preset: looks[i%len(looks)]},
			Reframe: &timeline.Reframe{Mode: modes[i%len(modes)]},
		}
		var overlap int64
		switch {
		case i == 0:
		case i%3 == 1:
			it.TransitionIn, overlap = &timeline.Transition{Type: timeline.TransitionCrossfade, DurationMs: 300}, 300
		case i%3 == 2:
			it.TransitionIn, overlap = &timeline.Transition{Type: timeline.TransitionWhip, DurationMs: 200}, 200
		}
		it.AtMs = prevEnd - overlap
		items = append(items, it)
		prevEnd = it.AtMs + timeline.OutputDuration(it)
	}
	spec := &timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"},
		Tracks: []timeline.Track{
			&timeline.VideoTrack{ID: timeline.MainTrackID, Items: items},
			&timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{
				&timeline.TrendSound{AudioCommon: timeline.AudioCommon{ID: "s1", SourceOffsetMs: 12000, Export: timeline.ExportCueOnly, GainDB: ptr(-3.0)}, TrendID: "trd_01jcmevy4tr3fffvjc82eh7rjm"},
			}},
		},
		Markers: []timeline.Marker{},
	}
	for ms := int64(0); ms < prevEnd; ms += 500 {
		kind := timeline.MarkerBeat
		if ms%2000 == 0 {
			kind = timeline.MarkerDownbeat
		}
		spec.Markers = append(spec.Markers, timeline.Marker{AtMs: ms, Kind: kind})
	}
	return spec
}

// shortSpec is a two-clip edit of about 3 s, for the tests that are not about the render itself.
func shortSpec(a, b store.Asset) *timeline.Spec {
	return &timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"},
		Tracks: []timeline.Track{&timeline.VideoTrack{ID: timeline.MainTrackID, Items: []timeline.VideoItem{
			{ID: "c1", AssetID: a.ID.String(), Source: timeline.Source{InMs: 0, OutMs: 1500}, AtMs: 0},
			{ID: "c2", AssetID: b.ID.String(), Source: timeline.Source{InMs: 1000, OutMs: 2500}, AtMs: 1500},
		}}},
		Markers: []timeline.Marker{},
	}
}

// fakePlan compiles spec with made-up file locations, for the parts of the plan that do not depend on them (the time map, the
// cut list, the sound cues, the length and the size).
func fakePlan(t *testing.T, spec *timeline.Spec, profile string) render.Plan {
	t.Helper()
	prof, err := render.ParseProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := render.Compile(spec, prof, render.ResolverFunc(func(id string, v render.Variant) (render.ResolvedAsset, error) {
		return render.ResolvedAsset{Location: "/fake/" + string(v) + "/" + id, HasAudio: true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// ---- probing ----------------------------------------------------------------------------------------------------------

type probed struct {
	Width, Height int
	DurationSec   float64
	HasAudio      bool
	Frames        int
}

func probeFile(t *testing.T, path string) probed {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-count_frames", "-show_entries", "stream=codec_type,width,height,nb_read_frames:format=duration", "-of", "json", path).Output() //nolint:gosec // test
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var j struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Frames    string `json:"nb_read_frames"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &j); err != nil {
		t.Fatal(err)
	}
	var p probed
	p.DurationSec, _ = strconv.ParseFloat(j.Format.Duration, 64)
	for _, s := range j.Streams {
		switch s.CodecType {
		case "video":
			p.Width, p.Height = s.Width, s.Height
			p.Frames, _ = strconv.Atoi(s.Frames)
		case "audio":
			p.HasAudio = true
		}
	}
	return p
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// tempDirEmpty fails the test when the pipeline left a job directory behind.
func (e *env) tempDirEmpty(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(e.tempDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	if len(names) != 0 {
		t.Errorf("job directories left behind: %s", strings.Join(names, ", "))
	}
}

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
