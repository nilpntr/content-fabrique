package renderjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// A 30 s edit renders as a preview (540x960, from the proxies) and as a final (1080x1920, from the originals), through the real
// service, queue row, pipeline, sandbox and storage, and every output is what the plan said: the length, the time map byte for
// byte, the sound cues, and a contact sheet with a tile for every shot and the last frame.
func TestRenderPreviewAndFinal(t *testing.T) {
	e := newEnv(t)
	clips := testClips(t)
	w := e.newWorkspace(t, "org_render_full")
	a, b := e.readyAsset(t, w, "a", clips[0]), e.readyAsset(t, w, "b", clips[1])
	p := e.newProject(t, w, a, b)
	spec := thirtySecondSpec(a, b)
	version := e.addVersion(t, w, p, spec)

	for _, prof := range []struct {
		name          string
		width, height int
	}{{"preview", 540, 960}, {"final", 1080, 1920}} {
		t.Run(prof.name, func(t *testing.T) {
			plan := fakePlan(t, spec, prof.name)
			if plan.DurationMs < 30000 || plan.DurationMs > 31000 {
				t.Fatalf("the test edit is %d ms long, want about 30 s", plan.DurationMs)
			}
			r, created := e.request(t, w, p, version, prof.name)
			if !created || r.Status != store.RenderQueued || r.Progress != 0 || r.RequestedByKind != store.RenderRequestedByUser || r.RequestedByID != e.user.String() {
				t.Fatalf("new render: created=%v %+v", created, r)
			}

			// Watch the progress column while the job runs.
			var (
				mu   sync.Mutex
				seen []int
			)
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					// A row that is ready (100) is not part of the run's progress: ready and 100 arrive in one write.
					if cur, err := e.svc.Get(context.Background(), w.ID, r.ID); err == nil && cur.Status != store.RenderReady {
						if cur.Progress >= 100 {
							t.Errorf("a %s render shows progress %d", cur.Status, cur.Progress)
						}
						mu.Lock()
						if n := len(seen); n == 0 || seen[n-1] != int(cur.Progress) {
							seen = append(seen, int(cur.Progress))
						}
						mu.Unlock()
					}
					select {
					case <-stop:
						return
					case <-time.After(50 * time.Millisecond):
					}
				}
			}()
			start := time.Now()
			got, outcome := e.runRender(t, w, r)
			wall := time.Since(start)
			close(stop)
			<-done
			t.Logf("%s job: %.1f s for a %.1f s edit (render row: wall %d ms, sandbox about %d ms)", prof.name, wall.Seconds(), float64(plan.DurationMs)/1000, *got.WallMs, *got.CpuMs/4)

			if outcome.Status != store.RenderReady {
				t.Fatalf("outcome %+v", outcome)
			}
			mu.Lock()
			// The watcher only counts rows that are not ready, so what it saw is a rising sequence below 100.
			if !slices.IsSorted(seen) || len(seen) < 4 || seen[len(seen)-1] >= 100 {
				t.Errorf("progress went %v, want a rising sequence below 100 until the row is ready", seen)
			}
			mu.Unlock()

			if got.Status != store.RenderReady || got.Progress != 100 || got.Stage != nil || got.FailureReason != nil || got.FinishedAt == nil || got.StartedAt == nil ||
				got.DurationMs == nil || *got.DurationMs != plan.DurationMs || got.Width == nil || int(*got.Width) != prof.width || got.Height == nil || int(*got.Height) != prof.height ||
				got.WallMs == nil || *got.WallMs <= 0 || got.CpuMs == nil || *got.CpuMs <= 0 {
				t.Fatalf("render row: %+v", got)
			}
			for name, key := range map[string]*string{storage.RenderMP4Name: got.Mp4Key, storage.RenderContactSheetName: got.ContactSheetKey, storage.RenderTimeMapName: got.TimeMapKey} {
				want, _ := storage.RenderKey(w.ID, p.ID, r.ID, name)
				if key == nil || *key != want || !e.exists(t, want) {
					t.Errorf("%s: key %v, want %s (and present)", name, key, want)
				}
			}

			// The video.
			mp4 := e.get(t, *got.Mp4Key)
			if got.SizeBytes == nil || *got.SizeBytes != int64(len(mp4)) {
				t.Errorf("size_bytes %v, the object has %d", got.SizeBytes, len(mp4))
			}
			mp4Path := writeTemp(t, "out.mp4", mp4)
			pr := probeFile(t, mp4Path)
			if pr.Width != prof.width || pr.Height != prof.height || !pr.HasAudio || pr.Frames != int(plan.TotalFrames) || pr.DurationSec < float64(plan.DurationMs)/1000-0.1 || pr.DurationSec > float64(plan.DurationMs)/1000+0.2 {
				t.Errorf("the MP4: %+v, want %dx%d, %d frames, %d ms, with audio", pr, prof.width, prof.height, plan.TotalFrames, plan.DurationMs)
			}

			// The time map is the plan's, byte for byte.
			wantMap, err := plan.TimeMapJSON()
			if err != nil {
				t.Fatal(err)
			}
			if gotMap := e.get(t, *got.TimeMapKey); !bytes.Equal(gotMap, wantMap) {
				t.Errorf("time_map.json differs from Plan.TimeMap:\n got %s\nwant %s", gotMap, wantMap)
			}
			if got.TimeMapBytes == nil || *got.TimeMapBytes != int64(len(wantMap)) {
				t.Errorf("time_map_bytes %v, want %d", got.TimeMapBytes, len(wantMap))
			}

			// The sound cue is kept, and was not mixed in.
			var cues []render.SoundCue
			if err := json.Unmarshal(got.SoundCues, &cues); err != nil || !slices.Equal(cues, plan.SoundCues) || len(cues) != 1 || cues[0].TrendID != "trd_01jcmevy4tr3fffvjc82eh7rjm" {
				t.Errorf("sound_cues %s (%v), want %+v", got.SoundCues, err, plan.SoundCues)
			}

			checkContactSheet(t, e, got, plan, mp4Path)
			e.tempDirEmpty(t)
		})
	}

	t.Run("the API shows the render with working URLs", func(t *testing.T) {
		r, created := e.request(t, w, p, version, "preview")
		if created || r.Status != store.RenderReady {
			t.Fatalf("a second request for the same version and profile: created=%v status=%s", created, r.Status)
		}
		h := NewAPI(e.svc, func(context.Context) (httpapi.Caller, bool) {
			return httpapi.Caller{Workspace: w.ID, User: e.user, Role: store.RoleViewer}, true
		}, quiet)
		resp, err := h.GetRender(context.Background(), api.GetRenderRequestObject{Id: r.ID.String()})
		if err != nil {
			t.Fatal(err)
		}
		ok, isOK := resp.(api.GetRender200JSONResponse)
		if !isOK {
			t.Fatalf("GetRender = %T", resp)
		}
		if ok.Files == nil || ok.UrlsExpiresAt == nil || ok.ContactSheet == nil || len(ok.ContactSheet.Tiles) != 11 || len(ok.SoundCues) != 1 ||
			ok.Status != api.RenderStatusReady || ok.Progress != 100 || ok.RequestedBy.Kind != api.AuthorKindUser || ok.RequestedBy.Id != e.user.String() {
			t.Fatalf("api render: %+v", ok)
		}
		for name, f := range map[string]api.RenderFile{"mp4": ok.Files.Mp4, "contact sheet": ok.Files.ContactSheet, "time map": ok.Files.TimeMap} {
			body := httpGet(t, f.Url)
			if int64(len(body)) != f.SizeBytes || len(body) == 0 {
				t.Errorf("%s: fetched %d bytes, the API says %d", name, len(body), f.SizeBytes)
			}
		}
		if body := httpGet(t, ok.Files.TimeMap.Url); !json.Valid(body) {
			t.Errorf("the time map is not JSON: %s", body)
		}
		// The agent's way to the picture.
		sheet, err := e.svc.ContactSheetJPEG(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(sheet)); err != nil || cfg.Width == 0 {
			t.Errorf("ContactSheetJPEG: %v %+v", err, cfg)
		}
	})
}

func httpGet(t *testing.T, url string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %.60s...: %d, %v", url, resp.StatusCode, err)
	}
	return b
}

// checkContactSheet looks at the sheet the render stored: a tile for every shot (the frame 100 ms after the cut that starts it)
// and the last frame, four across, with the tiles' pictures from the right frames of the video.
func checkContactSheet(t *testing.T, e *env, r Render, plan render.Plan, mp4Path string) {
	t.Helper()
	var layout SheetLayout
	if err := json.Unmarshal(r.ContactSheet, &layout); err != nil {
		t.Fatalf("contact_sheet: %v (%s)", err, r.ContactSheet)
	}
	var shots []render.TimeMapEntry
	for _, en := range plan.TimeMap {
		if en.SourceInMs != nil {
			shots = append(shots, en)
		}
	}
	if len(layout.Tiles) != len(shots)+1 || layout.Columns != 4 {
		t.Fatalf("the sheet has %d tiles in %d columns, want one per shot (%d) and the last frame", len(layout.Tiles), layout.Columns, len(shots))
	}
	frameMs := int64(1000 / plan.FPS)
	for i, tile := range layout.Tiles {
		if tile.Index != i {
			t.Errorf("tile %d has index %d", i, tile.Index)
		}
		if i < len(shots) {
			if tile.Kind != TileShot || tile.ItemID != shots[i].ItemID {
				t.Errorf("tile %d: %+v, want shot %s", i, tile, shots[i].ItemID)
			}
			// 100 ms after the cut, to the frame.
			if d := tile.OutMs - shots[i].OutStartMs; d < SheetOffsetMs-frameMs || d > SheetOffsetMs+frameMs {
				t.Errorf("tile %d is %d ms after its cut, want about %d", i, d, SheetOffsetMs)
			}
		} else if tile.Kind != TileLast || tile.Frame != plan.TotalFrames-1 {
			t.Errorf("last tile: %+v, want frame %d", tile, plan.TotalFrames-1)
		}
	}

	data := e.get(t, *r.ContactSheetKey)
	if dir := os.Getenv("CF_DUMP_SHEETS"); dir != "" { // to look at the picture: CF_DUMP_SHEETS=/tmp/sheets go test ...
		_ = os.MkdirAll(dir, 0o750)
		_ = os.WriteFile(filepath.Join(dir, r.Profile+".jpg"), data, 0o600)
	}
	if r.ContactSheetBytes == nil || *r.ContactSheetBytes != int64(len(data)) {
		t.Errorf("contact_sheet_bytes %v, the object has %d", r.ContactSheetBytes, len(data))
	}
	if len(data) > SheetTargetBytes+SheetTargetBytes/4 {
		t.Errorf("the contact sheet is %d bytes, the target is %d", len(data), SheetTargetBytes)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the contact sheet is not a JPEG: %v", err)
	}
	rows := (len(layout.Tiles) + 3) / 4
	if want := image.Rect(0, 0, 4*layout.TileWidth+3*sheetGap, rows*layout.TileHeight+(rows-1)*sheetGap); img.Bounds() != want {
		t.Errorf("sheet is %v, want %v", img.Bounds(), want)
	}

	// The picture in a tile is the video's frame at the time written on it: compare below the label with the frame ffmpeg
	// extracts independently.
	for _, i := range []int{0, 3, 4, len(layout.Tiles) - 1} {
		tile := layout.Tiles[i]
		want := extractFrame(t, mp4Path, tile.Frame, layout.TileWidth, layout.TileHeight)
		x0 := (i % 4) * (layout.TileWidth + sheetGap)
		y0 := (i / 4) * (layout.TileHeight + sheetGap)
		var sum, n float64
		for y := layout.TileHeight / 4; y < layout.TileHeight; y++ {
			for x := 0; x < layout.TileWidth; x++ {
				gr, gg, gb, _ := img.At(x0+x, y0+y).RGBA()
				wr, wg, wb, _ := want.At(x, y).RGBA()
				for _, d := range []int{int(gr>>8) - int(wr>>8), int(gg>>8) - int(wg>>8), int(gb>>8) - int(wb>>8)} {
					sum += float64(max(d, -d))
					n++
				}
			}
		}
		if mean := sum / n; mean > 12 {
			t.Errorf("tile %d (frame %d at %d ms): mean difference from the video's frame is %.1f/255", i, tile.Frame, tile.OutMs, mean)
		}
	}
	// And the label is there: the top left corner of every tile has bright text pixels on a dark box.
	for i := range layout.Tiles {
		x0 := (i % 4) * (layout.TileWidth + sheetGap)
		y0 := (i / 4) * (layout.TileHeight + sheetGap)
		bright := 0
		for y := 0; y < 24; y++ {
			for x := 0; x < 120; x++ {
				if r, g, b, _ := img.At(x0+x, y0+y).RGBA(); r>>8 > 230 && g>>8 > 230 && b>>8 > 230 {
					bright++
				}
			}
		}
		if bright < 30 {
			t.Errorf("tile %d has no timestamp (%d bright pixels in the label area)", i, bright)
		}
	}
}

// extractFrame decodes frame n of the video with the host's ffmpeg, scaled to w x h.
func extractFrame(t *testing.T, mp4 string, n int64, w, h int) image.Image {
	t.Helper()
	out := filepath.Join(t.TempDir(), "f.png")
	cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-i", mp4, "-vf", "select='eq(n,"+strconv.FormatInt(n, 10)+")',scale="+strconv.Itoa(w)+":"+strconv.Itoa(h)+":flags=area,format=rgb24", "-fps_mode", "passthrough", "-frames:v", "1", out) //nolint:gosec // test
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, b)
	}
	f, err := os.Open(out) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// TestRenderTiming is the measurement behind the acceptance criterion of P3-06: a 30 s edit renders as a preview in at most
// 10 s and as a final in at most 60 s on the target host class. It only logs (a timing assertion in CI would be flaky):
//
//	go test ./internal/renderjobs -run TestRenderTiming -timing -v [-docker-image media-tools:dev]
//
// The figure that counts is the job's: from the claim to the ready row, staging the footage, rendering, the contact sheet and
// the upload included. The test clips are 1080x1920 synthetic test patterns with a sine tone, so the originals are not typical
// phone footage; the proxies are 720x1280.
func TestRenderTiming(t *testing.T) {
	if !*timingFlag {
		t.Skip("run with -timing to measure")
	}
	e := newEnv(t)
	clips := testClips(t)
	w := e.newWorkspace(t, "org_render_timing")
	a, b := e.readyAsset(t, w, "a", clips[0]), e.readyAsset(t, w, "b", clips[1])
	p := e.newProject(t, w, a, b)
	spec := thirtySecondSpec(a, b)
	version := e.addVersion(t, w, p, spec)
	provider := "local sandbox (ffmpeg on this machine)"
	if *dockerImage != "" {
		provider = "docker sandbox, image " + *dockerImage
	}
	t.Logf("sandbox: %s", provider)
	for _, name := range []string{"preview", "final"} {
		plan := fakePlan(t, spec, name)
		r, _ := e.request(t, w, p, version, name)
		start := time.Now()
		got, out := e.runRender(t, w, r)
		wall := time.Since(start)
		if out.Status != store.RenderReady {
			t.Fatalf("%s: %+v", name, out)
		}
		t.Logf("TIMING %s: %.1f s of video (%dx%d, %d frames): job %.2f s end to end; of which the sandbox runs (ffmpeg render and contact sheet) %.2f s, staging and upload %.2f s; mp4 %.1f MB",
			name, float64(plan.DurationMs)/1000, plan.OutputSize.Width, plan.OutputSize.Height, plan.TotalFrames, wall.Seconds(),
			float64(*got.CpuMs)/4/1000, wall.Seconds()-float64(*got.CpuMs)/4/1000, float64(*got.SizeBytes)/1e6)
	}
}
