package editor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// fakeRunner plays ffmpeg: it reads the -ss time before every -i and writes a JPEG at each output path the command names,
// into the invocation's output directory. mutate can corrupt what it writes.
type fakeRunner struct {
	mu     sync.Mutex
	runs   []media.Invocation
	exit   int
	err    error
	mutate func(name string, data []byte) []byte
	// inputBytes records what the tool saw at /in/0.
	inputBytes []byte
}

func (r *fakeRunner) Run(_ context.Context, inv media.Invocation) (sandbox.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, inv)
	if r.err != nil {
		return sandbox.Result{}, r.err
	}
	if r.exit != 0 {
		return sandbox.Result{ExitCode: r.exit, Stderr: []byte("Invalid data found when processing input")}, nil
	}
	if len(inv.Inputs) == 1 {
		r.inputBytes, _ = os.ReadFile(inv.Inputs[0].Host)
	}
	var times []string
	for i, a := range inv.Args {
		if a == "-ss" {
			times = append(times, inv.Args[i+1])
		}
	}
	n := 0
	for i, a := range inv.Args {
		if a == "-q:v" && n < len(times) {
			name := strings.TrimPrefix(inv.Args[i+2], sandbox.WorkDir+"/")
			data := testJPEG("t"+times[n], 216, 384)
			if r.mutate != nil {
				data = r.mutate(name, data)
			}
			if err := os.WriteFile(filepath.Join(inv.OutputDir, name), data, 0o644); err != nil {
				return sandbox.Result{}, err
			}
			n++
		}
	}
	return sandbox.Result{}, nil
}

func (r *fakeRunner) runCount() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.runs) }

func newFrameService(e *env, r media.Runner) *FrameService {
	return &FrameService{Store: e.st, Objects: e.objs, Runner: r, TempDir: e.tmp(), Logger: quiet}
}

func (e *env) tmp() string {
	e.t.Helper()
	dir, err := os.MkdirTemp("", "editor-frames-")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestFrameServiceMakesAndCachesFrames(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{durMs: 10000})
	runner := &fakeRunner{}
	fs := newFrameService(e, runner)
	ctx := context.Background()

	frames, err := fs.Frames(ctx, e.wsA.ID, a, []int64{0, 2500, 9000})
	if err != nil {
		t.Fatal(err)
	}
	if runner.runCount() != 1 {
		t.Fatalf("%d runs of ffmpeg, want 1 for the three frames", runner.runCount())
	}
	inv := runner.runs[0]
	args := strings.Join(inv.Args, " ")
	for _, want := range []string{"-ss 0.000 -i /in/0", "-ss 2.500 -i /in/0", "-ss 9.000 -i /in/0", "force_original_aspect_ratio=decrease", "scale=384:384", "/work/f0.jpg", "/work/f2.jpg"} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpeg arguments lack %q: %s", want, args)
		}
	}
	if inv.Timeout <= 0 || len(inv.Inputs) != 1 || inv.Inputs[0].Container != "/in/0" || inv.OutputDir == "" {
		t.Errorf("invocation: %+v", inv)
	}
	// The tool read the asset's proxy.
	if !strings.Contains(string(runner.inputBytes), "not really a video") {
		t.Errorf("the tool saw %q", runner.inputBytes)
	}
	for i, at := range []int64{0, 2500, 9000} {
		want := fmt.Sprintf("ws/%s/assets/%s/frames/%08d_384.jpg", e.wsA.ID, a, at)
		if frames[i].AtMs != at || frames[i].Ref != want {
			t.Errorf("frame %d = %+v, want %s", i, frames[i], want)
		}
		data, ok := e.objs.m[want]
		if !ok {
			t.Fatalf("%s was not stored", want)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil || max(cfg.Width, cfg.Height) > FrameLongSide {
			t.Errorf("stored frame: %+v %v", cfg, err)
		}
	}
	if e.objs.putCount() != 3 {
		t.Fatalf("%d objects stored", e.objs.putCount())
	}

	// A second look at the same moments costs nothing: the frames are cached.
	if _, err := fs.Frames(ctx, e.wsA.ID, a, []int64{0, 2500, 9000}); err != nil {
		t.Fatal(err)
	}
	if runner.runCount() != 1 || e.objs.putCount() != 3 {
		t.Fatalf("a cached look ran ffmpeg again (%d runs, %d puts)", runner.runCount(), e.objs.putCount())
	}
	// A mix: only the new frame is cut.
	if _, err := fs.Frames(ctx, e.wsA.ID, a, []int64{2500, 4000}); err != nil {
		t.Fatal(err)
	}
	if runner.runCount() != 2 || e.objs.putCount() != 4 || !strings.Contains(strings.Join(runner.runs[1].Args, " "), "-ss 4.000") || strings.Contains(strings.Join(runner.runs[1].Args, " "), "-ss 2.500") {
		t.Fatalf("a mixed look: %d runs, %d puts, %v", runner.runCount(), e.objs.putCount(), runner.runs[1].Args)
	}
	// The working directory is cleaned up.
	if entries, _ := os.ReadDir(fs.TempDir); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}

func TestFrameServiceRefusals(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{})
	noProxy := e.asset(e.wsA, clip{noProxy: true})
	audio := e.asset(e.wsA, clip{kind: store.AssetKindAudio})
	foreign := e.asset(e.wsB, clip{})
	runner := &fakeRunner{}
	fs := newFrameService(e, runner)
	ctx := context.Background()

	if _, err := fs.Frames(ctx, e.wsA.ID, noProxy, []int64{0}); !errors.Is(err, ErrNoVideo) {
		t.Errorf("no proxy: %v", err)
	}
	if _, err := fs.Frames(ctx, e.wsA.ID, audio, []int64{0}); !errors.Is(err, ErrNoVideo) {
		t.Errorf("audio: %v", err)
	}
	// The asset of another workspace has no derivative in this one, whatever the caller believes.
	if _, err := fs.Frames(ctx, e.wsA.ID, foreign, []int64{0}); !errors.Is(err, ErrNoVideo) {
		t.Errorf("foreign asset: %v", err)
	}
	if runner.runCount() != 0 {
		t.Errorf("ffmpeg ran %d times for assets that cannot be viewed", runner.runCount())
	}
	for _, bad := range [][]int64{nil, {}, make([]int64, media.MaxFrameBatch+1), {-1}} {
		if _, err := fs.Frames(ctx, e.wsA.ID, a, bad); err == nil {
			t.Errorf("times %v accepted", bad)
		}
	}
	// A derivative row whose key is not a key of the asset is not followed.
	if _, err := e.pool.Exec(ctx, `UPDATE asset_derivatives SET r2_key = $1 WHERE asset_id = $2`, "ws/"+e.wsB.ID.String()+"/assets/"+foreign.String()+"/elsewhere.mp4", a); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Frames(ctx, e.wsA.ID, a, []int64{0}); err == nil || !strings.Contains(err.Error(), "not a derivative key") {
		t.Errorf("a proxy key that points elsewhere: %v", err)
	}
}

// The sandbox's output is untrusted: only a small regular JPEG is kept.
func TestFrameServiceDistrustsToolOutput(t *testing.T) {
	cases := map[string]func(name string, data []byte) []byte{
		"not a jpeg": func(string, []byte) []byte { return append([]byte("GIF89a"), make([]byte, 400)...) },
		"empty":      func(string, []byte) []byte { return nil },
		"huge":       func(_ string, d []byte) []byte { return append(d, make([]byte, maxFrameFileBytes)...) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			a := e.asset(e.wsA, clip{})
			fs := newFrameService(e, &fakeRunner{mutate: mutate})
			if _, err := fs.Frames(context.Background(), e.wsA.ID, a, []int64{0}); err == nil {
				t.Fatal("accepted")
			}
			if e.objs.putCount() != 0 {
				t.Fatal("a bad frame was stored")
			}
		})
	}
	t.Run("a symlink", func(t *testing.T) {
		e := newEnv(t)
		a := e.asset(e.wsA, clip{})
		secret := filepath.Join(e.tmp(), "secret.jpg")
		if err := os.WriteFile(secret, testJPEG("secret", 216, 384), 0o600); err != nil {
			t.Fatal(err)
		}
		r := &linkRunner{target: secret}
		fs := newFrameService(e, r)
		if _, err := fs.Frames(context.Background(), e.wsA.ID, a, []int64{0}); err == nil {
			t.Fatal("a symlink in the output was followed")
		}
		if e.objs.putCount() != 0 {
			t.Fatal("a symlinked file was stored")
		}
	})
	t.Run("ffmpeg fails", func(t *testing.T) {
		e := newEnv(t)
		a := e.asset(e.wsA, clip{})
		fs := newFrameService(e, &fakeRunner{exit: 1})
		_, err := fs.Frames(context.Background(), e.wsA.ID, a, []int64{0})
		var te *media.ToolError
		if !errors.As(err, &te) || te.ExitCode != 1 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the sandbox fails", func(t *testing.T) {
		e := newEnv(t)
		a := e.asset(e.wsA, clip{})
		fs := newFrameService(e, &fakeRunner{err: sandbox.ErrSandbox})
		if _, err := fs.Frames(context.Background(), e.wsA.ID, a, []int64{0}); !errors.Is(err, sandbox.ErrSandbox) {
			t.Fatalf("err = %v", err)
		}
	})
}

// linkRunner writes a symlink to a real JPEG where the frame should be.
type linkRunner struct{ target string }

func (r *linkRunner) Run(_ context.Context, inv media.Invocation) (sandbox.Result, error) {
	return sandbox.Result{}, os.Symlink(r.target, filepath.Join(inv.OutputDir, "f0.jpg"))
}

// ---- the image resolver ------------------------------------------------------------------------------------------------

func TestImageResolver(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{})
	key, err := storage.FrameKey(e.wsA.ID, a, 1500, 384)
	if err != nil {
		t.Fatal(err)
	}
	data := testJPEG("x", 216, 384)
	e.objs.set(key, data)
	r := ImageResolver{Objects: e.objs}
	ctxA := llm.WithWorkspace(context.Background(), e.wsA.ID)

	mt, got, err := r.ResolveImage(ctxA, key)
	if err != nil || mt != "image/jpeg" || !bytes.Equal(got, data) {
		t.Fatalf("resolve: %s %d bytes %v", mt, len(got), err)
	}

	original := storage.OriginalKey(e.wsA.ID, a)
	e.objs.set(original, data) // even a JPEG: the original is not a frame
	foreignKey := "ws/" + e.wsB.ID.String() + "/assets/x/frames/00000000_384.jpg"
	e.objs.set(foreignKey, data)
	big := "ws/" + e.wsA.ID.String() + "/previews/big.jpg"
	e.objs.set(big, append(append([]byte{}, jpegMagic...), make([]byte, MaxImageBytes)...))
	notJPEG := "ws/" + e.wsA.ID.String() + "/previews/not.jpg"
	e.objs.set(notJPEG, []byte("MZ this is an executable"))
	wsPrefix := "ws/" + e.wsA.ID.String() + "/"
	for name, ref := range map[string]string{
		"another workspace":       foreignKey,
		"the original":            original,
		"a dot-dot path":          wsPrefix + "assets/../../" + e.wsB.ID.String() + "/x.jpg",
		"a double slash":          wsPrefix + "assets//x.jpg",
		"a backslash":             wsPrefix + `assets\x.jpg`,
		"no workspace prefix":     "assets/x.jpg",
		"a video":                 wsPrefix + "assets/x/proxy_720.mp4",
		"a bucket-level key":      "healthcheck/x.jpg",
		"a workspace id prefix":   "ws/" + e.wsA.ID.String() + "x/frames/0.jpg",
		"empty":                   "",
		"a very long name":        wsPrefix + strings.Repeat("a", 400) + ".jpg",
		"too big":                 big,
		"bytes that are no jpeg":  notJPEG,
		"a key that is not there": wsPrefix + "previews/missing.jpg",
	} {
		if _, _, err := r.ResolveImage(ctxA, ref); err == nil {
			t.Errorf("%s: %q was resolved", name, ref)
		}
	}
	// No workspace in the context, or a zero one: nothing is read.
	if _, _, err := r.ResolveImage(context.Background(), key); err == nil {
		t.Error("resolved without a workspace in the context")
	}
	if _, _, err := r.ResolveImage(llm.WithWorkspace(context.Background(), ids.WorkspaceID{}), key); err == nil {
		t.Error("resolved for the zero workspace")
	}
	// The other workspace's own context reads the other workspace's own key.
	if _, _, err := r.ResolveImage(llm.WithWorkspace(context.Background(), e.wsB.ID), foreignKey); err != nil {
		t.Errorf("a workspace's own key: %v", err)
	}
	if _, _, err := r.ResolveImage(llm.WithWorkspace(context.Background(), e.wsB.ID), key); err == nil {
		t.Error("workspace B read a key of workspace A")
	}
	if mt, err := CheckImageRef(e.wsA.ID, wsPrefix+"p/sheet.PNG"); err != nil || mt != "image/png" {
		t.Errorf("png: %s %v", mt, err)
	}
}
