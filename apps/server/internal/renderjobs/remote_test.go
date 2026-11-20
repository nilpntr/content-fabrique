package renderjobs

import (
	"bytes"
	"context"
	"net/url"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/sandbox/sandboxtest"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The pipeline on MEDIA_SANDBOX_PROVIDER=remote: the same timeline rendered by the local pipeline and by a remote one (cf-job as a
// process of the test, the footage and the outputs in MinIO behind presigned URLs, nothing staged on disk) comes out as the same
// video, the same contact sheet and the same row; the remote render leaves no scratch files behind.
func TestRemoteRenderMatchesLocal(t *testing.T) {
	e := newEnv(t)
	clips := testClips(t)
	w := e.newWorkspace(t, "org_render_remote")
	a, b := e.readyAsset(t, w, "a", clips[0]), e.readyAsset(t, w, "b", clips[1])
	p := e.newProject(t, w, a, b)
	spec := thirtySecondSpec(a, b)
	localVersion := e.addVersion(t, w, p, spec)
	remoteVersion := e.addVersion(t, w, p, spec)

	ep, err := url.Parse(e.cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	const token = "renderjobs-remote-token-0123456789"
	remote, err := sandbox.NewRemote(sandboxtest.StartCFJob(t, token, ep.Host), token)
	if err != nil {
		t.Fatal(err)
	}
	remote.PollInterval = 100 * time.Millisecond
	fonts, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "packages", "media-tools", "fonts"))
	if err != nil {
		t.Fatal(err)
	}
	remotePipe, err := New(Config{Store: e.st, Objects: e.objs, TempDir: e.tempDir, Logger: quiet, MotionFallback: true,
		Remote: &Remote{Provider: remote, FontsDir: fonts}})
	if err != nil {
		t.Fatal(err)
	}
	// The local side runs the host's ffmpeg, as cf-job does here, whatever -docker-image says (the image's ffmpeg encodes other
	// bytes), and with MOTION_GRAPHICS=off like the remote side.
	root := filepath.Dir(e.tempDir)
	localRunner := &media.SandboxRunner{Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root}}), Image: "unused", Limits: render.DefaultLimits()}
	localPipe, err := New(Config{Store: e.st, Objects: e.objs, Runner: localRunner, TempDir: e.tempDir, Logger: quiet, MotionFallback: true})
	if err != nil {
		t.Fatal(err)
	}

	lr, _ := e.request(t, w, p, localVersion, "preview")
	lout, err := localPipe.Run(context.Background(), w.ID, lr.ID)
	if err != nil {
		t.Fatalf("local Run: %v", err)
	}
	local, lerr := e.svc.Get(context.Background(), w.ID, lr.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	rr, _ := e.request(t, w, p, remoteVersion, "preview")
	rout, err := remotePipe.Run(context.Background(), w.ID, rr.ID)
	if err != nil {
		t.Fatalf("remote Run: %v", err)
	}
	got, gerr := e.svc.Get(context.Background(), w.ID, rr.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if lout.Status != store.RenderReady || rout.Status != store.RenderReady || got.Status != store.RenderReady {
		t.Fatalf("local %+v, remote %+v (%+v)", lout, rout, got)
	}
	if *got.DurationMs != *local.DurationMs || *got.Width != *local.Width || *got.Height != *local.Height || *got.SizeBytes != *local.SizeBytes {
		t.Errorf("remote row %+v, local %+v", got, local)
	}
	if wantKey, _ := storage.RenderKey(w.ID, p.ID, rr.ID, storage.RenderMP4Name); *got.Mp4Key != wantKey {
		t.Errorf("mp4 key %s, want %s", *got.Mp4Key, wantKey)
	}
	lmp4, rmp4 := e.get(t, *local.Mp4Key), e.get(t, *got.Mp4Key)
	if !bytes.Equal(streamHash(t, writeTemp(t, "l.mp4", lmp4)), streamHash(t, writeTemp(t, "r.mp4", rmp4))) {
		t.Error("the remote video decodes differently from the local one")
	}
	if !bytes.Equal(e.get(t, *local.ContactSheetKey), e.get(t, *got.ContactSheetKey)) {
		t.Error("the contact sheets differ")
	}
	if !bytes.Equal(local.ContactSheet, got.ContactSheet) {
		t.Errorf("the contact sheet layouts differ:\n%s\n%s", local.ContactSheet, got.ContactSheet)
	}
	if fakePlan(t, spec, "preview").StemLabel != "" {
		ls, _ := storage.RenderKey(w.ID, p.ID, lr.ID, storage.RenderAudioStemName)
		rs, _ := storage.RenderKey(w.ID, p.ID, rr.ID, storage.RenderAudioStemName)
		if !bytes.Equal(streamHash(t, writeTemp(t, "l.m4a", e.get(t, ls))), streamHash(t, writeTemp(t, "r.m4a", e.get(t, rs)))) {
			t.Error("the audio stems differ")
		}
	}
	// The scratch files (subtitles, frames) are gone; nothing was staged on the worker's disk.
	list, err := storagetest.S3(e.cfg).ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
		Bucket: aws.String(e.cfg.Bucket), Prefix: aws.String(storage.RenderScratchPrefix(w.ID, p.ID, rr.ID)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Contents) != 0 {
		t.Errorf("%d scratch files left", len(list.Contents))
	}
	e.tempDirEmpty(t)
}

// streamHash is the SHA-256 of every decoded stream of a file: frames and samples, not the container.
func streamHash(t *testing.T, path string) []byte {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-i", path, "-map", "0", "-f", "streamhash", "-hash", "sha256", "-").Output() //nolint:gosec // test
	if err != nil {
		t.Fatalf("streamhash %s: %v", path, err)
	}
	return out
}
