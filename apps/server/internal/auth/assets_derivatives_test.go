package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// uploadedAsset makes an asset of who's workspace in `uploaded` state, through the API.
func (e *assetsEnv) uploadedAsset(who, name string) api.Asset {
	e.t.Helper()
	c := e.create(who, name, 4096)
	parts := e.uploadAll(who, c)
	var done api.Asset
	e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", who, api.CompleteAssetRequest{Parts: parts}, http.StatusOK, &done)
	return done
}

// ingested stands in for the worker: it claims the asset, writes derivative objects the way the pipeline does and finishes
// the ingest. It returns the content it wrote per kind.
func (e *assetsEnv) ingested(who string, a api.Asset) map[string][]byte {
	e.t.Helper()
	ctx := context.Background()
	ws := e.ws[who]
	id, err := ids.ParseAssetID(a.Id)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.ClaimAssetForIngest(ctx, ws, id); err != nil {
		e.t.Fatal(err)
	}
	files := map[string][]byte{
		store.DerivativeProxy720:     bytes.Repeat([]byte("PROXY"), 2000),
		store.DerivativePoster:       []byte("JPEGPOSTER"),
		store.DerivativeSprite:       []byte("JPEGSPRITE"),
		store.DerivativeSpriteVTT:    []byte("WEBVTT\n"),
		store.DerivativeWaveform:     []byte(`{"version":1}`),
		store.DerivativeHoverPreview: []byte("HOVERMP4"),
		store.DerivativeAudio16k:     []byte("RIFFWAVE"),
	}
	names := map[string]string{
		store.DerivativeProxy720: "proxy_720.mp4", store.DerivativePoster: "poster.jpg", store.DerivativeSprite: "sprite.jpg",
		store.DerivativeSpriteVTT: "sprite.vtt", store.DerivativeWaveform: "waveform.json", store.DerivativeHoverPreview: "hover_preview.mp4",
		store.DerivativeAudio16k: "audio_16k.wav",
	}
	types := map[string]string{
		store.DerivativeProxy720: "video/mp4", store.DerivativePoster: "image/jpeg", store.DerivativeSprite: "image/jpeg",
		store.DerivativeSpriteVTT: "text/vtt", store.DerivativeWaveform: "application/json", store.DerivativeHoverPreview: "video/mp4",
		store.DerivativeAudio16k: "audio/wav",
	}
	var rows []store.NewDerivative
	for kind, data := range files {
		key, err := storage.DerivativeKey(ws, id, names[kind])
		if err != nil {
			e.t.Fatal(err)
		}
		if err := e.objs.Put(ctx, key, bytes.NewReader(data), int64(len(data)), types[kind]); err != nil {
			e.t.Fatal(err)
		}
		meta := json.RawMessage(`{}`)
		if kind == store.DerivativeProxy720 {
			meta = json.RawMessage(`{"width":720,"height":1280,"duration_ms":87135}`)
		}
		rows = append(rows, store.NewDerivative{Kind: kind, Key: key, ContentType: types[kind], SizeBytes: int64(len(data)), Meta: meta})
	}
	if _, err := e.st.FinishAssetIngest(ctx, ws, id, store.IngestResult{
		DurationMs: 87135, Width: 2160, Height: 3840, FPS: 30, Rotation: 90, VideoCodec: "h264", AudioCodec: "aac", HasAudio: true,
		Probe: json.RawMessage(`{}`), Derivatives: rows,
	}); err != nil {
		e.t.Fatal(err)
	}
	return files
}

func httpGet(t *testing.T, url string, header ...string) (status int, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAssetDerivativeURLs(t *testing.T) {
	e := newAssetsEnv(t)
	ready := e.uploadedAsset("ada", "clip.mp4")
	files := e.ingested("ada", ready)

	// Before the ingest finished (here: an asset that is only uploaded) there are no derivatives and no media facts.
	waiting := e.uploadedAsset("ada", "waiting.mp4")
	var w api.Asset
	e.call(http.MethodGet, "/v1/assets/"+waiting.Id, "ada", nil, http.StatusOK, &w)
	if w.Status != api.AssetStatusUploaded || w.Derivatives != nil || w.Media != nil || w.DerivativeUrlsExpireAt != nil || w.IngestProgress != 0 {
		t.Fatalf("waiting asset: %+v", w)
	}

	var got api.Asset
	e.call(http.MethodGet, "/v1/assets/"+ready.Id, "ada", nil, http.StatusOK, &got)
	if got.Status != api.AssetStatusReady || got.IngestProgress != 100 || got.IngestStage != nil || got.FailureReason != nil {
		t.Fatalf("asset: %+v", got)
	}
	m := got.Media
	if m == nil || m.DurationMs != 87135 || *m.Width != 2160 || *m.Height != 3840 || m.Rotation != 90 || *m.VideoCodec != "h264" || *m.AudioCodec != "aac" || !m.HasAudio || m.Hdr || *m.Fps != 30 {
		t.Fatalf("media: %+v", m)
	}
	d := got.Derivatives
	if d == nil || d.Proxy720 == nil || d.Poster == nil || d.Sprite == nil || d.SpriteVtt == nil || d.Waveform == nil || d.HoverPreview == nil {
		t.Fatalf("derivatives: %+v", d)
	}
	if d.Proxy720.Width == nil || *d.Proxy720.Width != 720 || *d.Proxy720.Height != 1280 || *d.Proxy720.DurationMs != 87135 || d.Proxy720.ContentType != "video/mp4" || d.Proxy720.SizeBytes != 10000 {
		t.Fatalf("proxy: %+v", d.Proxy720)
	}
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), "audio_16k") || strings.Contains(string(raw), "RIFFWAVE") {
		t.Fatalf("audio_16k is internal and must not be in the answer: %s", raw)
	}
	if got.DerivativeUrlsExpireAt == nil || !got.DerivativeUrlsExpireAt.After(time.Now().Add(20*time.Minute)) || got.DerivativeUrlsExpireAt.After(time.Now().Add(31*time.Minute)) {
		t.Fatalf("expiry: %v", got.DerivativeUrlsExpireAt)
	}

	// Every URL is a working, signed GET of the right content; the proxy supports range requests.
	for kind, f := range map[string]*api.DerivativeFile{
		store.DerivativePoster: d.Poster, store.DerivativeSprite: d.Sprite, store.DerivativeSpriteVTT: d.SpriteVtt,
		store.DerivativeWaveform: d.Waveform, store.DerivativeHoverPreview: d.HoverPreview, store.DerivativeProxy720: d.Proxy720,
	} {
		status, body := httpGet(t, f.Url)
		if status != http.StatusOK || !bytes.Equal(body, files[kind]) {
			t.Errorf("%s: status %d, %d bytes", kind, status, len(body))
		}
		if !strings.Contains(f.Url, "X-Amz-Expires=1800") {
			t.Errorf("%s: URL does not expire in 30 minutes: %s", kind, f.Url)
		}
	}
	status, body := httpGet(t, d.Proxy720.Url, "Range", "bytes=5-9")
	if status != http.StatusPartialContent || string(body) != "PROXYPROXY"[5:10] {
		t.Errorf("range: status %d %q", status, body)
	}
	// A URL for one key cannot be turned into another object: the signature covers the path.
	other := strings.Replace(d.Poster.Url, "poster.jpg", "proxy_720.mp4", 1)
	if status, _ := httpGet(t, other); status == http.StatusOK {
		t.Error("a poster URL with another file name worked")
	}

	// The list carries the same, newest first: the waiting asset has none, the ready one has all.
	var list api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets", "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 2 || list.Items[0].Id != waiting.Id || list.Items[0].Derivatives != nil ||
		list.Items[1].Id != ready.Id || list.Items[1].Derivatives == nil || list.Items[1].Derivatives.Poster == nil || list.Items[1].Media == nil {
		t.Fatalf("list: %+v", list.Items)
	}
	// A viewer reads them too.
	e.call(http.MethodGet, "/v1/assets/"+ready.Id, "viewer", nil, http.StatusOK, &got)
	if got.Derivatives == nil {
		t.Fatal("a viewer sees no derivatives")
	}

	// Another workspace: the asset is unknown, its list has nothing, and its own assets never show these URLs.
	e.fail(http.MethodGet, "/v1/assets/"+ready.Id, "bob", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.call(http.MethodGet, "/v1/assets", "bob", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Fatalf("bob's list: %+v", list.Items)
	}
	bobs := e.uploadedAsset("bob", "bobs.mp4")
	e.ingested("bob", bobs)
	e.call(http.MethodGet, "/v1/assets", "bob", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].Id != bobs.Id || !strings.Contains(list.Items[0].Derivatives.Poster.Url, "/"+e.ws["bob"].String()+"/assets/"+bobs.Id+"/") {
		t.Fatalf("bob's list: %+v", list.Items)
	}
	for _, it := range list.Items {
		if raw, _ := json.Marshal(it); strings.Contains(string(raw), e.ws["ada"].String()) {
			t.Fatalf("bob's answer mentions ada's workspace: %s", raw)
		}
	}

	// Deleting hides the asset and its URLs.
	e.call(http.MethodDelete, "/v1/assets/"+ready.Id, "ada", nil, http.StatusNoContent, nil)
	e.fail(http.MethodGet, "/v1/assets/"+ready.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
}

func TestAssetProgressAndFailureShowInTheAPI(t *testing.T) {
	e := newAssetsEnv(t)
	ctx := context.Background()
	a := e.uploadedAsset("ada", "busy.mp4")
	ws := e.ws["ada"]
	id, _ := ids.ParseAssetID(a.Id)
	if _, err := e.st.ClaimAssetForIngest(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetIngestProgress(ctx, ws, id, 40, store.StageProxy); err != nil {
		t.Fatal(err)
	}
	var got api.Asset
	e.call(http.MethodGet, "/v1/assets/"+a.Id, "ada", nil, http.StatusOK, &got)
	if got.Status != api.AssetStatusProcessing || got.IngestProgress != 40 || got.IngestStage == nil || *got.IngestStage != "proxy" || got.Derivatives != nil || got.Media != nil {
		t.Fatalf("processing: %+v", got)
	}
	// Another workspace cannot move this asset's progress or fail it.
	if err := e.st.SetIngestProgress(ctx, e.ws["bob"], id, 99, store.StageUploading); err == nil {
		t.Fatal("workspace B moved A's progress")
	}
	if _, err := e.st.FailAssetIngest(ctx, e.ws["bob"], id, store.FailureCorruptMedia); err == nil {
		t.Fatal("workspace B failed A's asset")
	}
	if _, err := e.st.FinishAssetIngest(ctx, e.ws["bob"], id, store.IngestResult{DurationMs: 1, Width: 1, Height: 1}); err == nil {
		t.Fatal("workspace B finished A's asset")
	}

	if _, err := e.st.FailAssetIngest(ctx, ws, id, store.FailureCorruptMedia); err != nil {
		t.Fatal(err)
	}
	e.call(http.MethodGet, "/v1/assets/"+a.Id, "ada", nil, http.StatusOK, &got)
	if got.Status != api.AssetStatusFailed || got.FailureReason == nil || *got.FailureReason != "corrupt_media" || got.IngestStage != nil || got.Derivatives != nil {
		t.Fatalf("failed: %+v", got)
	}
}
