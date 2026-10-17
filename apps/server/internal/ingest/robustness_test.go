package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// spyRunner wraps the harness's runner: it records every invocation and can answer chosen tools with a canned result or error.
type spyRunner struct {
	inner media.Runner
	// fake returns a result for an invocation that is not to run; ok false runs it for real.
	fake func(inv media.Invocation) (res sandbox.Result, err error, ok bool)

	mu    sync.Mutex
	calls []media.Invocation
}

func (s *spyRunner) Run(ctx context.Context, inv media.Invocation) (sandbox.Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, inv)
	s.mu.Unlock()
	if s.fake != nil {
		if res, err, ok := s.fake(inv); ok {
			return res, err
		}
	}
	return s.inner.Run(ctx, inv)
}

// invocations returns the recorded calls of one program whose arguments contain all of the given words.
func (s *spyRunner) invocations(prog string, words ...string) []media.Invocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []media.Invocation
	for _, c := range s.calls {
		if c.Args[0] != prog {
			continue
		}
		j := strings.Join(c.Args, " ")
		match := true
		for _, w := range words {
			match = match && strings.Contains(j, w)
		}
		if match {
			out = append(out, c)
		}
	}
	return out
}

func (h *harness) spy(fake func(inv media.Invocation) (sandbox.Result, error, bool)) *spyRunner {
	sr := &spyRunner{inner: h.p.cfg.Runner, fake: fake}
	h.p.cfg.Runner = sr
	return sr
}

// The tool timeouts follow the clip's length, the job timeout covers the longest accepted clip, and River's rescue comes
// after the job timeout.
func TestTimeoutsAreConsistent(t *testing.T) {
	if got := HeavyTimeout(0); got != 2*time.Minute {
		t.Errorf("HeavyTimeout(0) = %v", got)
	}
	if got := HeavyTimeout(87 * time.Second); got != 3*87*time.Second+2*time.Minute {
		t.Errorf("HeavyTimeout(87s) = %v", got)
	}
	if got := HeavyTimeout(100 * time.Hour); got != 2*time.Hour {
		t.Errorf("HeavyTimeout is capped: %v", got)
	}
	if got := LightTimeout(time.Minute); got != ProbeTimeout {
		t.Errorf("LightTimeout(1m) = %v", got)
	}
	if got := LightTimeout(30 * time.Minute); got != 10*time.Minute {
		t.Errorf("LightTimeout(30m) = %v", got)
	}
	if got := LightTimeout(100 * time.Hour); got != time.Hour {
		t.Errorf("LightTimeout is capped: %v", got)
	}
	if w := WorstCase(DefaultMaxDuration); w > JobTimeout {
		t.Fatalf("the worst case of the longest accepted clip, %v, does not fit in the job timeout %v", w, JobTimeout)
	}
	if WorstCase(DefaultMaxDuration+time.Hour) <= JobTimeout {
		t.Error("the check would not notice a duration limit that is too generous")
	}
	h := newHarness(t)
	_, err := New(Config{Store: h.store, Objects: h.objects, Runner: h.p.cfg.Runner, TempDir: h.p.cfg.TempDir, MaxDuration: 4 * time.Hour})
	if err == nil || !strings.Contains(err.Error(), "job timeout") {
		t.Fatalf("a 4 hour limit must be refused: %v", err)
	}
}

func TestToolTimeoutsScaleWithTheClip(t *testing.T) {
	h := newHarness(t)
	sr := h.spy(nil)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "timeouts", w: 640, h: 360, seconds: 6, audio: true}))
	h.run(a)
	dur := 6 * time.Second
	for _, c := range []struct {
		name  string
		calls []media.Invocation
		want  time.Duration
	}{
		{"decode check", sr.invocations("ffmpeg", "-xerror"), ProbeTimeout},
		{"proxy", sr.invocations("ffmpeg", "libx264", "-crf 23"), HeavyTimeout(dur + 40*time.Millisecond)},
		{"poster", sr.invocations("ffmpeg", "thumbnail"), LightTimeout(dur)},
		{"sprite", sr.invocations("ffmpeg", "tile="), LightTimeout(dur)},
		{"audio", sr.invocations("ffmpeg", "pcm_s16le"), LightTimeout(dur)},
	} {
		if len(c.calls) != 1 {
			t.Errorf("%s: %d runs", c.name, len(c.calls))
			continue
		}
		if got := c.calls[0].Timeout; got < c.want-time.Second || got > c.want+time.Second {
			t.Errorf("%s: timeout %v, want about %v", c.name, got, c.want)
		}
	}
}

// A tool that is killed (the out-of-memory killer), times out, or cannot be run says nothing about the file: the asset is
// not failed, the run returns an error for River to retry, and the proxy is not made again without audio.
func TestInfrastructureFailuresAreRetriedNotReportedAsBadFiles(t *testing.T) {
	for _, c := range []struct {
		name string
		fake func(inv media.Invocation) (sandbox.Result, error, bool)
		tool string
	}{
		{"the proxy is OOM killed", func(inv media.Invocation) (sandbox.Result, error, bool) {
			return sandbox.Result{ExitCode: 137, Stderr: []byte("Killed")}, nil, strings.Contains(strings.Join(inv.Args, " "), "libx264 -preset veryfast")
		}, "proxy"},
		{"the proxy is terminated", func(inv media.Invocation) (sandbox.Result, error, bool) {
			return sandbox.Result{ExitCode: 143}, nil, strings.Contains(strings.Join(inv.Args, " "), "libx264 -preset veryfast")
		}, "proxy"},
		{"the decode check is killed", func(inv media.Invocation) (sandbox.Result, error, bool) {
			return sandbox.Result{ExitCode: 137}, nil, strings.Contains(strings.Join(inv.Args, " "), "-xerror")
		}, "decode"},
		{"ffprobe is killed", func(inv media.Invocation) (sandbox.Result, error, bool) {
			return sandbox.Result{ExitCode: 137}, nil, inv.Args[0] == "ffprobe"
		}, "probe"},
		{"the proxy times out", func(inv media.Invocation) (sandbox.Result, error, bool) {
			return sandbox.Result{ExitCode: -1, TimedOut: true}, sandbox.ErrTimeout, strings.Contains(strings.Join(inv.Args, " "), "libx264 -preset veryfast")
		}, "proxy"},
		{"the sandbox cannot run", func(_ media.Invocation) (sandbox.Result, error, bool) {
			return sandbox.Result{}, sandbox.ErrSandbox, true
		}, "any"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			sr := h.spy(c.fake)
			a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "infra", w: 320, h: 240, seconds: 2, audio: true}))
			res, err := h.p.Run(context.Background(), a.WorkspaceID, a.ID)
			if err == nil || errors.Is(err, ErrAssetGone) || res.Status == store.AssetFailed {
				t.Fatalf("got %+v, %v; want an error for the retry", res, err)
			}
			if got := h.store.get(a.WorkspaceID, a.ID); got.Status != store.AssetProcessing || got.FailureReason != nil {
				t.Fatalf("the asset must stay processing for the retry: %+v", got)
			}
			if c.tool == "proxy" {
				if n := len(sr.invocations("ffmpeg", "libx264 -preset veryfast")); n != 1 {
					t.Errorf("the proxy was tried %d times: an OOM kill must not trigger the retry without audio", n)
				}
			}
			if len(h.objects.puts) != 0 {
				t.Error("something was uploaded")
			}
		})
	}
}

// ffmpeg's own error exit codes are the file's fault, as before: a decode check that fails with ffmpeg's invalid-data code
// fails the asset.
func TestRealDecodeErrorsAreStillBadFiles(t *testing.T) {
	h := newHarness(t)
	h.spy(func(inv media.Invocation) (sandbox.Result, error, bool) {
		return sandbox.Result{ExitCode: 183, Stderr: []byte("Invalid data found when processing input")}, nil, strings.Contains(strings.Join(inv.Args, " "), "-xerror")
	})
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "baddecode", w: 320, h: 240, seconds: 2}))
	if res := h.run(a); res.Status != store.AssetFailed || res.Reason != store.FailureUndecodableVideo {
		t.Fatalf("%+v", res)
	}
}

func TestKilledExit(t *testing.T) {
	for code, want := range map[int]bool{0: false, 1: false, 128: false, 129: true, 137: true, 139: true, 143: true, 144: false, 183: false, 187: false, 234: false, 255: false} {
		if killedExit(code) != want {
			t.Errorf("killedExit(%d) = %v", code, !want)
		}
	}
}

// Deleted while the derivatives go up: the progress call after an upload finds the asset gone, and what this run already
// uploaded is removed.
func TestAssetGoneDuringUploadRemovesWhatWasUploaded(t *testing.T) {
	h := newHarness(t)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "midupload", w: 320, h: 240, seconds: 2, audio: true}))
	h.store.onProgress = func(progress int, stage string) {
		if stage == store.StageUploading && progress > pctUpload+3 { // after the second file
			now := time.Now()
			h.store.assets[fkey(a.WorkspaceID, a.ID)].DeletedAt = &now
		}
	}
	if _, err := h.p.Run(context.Background(), a.WorkspaceID, a.ID); !errors.Is(err, ErrAssetGone) {
		t.Fatalf("got %v", err)
	}
	if len(h.objects.puts) < 2 || len(h.objects.puts) >= 7 {
		t.Fatalf("puts %d: the test meant to stop part way", len(h.objects.puts))
	}
	if len(h.objects.objects) != 1 || h.objects.objects[a.R2Key] == nil {
		t.Fatalf("objects left: %d", len(h.objects.objects))
	}
}

// A re-run that no longer makes a kind (the new original has no audio) removes that kind's rows and objects.
func TestRerunRemovesDerivativesItNoLongerMakes(t *testing.T) {
	h := newHarness(t)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "withaudio", w: 320, h: 240, seconds: 2, audio: true}))
	h.run(a)
	if k := strings.Join(h.kinds(a), ","); !strings.Contains(k, "audio_16k") || !strings.Contains(k, "waveform") {
		t.Fatalf("first run: %s", k)
	}
	// The same asset, now with a silent original.
	silent, err := os.ReadFile(h.makeClip(clip{name: "silent", w: 320, h: 240, seconds: 2}))
	if err != nil {
		t.Fatal(err)
	}
	h.objects.objects[a.R2Key] = silent
	h.store.assets[fkey(a.WorkspaceID, a.ID)].SizeBytes = int64(len(silent))
	h.run(a)
	if k := strings.Join(h.kinds(a), ","); k != "hover_preview,poster,proxy_720,sprite,sprite_vtt" {
		t.Fatalf("second run: %s", k)
	}
	if n := len(h.objects.objects); n != 6 { // the original and five derivatives
		t.Fatalf("%d objects left: the audio and waveform objects must be gone", n)
	}
	for key := range h.objects.objects {
		if strings.HasSuffix(key, "audio_16k.wav") || strings.HasSuffix(key, "waveform.json") {
			t.Errorf("stale object %s", key)
		}
	}
}

// The rule for a re-run that fails on the file: the asset becomes failed and loses its derivatives, rows and objects (the
// original stays). A failed asset never has derivatives.
func TestFailedRerunRemovesTheOldDerivatives(t *testing.T) {
	h := newHarness(t)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "rerunfail", w: 320, h: 240, seconds: 2, audio: true}))
	h.run(a)
	if len(h.objects.objects) != 8 {
		t.Fatalf("%d objects after the first run", len(h.objects.objects))
	}
	h.objects.objects[a.R2Key] = []byte("this is no longer a video, not at all, not even a little bit")
	h.store.assets[fkey(a.WorkspaceID, a.ID)].SizeBytes = int64(len(h.objects.objects[a.R2Key]))
	res := h.run(a)
	if res.Status != store.AssetFailed {
		t.Fatalf("%+v", res)
	}
	if got := h.store.get(a.WorkspaceID, a.ID); got.Status != store.AssetFailed || got.FailureReason == nil {
		t.Fatalf("asset %+v", got)
	}
	if len(h.store.derivatives[fkey(a.WorkspaceID, a.ID)]) != 0 {
		t.Fatal("a failed asset kept its derivative rows")
	}
	if len(h.objects.objects) != 1 || h.objects.objects[a.R2Key] == nil {
		t.Fatalf("%d objects left; only the original may stay", len(h.objects.objects))
	}
}

// Fail (the last attempt of a job) clears the derivatives too, and removes only derivative keys of that asset.
func TestFailRemovesDerivativesOfThatAssetOnly(t *testing.T) {
	h := newHarness(t)
	a := h.upload(store.AssetKindVideo, h.makeClip(clip{name: "failme", w: 320, h: 240, seconds: 2}))
	h.run(a)
	other := h.uploadTo(newWorkspaceID(), store.AssetKindVideo, h.makeClip(clip{name: "other", w: 320, h: 240, seconds: 2}))
	h.run(other)
	before := len(h.objects.objects)
	// The asset is processing again (a re-run was claimed), then gives up.
	if _, err := h.store.ClaimAssetForIngest(context.Background(), a.WorkspaceID, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.p.Fail(context.Background(), a.WorkspaceID, a.ID, store.FailureTimeout); err != nil {
		t.Fatal(err)
	}
	if got := h.store.get(a.WorkspaceID, a.ID); got.Status != store.AssetFailed || *got.FailureReason != store.FailureTimeout {
		t.Fatalf("asset %+v", got)
	}
	if after := len(h.objects.objects); after != before-5 { // five derivatives, the original stays; the other asset is untouched
		t.Fatalf("%d objects, was %d", after, before)
	}
	if len(h.store.derivatives[fkey(other.WorkspaceID, other.ID)]) == 0 {
		t.Fatal("the other asset lost its derivatives")
	}
	// A key outside the asset's prefix in the rows is never deleted.
	h.p.deleteDerivatives(context.Background(), a.WorkspaceID, a.ID, []string{other.R2Key, "ws/x/assets/y/poster.jpg", a.R2Key})
	if h.objects.objects[other.R2Key] == nil || h.objects.objects[a.R2Key] == nil {
		t.Fatal("deleteDerivatives removed an original or another asset's object")
	}
}

func TestStaleJobDirectoriesAreSweptAtStart(t *testing.T) {
	h := newHarness(t)
	jobs := h.p.cfg.TempDir
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(jobs, name)
		if err := os.MkdirAll(filepath.Join(p, "in"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "in", "original"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := mk("ingest-stale", JobTimeout+time.Hour)
	recent := mk("ingest-recent", JobTimeout-time.Hour)   // a run on another worker may still be using it
	foreign := mk("somebody-elses", JobTimeout+time.Hour) // not ours to remove
	if _, err := New(Config{Store: h.store, Objects: h.objects, Runner: h.p.cfg.Runner, TempDir: jobs, Logger: quiet}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the stale directory is still there: %v", err)
	}
	for _, p := range []string{recent, foreign} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}
