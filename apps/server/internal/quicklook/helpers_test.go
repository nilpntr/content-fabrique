package quicklook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/spend/spendtest"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const testKey = "sk-ant-api03-TESTKEY-do-not-leak-0123456789abcdef"

// ---- Memory object storage ---------------------------------------------------------------------------------------------

type memObjects struct {
	mu   sync.Mutex
	objs map[string][]byte
	puts []string
	gets int
}

func newMemObjects() *memObjects { return &memObjects{objs: map[string][]byte{}} }

func (m *memObjects) Get(_ context.Context, key string) (storage.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	b, ok := m.objs[key]
	if !ok {
		return storage.Object{}, storage.ErrNotFound
	}
	return storage.Object{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b)), ContentType: "application/octet-stream"}, nil
}

func (m *memObjects) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	return storage.ObjectInfo{Size: int64(len(b)), ContentType: "image/jpeg"}, nil
}

func (m *memObjects) Put(_ context.Context, key string, body io.ReadSeeker, size int64, _ string) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return fmt.Errorf("put %s: %d bytes, said %d", key, len(b), size)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = b
	m.puts = append(m.puts, key)
	return nil
}

func (m *memObjects) getCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gets
}

func (m *memObjects) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objs {
		out = append(out, k)
	}
	return out
}

// ---- A fake sandbox ----------------------------------------------------------------------------------------------------

// fakeRunner stands in for the sandbox: for every output f<n>.jpg of the ffmpeg command it writes the fixture frame of the
// candidate time of the n-th -ss (the frame service names its outputs f0, f1 ... in the order of its inputs).
type fakeRunner struct {
	mu    sync.Mutex
	calls []media.Invocation
	// exit and stderr make the "tool" fail.
	exit   int
	stderr string
	// skip names candidates (by index) whose picture is not written.
	skip map[int]bool
}

func (f *fakeRunner) Run(_ context.Context, inv media.Invocation) (sandbox.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, inv)
	f.mu.Unlock()
	if f.exit != 0 {
		return sandbox.Result{ExitCode: f.exit, Stderr: []byte(f.stderr)}, nil
	}
	var times []float64
	for i, a := range inv.Args {
		if a == "-ss" && i+1 < len(inv.Args) {
			var sec float64
			if _, err := fmt.Sscanf(inv.Args[i+1], "%f", &sec); err != nil {
				return sandbox.Result{}, err
			}
			times = append(times, sec)
		}
	}
	candidates := CandidateTimes(12_000)
	for _, a := range inv.Args {
		var n int
		if _, err := fmt.Sscanf(a, sandbox.WorkDir+"/f%d.jpg", &n); err != nil || n >= len(times) {
			continue
		}
		idx := slices.Index(candidates, int64(times[n]*1000+0.5))
		if idx < 0 {
			idx = 0
		}
		if f.skip[idx] {
			continue
		}
		if err := os.WriteFile(filepath.Join(inv.OutputDir, fmt.Sprintf("f%d.jpg", n)), fixtureFrame(idx), 0o644); err != nil {
			return sandbox.Result{}, err
		}
	}
	return sandbox.Result{}, nil
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// ---- A fake store ------------------------------------------------------------------------------------------------------

// fakeStore keeps one asset in memory. ApplyQuickLook mirrors the SQL's title rule (the real one is tested against Postgres
// in package store).
type fakeStore struct {
	mu     sync.Mutex
	asset  store.Asset
	derivs []store.Derivative
}

func (f *fakeStore) GetAsset(_ context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ws != f.asset.WorkspaceID || id != f.asset.ID {
		return store.Asset{}, store.ErrNotFound
	}
	return f.asset, nil
}

func (f *fakeStore) ListAssetDerivatives(_ context.Context, ws ids.WorkspaceID, assetIDs []ids.AssetID) ([]store.Derivative, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ws != f.asset.WorkspaceID || len(assetIDs) != 1 || assetIDs[0] != f.asset.ID {
		return nil, nil
	}
	return f.derivs, nil
}

func (f *fakeStore) ApplyQuickLook(_ context.Context, ws ids.WorkspaceID, id ids.AssetID, r store.QuickLookResult) (store.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ws != f.asset.WorkspaceID || id != f.asset.ID {
		return store.Asset{}, store.ErrNotFound
	}
	if f.asset.TitleSource != store.TitleFromUser {
		f.asset.Title, f.asset.TitleSource = r.Title, store.TitleFromQuickLook
	}
	f.asset.Description, f.asset.Tags = r.Description, r.Tags
	st := store.QuickLookSucceeded
	f.asset.QuickLookStatus, f.asset.QuickLook = &st, r.Detail
	return f.asset, nil
}

func (f *fakeStore) MergeQuickLookDetail(_ context.Context, ws ids.WorkspaceID, id ids.AssetID, patch json.RawMessage) (store.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ws != f.asset.WorkspaceID || id != f.asset.ID || f.asset.QuickLookStatus == nil || *f.asset.QuickLookStatus != store.QuickLookSucceeded {
		return store.Asset{}, store.ErrNotFound
	}
	merged := map[string]json.RawMessage{}
	_ = json.Unmarshal(f.asset.QuickLook, &merged)
	var add map[string]json.RawMessage
	if err := json.Unmarshal(patch, &add); err != nil {
		return store.Asset{}, err
	}
	for k, v := range add {
		merged[k] = v
	}
	f.asset.QuickLook, _ = json.Marshal(merged)
	return f.asset, nil
}

func (f *fakeStore) SkipQuickLook(_ context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ws != f.asset.WorkspaceID || id != f.asset.ID || f.asset.QuickLookStatus == nil || *f.asset.QuickLookStatus != store.QuickLookPending {
		return store.Asset{}, store.ErrNotFound
	}
	st := store.QuickLookSkipped
	f.asset.QuickLookStatus = &st
	return f.asset, nil
}

func (f *fakeStore) FailQuickLook(_ context.Context, ws ids.WorkspaceID, id ids.AssetID, detail json.RawMessage) (store.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ws != f.asset.WorkspaceID || id != f.asset.ID {
		return store.Asset{}, store.ErrNotFound
	}
	st := store.QuickLookFailed
	f.asset.QuickLookStatus, f.asset.QuickLook = &st, detail
	return f.asset, nil
}

// edit changes the asset as a person would, between two steps of a run.
func (f *fakeStore) edit(fn func(*store.Asset)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.asset)
}

func (f *fakeStore) snapshot() store.Asset {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asset
}

// newReadyVideo is a ready 12 s video with a proxy object in objs.
func newReadyVideo(t *testing.T, objs *memObjects) (*fakeStore, store.Asset) {
	t.Helper()
	ws := ids.WorkspaceID{UUID: uuid.New()}
	id := ids.AssetID{UUID: uuid.New()}
	dur, pending := int64(12_000), store.QuickLookPending
	a := store.Asset{
		ID: id, WorkspaceID: ws, Kind: store.AssetKindVideo, Status: store.AssetReady, Filename: "IMG_0042.MOV", Title: "IMG_0042",
		TitleSource: store.TitleFromFilename, Tags: []string{}, DurationMs: &dur, QuickLookStatus: &pending,
		R2Key: storage.OriginalKey(ws, id),
	}
	proxyKey := storage.AssetPrefix(ws, id) + "proxy_720.mp4"
	objs.objs[proxyKey] = []byte("not really an mp4: the fake sandbox never reads it")
	return &fakeStore{asset: a, derivs: []store.Derivative{{
		WorkspaceID: ws, AssetID: id, Kind: store.DerivativeProxy720, R2Key: proxyKey, ContentType: "video/mp4", SizeBytes: 50,
		Meta: json.RawMessage(`{"duration_ms":12000}`),
	}}}, a
}

// ---- A fake Anthropic API and ledger -----------------------------------------------------------------------------------

type apiCall struct {
	Path string
	Body []byte
}

func (c apiCall) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return m
}

// fakeAPI is the HTTP transport under the SDK. reply builds the answer of a /v1/messages call.
type fakeAPI struct {
	mu    sync.Mutex
	calls []apiCall
	// reply returns the JSON body of the n-th (1-based) messages call, or an error answer when status is not 200.
	reply func(n int) (status int, body string)
	// tokens is what count_tokens answers.
	tokens int
	// forbid fails the test on any call (CI must make none).
	forbid *testing.T
}

func (f *fakeAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.forbid != nil {
		f.forbid.Errorf("a call reached the API: %s %s", r.Method, r.URL.Path)
	}
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	f.mu.Lock()
	f.calls = append(f.calls, apiCall{Path: r.URL.Path, Body: body})
	n := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c.Path, "/v1/messages") {
			n++
		}
	}
	f.mu.Unlock()
	status, out := 200, ""
	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/messages/count_tokens"):
		out = fmt.Sprintf(`{"input_tokens": %d}`, f.tokens)
	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		status, out = f.reply(n)
	default:
		status, out = 404, `{"type":"error","error":{"type":"not_found_error","message":"no route"}}`
	}
	h := http.Header{"Content-Type": {"application/json"}, "Request-Id": {fmt.Sprintf("req_fake_%d", len(f.calls))}}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: h, Body: io.NopCloser(strings.NewReader(out)), Request: r, ProtoMajor: 1, ProtoMinor: 1}, nil
}

func (f *fakeAPI) messageCalls() []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []apiCall
	for _, c := range f.calls {
		if strings.HasSuffix(c.Path, "/v1/messages") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeAPI) total() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// goodAnswer is a describe_clip call as the model would make it.
const goodAnswer = `{"title":"Barbell row","description":"A lifter does bent-over barbell rows in a gym, filmed from the side.","tags":["barbell row","back","gym","side view","lats"],"subjects":[{"frame":1,"present":true,"x":0.22,"y":0.2,"w":0.56,"h":0.7,"head_top_y":0.22,"head_x":0.44,"head_w":0.12,"shoulder_y":0.33},{"frame":2,"present":true,"x":0.2,"y":0.24,"w":0.6,"h":0.66,"head_top_y":0.26,"head_x":0,"head_w":0,"shoulder_y":0},{"frame":3,"present":true,"x":0.2,"y":0.22,"w":0.6,"h":0.68,"head_top_y":0.25},{"frame":4,"present":false,"x":0,"y":0,"w":0,"h":0,"head_top_y":0}]}`

// toolReply is a Messages API answer in which the model calls describe_clip with input.
func toolReply(input string, in, out int) (int, string) {
	return 200, fmt.Sprintf(`{"id":"msg_fake","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"describe_clip","input":%s}],"stop_reason":"tool_use","stop_sequence":null,`+
		`"usage":{"input_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":%d}}`, input, in, out)
}

func goodReply(int) (int, string) { return toolReply(goodAnswer, 1750, 110) }

// fakeLedger is the in-memory spend backend: it collects the usage_ledger rows (spendtest.Memory).
type fakeLedger struct{ spendtest.Memory }

func (l *fakeLedger) snapshot() []store.UsageEntry { return l.Rows() }

// ---- A service over the fakes ------------------------------------------------------------------------------------------

type rig struct {
	svc    *Service
	store  *fakeStore
	asset  store.Asset
	objs   *memObjects
	runner *fakeRunner
	api    *fakeAPI
	ledger *fakeLedger
	router *llm.Router
}

// rigOptions tune newRig.
type rigOptions struct {
	mode        llm.Mode
	cassetteDir string
	profile     string
	api         *fakeAPI
	noKey       bool
	route       Router
	// runner replaces the fake sandbox; tempDir is then the job directory (inside the runner's allowed roots).
	runner  media.Runner
	tempDir string
}

func newFactory(t *testing.T, o rigOptions, ledger spend.Backend, api *fakeAPI) *llm.Factory {
	t.Helper()
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	settings := llm.Settings{
		Env: "local", Mode: o.mode, RoutingProfile: o.profile, MaxRequestMicros: llm.DefaultMaxRequestMicros,
		DailySpendCapMicros: llm.DefaultDailySpendCapMicros, Concurrency: 4, CassetteDir: o.cassetteDir,
	}
	getenv := func(k string) string {
		if k == "ANTHROPIC_API_KEY" && !o.noKey {
			return testKey
		}
		return ""
	}
	f, err := llm.NewFactory(llm.FactoryOptions{Settings: settings, Getenv: getenv, Prices: prices, Ledger: ledger, HTTPClient: &http.Client{Transport: api}, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newRig(t *testing.T, o rigOptions) *rig {
	t.Helper()
	if o.mode == "" {
		o.mode = llm.ModeLive
	}
	if o.profile == "" {
		o.profile = "dev-cheap"
	}
	if o.api == nil {
		o.api = &fakeAPI{reply: goodReply, tokens: 1750}
	}
	objs := newMemObjects()
	st, a := newReadyVideo(t, objs)
	router, err := llm.NewRouter(configfiles.Routing(), o.profile)
	if err != nil {
		t.Fatal(err)
	}
	var route Router = router
	if o.route != nil {
		route = o.route
	}
	ledger := &fakeLedger{}
	runner := &fakeRunner{}
	var run media.Runner = runner
	if o.runner != nil {
		run = o.runner
	}
	tempDir := o.tempDir
	if tempDir == "" {
		tempDir = t.TempDir()
	}
	frames := &editor.FrameService{Store: st, Objects: objs, Runner: run, TempDir: tempDir, Logger: quiet}
	svc, err := New(Config{
		Store: st, Objects: objs, Frames: frames, Models: newFactory(t, o, ledger, o.api),
		Router: route, Logger: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{svc: svc, store: st, asset: a, objs: objs, runner: runner, api: o.api, ledger: ledger, router: router}
}
