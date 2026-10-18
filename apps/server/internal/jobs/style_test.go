package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/creatorstyle"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// styleModel answers the two model calls of the style: the title read and the summary. No real model is called.
type styleModel struct {
	mu    sync.Mutex
	calls map[string]int
}

func (m *styleModel) Messages(_ context.Context, req llm.Request) (llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls == nil {
		m.calls = map[string]int{}
	}
	name := req.ToolChoice.Name
	m.calls[name]++
	input := `{"title_present":true,"text":"BACK DAY 101","case":"upper","colour":"#f2e9a0","position":"upper_third","alignment":"center","weight":"bold","condensed":true,"lines":1,"text_height_pct":4,"shadow":"none","exercise_labels":false,"captions":false,"title_entrance":"pop","chapter_titles":[]}`
	if name == styleprofile.SummaryToolName {
		input = `{"summary":"Long takes of your sets, music on every video, cream capitals at the top, no flashy effects.","vibe":"lively","transitions":"subtle","text_animation":"on","camera_motion":"subtle","title_colour":"#F2E9A0","title_position":"top","title_case":"upper","music":"always","pace":"like_mine","chapter_titles":"off","ending_hold":"long","cut_timing":"full_reps","angle_switches":"off"}`
	}
	return llm.Response{Model: "claude-haiku-5-5", StopReason: "tool_use", Usage: llm.Usage{InputTokens: 1000, OutputTokens: 100},
		Content: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: name, Input: json.RawMessage(input)}}}, nil
}

func (m *styleModel) CountTokens(context.Context, llm.Request) (int64, error) { return 1000, nil }

func (m *styleModel) count(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[name]
}

type styleFactory struct{ m *styleModel }

func (f styleFactory) For(context.Context, ids.WorkspaceID, string) (llm.Client, llm.Billing, error) {
	return f.m, llm.BilledToTest, nil
}

// styleReference makes an uploaded style reference whose original is the file at path.
func (e *assetsEnv) styleReference(t *testing.T, w store.Workspace, path string) store.Asset {
	t.Helper()
	ctx := context.Background()
	data, err := os.ReadFile(path) // #nosec G304 -- a file this test made
	if err != nil {
		t.Fatal(err)
	}
	id, err := e.api.NewAssetID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := storage.OriginalKey(w.ID, id)
	if _, err := storagetest.S3(e.cfg).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(e.cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(data)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.api.CreateAssetWithUpload(ctx, w.ID, store.NewAsset{
		ID: id, Kind: store.AssetKindVideo, Filename: filepath.Base(path), ContentType: "video/mp4", SizeBytes: int64(len(data)), Title: "ref", R2Key: key,
		CreatedBy: e.user, R2UploadID: "done", PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
		Purpose: store.PurposeStyleReference, MaxReferences: styleprofile.MaxReferences,
	}, 0); err != nil {
		t.Fatal(err)
	}
	a, err := e.api.CompleteUpload(ctx, w.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (e *env) countJobs(t *testing.T, kind, asset string) int {
	t.Helper()
	var n int
	if err := e.super.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'asset_id' = $2`, kind, asset).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The whole life of a style reference through the real queue, store, object storage and ffmpeg (the local sandbox), with a fake model:
// the ingest finishing a reference queues its measuring and nothing a clip gets (no quick look, transcript or clip analysis); the
// measuring stores the numbers and the title read; the profile job makes the profile with its summary and leaves the creator's settings
// alone; removing the reference makes the profile job remove the profile. Skips without ffmpeg.
func TestAStyleReferenceIsLearntThroughTheQueue(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("skipping: %s is not installed", tool)
		}
	}
	old := jobs.StyleProfileWindow
	jobs.StyleProfileWindow = time.Second
	t.Cleanup(func() { jobs.StyleProfileWindow = old })

	e := newAssetsEnv(t)
	ctx := context.Background()
	w := e.newWorkspace(t, "org_style")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(root, "my-video.mp4")
	gen := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=270x480:rate=30:duration=3", "-f", "lavfi", "-i", "color=c=0x2060c0:s=270x480:r=30:d=3",
		"-f", "lavfi", "-i", "sine=frequency=220:sample_rate=44100:duration=6",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0,format=yuv420p[v]", "-map", "[v]", "-map", "2:a", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot make the test clip: %v: %s", err, out)
	}
	work := filepath.Join(root, "style")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	runner := &media.SandboxRunner{Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root}}), Image: "unused",
		Limits: sandbox.Limits{CPU: 2, MemoryMB: 1024, Timeout: 2 * time.Minute, PIDs: 256}}
	router, err := llm.NewRouter(configfiles.Routing(), "dev-cheap")
	if err != nil {
		t.Fatal(err)
	}
	model := &styleModel{}
	learner, err := creatorstyle.NewLearner(creatorstyle.LearnerConfig{
		Store: e.api, Objects: e.objs, TempDir: work, Logger: quiet, Jobs: jobs.ContextInserter{Logger: quiet},
		Analyzer: &styleprofile.Analyzer{Runner: runner, TempDir: work, Reader: &styleprofile.Reader{Models: styleFactory{model}, Router: router, Runner: runner, TempDir: work}},
		Writer:   &styleprofile.Writer{Models: styleFactory{model}, Router: router},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The creator chose a vibe before uploading anything.
	if _, err := e.api.SaveCreatorStyleSettings(ctx, w.ID, json.RawMessage(`{"vibe":"hype"}`), ids.UserID{}); err != nil {
		t.Fatal(err)
	}
	e.startWorker(t, jobs.WorkerOptions{Style: learner})

	ref := e.styleReference(t, w, clip)
	// The reference is measured from its 720 px proxy (what the clip analysis reads too), not the original of up to 1 GiB: the proxy is
	// the clip, the original here is not a video at all. The frame rate is the original's.
	data, err := os.ReadFile(clip) // #nosec G304 -- a file this test made
	if err != nil {
		t.Fatal(err)
	}
	proxyKey, err := storage.DerivativeKey(w.ID, ref.ID, "proxy_720.mp4")
	if err != nil {
		t.Fatal(err)
	}
	s3c := storagetest.S3(e.cfg)
	for key, body := range map[string][]byte{proxyKey: data, ref.R2Key: bytes.Repeat([]byte{0}, int(ref.SizeBytes))} {
		if _, err := s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(e.cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(body)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.api.ClaimAssetForIngest(ctx, w.ID, ref.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.FinishAssetIngest(ctx, w.ID, ref.ID, store.IngestResult{DurationMs: 6000, Width: 270, Height: 480, FPS: 60,
		Derivatives: []store.NewDerivative{{Kind: store.DerivativeProxy720, Key: proxyKey, ContentType: "video/mp4", SizeBytes: int64(len(data))}}}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"quick_look", "transcribe", "analyze"} {
		if n := e.countJobs(t, kind, ref.ID.String()); n != 0 {
			t.Errorf("a style reference got %d %s jobs", n, kind)
		}
	}
	if n := e.countJobs(t, "style_reference", ref.ID.String()); n != 1 {
		t.Fatalf("%d style_reference jobs", n)
	}
	eventually(t, "the reference to be measured", func() bool {
		r, err := e.api.GetStyleReferenceByAsset(ctx, w.ID, ref.ID)
		return err == nil && r.Status == store.StyleRefReady
	})
	row, _ := e.api.GetStyleReferenceByAsset(ctx, w.ID, ref.ID)
	var m styleprofile.Measures
	if err := json.Unmarshal(row.Measures, &m); err != nil || m.Name != "my-video.mp4" || m.Text == nil || m.Text.Text != "BACK DAY 101" || len(m.CutsS) != 1 || m.FPS != 60 {
		t.Fatalf("measures %+v %v", m, err)
	}
	eventually(t, "the profile to be made", func() bool {
		p, err := e.api.GetCreatorStyle(ctx, w.ID)
		return err == nil && len(p.Summary) > 0
	})
	st, err := styleprofile.LoadStyle(ctx, e.api, w.ID)
	if err != nil || st.Profile == nil || st.Profile.References != 1 || st.Summary == nil || !strings.HasPrefix(st.Summary.Text, "Long takes") {
		t.Fatalf("style %+v %v", st, err)
	}
	if st.Mine.Vibe != styleprofile.VibeHype || st.Resolved.Vibe != styleprofile.VibeHype || st.Resolved.Transitions != styleprofile.LevelLots {
		t.Errorf("learning the style changed the creator's choice: %+v / %+v", st.Mine, st.Resolved.Settings)
	}
	if got := model.count(styleprofile.SummaryToolName); got != 1 {
		t.Errorf("%d summary calls, want 1", got)
	}

	// Removing the reference: the asset is deleted, and the profile job removes the profile it was made from.
	svc, err := creatorstyle.NewService(e.api, nil, e.enq, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if fail := svc.Remove(ctx, creatorstyle.Actor{Workspace: w.ID}, ref.ID); fail != nil {
		t.Fatal(fail)
	}
	if _, err := e.api.GetAsset(ctx, w.ID, ref.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the reference asset is still there: %v", err)
	}
	eventually(t, "the profile to be removed", func() bool {
		_, err := e.api.GetCreatorStyle(ctx, w.ID)
		return errors.Is(err, store.ErrNotFound)
	})
	if s, err := e.api.GetCreatorStyleSettings(ctx, w.ID); err != nil || !strings.Contains(string(s.Settings), "hype") {
		t.Errorf("the settings went with the profile: %s %v", s.Settings, err)
	}
}

// A worker that cannot learn styles leaves the job waiting without using an attempt.
func TestAStyleJobWaitsForAWorkerThatCanLearn(t *testing.T) {
	e := newAssetsEnv(t)
	ctx := context.Background()
	w := e.newWorkspace(t, "org_style_wait")
	e.startWorker(t, jobs.WorkerOptions{})
	a := e.newUploadedAsset(t, w, store.AssetKindVideo, "ref.mp4", []byte("data"))
	if _, err := e.super.Exec(ctx, `UPDATE assets SET purpose = 'style_reference' WHERE id = $1`, a.ID.UUID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.ClaimAssetForIngest(ctx, w.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.FinishAssetIngest(ctx, w.ID, a.ID, store.IngestResult{DurationMs: 5000, Width: 1080, Height: 1920}); err != nil {
		t.Fatal(err)
	}
	if r, err := e.api.GetStyleReferenceByAsset(ctx, w.ID, a.ID); err != nil || r.Status != store.StyleRefQueued {
		t.Fatalf("the reference row: %+v %v", r, err)
	}
	eventually(t, "the style job to be snoozed", func() bool { s, _ := e.jobState(t, "style_reference", a.ID.String()); return s == "scheduled" })
}

// A reference's measuring is one job while it waits or runs (a second insert is the same work and the same model call), and a finished
// one does not block the next pass of "learn again".
func TestAStyleReferenceJobIsUniqueWhileItWaits(t *testing.T) {
	e := newAssetsEnv(t)
	ctx := context.Background()
	w := e.newWorkspace(t, "org_style_unique")
	a := e.newUploadedAsset(t, w, store.AssetKindVideo, "ref.mp4", []byte("data"))
	args := jobs.StyleReferenceArgs{WorkspaceID: w.ID, AssetID: a.ID}
	insert := func() {
		t.Helper()
		if err := e.api.InTx(ctx, func(tx *store.Store) error { _, err := e.enq.InsertInStore(ctx, tx, args, nil); return err }); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		insert()
	}
	if n := e.countJobs(t, "style_reference", a.ID.String()); n != 1 {
		t.Fatalf("%d jobs for one reference waiting", n)
	}
	if _, err := e.super.Exec(ctx, `UPDATE river_job SET state = 'completed', finalized_at = now() WHERE kind = 'style_reference'`); err != nil {
		t.Fatal(err)
	}
	insert()
	if n := e.countJobs(t, "style_reference", a.ID.String()); n != 2 {
		t.Fatalf("%d jobs after the first finished", n)
	}
}
