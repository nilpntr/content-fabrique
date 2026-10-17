package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeStore struct {
	mu      sync.Mutex
	asset   store.Asset
	derivs  []store.Derivative
	signals *store.AssetSignals
	saved   []store.AnalysisResult
	saveErr error
	missing bool
}

func (f *fakeStore) GetAsset(_ context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error) {
	if f.missing || ws != f.asset.WorkspaceID || id != f.asset.ID {
		return store.Asset{}, store.ErrNotFound
	}
	return f.asset, nil
}

func (f *fakeStore) ListAssetDerivatives(_ context.Context, ws ids.WorkspaceID, _ []ids.AssetID) ([]store.Derivative, error) {
	if ws != f.asset.WorkspaceID {
		return nil, nil
	}
	return f.derivs, nil
}

func (f *fakeStore) SaveAnalysis(_ context.Context, _ ids.WorkspaceID, _ ids.AssetID, r store.AnalysisResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, r)
	f.signals = &store.AssetSignals{Version: int32(r.Version)}
	return nil
}

func (f *fakeStore) GetAssetSignals(context.Context, ids.WorkspaceID, ids.AssetID) (store.AssetSignals, error) {
	if f.signals == nil {
		return store.AssetSignals{}, store.ErrNotFound
	}
	return *f.signals, nil
}

type memObjects map[string][]byte

func (m memObjects) Get(_ context.Context, key string) (storage.Object, error) {
	b, ok := m[key]
	if !ok {
		return storage.Object{}, storage.ErrNotFound
	}
	return storage.Object{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b)), ContentType: "video/mp4"}, nil
}

// fakeRunner stands in for the sandbox: it records the run and writes the three output files itself.
type fakeRunner struct {
	runs   int
	exit   int
	files  map[string][]byte
	runErr error
}

func (r *fakeRunner) Run(_ context.Context, inv media.Invocation) (sandbox.Result, error) {
	r.runs++
	if r.runErr != nil {
		return sandbox.Result{}, r.runErr
	}
	for name, b := range r.files {
		if err := os.WriteFile(filepath.Join(inv.OutputDir, name), b, 0o600); err != nil {
			return sandbox.Result{}, err
		}
	}
	return sandbox.Result{ExitCode: r.exit, Stderr: []byte("boom")}, nil
}

func fixture(t *testing.T) (*Service, *fakeStore, *fakeRunner) {
	t.Helper()
	ws := ids.WorkspaceID{UUID: uuid.New()}
	id := ids.AssetID{UUID: uuid.New()}
	dur := int64(30_000)
	a := store.Asset{
		ID: id, WorkspaceID: ws, Kind: store.AssetKindVideo, Status: store.AssetReady, Filename: "lat-pulldown.mov", Title: "Lat pulldown",
		Tags: []string{"lat pulldown", "back"}, DurationMs: &dur,
	}
	key := storage.AssetPrefix(ws, id) + "proxy_720.mp4"
	st := &fakeStore{asset: a, derivs: []store.Derivative{{WorkspaceID: ws, AssetID: id, Kind: store.DerivativeProxy720, R2Key: key}}}

	s := synth{seconds: 40, pos: sine(3), noise: 3, seed: 21}
	raw := s.raw()
	var sharp, scene strings.Builder
	for i := range raw.Sharp {
		sharp.WriteString("frame:" + itoa(i) + "    pts:" + itoa(i) + "       pts_time:0\nlavfi.signalstats.YAVG=5\n")
		scene.WriteString("frame:" + itoa(i) + "    pts:" + itoa(i) + "       pts_time:0\nlavfi.scd.score=0.000\n")
	}
	run := &fakeRunner{files: map[string][]byte{media.AnalysisThumbs: raw.Thumbs, media.AnalysisSharp: []byte(sharp.String()), media.AnalysisScene: []byte(scene.String())}}
	svc, err := New(Config{Store: st, Objects: memObjects{key: []byte("proxy bytes")}, Runner: run, TempDir: t.TempDir(), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, run
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestRunStoresTheAnalysis(t *testing.T) {
	svc, st, run := fixture(t)
	res, err := svc.Run(context.Background(), st.asset.WorkspaceID, st.asset.ID, Options{})
	if err != nil || res.Outcome != OutcomeDone {
		t.Fatalf("%+v %v", res, err)
	}
	if run.runs != 1 || len(st.saved) != 1 {
		t.Fatalf("runs %d saved %d", run.runs, len(st.saved))
	}
	saved := st.saved[0]
	if saved.Version != Version || saved.StepMs != 100 || saved.DurationMs != 40000 || len(saved.Shots) != 1 {
		t.Fatalf("%+v", saved)
	}
	var reps int
	for _, m := range saved.Moments {
		if m.Kind == KindRep {
			reps++
		}
		// the quick look's tags label every moment
		if len(m.Labels) != 2 || m.Labels[0] != "lat pulldown" {
			t.Fatalf("labels %v", m.Labels)
		}
		if m.Score < 0 || m.Score > 1 || m.EndMs <= m.StartMs {
			t.Fatalf("moment %+v", m)
		}
		if !json.Valid(m.Attrs) {
			t.Fatalf("attrs %s", m.Attrs)
		}
	}
	if reps < 10 {
		t.Errorf("%d reps", reps)
	}
	var sum Summary
	if err := json.Unmarshal(saved.Summary, &sum); err != nil || sum.Reps != reps {
		t.Fatalf("summary %s: %v", saved.Summary, err)
	}
	var series map[string]any
	if err := json.Unmarshal(saved.Series, &series); err != nil {
		t.Fatalf("series: %v", err)
	}
	if motion, _ := series["motion"].([]any); len(motion) != 400 {
		t.Fatalf("%d motion samples, want 400", len(motion))
	}
	if len(saved.Series) > 40_000 {
		t.Errorf("the series of 40 s is %d bytes of JSON", len(saved.Series))
	}
	// a second run is skipped unless forced
	if res, err := svc.Run(context.Background(), st.asset.WorkspaceID, st.asset.ID, Options{}); err != nil || res.Outcome != OutcomeSkipped || run.runs != 1 {
		t.Fatalf("second run: %+v %v runs %d", res, err, run.runs)
	}
	if res, err := svc.Run(context.Background(), st.asset.WorkspaceID, st.asset.ID, Options{Force: true}); err != nil || res.Outcome != OutcomeDone || run.runs != 2 {
		t.Fatalf("forced run: %+v %v", res, err)
	}
}

func TestRunOutcomes(t *testing.T) {
	ctx := context.Background()
	t.Run("another workspace", func(t *testing.T) {
		svc, st, run := fixture(t)
		other := ids.WorkspaceID{UUID: uuid.New()}
		if res, err := svc.Run(ctx, other, st.asset.ID, Options{}); err != nil || res.Outcome != OutcomeGone || run.runs != 0 {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("not a video", func(t *testing.T) {
		svc, st, _ := fixture(t)
		st.asset.Kind = store.AssetKindAudio
		if res, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{}); err != nil || res.Outcome != OutcomeGone {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("failed asset", func(t *testing.T) {
		svc, st, _ := fixture(t)
		st.asset.Status = store.AssetFailed
		if res, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{}); err != nil || res.Outcome != OutcomeGone {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("being ingested again", func(t *testing.T) {
		svc, st, _ := fixture(t)
		st.asset.Status = store.AssetProcessing
		if _, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{}); !errors.Is(err, ErrNotReady) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("no proxy", func(t *testing.T) {
		svc, st, run := fixture(t)
		st.derivs = nil
		if res, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{}); err != nil || res.Outcome != OutcomeNoProxy || run.runs != 0 {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("a proxy key that is not this asset's", func(t *testing.T) {
		svc, st, run := fixture(t)
		st.derivs[0].R2Key = storage.AssetPrefix(st.asset.WorkspaceID, ids.AssetID{UUID: uuid.New()}) + "proxy_720.mp4"
		if _, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{}); err == nil || run.runs != 0 {
			t.Fatalf("want an error and no run, got %v (runs %d)", err, run.runs)
		}
	})
	t.Run("deleted while it ran", func(t *testing.T) {
		svc, st, _ := fixture(t)
		st.saveErr = store.ErrNotFound
		if res, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{}); err != nil || res.Outcome != OutcomeGone {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("ffmpeg fails", func(t *testing.T) {
		svc, st, run := fixture(t)
		run.exit = 1
		_, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{})
		var te *media.ToolError
		if !errors.As(err, &te) || te.ExitCode != 1 || len(st.saved) != 0 {
			t.Fatalf("%v", err)
		}
	})
	t.Run("the sandbox fails", func(t *testing.T) {
		svc, st, run := fixture(t)
		run.runErr = errors.New("docker is down")
		if _, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{}); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("the sandbox wrote nonsense", func(t *testing.T) {
		svc, st, run := fixture(t)
		run.files[media.AnalysisThumbs] = []byte("not a multiple of 1024 bytes")
		if _, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{}); err == nil || len(st.saved) != 0 {
			t.Fatalf("want an error, got %v", err)
		}
		run.files = map[string][]byte{} // nothing written at all
		if _, err := svc.Run(ctx, st.asset.WorkspaceID, st.asset.ID, Options{}); err == nil {
			t.Fatal("want an error for missing output")
		}
	})
}

func TestReadOutputRefusesNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks here")
	}
	if _, err := readOutput(link, 100); err == nil {
		t.Error("a symlink the sandbox wrote must not be followed")
	}
	if _, err := readOutput(target, 3); err == nil {
		t.Error("a file over the limit must be refused")
	}
	if b, err := readOutput(target, 100); err != nil || string(b) != "secret" {
		t.Errorf("%q %v", b, err)
	}
}

func TestTimeoutFollowsTheClip(t *testing.T) {
	if timeoutFor(0) != 2*time.Minute || timeoutFor(30*60*1000) != 17*time.Minute {
		t.Errorf("%s %s", timeoutFor(0), timeoutFor(30*60*1000))
	}
}
