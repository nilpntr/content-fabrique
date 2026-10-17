package ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// fileObjects serves one file as the original (streamed, never held in memory) and keeps everything else in memory.
type fileObjects struct {
	*fakeObjects
	key, path string
}

func (f fileObjects) Get(ctx context.Context, key string) (storage.Object, error) {
	if key != f.key {
		return f.fakeObjects.Get(ctx, key)
	}
	file, err := os.Open(f.path)
	if err != nil {
		return storage.Object{}, err
	}
	fi, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return storage.Object{}, err
	}
	return storage.Object{Body: file, Size: fi.Size()}, nil
}

// TestRealClip is the manual proof run on a real phone clip, with the real media-tools image:
//
//	CF_REAL_CLIP=$HOME/Downloads/cf_footage_test/row.mov go test ./internal/ingest -run TestRealClip -v -timeout 30m -docker-image media-tools:dev
//
// The original is only read (streamed into the job's temp directory, which is removed afterwards); nothing is written to
// the repository. It logs metadata only.
func TestRealClip(t *testing.T) {
	path := os.Getenv("CF_REAL_CLIP")
	if path == "" {
		t.Skip("set CF_REAL_CLIP to a video file to run the pipeline on it")
	}
	h := newHarness(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	a := h.upload(store.AssetKindVideo, writeSmall(t, h)) // a row of the harness's workspace; the original is replaced below
	delete(h.objects.objects, a.R2Key)
	a.SizeBytes = fi.Size()
	h.store.add(a)
	h.p.cfg.Objects = fileObjects{fakeObjects: h.objects, key: a.R2Key, path: path}

	start := time.Now()
	res := h.run(a)
	t.Logf("ingest of %s (%d MB): %s in %s, derivatives %v", filepath.Base(path), fi.Size()>>20, res.Status, time.Since(start).Round(time.Second), res.Derivatives)
	if res.Status != store.AssetReady {
		t.Fatalf("%+v", res)
	}
	r := h.store.results[fkey(a.WorkspaceID, a.ID)]
	t.Logf("asset: %dx%d (display) rotation %d fps %.2f duration %d ms video %s audio %s has_audio %v hdr %v", r.Width, r.Height, r.Rotation, r.FPS, r.DurationMs, r.VideoCodec, r.AudioCodec, r.HasAudio, r.HDR)

	for _, d := range h.store.derivatives[fkey(a.WorkspaceID, a.ID)] {
		t.Logf("derivative %-14s %9d bytes  %s", d.Kind, d.SizeBytes, d.Meta)
	}
	proxyPath, _ := h.derivative(a, store.DerivativeProxy720)
	pm := h.probe(proxyPath)
	ks := keyframes(t, proxyPath)
	minGap, maxGap := 1e9, 0.0
	for i := 1; i < len(ks); i++ {
		g := ks[i] - ks[i-1]
		minGap, maxGap = min(minGap, g), max(maxGap, g)
	}
	t.Logf("proxy: %dx%d stored %dx%d rotation %d pix_fmt %s %s color %s/%s/%s, %d keyframes, gap %.3f..%.3f s, faststart %v, audio %+v",
		pm.Width, pm.Height, pm.CodedWidth, pm.CodedHeight, pm.Rotation, pm.PixFmt, pm.Codecs.Video, pm.ColorSpace, pm.ColorPrimaries, pm.ColorTransfer,
		len(ks), minGap, maxGap, moovBeforeMdat(t, proxyPath), pm.AudioStreams)
	hoverPath, hrow := h.derivative(a, store.DerivativeHoverPreview)
	hm := h.probe(hoverPath)
	t.Logf("hover: %dx%d %d ms %d bytes (%.0f kB) audio streams %d faststart %v", hm.Width, hm.Height, hm.DurationMs, hrow.SizeBytes, float64(hrow.SizeBytes)/1000, len(hm.AudioStreams), moovBeforeMdat(t, hoverPath))
	wp, _ := h.derivative(a, store.DerivativeWaveform)
	var wf Waveform
	b, _ := os.ReadFile(wp)
	_ = json.Unmarshal(b, &wf)
	t.Logf("waveform: %d peaks over %d ms", len(wf.Peaks), wf.DurationMs)
	vp, _ := h.derivative(a, store.DerivativeSpriteVTT)
	vtt, _ := os.ReadFile(vp)
	t.Logf("sprite vtt: %d cues", strings.Count(string(vtt), "-->"))
}

// writeSmall makes a tiny placeholder file so the harness can register an asset row.
func writeSmall(t *testing.T, h *harness) string {
	t.Helper()
	p := filepath.Join(h.clips, "placeholder")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
