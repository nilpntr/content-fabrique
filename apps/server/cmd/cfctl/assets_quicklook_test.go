package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// fakeAnthropic answers /v1/messages with a describe_clip call and /v1/messages/count_tokens with 1,750.
type fakeAnthropic struct {
	mu       sync.Mutex
	messages int
	counts   int
}

func (f *fakeAnthropic) RoundTrip(r *http.Request) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	body := `{"type":"error","error":{"type":"not_found_error","message":"no route"}}`
	status := http.StatusNotFound
	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/messages/count_tokens"):
		f.counts++
		status, body = 200, `{"input_tokens": 1750}`
	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		f.messages++
		status, body = 200, `{"id":"msg_fake","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"describe_clip",`+
			`"input":{"title":"Barbell row","description":"A lifter does bent-over barbell rows in a gym.","tags":["barbell row","back","gym"]}}],"stop_reason":"tool_use","stop_sequence":null,`+
			`"usage":{"input_tokens":1750,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":110}}`
	}
	h := http.Header{"Content-Type": {"application/json"}, "Request-Id": {fmt.Sprintf("req_cli_%d", f.messages+f.counts)}}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r, ProtoMajor: 1, ProtoMinor: 1}, nil
}

func (r *importRig) quickLookArgs(extra ...string) []string {
	return append([]string{"quicklook", "--env-file", r.envFile, "--yes"}, extra...)
}

func (r *importRig) quickLook(extra ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	code = cmdAssets(r.quickLookArgs(extra...), &out, &errb, fakeProcessEnv(map[string]string{}))
	return code, out.String(), errb.String()
}

// readyVideo makes a ready video of the rig's workspace the way the ingest leaves it, with a real (tiny) proxy in object storage.
func (r *importRig) readyVideo(clip, filename string) store.Asset {
	r.t.Helper()
	ctx := context.Background()
	id, err := r.st.NewAssetID(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, _, err := r.st.CreateAssetWithUpload(ctx, r.ws.ID, store.NewAsset{
		ID: id, Kind: store.AssetKindVideo, Filename: filename, ContentType: "video/mp4", SizeBytes: 1000, Title: strings.TrimSuffix(filename, ".mp4"),
		R2Key: storage.OriginalKey(r.ws.ID, id), CreatedBy: r.owner.ID, R2UploadID: "r2", PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
	}, 0); err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.InTx(ctx, func(tx *store.Store) error { _, err := tx.CompleteUpload(ctx, r.ws.ID, id); return err }); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.st.ClaimAssetForIngest(ctx, r.ws.ID, id); err != nil {
		r.t.Fatal(err)
	}
	mp4, err := os.ReadFile(clip)
	if err != nil {
		r.t.Fatal(err)
	}
	key, err := storage.DerivativeKey(r.ws.ID, id, "proxy_720.mp4")
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.objs.Put(ctx, key, bytes.NewReader(mp4), int64(len(mp4)), "video/mp4"); err != nil {
		r.t.Fatal(err)
	}
	a, err := r.st.FinishAssetIngest(ctx, r.ws.ID, id, store.IngestResult{
		DurationMs: 1000, Width: 160, Height: 120, FPS: 15, VideoCodec: "mpeg4",
		Derivatives: []store.NewDerivative{{Kind: store.DerivativeProxy720, Key: key, ContentType: "video/mp4", SizeBytes: int64(len(mp4))}},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

func TestAssetsQuickLook(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	r := newImportRig(t)
	clip := filepath.Join(r.dir, "proxy.mp4")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=duration=1:size=160x120:rate=15", //nolint:gosec // fixed arguments
		"-c:v", "libx264", "-pix_fmt", "yuv420p", clip).CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot make the test clip: %v: %s", err, out)
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extra := fmt.Sprintf("MEDIA_SANDBOX_PROVIDER=local\nMEDIA_TEMP_DIR=%s\nANTHROPIC_API_KEY=sk-ant-api03-CLI-TEST-KEY-do-not-print\nLLM_MODE=live\nROUTING_PROFILE=dev-cheap\n", tmp)
	body, err := os.ReadFile(r.envFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.envFile, append(body, extra...), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &fakeAnthropic{}
	quickLookHTTPClient = &http.Client{Transport: api}
	t.Cleanup(func() { quickLookHTTPClient = nil })

	a := r.readyVideo(clip, "IMG_0042.mp4")
	if a.QuickLookStatus == nil || *a.QuickLookStatus != store.QuickLookPending {
		t.Fatalf("the ingest should leave the quick look pending: %v", a.QuickLookStatus)
	}
	ctx := context.Background()

	code, out, errOut := r.quickLook("--workspace", r.ws.ID.String())
	if code != 0 {
		t.Fatalf("quicklook = %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, a.ID.String()) || !strings.Contains(out, `"Barbell row"`) || !strings.Contains(out, "[barbell row, back, gym]") ||
		!strings.Contains(out, "1 video(s)") || !strings.Contains(out, "total cost $0.000") {
		t.Fatalf("output:\n%s", out)
	}
	if strings.Contains(out+errOut, "CLI-TEST-KEY") {
		t.Errorf("the output holds the API key:\n%s%s", out, errOut)
	}
	got, err := r.st.GetAsset(ctx, r.ws.ID, a.ID)
	if err != nil || got.Title != "Barbell row" || got.TitleSource != store.TitleFromQuickLook || *got.QuickLookStatus != store.QuickLookSucceeded ||
		strings.Join(got.Tags, ",") != "barbell row,back,gym" {
		t.Fatalf("asset = %+v %v", got, err)
	}

	// One call, metered: the ledger row is the asset's, billed as a test.
	rows, err := r.st.UsageForRun(ctx, r.ws.ID, a.ID.UUID)
	if err != nil || len(rows) != 1 || rows[0].Stage != "quicklook" || rows[0].BilledTo != "test" || rows[0].ListCostMicros <= 0 || rows[0].ListCostMicros > 3000 {
		t.Fatalf("ledger rows = %+v %v", rows, err)
	}
	if api.messages != 1 {
		t.Fatalf("%d model calls, want 1", api.messages)
	}

	// Nothing left to do by default; --all runs it again (the frames are cached), and keeps a title a person edited.
	if code, out, errOut := r.quickLook("--workspace", r.ws.ID.String()); code != 0 || !strings.Contains(out, "0 video(s)") || api.messages != 1 {
		t.Fatalf("second run = %d, %d calls\n%s%s", code, api.messages, out, errOut)
	}
	if _, err := r.st.UpdateAssetTitle(ctx, r.ws.ID, a.ID, "Gino rows"); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := r.quickLook("--workspace", "org_import", "--all"); code != 0 || !strings.Contains(out, "1 video(s)") || api.messages != 2 {
		t.Fatalf("--all = %d, %d calls\n%s%s", code, api.messages, out, errOut)
	}
	if got, _ := r.st.GetAsset(ctx, r.ws.ID, a.ID); got.Title != "Gino rows" || got.TitleSource != store.TitleFromUser {
		t.Errorf("the user's title was overwritten: %+v", got)
	}
	if rows, _ := r.st.UsageForRun(ctx, r.ws.ID, a.ID.UUID); len(rows) != 2 {
		t.Errorf("%d ledger rows after two calls", len(rows))
	}

	// --asset names one; another workspace's or a made-up asset is refused.
	if code, out, errOut := r.quickLook("--workspace", r.ws.ID.String(), "--asset", a.ID.String()); code != 0 || api.messages != 3 {
		t.Fatalf("--asset = %d\n%s%s", code, out, errOut)
	}
	other, err := r.st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_other_ql", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := r.quickLook("--workspace", other.ID.String(), "--asset", a.ID.String()); code != 1 || !strings.Contains(errOut, "no such asset") || api.messages != 3 {
		t.Errorf("another workspace's asset: exit %d, %d calls: %s", code, api.messages, errOut)
	}
	if code, out, _ := r.quickLook("--workspace", other.ID.String(), "--all"); code != 0 || !strings.Contains(out, "0 video(s)") {
		t.Errorf("another workspace lists %d: %s", code, out)
	}

	// Arguments and the guards.
	for name, args := range map[string][]string{
		"no workspace":  {},
		"bad workspace": {"--workspace", "ast_01j3er3kwcgrasdgd4aznmxxe0"},
		"bad asset":     {"--workspace", r.ws.ID.String(), "--asset", "wks_01j3er3kwcgrasdgd4aznmxxe0"},
		"bad limit":     {"--workspace", r.ws.ID.String(), "--limit", "0"},
		"stray words":   {"--workspace", r.ws.ID.String(), "extra"},
	} {
		if code, _, errOut := r.quickLook(args...); code != 2 {
			t.Errorf("%s: exit %d: %s", name, code, errOut)
		}
	}
	for _, env := range []string{"staging", "production"} {
		r.writeEnvWith(env)
		if code, _, errOut := r.quickLook("--workspace", r.ws.ID.String(), "--all"); code != 2 || !strings.Contains(errOut, "development only") {
			t.Errorf("CF_ENV=%s: exit %d: %s", env, code, errOut)
		}
	}
}

// writeEnvWith rewrites the env file with another CF_ENV.
func (r *importRig) writeEnvWith(cfEnv string) {
	r.t.Helper()
	body, err := os.ReadFile(r.envFile)
	if err != nil {
		r.t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "CF_ENV=") {
			line = "CF_ENV=" + cfEnv
		}
		kept = append(kept, line)
	}
	if err := os.WriteFile(r.envFile, []byte(strings.Join(kept, "\n")), 0o600); err != nil {
		r.t.Fatal(err)
	}
}
