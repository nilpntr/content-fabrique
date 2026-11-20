package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/sandbox/sandboxtest"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
)

// Renders a fixture through MEDIA_SANDBOX_PROVIDER=remote and through the local provider, and checks that both made the same
// video (docs/design/render-runtime.md). The remote side is the real thing minus the image: MinIO holds the footage, cf-job (built
// from packages/media-tools/job-runner and run as a process of the test) downloads it from presigned URLs, runs the host's ffmpeg
// and uploads the video, the audio stem and an After step's frame to presigned PUT URLs. The local side is render.Execute over
// sandbox.NewLocal with the same ffmpeg, so the two decode to the same frames and samples: equal frame count, duration and stream
// hashes. v30_full_edit's preview has the loudness measuring pass (its own remote job), the subtitles (a /work input) and the stem.
//
// It needs ffmpeg 7+, Go (to build cf-job) and Docker (MinIO); without them it skips.
func TestRemoteRunnerMatchesLocal(t *testing.T) {
	ffmpeg, ffprobe := ffmpegTools(t)
	dir, cat := ensureMedia(t)
	bucket := storagetest.NewBucket(t)
	objects, err := storage.New(bucket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	spec := loadFixture(t, "v30_full_edit", cat)
	plan, err := Compile(spec, Preview, mediaResolver(dir, cat))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Loudnorm == nil || !plan.Loudnorm.TwoPass || plan.Subtitles == nil || plan.StemLabel == "" || plan.Motion != nil {
		t.Fatal("the fixture no longer has a measuring pass, subtitles and a stem without motion graphics: pick another")
	}
	fonts, err := findFontsDir("", plan.Subtitles.Fonts)
	if err != nil {
		t.Fatal(err)
	}

	// Local: the local provider over the host's ffmpeg, the one cf-job runs here, whatever -docker-image says (the image's
	// ffmpeg encodes other bytes).
	work := t.TempDir()
	local := filepath.Join(work, "local.mp4")
	localStem := filepath.Join(work, "local-stem.m4a")
	var roots []string
	for _, d := range []string{dir, os.TempDir()} { // Execute makes its work directory in the temp dir
		r, err := filepath.EvalSymlinks(d)
		if err != nil {
			t.Fatal(err)
		}
		roots = append(roots, r)
	}
	localRunner := &media.SandboxRunner{Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: roots}), Limits: DefaultLimits()}
	if err := Execute(ctx, plan, localRunner, ExecuteOptions{Output: local, StemOutput: localStem, FontsDir: fonts}); err != nil {
		t.Fatalf("local render: %v", err)
	}

	// Remote: cf-job reaches MinIO only (its allowlist), and nothing is mounted.
	ep, err := url.Parse(bucket.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	const token = "remote-runner-test-token-0123456789"
	remote, err := sandbox.NewRemote(sandboxtest.StartCFJob(t, token, ep.Host), token)
	if err != nil {
		t.Fatal(err)
	}
	remote.PollInterval = 100 * time.Millisecond
	files := &bucketFiles{t: t, objects: objects}
	put := func(key, contentType string) string {
		u, err := objects.PresignPut(ctx, key, contentType, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	var stages []string
	_, err = ExecuteRemote(ctx, plan, RemoteOptions{
		Provider: remote, JobID: "ren_remote-test-1", Limits: DefaultLimits(), Files: files, FontsDir: fonts,
		Labels: map[string]string{"cf.job": "render"},
		Video:  sandbox.Output{URL: put("out/render.mp4", "video/mp4"), ContentType: "video/mp4", MaxBytes: 256 << 20},
		Stem:   &sandbox.Output{URL: put("out/stem.m4a", "audio/mp4"), ContentType: "audio/mp4", MaxBytes: 64 << 20},
		After: []RemoteStep{{Name: "poster", Step: sandbox.Step{Args: []string{"ffmpeg", "-nostdin", "-v", "error", "-y", "-i", RemoteVideoPath,
			"-frames:v", "1", "/work/poster.png"}, Timeout: time.Minute}}},
		AfterOutputs: []sandbox.Output{{Path: "/work/poster.png", URL: put("out/poster.png", "image/png"), ContentType: "image/png"}},
		Progress:     func(p Progress) { stages = append(stages, p.Stage) },
	})
	if err != nil {
		t.Fatalf("remote render: %v", err)
	}
	if strings.Join(stages, ",") != "measure,render" {
		t.Errorf("progress stages %v, want the measuring pass and the render", stages)
	}
	rv, rs := files.download(ctx, "out/render.mp4", work), files.download(ctx, "out/stem.m4a", work)
	if info, err := objects.Head(ctx, "out/poster.png"); err != nil || info.Size == 0 {
		t.Errorf("the After step's output: %+v %v", info, err)
	}

	lp, rp := probeStream(t, ffprobe, local), probeStream(t, ffprobe, rv)
	if lp.frames != rp.frames || lp.durationMs != rp.durationMs || lp.frames != plan.TotalFrames {
		t.Errorf("local %+v, remote %+v; the plan has %d frames", lp, rp, plan.TotalFrames)
	}
	for name, pair := range map[string][2]string{"video": {local, rv}, "stem": {localStem, rs}} {
		lh, rh := streamHash(t, ffmpeg, pair[0]), streamHash(t, ffmpeg, pair[1])
		if lh != rh {
			t.Errorf("%s: the remote render decodes differently from the local one:\nlocal  %s\nremote %s", name, lh, rh)
		}
	}
	t.Logf("%d frames, %d ms; the files are byte-identical: %v", rp.frames, rp.durationMs, fileHash(t, local) == fileHash(t, rv))
}

// bucketFiles is RemoteFiles over a MinIO bucket: an input is uploaded the first time its URL is asked for.
type bucketFiles struct {
	t       *testing.T
	objects *storage.Client
	mu      sync.Mutex
	inputs  map[string]string
}

func (f *bucketFiles) InputURL(ctx context.Context, location string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inputs == nil {
		f.inputs = map[string]string{}
	}
	key, ok := f.inputs[location]
	if !ok {
		key = fmt.Sprintf("in/%d", len(f.inputs))
		file, err := os.Open(location) //nolint:gosec // a generated test clip
		if err != nil {
			return "", err
		}
		defer file.Close() //nolint:errcheck // read-only
		fi, err := file.Stat()
		if err != nil {
			return "", err
		}
		if err := f.objects.Put(ctx, key, file, fi.Size(), "application/octet-stream"); err != nil {
			return "", err
		}
		f.inputs[location] = key
	}
	return f.objects.PresignGet(ctx, key, time.Hour)
}

func (f *bucketFiles) PutScratch(ctx context.Context, name string, data []byte, contentType string) (string, error) {
	if err := f.objects.Put(ctx, "scratch/"+name, bytes.NewReader(data), int64(len(data)), contentType); err != nil {
		return "", err
	}
	return f.objects.PresignGet(ctx, "scratch/"+name, time.Hour)
}

func (f *bucketFiles) ScratchURL(ctx context.Context, name, contentType string) (string, error) {
	return f.objects.PresignPut(ctx, "scratch/"+name, contentType, time.Hour)
}

func (f *bucketFiles) GetScratch(ctx context.Context, name string, limit int64) ([]byte, error) {
	obj, err := f.objects.Get(ctx, "scratch/"+name)
	if err != nil {
		return nil, err
	}
	defer obj.Body.Close() //nolint:errcheck // read-only
	return io.ReadAll(io.LimitReader(obj.Body, limit))
}

// download writes an object to a file in dir and returns its path.
func (f *bucketFiles) download(ctx context.Context, key, dir string) string {
	f.t.Helper()
	obj, err := f.objects.Get(ctx, key)
	if err != nil {
		f.t.Fatalf("get %s: %v", key, err)
	}
	defer obj.Body.Close() //nolint:errcheck // read-only
	path := filepath.Join(dir, "remote-"+filepath.Base(key))
	data, err := io.ReadAll(obj.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

type streamProbe struct {
	frames     int64
	durationMs int64
}

// probeStream counts the decoded video frames and reads the duration.
func probeStream(t *testing.T, ffprobe, path string) streamProbe {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-count_frames", "-select_streams", "v:0", //nolint:gosec // test helper
		"-show_entries", "stream=nb_read_frames:format=duration", "-of", "default=noprint_wrappers=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var p streamProbe
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "nb_read_frames":
			_, _ = fmt.Sscan(v, &p.frames)
		case "duration":
			var secs float64
			_, _ = fmt.Sscan(v, &secs)
			p.durationMs = int64(secs*1000 + 0.5)
		}
	}
	return p
}

// streamHash is the SHA-256 of every decoded stream of a file (frames and samples, not the container).
func streamHash(t *testing.T, ffmpeg, path string) string {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-v", "error", "-nostdin", "-i", path, "-map", "0", "-f", "streamhash", "-hash", "sha256", "-").Output() //nolint:gosec // test helper
	if err != nil {
		t.Fatalf("streamhash %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a file of this test
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
